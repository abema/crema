package crema

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

type testMetricsProvider struct {
	BaseMetricsProvider
}

func TestCache_SetSkipsExpired(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	cache := NewCache(provider, NoopSerializationCodec[int]{})
	impl := cache.(*cacheImpl[int, CacheObject[int]])
	impl.now = func() time.Time { return time.UnixMilli(1000) }

	err := cache.Set(context.Background(), "stale", CacheObject[int]{
		Value:          1,
		ExpireAtMillis: 900,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if _, ok := provider.items["stale"]; ok {
		t.Fatalf("expected expired entry not to be stored")
	}
}

func TestCache_GetOrLoadUsesCachedValue(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	provider.items["answer"] = CacheObject[int]{
		Value:          42,
		ExpireAtMillis: 2000,
	}
	cache := NewCache(provider, NoopSerializationCodec[int]{})
	impl := cache.(*cacheImpl[int, CacheObject[int]])
	impl.now = func() time.Time { return time.UnixMilli(1000) }
	impl.random = fakeRandom(1)

	var calls int32
	loader := func(context.Context) (int, error) {
		atomic.AddInt32(&calls, 1)
		return 0, nil
	}

	value, err := cache.GetOrLoad(context.Background(), "answer", time.Second, loader)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if value != 42 {
		t.Fatalf("expected cached value 42, got %d", value)
	}
	if calls != 0 {
		t.Fatalf("expected loader not to be called, got %d", calls)
	}
}

func TestCache_GetOrLoadRevalidatesExpired(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	provider.items["answer"] = CacheObject[int]{
		Value:          1,
		ExpireAtMillis: 900,
	}
	cache := NewCache(provider, NoopSerializationCodec[int]{})
	impl := cache.(*cacheImpl[int, CacheObject[int]])
	impl.now = func() time.Time { return time.UnixMilli(1000) }

	value, err := cache.GetOrLoad(context.Background(), "answer", 2*time.Second, func(context.Context) (int, error) {
		return 99, nil
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if value != 99 {
		t.Fatalf("expected loaded value 99, got %d", value)
	}
	stored, ok := provider.items["answer"]
	if !ok {
		t.Fatalf("expected refreshed cache entry to be stored")
	}
	if stored.ExpireAtMillis != 3000 {
		t.Fatalf("expected refreshed expiry 3000, got %d", stored.ExpireAtMillis)
	}
}

func TestCache_GetOrLoadLoaderErrorSkipsCache(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	cache := NewCache(provider, NoopSerializationCodec[int]{})
	impl := cache.(*cacheImpl[int, CacheObject[int]])
	impl.now = func() time.Time { return time.UnixMilli(1000) }

	expectErr := errors.New("loader failed")
	value, err := cache.GetOrLoad(context.Background(), "answer", time.Second, func(context.Context) (int, error) {
		return 0, expectErr
	})
	if err != expectErr {
		t.Fatalf("expected error %v, got %v", expectErr, err)
	}
	if value != 0 {
		t.Fatalf("expected zero value, got %d", value)
	}
	if _, ok := provider.items["answer"]; ok {
		t.Fatalf("expected no cache entry when loader fails")
	}
}

func TestCache_GetPropagatesProviderGetError(t *testing.T) {
	t.Parallel()

	expectErr := errors.New("get failed")
	provider := &errorProvider[CacheObject[int]]{getErr: expectErr}
	cache := NewCache(provider, NoopSerializationCodec[int]{})

	_, ok, err := cache.Get(context.Background(), "key")
	if err != expectErr {
		t.Fatalf("expected error %v, got %v", expectErr, err)
	}
	if ok {
		t.Fatalf("expected ok=false on error")
	}
}

func TestCache_GetOrLoadSkipsCacheOnGetError(t *testing.T) {
	t.Parallel()

	expectErr := errors.New("get failed")
	provider := &errorProvider[CacheObject[int]]{getErr: expectErr}
	cache := NewCache(provider, NoopSerializationCodec[int]{})
	impl := cache.(*cacheImpl[int, CacheObject[int]])
	impl.now = func() time.Time { return time.UnixMilli(1000) }

	var calls int32
	loader := func(context.Context) (int, error) {
		atomic.AddInt32(&calls, 1)
		return 77, nil
	}

	value, err := cache.GetOrLoad(context.Background(), "answer", time.Second, loader)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if value != 77 {
		t.Fatalf("expected value 77, got %d", value)
	}

	value, err = cache.GetOrLoad(context.Background(), "answer", time.Second, loader)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if value != 77 {
		t.Fatalf("expected value 77, got %d", value)
	}
	if calls != 2 {
		t.Fatalf("expected loader to be called twice, got %d", calls)
	}
}

func TestCache_SetPropagatesProviderSetError(t *testing.T) {
	t.Parallel()

	expectErr := errors.New("set failed")
	provider := &errorProvider[CacheObject[int]]{setErr: expectErr}
	cache := NewCache(provider, NoopSerializationCodec[int]{})
	impl := cache.(*cacheImpl[int, CacheObject[int]])
	impl.now = func() time.Time { return time.UnixMilli(1000) }

	err := cache.Set(context.Background(), "key", CacheObject[int]{
		Value:          1,
		ExpireAtMillis: 2000,
	})
	if err != expectErr {
		t.Fatalf("expected error %v, got %v", expectErr, err)
	}
}

func TestCache_DeleteRemovesEntry(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	provider.items["answer"] = CacheObject[int]{
		Value:          42,
		ExpireAtMillis: 2000,
	}
	cache := NewCache(provider, NoopSerializationCodec[int]{})

	if err := cache.Delete(context.Background(), "answer"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if _, ok := provider.items["answer"]; ok {
		t.Fatalf("expected entry to be deleted")
	}
}

func TestCache_DeletePropagatesProviderError(t *testing.T) {
	t.Parallel()

	expectErr := errors.New("delete failed")
	provider := &errorProvider[CacheObject[int]]{deleteErr: expectErr}
	cache := NewCache(provider, NoopSerializationCodec[int]{})

	if err := cache.Delete(context.Background(), "key"); err != expectErr {
		t.Fatalf("expected error %v, got %v", expectErr, err)
	}
}

func TestCache_GetOrLoadSetErrorReturnsValue(t *testing.T) {
	t.Parallel()

	expectErr := errors.New("set failed")
	provider := &errorProvider[CacheObject[int]]{setErr: expectErr}
	cache := NewCache(provider, NoopSerializationCodec[int]{})
	impl := cache.(*cacheImpl[int, CacheObject[int]])
	impl.now = func() time.Time { return time.UnixMilli(1000) }

	value, err := cache.GetOrLoad(context.Background(), "answer", time.Second, func(context.Context) (int, error) {
		return 11, nil
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if value != 11 {
		t.Fatalf("expected value 11, got %d", value)
	}
}

func TestCache_GetDecodeError(t *testing.T) {
	t.Parallel()

	provider := &byteProvider{items: make(map[string][]byte)}
	provider.items["key"] = []byte("{")
	cache := NewCache(provider, JSONByteStringCodec[func()]{})

	_, ok, err := cache.Get(context.Background(), "key")
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
	if ok {
		t.Fatalf("expected ok=false on decode error")
	}
}

func TestCache_SetEncodeError(t *testing.T) {
	t.Parallel()

	provider := &byteProvider{items: make(map[string][]byte)}
	cache := NewCache(provider, JSONByteStringCodec[func()]{})
	impl := cache.(*cacheImpl[func(), []byte])
	impl.now = func() time.Time { return time.UnixMilli(1000) }

	err := cache.Set(context.Background(), "key", CacheObject[func()]{
		Value:          func() {},
		ExpireAtMillis: 2000,
	})
	if err == nil {
		t.Fatal("expected encode error, got nil")
	}
}

func TestCache_ShouldRevalidateProbability(t *testing.T) {
	t.Parallel()

	steepness, window := calculateSteepnessAndRevalidationWindow(1000)
	cache := &cacheImpl[int, CacheObject[int]]{
		steepness:                      steepness,
		revalidationWindowMilliseconds: window,
	}

	cache.random = fakeRandom(0)
	if !cache.shouldRevalidate(0, 500) {
		t.Fatalf("expected revalidation when random draw is below probability")
	}

	cache.random = fakeRandom(1)
	if cache.shouldRevalidate(0, 500) {
		t.Fatalf("expected no revalidation when random draw is above probability")
	}

	if cache.shouldRevalidate(0, 5000) {
		t.Fatalf("expected no revalidation outside the window")
	}

	if !cache.shouldRevalidate(0, -1) {
		t.Fatalf("expected revalidation for expired entry")
	}
}

func TestCalculateSteepnessAndRevalidationWindow_Defaults(t *testing.T) {
	t.Parallel()

	steepness, window := calculateSteepnessAndRevalidationWindow(-1)
	if window <= 0 {
		t.Fatalf("expected positive revalidation window, got %d", window)
	}
	if steepness <= 0 {
		t.Fatalf("expected positive steepness, got %f", steepness)
	}
}

func TestWithLogger_SetsLogger(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	custom := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))

	cache := NewCache(provider, NoopSerializationCodec[int]{}, WithLogger[int, CacheObject[int]](custom))
	impl := cache.(*cacheImpl[int, CacheObject[int]])

	if impl.logger != custom {
		t.Fatalf("expected custom logger to be set")
	}
}

func TestWithMetricsProvider_SetsMetrics(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	metrics := &testMetricsProvider{}

	cache := NewCache(provider, NoopSerializationCodec[int]{}, WithMetricsProvider[int, CacheObject[int]](metrics))
	impl := cache.(*cacheImpl[int, CacheObject[int]])

	if impl.metrics != metrics {
		t.Fatalf("expected custom metrics provider to be set")
	}
	loader, ok := impl.internalLoader.(*singleflightLoader[int])
	if !ok {
		t.Fatalf("expected internal loader to be singleflightLoader")
	}
	if loader.metrics != metrics {
		t.Fatalf("expected loader metrics to be set")
	}
}

func TestWithMetricsProvider_WithDirectLoader(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	metrics := &testMetricsProvider{}

	cache := NewCache(
		provider,
		NoopSerializationCodec[int]{},
		WithDirectLoader[int, CacheObject[int]](),
		WithMetricsProvider[int, CacheObject[int]](metrics),
	)
	impl := cache.(*cacheImpl[int, CacheObject[int]])

	if impl.metrics != metrics {
		t.Fatalf("expected custom metrics provider to be set")
	}
	if _, ok := impl.internalLoader.(directLoader[int]); !ok {
		t.Fatalf("expected internal loader to be directLoader")
	}
}

func TestWithMetricsProvider_NilFallsBackToNoop(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	cache := NewCache(provider, NoopSerializationCodec[int]{}, WithMetricsProvider[int, CacheObject[int]](nil))
	impl := cache.(*cacheImpl[int, CacheObject[int]])

	if impl.metrics == nil {
		t.Fatalf("expected metrics provider to be set")
	}
	if _, ok := impl.metrics.(NoopMetricsProvider); !ok {
		t.Fatalf("expected NoopMetricsProvider fallback")
	}
	loader, ok := impl.internalLoader.(*singleflightLoader[int])
	if !ok {
		t.Fatalf("expected internal loader to be singleflightLoader")
	}
	if _, ok := loader.metrics.(NoopMetricsProvider); !ok {
		t.Fatalf("expected loader metrics to be NoopMetricsProvider")
	}
}

func TestWithDirectLoader_UsesDirectLoader(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	cache := NewCache(provider, NoopSerializationCodec[int]{}, WithDirectLoader[int, CacheObject[int]]())
	impl := cache.(*cacheImpl[int, CacheObject[int]])

	if _, ok := impl.internalLoader.(directLoader[int]); !ok {
		t.Fatalf("expected internal loader to be directLoader")
	}
}

func TestWithRevalidationWindow_SetsValues(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	target := 1500 * time.Millisecond
	expectedSteepness, expectedWindow := calculateSteepnessAndRevalidationWindow(target.Milliseconds())

	cache := NewCache(provider, NoopSerializationCodec[int]{}, WithRevalidationWindow[int, CacheObject[int]](target))
	impl := cache.(*cacheImpl[int, CacheObject[int]])

	if impl.steepness != expectedSteepness {
		t.Fatalf("expected steepness %f, got %f", expectedSteepness, impl.steepness)
	}
	if impl.revalidationWindowMilliseconds != expectedWindow {
		t.Fatalf("expected revalidation window %d, got %d", expectedWindow, impl.revalidationWindowMilliseconds)
	}
}

func TestWithRevalidationWindow_DefaultsOnZero(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	cache := NewCache(provider, NoopSerializationCodec[int]{}, WithRevalidationWindow[int, CacheObject[int]](0))
	impl := cache.(*cacheImpl[int, CacheObject[int]])

	if impl.steepness != 0 {
		t.Fatalf("expected steepness 0, got %f", impl.steepness)
	}
	if impl.revalidationWindowMilliseconds != 0 {
		t.Fatalf("expected revalidation window 0, got %d", impl.revalidationWindowMilliseconds)
	}
}

func TestCalculateSteepnessAndRevalidationWindow_ZeroDisables(t *testing.T) {
	t.Parallel()

	steepness, window := calculateSteepnessAndRevalidationWindow(0)
	if steepness != 0 {
		t.Fatalf("expected steepness 0, got %f", steepness)
	}
	if window != 0 {
		t.Fatalf("expected revalidation window 0, got %d", window)
	}
}

func TestNewCache_IgnoresNilOption(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("expected no panic, got %v", r)
		}
	}()

	cache := NewCache(provider, NoopSerializationCodec[int]{}, nil)
	impl := cache.(*cacheImpl[int, CacheObject[int]])

	if impl.internalLoader == nil {
		t.Fatalf("expected internal loader to be set")
	}
	if impl.logger == nil {
		t.Fatalf("expected logger to be set")
	}
}
