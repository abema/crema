package crema

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type negativeMetricsProvider struct {
	BaseMetricsProvider

	hits atomic.Int64
	sets atomic.Int64
}

func (m *negativeMetricsProvider) RecordNegativeCacheHit(context.Context) {
	m.hits.Add(1)
}

func (m *negativeMetricsProvider) RecordNegativeCacheSet(context.Context) {
	m.sets.Add(1)
}

func newTestClock(startMillis int64) (func() time.Time, func(int64)) {
	var mu sync.Mutex
	current := startMillis

	return func() time.Time {
			mu.Lock()
			defer mu.Unlock()

			return time.UnixMilli(current)
		}, func(millis int64) {
			mu.Lock()
			current = millis
			mu.Unlock()
		}
}

func TestDefaultNegativeCacheErrorPredicate(t *testing.T) {
	t.Parallel()

	if DefaultNegativeCacheErrorPredicate(nil) {
		t.Fatalf("expected nil error not to be cached")
	}
	if DefaultNegativeCacheErrorPredicate(context.Canceled) {
		t.Fatalf("expected context.Canceled not to be cached")
	}
	if DefaultNegativeCacheErrorPredicate(context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded not to be cached")
	}
	if DefaultNegativeCacheErrorPredicate(fmt.Errorf("load: %w", context.Canceled)) {
		t.Fatalf("expected wrapped context.Canceled not to be cached")
	}
	if !DefaultNegativeCacheErrorPredicate(errors.New("backend down")) {
		t.Fatalf("expected backend error to be cached")
	}
}

func setNegative(
	store *negativeCacheStore,
	key string,
	err error,
	nowMillis int64,
	expireAtMillis int64,
) bool {
	return store.set(key, err, nowMillis, expireAtMillis, store.tokenFor(key))
}

func TestCache_NegativeCacheDisabledByDefault(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	cache := NewCache(provider, NoopCacheStorageCodec[int]{})
	impl := cache.(*cacheImpl[int, CacheObject[int]])
	impl.now = func() time.Time { return time.UnixMilli(1000) }

	if impl.negativeCache != nil {
		t.Fatalf("expected negative cache to be disabled by default")
	}

	expectErr := errors.New("loader failed")
	var calls atomic.Int64
	loader := func(context.Context) (int, error) {
		calls.Add(1)

		return 0, expectErr
	}

	for range 2 {
		if _, err := cache.GetOrLoad(context.Background(), "answer", time.Second, loader); !errors.Is(err, expectErr) {
			t.Fatalf("expected error %v, got %v", expectErr, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("expected loader to be called twice, got %d", calls.Load())
	}
}

func TestCache_NegativeCacheSuppressesReload(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	metrics := &negativeMetricsProvider{}
	cache := NewCache(
		provider,
		NoopCacheStorageCodec[int]{},
		WithMetricsProvider[int, CacheObject[int]](metrics),
		WithNegativeCache[int, CacheObject[int]](5*time.Second),
	)
	impl := cache.(*cacheImpl[int, CacheObject[int]])
	now, setNow := newTestClock(1000)
	impl.now = now

	expectErr := errors.New("backend down")
	var calls atomic.Int64
	loader := func(context.Context) (int, error) {
		calls.Add(1)

		return 0, expectErr
	}

	value, err := cache.GetOrLoad(context.Background(), "answer", time.Minute, loader)
	if !errors.Is(err, expectErr) {
		t.Fatalf("expected error %v, got %v", expectErr, err)
	}
	if value != 0 {
		t.Fatalf("expected zero value, got %d", value)
	}

	setNow(4000)
	if _, err := cache.GetOrLoad(context.Background(), "answer", time.Minute, loader); !errors.Is(err, expectErr) {
		t.Fatalf("expected cached error %v, got %v", expectErr, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected loader to be called once, got %d", calls.Load())
	}
	if metrics.hits.Load() != 1 {
		t.Fatalf("expected 1 negative cache hit, got %d", metrics.hits.Load())
	}
	if metrics.sets.Load() != 1 {
		t.Fatalf("expected 1 negative cache set, got %d", metrics.sets.Load())
	}

	// The negative TTL is independent of the value TTL passed to GetOrLoad.
	setNow(6001)
	if _, err := cache.GetOrLoad(context.Background(), "answer", time.Minute, loader); !errors.Is(err, expectErr) {
		t.Fatalf("expected error %v, got %v", expectErr, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected loader to be called again after the negative TTL, got %d", calls.Load())
	}
	if metrics.hits.Load() != 1 {
		t.Fatalf("expected no additional negative cache hit, got %d", metrics.hits.Load())
	}
}

func TestCache_NegativeCacheReturnsIdenticalError(t *testing.T) {
	t.Parallel()

	type backendError struct{ error }

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	cache := NewCache(
		provider,
		NoopCacheStorageCodec[int]{},
		WithNegativeCache[int, CacheObject[int]](time.Second),
	)
	impl := cache.(*cacheImpl[int, CacheObject[int]])
	impl.now = func() time.Time { return time.UnixMilli(1000) }

	expectErr := backendError{errors.New("backend down")}
	loader := func(context.Context) (int, error) {
		return 0, fmt.Errorf("wrapped: %w", expectErr)
	}

	if _, err := cache.GetOrLoad(context.Background(), "answer", time.Minute, loader); err == nil {
		t.Fatal("expected error, got nil")
	}

	_, err := cache.GetOrLoad(context.Background(), "answer", time.Minute, loader)
	var target backendError
	if !errors.As(err, &target) {
		t.Fatalf("expected cached error to keep its chain, got %v", err)
	}
	if target != expectErr {
		t.Fatalf("expected identical error value, got %v", target)
	}
}

func TestCache_NegativeCacheSkipsContextCancellation(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	cache := NewCache(
		provider,
		NoopCacheStorageCodec[int]{},
		WithDirectLoader[int, CacheObject[int]](),
		WithNegativeCache[int, CacheObject[int]](time.Minute),
	)
	impl := cache.(*cacheImpl[int, CacheObject[int]])
	impl.now = func() time.Time { return time.UnixMilli(1000) }

	var calls atomic.Int64
	loader := func(context.Context) (int, error) {
		calls.Add(1)

		return 0, fmt.Errorf("aborted: %w", context.Canceled)
	}

	for range 2 {
		if _, err := cache.GetOrLoad(context.Background(), "answer", time.Minute, loader); !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("expected loader to be called twice, got %d", calls.Load())
	}
}

func TestCache_NegativeCacheSkipsWhenCallerContextDone(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	cache := NewCache(
		provider,
		NoopCacheStorageCodec[int]{},
		WithDirectLoader[int, CacheObject[int]](),
		WithNegativeCache[int, CacheObject[int]](time.Minute),
		WithNegativeCacheErrorPredicate[int, CacheObject[int]](func(error) bool { return true }),
	)
	impl := cache.(*cacheImpl[int, CacheObject[int]])
	impl.now = func() time.Time { return time.UnixMilli(1000) }

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	expectErr := errors.New("backend down")
	if _, err := cache.GetOrLoad(ctx, "answer", time.Minute, func(context.Context) (int, error) {
		return 0, expectErr
	}); !errors.Is(err, expectErr) {
		t.Fatalf("expected error %v, got %v", expectErr, err)
	}
	if _, ok := impl.negativeCache.get("answer", 1000); ok {
		t.Fatalf("expected no negative entry when the caller context is done")
	}
}

func TestCache_NegativeCacheSurvivesLeaderCancellation(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	metrics := &negativeMetricsProvider{}
	cache := NewCache(
		provider,
		NoopCacheStorageCodec[int]{},
		WithMetricsProvider[int, CacheObject[int]](metrics),
		WithNegativeCache[int, CacheObject[int]](time.Minute),
	)
	impl := cache.(*cacheImpl[int, CacheObject[int]])
	impl.now = func() time.Time { return time.UnixMilli(1000) }
	singleflight := impl.internalLoader.(*singleflightLoader[int])

	expectErr := errors.New("backend down")
	started := make(chan struct{})
	release := make(chan struct{})
	loader := func(context.Context) (int, error) {
		close(started)
		<-release

		return 0, expectErr
	}

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() {
		_, err := cache.GetOrLoad(leaderCtx, "answer", time.Minute, loader)
		leaderDone <- err
	}()
	<-started

	followerDone := make(chan error, 1)
	go func() {
		_, err := cache.GetOrLoad(context.Background(), "answer", time.Minute, loader)
		followerDone <- err
	}()

	deadline := time.Now().Add(time.Second)
	for {
		shard := singleflight.shardFor("answer")
		shard.mu.Lock()
		refs := 0
		if inf := shard.inflight["answer"]; inf != nil {
			refs = inf.refs
		}
		shard.mu.Unlock()
		if refs == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("follower did not join the inflight load")
		}
		runtime.Gosched()
	}

	cancelLeader()
	if err := <-leaderDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected leader cancellation, got %v", err)
	}
	close(release)
	if err := <-followerDone; !errors.Is(err, expectErr) {
		t.Fatalf("expected follower error %v, got %v", expectErr, err)
	}

	var calls atomic.Int64
	if _, err := cache.GetOrLoad(context.Background(), "answer", time.Minute, func(context.Context) (int, error) {
		calls.Add(1)

		return 0, errors.New("unexpected load")
	}); !errors.Is(err, expectErr) {
		t.Fatalf("expected cached error %v, got %v", expectErr, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("loader calls = %d, want 0", calls.Load())
	}
	if metrics.sets.Load() != 1 {
		t.Fatalf("negative cache sets = %d, want 1", metrics.sets.Load())
	}
}

func TestCache_NegativeCacheCustomPredicate(t *testing.T) {
	t.Parallel()

	cachedErr := errors.New("cache me")
	skippedErr := errors.New("skip me")

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	cache := NewCache(
		provider,
		NoopCacheStorageCodec[int]{},
		WithNegativeCache[int, CacheObject[int]](time.Minute),
		WithNegativeCacheErrorPredicate[int, CacheObject[int]](func(err error) bool {
			return errors.Is(err, cachedErr)
		}),
	)
	impl := cache.(*cacheImpl[int, CacheObject[int]])
	impl.now = func() time.Time { return time.UnixMilli(1000) }

	var calls atomic.Int64
	loaderFor := func(err error) CacheLoadFunc[int] {
		return func(context.Context) (int, error) {
			calls.Add(1)

			return 0, err
		}
	}

	for range 2 {
		if _, err := cache.GetOrLoad(context.Background(), "skipped", time.Minute, loaderFor(skippedErr)); !errors.Is(err, skippedErr) {
			t.Fatalf("expected error %v, got %v", skippedErr, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("expected loader to be called twice for a rejected error, got %d", calls.Load())
	}

	calls.Store(0)
	for range 2 {
		if _, err := cache.GetOrLoad(context.Background(), "cached", time.Minute, loaderFor(cachedErr)); !errors.Is(err, cachedErr) {
			t.Fatalf("expected error %v, got %v", cachedErr, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("expected loader to be called once for an accepted error, got %d", calls.Load())
	}
}

func TestCache_NegativeCacheKeepsServingCachedValue(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	cache := NewCache(
		provider,
		NoopCacheStorageCodec[int]{},
		WithNegativeCache[int, CacheObject[int]](time.Minute),
	)
	impl := cache.(*cacheImpl[int, CacheObject[int]])
	impl.now = func() time.Time { return time.UnixMilli(1000) }
	impl.random = fakeRandom(1)

	expectErr := errors.New("backend down")
	if _, err := cache.GetOrLoad(context.Background(), "answer", time.Minute, func(context.Context) (int, error) {
		return 0, expectErr
	}); !errors.Is(err, expectErr) {
		t.Fatalf("expected error %v, got %v", expectErr, err)
	}

	// A fresh cached value short-circuits before the negative cache is consulted.
	provider.items["answer"] = CacheObject[int]{Value: 42, ExpireAtMillis: 61000}

	value, err := cache.GetOrLoad(context.Background(), "answer", time.Minute, func(context.Context) (int, error) {
		return 0, expectErr
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if value != 42 {
		t.Fatalf("expected cached value 42, got %d", value)
	}
}

func TestCache_NegativeCacheHonorsRevalidationFallback(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: map[string]CacheObject[int]{
		"answer": {Value: 42, ExpireAtMillis: 2000},
	}}
	metrics := &negativeMetricsProvider{}
	cache := NewCache(
		provider,
		NoopCacheStorageCodec[int]{},
		WithDirectLoader[int, CacheObject[int]](),
		WithMetricsProvider[int, CacheObject[int]](metrics),
		WithNegativeCache[int, CacheObject[int]](time.Minute),
	)
	impl := cache.(*cacheImpl[int, CacheObject[int]])
	impl.now = func() time.Time { return time.UnixMilli(1000) }
	impl.random = fakeRandom(0)

	expectErr := errors.New("backend down")
	var calls atomic.Int64
	loader := func(context.Context) (int, error) {
		calls.Add(1)

		return 0, expectErr
	}

	for range 2 {
		value, err := cache.GetOrLoad(context.Background(), "answer", time.Minute, loader)
		if err != nil {
			t.Fatalf("expected fallback value, got %v", err)
		}
		if value != 42 {
			t.Fatalf("value = %d, want 42", value)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("loader calls = %d, want 1", calls.Load())
	}
	if metrics.hits.Load() != 1 {
		t.Fatalf("negative cache hits = %d, want 1", metrics.hits.Load())
	}
}

func TestCache_NegativeCacheClearedOnDelete(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	cache := NewCache(
		provider,
		NoopCacheStorageCodec[int]{},
		WithNegativeCache[int, CacheObject[int]](time.Minute),
	)
	impl := cache.(*cacheImpl[int, CacheObject[int]])
	impl.now = func() time.Time { return time.UnixMilli(1000) }

	expectErr := errors.New("backend down")
	if _, err := cache.GetOrLoad(context.Background(), "answer", time.Minute, func(context.Context) (int, error) {
		return 0, expectErr
	}); !errors.Is(err, expectErr) {
		t.Fatalf("expected error %v, got %v", expectErr, err)
	}

	if err := cache.Delete(context.Background(), "answer"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if _, ok := impl.negativeCache.get("answer", 1000); ok {
		t.Fatalf("expected negative entry to be removed by Delete")
	}
}

func TestCache_NegativeCacheClearedOnSet(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	cache := NewCache(
		provider,
		NoopCacheStorageCodec[int]{},
		WithNegativeCache[int, CacheObject[int]](time.Minute),
	)
	impl := cache.(*cacheImpl[int, CacheObject[int]])
	impl.now = func() time.Time { return time.UnixMilli(1000) }

	expectErr := errors.New("backend down")
	if _, err := cache.GetOrLoad(context.Background(), "answer", time.Minute, func(context.Context) (int, error) {
		return 0, expectErr
	}); !errors.Is(err, expectErr) {
		t.Fatalf("expected error %v, got %v", expectErr, err)
	}

	err := cache.Set(context.Background(), "answer", CacheObject[int]{Value: 5, ExpireAtMillis: 61000})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if _, ok := impl.negativeCache.get("answer", 1000); ok {
		t.Fatalf("expected negative entry to be removed by Set")
	}
}

func TestCache_NegativeCacheRejectsErrorFromInvalidatedLoad(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		invalidate func(Cache[int, CacheObject[int]]) error
	}{
		{
			name: "set",
			invalidate: func(cache Cache[int, CacheObject[int]]) error {
				return cache.Set(context.Background(), "answer", CacheObject[int]{
					Value:          42,
					ExpireAtMillis: 61000,
				})
			},
		},
		{
			name: "delete",
			invalidate: func(cache Cache[int, CacheObject[int]]) error {
				return cache.Delete(context.Background(), "answer")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
			cache := NewCache(
				provider,
				NoopCacheStorageCodec[int]{},
				WithDirectLoader[int, CacheObject[int]](),
				WithNegativeCache[int, CacheObject[int]](time.Minute),
			)
			impl := cache.(*cacheImpl[int, CacheObject[int]])
			impl.now = func() time.Time { return time.UnixMilli(1000) }

			started := make(chan struct{})
			release := make(chan struct{})
			expectErr := errors.New("backend down")
			done := make(chan error, 1)
			go func() {
				_, err := cache.GetOrLoad(context.Background(), "answer", time.Minute, func(context.Context) (int, error) {
					close(started)
					<-release

					return 0, expectErr
				})
				done <- err
			}()
			<-started

			if err := tt.invalidate(cache); err != nil {
				t.Fatalf("invalidate: %v", err)
			}
			close(release)
			if err := <-done; !errors.Is(err, expectErr) {
				t.Fatalf("expected load error %v, got %v", expectErr, err)
			}
			if _, ok := impl.negativeCache.get("answer", 1000); ok {
				t.Fatal("stale load error was cached after invalidation")
			}
		})
	}
}

func TestWithNegativeCache_NonPositiveTTLKeepsDisabled(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	cache := NewCache(
		provider,
		NoopCacheStorageCodec[int]{},
		WithNegativeCache[int, CacheObject[int]](time.Minute),
		WithNegativeCache[int, CacheObject[int]](0),
	)
	impl := cache.(*cacheImpl[int, CacheObject[int]])

	if impl.negativeCache != nil {
		t.Fatalf("expected negative cache to be disabled")
	}
	if impl.negativeCacheTTL != 0 {
		t.Fatalf("expected negative cache TTL 0, got %v", impl.negativeCacheTTL)
	}
}

func TestWithNegativeCache_SetsTTL(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	ttl := 3 * time.Second
	cache := NewCache(provider, NoopCacheStorageCodec[int]{}, WithNegativeCache[int, CacheObject[int]](ttl))
	impl := cache.(*cacheImpl[int, CacheObject[int]])

	if impl.negativeCache == nil {
		t.Fatalf("expected negative cache to be enabled")
	}
	if impl.negativeCacheTTL != ttl {
		t.Fatalf("expected negative cache TTL %v, got %v", ttl, impl.negativeCacheTTL)
	}
	if impl.negativeCacheCapacity != DefaultNegativeCacheCapacity {
		t.Fatalf("negative cache capacity = %d, want %d", impl.negativeCacheCapacity, DefaultNegativeCacheCapacity)
	}
}

func TestWithNegativeCacheCapacity(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	cache := NewCache(
		provider,
		NoopCacheStorageCodec[int]{},
		WithNegativeCacheCapacity[int, CacheObject[int]](3),
		WithNegativeCache[int, CacheObject[int]](time.Minute),
	)
	impl := cache.(*cacheImpl[int, CacheObject[int]])

	if impl.negativeCacheCapacity != 3 {
		t.Fatalf("negative cache capacity = %d, want 3", impl.negativeCacheCapacity)
	}
	if impl.negativeCache == nil {
		t.Fatal("expected negative cache to be enabled")
	}
	if len(impl.negativeCache.shards) != 3 {
		t.Fatalf("shards = %d, want 3", len(impl.negativeCache.shards))
	}
	totalCapacity := 0
	for _, shard := range impl.negativeCache.shards {
		totalCapacity += shard.capacity
	}
	if totalCapacity != 3 {
		t.Fatalf("total shard capacity = %d, want 3", totalCapacity)
	}

	disabled := NewCache(
		provider,
		NoopCacheStorageCodec[int]{},
		WithNegativeCache[int, CacheObject[int]](time.Minute),
		WithNegativeCacheCapacity[int, CacheObject[int]](0),
	).(*cacheImpl[int, CacheObject[int]])
	if disabled.negativeCache != nil {
		t.Fatal("expected zero capacity to disable negative caching")
	}
}

func TestWithNegativeCacheErrorPredicate_NilKeepsDefault(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	cache := NewCache(provider, NoopCacheStorageCodec[int]{}, WithNegativeCacheErrorPredicate[int, CacheObject[int]](nil))
	impl := cache.(*cacheImpl[int, CacheObject[int]])

	if impl.negativeCacheErrorPredicate == nil {
		t.Fatalf("expected default predicate to be kept")
	}
	if impl.negativeCacheErrorPredicate(context.Canceled) {
		t.Fatalf("expected default predicate behavior")
	}
}

func TestNegativeCacheStore_ExpiryAndCapacity(t *testing.T) {
	t.Parallel()

	store := newNegativeCacheStore(4)
	expectErr := errors.New("boom")

	if !setNegative(store, "key", expectErr, 1000, 2000) {
		t.Fatal("expected entry to be stored")
	}
	if err, ok := store.get("key", 1999); !ok || !errors.Is(err, expectErr) {
		t.Fatalf("expected live entry, got %v %v", err, ok)
	}
	if _, ok := store.get("key", 2000); ok {
		t.Fatalf("expected entry to be expired")
	}

	shard := store.shardFor("key")
	shard.mu.RLock()
	_, exists := shard.entries["key"]
	shard.mu.RUnlock()
	if exists {
		t.Fatalf("expected expired entry to be dropped on read")
	}

	setNegative(store, "noop", expectErr, 1000, 1000)
	if _, ok := store.get("noop", 999); ok {
		t.Fatalf("expected no entry for a non-positive TTL")
	}

	targetShard := store.shardFor("key")
	inserted := 0
	for i := 0; inserted <= targetShard.capacity; i++ {
		key := fmt.Sprintf("capacity-%d", i)
		if store.shardFor(key) != targetShard {
			continue
		}
		setNegative(store, key, expectErr, 1000, 2000)
		inserted++
	}
	targetShard.mu.RLock()
	size := len(targetShard.entries)
	targetShard.mu.RUnlock()
	if size > targetShard.capacity {
		t.Fatalf("shard size = %d, want <= %d", size, targetShard.capacity)
	}
}

func TestNegativeCacheStore_SetIfAbsentAndInvalidation(t *testing.T) {
	t.Parallel()

	store := newNegativeCacheStore(DefaultNegativeCacheCapacity)
	firstErr := errors.New("first")
	secondErr := errors.New("second")
	token := store.tokenFor("key")

	if !store.set("key", firstErr, 1000, 2000, token) {
		t.Fatal("expected first store to succeed")
	}
	if store.set("key", secondErr, 1000, 3000, token) {
		t.Fatal("expected live entry not to be replaced")
	}
	if err, ok := store.get("key", 2500); ok || err != nil {
		t.Fatalf("expected original expiry to be retained, got %v %t", err, ok)
	}

	staleToken := store.tokenFor("stale")
	store.invalidate("stale")
	if store.set("stale", firstErr, 1000, 2000, staleToken) {
		t.Fatal("expected invalidated generation not to be stored")
	}
}

func TestNegativeCacheStore_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	store := newNegativeCacheStore(DefaultNegativeCacheCapacity)
	expectErr := errors.New("boom")

	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("key-%d", i%4)
			for j := range 200 {
				setNegative(store, key, expectErr, int64(j), int64(j+2))
				store.get(key, int64(j))
				if j%20 == 0 {
					store.invalidate(key)
				}
			}
		}(i)
	}
	wg.Wait()
}
