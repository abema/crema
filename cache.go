package crema

import (
	"context"
	"log/slog"
	"math"
	"math/rand/v2"
	"time"
)

// Cache coordinates probabilistic revalidation with optional singleflight loading.
// Implementations are safe for concurrent use as long as CacheProvider and
// CacheStorageCodec implementations are goroutine-safe.
// Use NewCache to construct an implementation.
type Cache[V any, S any] interface {
	// Get returns the cached entry for key.
	Get(ctx context.Context, key string) (CacheObject[V], bool, error)
	// Set stores a cached entry for key.
	Set(ctx context.Context, key string, value CacheObject[V]) error
	// Delete removes a cached entry for key.
	Delete(ctx context.Context, key string) error
	// GetOrLoad returns a cached value or uses loader when missing or revalidating.
	GetOrLoad(ctx context.Context, key string, ttl time.Duration, loader CacheLoadFunc[V]) (V, error)
}

type cacheImpl[V any, S any] struct {
	_                              noCopy
	provider                       CacheProvider[S]
	codec                          CacheStorageCodec[V, S]
	logger                         *slog.Logger
	metrics                        MetricsProvider
	internalLoader                 internalLoader[V]
	now                            func() time.Time
	steepness                      float64
	revalidationWindowMilliseconds int64
	maxLoadTimeout                 time.Duration
	revalidationFallback           bool
	negativeCache                  *negativeCache
	negativeCacheTTL               time.Duration
	negativeCacheProvider          CacheProvider[error]
	negativeCacheErrorPredicate    LoadErrorCacheErrorPredicate
	random                         func() float64 // must goroutine safe
	useDirectLoader                bool
}

// CacheObject wraps a cached value with its absolute expiration time.
type CacheObject[V any] struct {
	// Value is the cached value.
	Value V
	// ExpireAtMillis is the absolute expiration time in milliseconds since epoch.
	ExpireAtMillis int64
}

// CacheLoadFunc loads a value when it is missing or needs revalidation.
type CacheLoadFunc[V any] func(ctx context.Context) (V, error)

// CacheOption configures a Cache instance.
type CacheOption[V any, S any] func(*cacheImpl[V, S])

const defaultRevalidationWindowMilliseconds = 300000

// WithLogger overrides the default logger used for cache warnings.
func WithLogger[V any, S any](logger *slog.Logger) CacheOption[V, S] {
	return func(c *cacheImpl[V, S]) {
		if logger != nil {
			c.logger = logger
		}
	}
}

// WithMetricsProvider overrides the default metrics provider.
func WithMetricsProvider[V any, S any](metrics MetricsProvider) CacheOption[V, S] {
	return func(c *cacheImpl[V, S]) {
		if metrics == nil {
			metrics = NoopMetricsProvider{}
		}
		c.metrics = metrics
	}
}

// WithDirectLoader disables singleflight and calls loaders directly.
func WithDirectLoader[V any, S any]() CacheOption[V, S] {
	return func(c *cacheImpl[V, S]) {
		c.useDirectLoader = true
	}
}

// WithRevalidationWindow sets how long before expiry probabilistic revalidation starts.
// A zero duration disables probabilistic revalidation.
func WithRevalidationWindow[V any, S any](duration time.Duration) CacheOption[V, S] {
	steepness, revalidationWindowMilliseconds := calculateSteepnessAndRevalidationWindow(duration.Milliseconds())

	return func(c *cacheImpl[V, S]) {
		c.steepness = steepness
		c.revalidationWindowMilliseconds = revalidationWindowMilliseconds
	}
}

// WithMaxLoadTimeout sets the maximum duration allowed for loader execution.
// A non-positive duration disables the timeout.
func WithMaxLoadTimeout[V any, S any](duration time.Duration) CacheOption[V, S] {
	return func(c *cacheImpl[V, S]) {
		c.maxLoadTimeout = duration
	}
}

// WithRevalidationFallback controls whether GetOrLoad returns a still-valid
// cached value when revalidation fails. It is enabled by default.
func WithRevalidationFallback[V any, S any](enabled bool) CacheOption[V, S] {
	return func(c *cacheImpl[V, S]) {
		c.revalidationFallback = enabled
	}
}

// WithLoadErrorCacheProvider caches errors selected by shouldCache through provider.
func WithLoadErrorCacheProvider[V any, S any](
	provider CacheProvider[error],
	ttl time.Duration,
	shouldCache LoadErrorCacheErrorPredicate,
) CacheOption[V, S] {
	return func(c *cacheImpl[V, S]) {
		if provider == nil || shouldCache == nil || ttl <= 0 {
			c.negativeCacheProvider = nil
			c.negativeCacheTTL = 0

			return
		}
		c.negativeCacheProvider = provider
		c.negativeCacheTTL = ttl
		c.negativeCacheErrorPredicate = shouldCache
	}
}

// WithNegativeCacheProvider caches errors selected by isNegative as absent values.
// It replaces any load-error-cache provider configured by earlier options.
func WithNegativeCacheProvider[V any, S any](
	provider CacheProvider[error],
	ttl time.Duration,
	isNegative NegativeCacheErrorPredicate,
) CacheOption[V, S] {
	return func(c *cacheImpl[V, S]) {
		if provider == nil || isNegative == nil || ttl <= 0 {
			c.negativeCacheProvider = nil
			c.negativeCacheTTL = 0

			return
		}
		c.negativeCacheProvider = provider
		c.negativeCacheTTL = ttl
		c.negativeCacheErrorPredicate = LoadErrorCacheErrorPredicate(isNegative)
	}
}

// NewCache constructs a Cache with defaults and optional overrides.
func NewCache[V any, S any](provider CacheProvider[S], codec CacheStorageCodec[V, S], opts ...CacheOption[V, S]) Cache[V, S] {
	steepness, revalidationWindowMilliseconds := calculateSteepnessAndRevalidationWindow(defaultRevalidationWindowMilliseconds)
	cache := &cacheImpl[V, S]{
		provider:                       provider,
		codec:                          codec,
		logger:                         slog.New(noopLogHandler{}),
		metrics:                        NoopMetricsProvider{},
		now:                            time.Now,
		random:                         rand.Float64,
		steepness:                      steepness,
		revalidationWindowMilliseconds: revalidationWindowMilliseconds,
		maxLoadTimeout:                 0,
		revalidationFallback:           true,
		useDirectLoader:                false,
	}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		opt(cache)
	}
	if cache.negativeCacheProvider != nil && cache.negativeCacheTTL > 0 {
		cache.negativeCache = newNegativeCache(cache.negativeCacheProvider)
	}
	if cache.useDirectLoader {
		cache.internalLoader = newDirectLoader[V](cache.metrics)
	} else {
		cache.internalLoader = newSingleflightLoader[V](cache.metrics, cache.maxLoadTimeout)
	}

	return cache
}

// Get returns the cached entry for key, if present.
func (c *cacheImpl[V, S]) Get(ctx context.Context, key string) (CacheObject[V], bool, error) {
	c.metrics.RecordCacheGet(ctx)

	rv, exists, err := c.provider.Get(ctx, key)
	if err != nil {
		return CacheObject[V]{}, false, err
	}
	if !exists {
		return CacheObject[V]{}, false, nil
	}

	co, err := c.codec.Decode(rv)
	if err != nil {
		return CacheObject[V]{}, false, err
	}
	c.metrics.RecordCacheHit(ctx)

	return co, true, nil
}

// Set stores a cache entry, skipping writes when already expired.
func (c *cacheImpl[V, S]) Set(ctx context.Context, key string, value CacheObject[V]) error {
	c.metrics.RecordCacheSet(ctx)

	encoded, err := c.codec.Encode(value)
	if err != nil {
		return err
	}
	ttl := time.UnixMilli(value.ExpireAtMillis).Sub(c.now())
	if ttl <= 0 {
		return nil
	}

	if err := c.provider.Set(ctx, key, encoded, ttl); err != nil {
		return err
	}
	if c.negativeCache != nil {
		if err := c.negativeCache.invalidate(ctx, key); err != nil {
			c.logger.Warn("failed to invalidate negative cache", slog.String("key", key), slog.String("error", err.Error()))
		}
	}

	return nil
}

// Delete removes a cached entry for key, including any negative cache entry.
func (c *cacheImpl[V, S]) Delete(ctx context.Context, key string) error {
	c.metrics.RecordCacheDelete(ctx)

	if err := c.provider.Delete(ctx, key); err != nil {
		return err
	}
	if c.negativeCache != nil {
		if err := c.negativeCache.invalidate(ctx, key); err != nil {
			c.logger.Warn("failed to invalidate negative cache", slog.String("key", key), slog.String("error", err.Error()))
		}
	}

	return nil
}

// GetOrLoad returns a cached value or uses loader when missing or revalidating.
func (c *cacheImpl[V, S]) GetOrLoad(ctx context.Context, key string, ttl time.Duration, loader CacheLoadFunc[V]) (V, error) {
	negativeToken := c.negativeCacheToken(key)
	reason := LoadReasonMiss
	value, found, err := c.Get(ctx, key)
	if err != nil {
		c.logger.Warn("failed to get from cache", slog.String("key", key), slog.String("error", err.Error()))
		found = false
		reason = LoadReasonGetError
	}
	if found {
		nowMillis := c.now().UnixMilli()
		switch {
		case value.ExpireAtMillis <= nowMillis:
			reason = LoadReasonExpired
		case c.shouldRevalidate(nowMillis, value.ExpireAtMillis):
			reason = LoadReasonRevalidation
		default:
			return value.Value, nil
		}
	}

	if v, err, ok := c.negativeCacheValue(ctx, key, found, value); ok {
		return v, err
	}

	load := c.withNegativeCache(key, negativeToken, loader)
	v, leader, err := c.internalLoader.load(ctx, key, reason, load)
	if err != nil {
		return c.handleLoadError(ctx, key, found, value, err)
	}
	if leader {
		co := CacheObject[V]{
			Value:          v,
			ExpireAtMillis: c.now().Add(ttl).UnixMilli(),
		}
		if err := c.Set(ctx, key, co); err != nil {
			c.logger.Warn("failed to set cache", slog.String("key", key), slog.String("error", err.Error()))
		}
	}

	return v, nil
}

func (c *cacheImpl[V, S]) handleLoadError(
	ctx context.Context,
	key string,
	found bool,
	value CacheObject[V],
	err error,
) (V, error) {
	if c.canFallback(ctx, found, value) {
		c.logger.Warn("failed to load, falling back to cached value", slog.String("key", key), slog.String("error", err.Error()))

		return value.Value, nil
	}
	var zero V

	return zero, err
}

func (c *cacheImpl[V, S]) canFallback(ctx context.Context, found bool, value CacheObject[V]) bool {
	return ctx.Err() == nil &&
		c.revalidationFallback &&
		found &&
		value.ExpireAtMillis > c.now().UnixMilli()
}

func (c *cacheImpl[V, S]) negativeCacheGet(ctx context.Context, key string) (error, bool) {
	if c.negativeCache == nil {
		return nil, false
	}

	err, found, getErr := c.negativeCache.get(ctx, key)
	if getErr != nil {
		c.logger.Warn("failed to get from negative cache", slog.String("key", key), slog.String("error", getErr.Error()))

		return nil, false
	}

	return err, found
}

func (c *cacheImpl[V, S]) negativeCacheValue(
	ctx context.Context,
	key string,
	found bool,
	value CacheObject[V],
) (V, error, bool) {
	cachedErr, ok := c.negativeCacheGet(ctx, key)
	if !ok {
		var zero V

		return zero, nil, false
	}

	c.recordNegativeCacheHit(ctx)
	if c.canFallback(ctx, found, value) {
		return value.Value, nil, true
	}
	var zero V

	return zero, cachedErr, true
}

func (c *cacheImpl[V, S]) negativeCacheToken(key string) negativeCacheToken {
	if c.negativeCache == nil {
		return negativeCacheToken{}
	}

	return c.negativeCache.token(key)
}

func (c *cacheImpl[V, S]) withNegativeCache(
	key string,
	token negativeCacheToken,
	loader CacheLoadFunc[V],
) CacheLoadFunc[V] {
	if c.negativeCache == nil {
		return loader
	}

	return func(ctx context.Context) (V, error) {
		v, err := loader(ctx)
		if err != nil {
			c.negativeCacheSet(ctx, key, err, token)
		}

		return v, err
	}
}

func (c *cacheImpl[V, S]) negativeCacheSet(
	ctx context.Context,
	key string,
	err error,
	token negativeCacheToken,
) {
	if c.negativeCache == nil || ctx.Err() != nil || !c.negativeCacheErrorPredicate(err) {
		return
	}

	stored, setErr := c.negativeCache.set(ctx, key, err, c.negativeCacheTTL, token)
	if setErr != nil {
		c.logger.Warn("failed to set negative cache", slog.String("key", key), slog.String("error", setErr.Error()))

		return
	}
	if stored {
		c.recordNegativeCacheSet(ctx)
	}
}

func (c *cacheImpl[V, S]) recordNegativeCacheHit(ctx context.Context) {
	if metrics, ok := c.metrics.(NegativeCacheMetricsProvider); ok {
		metrics.RecordNegativeCacheHit(ctx)
	}
}

func (c *cacheImpl[V, S]) recordNegativeCacheSet(ctx context.Context) {
	if metrics, ok := c.metrics.(NegativeCacheMetricsProvider); ok {
		metrics.RecordNegativeCacheSet(ctx)
	}
}

// shouldRevalidate returns true for expired entries or when a random draw is
// below p(t)=1-exp(-steepness*(w-t)), where t is the remaining TTL and w is the
// revalidation window.
func (c *cacheImpl[V, S]) shouldRevalidate(nowMillis int64, expireAtMillis int64) bool {
	remainMillis := expireAtMillis - nowMillis
	if remainMillis <= 0 {
		return true
	}

	if remainMillis > c.revalidationWindowMilliseconds {
		return false
	}

	elapsedMillis := c.revalidationWindowMilliseconds - remainMillis
	p := 1.0 - math.Exp(-c.steepness*float64(elapsedMillis))

	return c.random() < p
}

// calculateSteepnessAndRevalidationWindow derives the steepness so the
// probability approaches 0.999 at expiry. Zero disables revalidation and a
// negative value selects the default window.
func calculateSteepnessAndRevalidationWindow(targetRevalidationWindowMilliseconds int64) (float64, int64) {
	target := 0.999

	if targetRevalidationWindowMilliseconds == 0 {
		return 0, 0
	}
	if targetRevalidationWindowMilliseconds < 0 {
		targetRevalidationWindowMilliseconds = defaultRevalidationWindowMilliseconds
	}
	targetMilliseconds := float64(targetRevalidationWindowMilliseconds)

	steepness := -math.Log(1.0-target) / targetMilliseconds

	return steepness, targetRevalidationWindowMilliseconds
}
