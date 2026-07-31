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
type Cache[V any] interface {
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
	negativeCache                  *fencedCache[CacheLoadResult[V]]
	negativeCacheTTL               time.Duration
	negativeCachePredicate         NegativeCachePredicate[V]
	loadErrorCache                 *fencedCache[error]
	loadErrorCacheTTL              time.Duration
	loadErrorCacheProvider         CacheProvider[error]
	loadErrorCachePredicate        LoadErrorCacheErrorPredicate
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

type cacheConfig struct {
	logger                         *slog.Logger
	metrics                        MetricsProvider
	steepness                      float64
	revalidationWindowMilliseconds int64
	maxLoadTimeout                 time.Duration
	revalidationFallback           bool
	negativeCacheTTL               time.Duration
	negativeCacheProvider          any
	negativeCachePredicate         any
	loadErrorCacheTTL              time.Duration
	loadErrorCacheProvider         CacheProvider[error]
	loadErrorCachePredicate        LoadErrorCacheErrorPredicate
	useDirectLoader                bool
}

// CacheOption configures a Cache instance.
type CacheOption func(*cacheConfig)

const defaultRevalidationWindowMilliseconds = 300000

// WithLogger overrides the default logger used for cache warnings.
func WithLogger(logger *slog.Logger) CacheOption {
	return func(c *cacheConfig) {
		if logger != nil {
			c.logger = logger
		}
	}
}

// WithMetricsProvider overrides the default metrics provider.
func WithMetricsProvider(metrics MetricsProvider) CacheOption {
	return func(c *cacheConfig) {
		if metrics == nil {
			metrics = NoopMetricsProvider{}
		}
		c.metrics = metrics
	}
}

// WithDirectLoader disables singleflight and calls loaders directly.
func WithDirectLoader() CacheOption {
	return func(c *cacheConfig) {
		c.useDirectLoader = true
	}
}

// WithRevalidationWindow sets how long before expiry probabilistic revalidation starts.
// A zero duration disables probabilistic revalidation.
func WithRevalidationWindow(duration time.Duration) CacheOption {
	steepness, revalidationWindowMilliseconds := calculateSteepnessAndRevalidationWindow(duration.Milliseconds())

	return func(c *cacheConfig) {
		c.steepness = steepness
		c.revalidationWindowMilliseconds = revalidationWindowMilliseconds
	}
}

// WithMaxLoadTimeout sets the maximum duration allowed for singleflight loader
// execution, including its synchronous cache writeback.
// A non-positive duration disables the timeout.
func WithMaxLoadTimeout(duration time.Duration) CacheOption {
	return func(c *cacheConfig) {
		c.maxLoadTimeout = duration
	}
}

// WithRevalidationFallback controls whether GetOrLoad returns a still-valid
// cached value when revalidation fails. It is enabled by default.
func WithRevalidationFallback(enabled bool) CacheOption {
	return func(c *cacheConfig) {
		c.revalidationFallback = enabled
	}
}

// WithLoadErrorCacheProvider caches errors selected by shouldCache through provider.
func WithLoadErrorCacheProvider(
	provider CacheProvider[error],
	ttl time.Duration,
	shouldCache LoadErrorCacheErrorPredicate,
) CacheOption {
	return func(c *cacheConfig) {
		if provider == nil || shouldCache == nil || ttl <= 0 {
			c.loadErrorCacheProvider = nil
			c.loadErrorCacheTTL = 0

			return
		}
		c.loadErrorCacheProvider = provider
		c.loadErrorCacheTTL = ttl
		c.loadErrorCachePredicate = shouldCache
	}
}

// WithNegativeCacheProvider caches loader results selected by isNegative as absent values.
func WithNegativeCacheProvider[V any](
	provider CacheProvider[CacheLoadResult[V]],
	ttl time.Duration,
	isNegative NegativeCachePredicate[V],
) CacheOption {
	return func(c *cacheConfig) {
		if provider == nil || isNegative == nil || ttl <= 0 {
			c.negativeCacheProvider = nil
			c.negativeCacheTTL = 0

			return
		}
		c.negativeCacheProvider = provider
		c.negativeCacheTTL = ttl
		c.negativeCachePredicate = isNegative
	}
}

// NewCache constructs a Cache with defaults and optional overrides.
func NewCache[V any, S any](provider CacheProvider[S], codec CacheStorageCodec[V, S], opts ...CacheOption) Cache[V] {
	steepness, revalidationWindowMilliseconds := calculateSteepnessAndRevalidationWindow(defaultRevalidationWindowMilliseconds)
	config := cacheConfig{
		logger:                         slog.New(noopLogHandler{}),
		metrics:                        NoopMetricsProvider{},
		steepness:                      steepness,
		revalidationWindowMilliseconds: revalidationWindowMilliseconds,
		revalidationFallback:           true,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&config)
		}
	}
	cache := &cacheImpl[V, S]{
		provider:                       provider,
		codec:                          codec,
		logger:                         config.logger,
		metrics:                        config.metrics,
		now:                            time.Now,
		random:                         rand.Float64,
		steepness:                      config.steepness,
		revalidationWindowMilliseconds: config.revalidationWindowMilliseconds,
		maxLoadTimeout:                 config.maxLoadTimeout,
		revalidationFallback:           config.revalidationFallback,
		negativeCacheTTL:               config.negativeCacheTTL,
		loadErrorCacheProvider:         config.loadErrorCacheProvider,
		loadErrorCacheTTL:              config.loadErrorCacheTTL,
		loadErrorCachePredicate:        config.loadErrorCachePredicate,
		useDirectLoader:                config.useDirectLoader,
	}
	if config.negativeCacheProvider != nil && config.negativeCacheTTL > 0 {
		provider, providerOK := config.negativeCacheProvider.(CacheProvider[CacheLoadResult[V]])
		predicate, predicateOK := config.negativeCachePredicate.(NegativeCachePredicate[V])
		if !providerOK || !predicateOK {
			panic("crema: negative cache option value type does not match cache value type")
		}
		cache.negativeCache = newFencedCache(provider)
		cache.negativeCachePredicate = predicate
	}
	if cache.loadErrorCacheProvider != nil && cache.loadErrorCacheTTL > 0 {
		cache.loadErrorCache = newFencedCache(cache.loadErrorCacheProvider)
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

	ttl := time.UnixMilli(value.ExpireAtMillis).Sub(c.now())
	if ttl <= 0 {
		return nil
	}

	encoded, err := c.codec.Encode(value)
	if err != nil {
		return err
	}

	if err := c.provider.Set(ctx, key, encoded, ttl); err != nil {
		return err
	}
	c.invalidateLoadCaches(ctx, key)

	return nil
}

// Delete removes a cached entry for key, including any negative cache entry.
func (c *cacheImpl[V, S]) Delete(ctx context.Context, key string) error {
	c.metrics.RecordCacheDelete(ctx)

	if err := c.provider.Delete(ctx, key); err != nil {
		return err
	}
	c.invalidateLoadCaches(ctx, key)

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
	if v, err, ok := c.loadErrorCacheValue(ctx, key, found, value); ok {
		return v, err
	}

	load := c.wrapLoad(key, ttl, negativeToken, loader)
	v, _, err := c.internalLoader.load(ctx, key, reason, load)
	if err != nil {
		return c.handleLoadError(ctx, key, found, value, err)
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

func (c *cacheImpl[V, S]) negativeCacheGet(ctx context.Context, key string) (CacheLoadResult[V], bool) {
	if c.negativeCache == nil {
		return CacheLoadResult[V]{}, false
	}

	result, found, getErr := c.negativeCache.get(ctx, key)
	if getErr != nil {
		c.logger.Warn("failed to get from negative cache", slog.String("key", key), slog.String("error", getErr.Error()))

		return CacheLoadResult[V]{}, false
	}

	return result, found
}

func (c *cacheImpl[V, S]) negativeCacheValue(
	ctx context.Context,
	key string,
	found bool,
	value CacheObject[V],
) (V, error, bool) {
	result, ok := c.negativeCacheGet(ctx, key)
	if !ok {
		var zero V

		return zero, nil, false
	}

	c.recordNegativeCacheHit(ctx)
	if c.canFallback(ctx, found, value) {
		return value.Value, nil, true
	}

	return result.Value, result.Err, true
}

func (c *cacheImpl[V, S]) loadErrorCacheValue(
	ctx context.Context,
	key string,
	found bool,
	value CacheObject[V],
) (V, error, bool) {
	if c.loadErrorCache == nil {
		var zero V

		return zero, nil, false
	}

	cachedErr, cached, err := c.loadErrorCache.get(ctx, key)
	if err != nil {
		c.logger.Warn("failed to get from load-error cache", slog.String("key", key), slog.String("error", err.Error()))
	}
	if !cached || err != nil {
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

func (c *cacheImpl[V, S]) loadErrorCacheToken(key string) negativeCacheToken {
	if c.loadErrorCache == nil {
		return negativeCacheToken{}
	}

	return c.loadErrorCache.token(key)
}

func (c *cacheImpl[V, S]) wrapLoad(
	key string,
	ttl time.Duration,
	negativeToken negativeCacheToken,
	loader CacheLoadFunc[V],
) CacheLoadFunc[V] {
	switch {
	case c.negativeCache == nil && c.loadErrorCache == nil:
		return c.withStoreLoadedValue(key, ttl, loader)
	case c.negativeCache != nil && c.loadErrorCache == nil:
		return c.withStoreLoadedValueAndNegativeCache(key, ttl, negativeToken, loader)
	case c.negativeCache == nil && c.loadErrorCache != nil:
		return c.withStoreLoadedValueAndLoadErrorCache(key, ttl, c.loadErrorCacheToken(key), loader)
	default:
		return c.withStoreLoadedValueAndLoadCaches(key, ttl, negativeToken, c.loadErrorCacheToken(key), loader)
	}
}

func (c *cacheImpl[V, S]) withStoreLoadedValue(
	key string,
	ttl time.Duration,
	loader CacheLoadFunc[V],
) CacheLoadFunc[V] {
	return func(ctx context.Context) (V, error) {
		value, err := loader(ctx)
		if err == nil {
			c.storeLoadedValue(ctx, key, ttl, value)
		}

		return value, err
	}
}

func (c *cacheImpl[V, S]) withStoreLoadedValueAndNegativeCache(
	key string,
	ttl time.Duration,
	token negativeCacheToken,
	loader CacheLoadFunc[V],
) CacheLoadFunc[V] {
	return func(ctx context.Context) (V, error) {
		v, err := loader(ctx)
		if c.cacheNegativeResult(ctx, key, v, err, token) {
			return v, err
		}
		if err == nil {
			c.storeLoadedValue(ctx, key, ttl, v)
		}

		return v, err
	}
}

func (c *cacheImpl[V, S]) withStoreLoadedValueAndLoadErrorCache(
	key string,
	ttl time.Duration,
	token negativeCacheToken,
	loader CacheLoadFunc[V],
) CacheLoadFunc[V] {
	return func(ctx context.Context) (V, error) {
		v, err := loader(ctx)
		if err != nil {
			c.cacheLoadError(ctx, key, err, token)
		} else {
			c.storeLoadedValue(ctx, key, ttl, v)
		}

		return v, err
	}
}

func (c *cacheImpl[V, S]) withStoreLoadedValueAndLoadCaches(
	key string,
	ttl time.Duration,
	negativeToken, loadErrorToken negativeCacheToken,
	loader CacheLoadFunc[V],
) CacheLoadFunc[V] {
	return func(ctx context.Context) (V, error) {
		v, err := loader(ctx)
		if c.cacheNegativeResult(ctx, key, v, err, negativeToken) {
			return v, err
		}
		if err != nil {
			c.cacheLoadError(ctx, key, err, loadErrorToken)
		} else {
			c.storeLoadedValue(ctx, key, ttl, v)
		}

		return v, err
	}
}

func (c *cacheImpl[V, S]) storeLoadedValue(ctx context.Context, key string, ttl time.Duration, value V) {
	co := CacheObject[V]{
		Value:          value,
		ExpireAtMillis: c.now().Add(ttl).UnixMilli(),
	}
	storeCtx, cancel := withoutCancelPreservingDeadline(ctx)
	defer cancel()
	if err := c.Set(storeCtx, key, co); err != nil {
		c.logger.Warn("failed to set cache", slog.String("key", key), slog.String("error", err.Error()))
	}
}

// withoutCancelPreservingDeadline detaches a writeback from caller
// cancellation while retaining the load's absolute deadline.
func withoutCancelPreservingDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	detached := context.WithoutCancel(ctx)
	deadline, ok := ctx.Deadline()
	if !ok {
		return detached, func() {}
	}

	return context.WithDeadline(detached, deadline)
}

func (c *cacheImpl[V, S]) cacheNegativeResult(
	ctx context.Context,
	key string,
	value V,
	err error,
	token negativeCacheToken,
) bool {
	if c.negativeCache == nil || ctx.Err() != nil || !c.negativeCachePredicate(value, err) {
		return false
	}

	stored, setErr := c.negativeCache.set(ctx, key, CacheLoadResult[V]{Value: value, Err: err}, c.negativeCacheTTL, token)
	if setErr != nil {
		c.logger.Warn("failed to set negative cache", slog.String("key", key), slog.String("error", setErr.Error()))
	}
	if stored {
		c.recordNegativeCacheSet(ctx)
	}

	return true
}

func (c *cacheImpl[V, S]) cacheLoadError(
	ctx context.Context,
	key string,
	err error,
	token negativeCacheToken,
) {
	if c.loadErrorCache == nil || ctx.Err() != nil || !c.loadErrorCachePredicate(err) {
		return
	}

	stored, setErr := c.loadErrorCache.set(ctx, key, err, c.loadErrorCacheTTL, token)
	if setErr != nil {
		c.logger.Warn("failed to set load-error cache", slog.String("key", key), slog.String("error", setErr.Error()))

		return
	}
	if stored {
		c.recordNegativeCacheSet(ctx)
	}
}

func (c *cacheImpl[V, S]) invalidateLoadCaches(ctx context.Context, key string) {
	if c.negativeCache != nil {
		if err := c.negativeCache.invalidate(ctx, key); err != nil {
			c.logger.Warn("failed to invalidate negative cache", slog.String("key", key), slog.String("error", err.Error()))
		}
	}
	if c.loadErrorCache != nil {
		if err := c.loadErrorCache.invalidate(ctx, key); err != nil {
			c.logger.Warn("failed to invalidate load-error cache", slog.String("key", key), slog.String("error", err.Error()))
		}
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
