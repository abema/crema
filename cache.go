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
// SerializationCodec are goroutine-safe.
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
	codec                          SerializationCodec[V, S]
	logger                         *slog.Logger
	metrics                        MetricsProvider
	internalLoader                 internalLoader[V]
	now                            func() time.Time
	steepness                      float64
	revalidationWindowMilliseconds int64
	random                         func() float64 // must goroutine safe
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
		if loader, ok := c.internalLoader.(*singleflightLoader[V]); ok {
			loader.metrics = metrics
		}
	}
}

// WithDirectLoader disables singleflight and calls loaders directly.
func WithDirectLoader[V any, S any]() CacheOption[V, S] {
	return func(c *cacheImpl[V, S]) {
		c.internalLoader = directLoader[V]{}
	}
}

// WithRevalidationWindow sets the target revalidation window duration.
func WithRevalidationWindow[V any, S any](duration time.Duration) CacheOption[V, S] {
	steepness, revalidationWindowMilliseconds := calculateSteepnessAndRevalidationWindow(duration.Milliseconds())

	return func(c *cacheImpl[V, S]) {
		c.steepness = steepness
		c.revalidationWindowMilliseconds = revalidationWindowMilliseconds
	}
}

// NewCache constructs a Cache with defaults and optional overrides.
func NewCache[V any, S any](provider CacheProvider[S], codec SerializationCodec[V, S], opts ...CacheOption[V, S]) Cache[V, S] {
	steepness, revalidationWindowMilliseconds := calculateSteepnessAndRevalidationWindow(defaultRevalidationWindowMilliseconds)
	metrics := NoopMetricsProvider{}
	cache := &cacheImpl[V, S]{
		provider:                       provider,
		codec:                          codec,
		logger:                         slog.New(noopLogHandler{}),
		metrics:                        metrics,
		internalLoader:                 newSingleflightLoader[V](metrics),
		now:                            time.Now,
		random:                         rand.Float64,
		steepness:                      steepness,
		revalidationWindowMilliseconds: revalidationWindowMilliseconds,
	}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		opt(cache)
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

	return c.provider.Set(ctx, key, encoded, ttl)
}

// Delete removes a cached entry for key.
func (c *cacheImpl[V, S]) Delete(ctx context.Context, key string) error {
	c.metrics.RecordCacheDelete(ctx)

	return c.provider.Delete(ctx, key)
}

// GetOrLoad returns a cached value or uses loader when missing or revalidating.
func (c *cacheImpl[V, S]) GetOrLoad(ctx context.Context, key string, ttl time.Duration, loader CacheLoadFunc[V]) (V, error) {
	value, found, err := c.Get(ctx, key)
	if err != nil {
		c.logger.Warn("failed to get from cache", slog.String("key", key), slog.String("error", err.Error()))
		found = false
	}
	if found && !c.shouldRevalidate(c.now().UnixMilli(), value.ExpireAtMillis) {
		return value.Value, nil
	}

	v, leader, err := c.internalLoader.load(ctx, key, loader)
	if err != nil {
		var zero V

		return zero, err
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

// shouldRevalidate returns true if the entry is expired, or if the remaining
// TTL is within the revalidation window and a random draw falls under the
// revalidation probability p(t)=1-exp(-steepness*t).
func (c *cacheImpl[V, S]) shouldRevalidate(nowMillis int64, expireAtMillis int64) bool {
	remainMillis := expireAtMillis - nowMillis
	if remainMillis <= 0 {
		return true
	}

	if remainMillis > c.revalidationWindowMilliseconds {
		return false
	}

	p := 1.0 - math.Exp(-c.steepness*float64(remainMillis))

	return c.random() < p
}

// calculateSteepnessAndRevalidationWindow derives the steepness for
// p(t)=1-exp(-steepness*t) so that p(targetRevalidationWindowMilliseconds)=0.999,
// then returns the smallest window (in milliseconds) where p(t) reaches 0.995.
func calculateSteepnessAndRevalidationWindow(targetRevalidationWindowMilliseconds int64) (float64, int64) {
	target := 0.999
	targetThreshold := 0.995

	if targetRevalidationWindowMilliseconds == 0 {
		return 0, 0
	}
	if targetRevalidationWindowMilliseconds < 0 {
		targetRevalidationWindowMilliseconds = defaultRevalidationWindowMilliseconds
	}
	targetMilliseconds := float64(targetRevalidationWindowMilliseconds)

	steepness := -math.Log(1.0-target) / targetMilliseconds
	tf := -math.Log(1.0-targetThreshold) / steepness
	revalidationWindowMilliSeconds := int64(math.Ceil(tf))

	return float64(steepness), revalidationWindowMilliSeconds
}
