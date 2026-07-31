package crema

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testMetricsProvider struct {
	BaseMetricsProvider

	loads       atomic.Int64
	loadErrors  atomic.Int64
	mu          sync.Mutex
	reasons     []LoadReason
	concurrency []int
}

func (m *testMetricsProvider) RecordLoad(context.Context) {
	m.loads.Add(1)
}

func (m *testMetricsProvider) RecordLoadError(context.Context) {
	m.loadErrors.Add(1)
}

func (m *testMetricsProvider) RecordLoadReason(_ context.Context, reason LoadReason) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reasons = append(m.reasons, reason)
}

func (m *testMetricsProvider) RecordLoadConcurrency(_ context.Context, concurrency int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.concurrency = append(m.concurrency, concurrency)
}

func (m *testMetricsProvider) recordedReasons() []LoadReason {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]LoadReason(nil), m.reasons...)
}

func (m *testMetricsProvider) recordedConcurrency() []int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]int(nil), m.concurrency...)
}

type countingMetricsProvider struct {
	BaseMetricsProvider

	loadErrors int32
}

func (m *countingMetricsProvider) RecordLoadError(context.Context) {
	atomic.AddInt32(&m.loadErrors, 1)
}

func TestCache_SetSkipsExpired(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	var encodeCalls atomic.Int32
	cache := NewCache(provider, countingIntCacheStorageCodec{encodeCalls: &encodeCalls})
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
	if got := encodeCalls.Load(); got != 0 {
		t.Fatalf("expected expired entry not to be encoded, got %d calls", got)
	}
}

func TestCache_SetUsesTTLFromInitialCheck(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	var encodeCalls atomic.Int32
	cache := NewCache(provider, countingIntCacheStorageCodec{encodeCalls: &encodeCalls})
	impl := cache.(*cacheImpl[int, CacheObject[int]])
	var nowCalls int
	impl.now = func() time.Time {
		nowCalls++
		if nowCalls == 1 {
			return time.UnixMilli(1000)
		}

		return time.UnixMilli(2000)
	}

	err := cache.Set(context.Background(), "stale", CacheObject[int]{
		Value:          1,
		ExpireAtMillis: 1500,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if _, ok := provider.items["stale"]; !ok {
		t.Fatalf("expected entry to be stored with the initial TTL")
	}
	if got := encodeCalls.Load(); got != 1 {
		t.Fatalf("expected one encode call, got %d", got)
	}
	if nowCalls != 1 {
		t.Fatalf("expected one time check, got %d", nowCalls)
	}
}

type countingIntCacheStorageCodec struct {
	encodeCalls *atomic.Int32
}

func (c countingIntCacheStorageCodec) Encode(value CacheObject[int]) (CacheObject[int], error) {
	c.encodeCalls.Add(1)

	return value, nil
}

func (countingIntCacheStorageCodec) Decode(value CacheObject[int]) (CacheObject[int], error) {
	return value, nil
}

func TestCache_GetOrLoadUsesCachedValue(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	provider.items["answer"] = CacheObject[int]{
		Value:          42,
		ExpireAtMillis: 2000,
	}
	cache := NewCache(provider, NoopCacheStorageCodec[int]{})
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
	cache := NewCache(provider, NoopCacheStorageCodec[int]{})
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
	cache := NewCache(provider, NoopCacheStorageCodec[int]{})
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

func TestCache_GetOrLoadRecordsLoadReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		entry      *CacheObject[int]
		random     float64
		wantLoads  int64
		wantReason LoadReason
	}{
		{name: "miss", wantLoads: 1, wantReason: LoadReasonMiss},
		{
			name:       "expired",
			entry:      &CacheObject[int]{Value: 1, ExpireAtMillis: 900},
			wantLoads:  1,
			wantReason: LoadReasonExpired,
		},
		{
			name:       "revalidation",
			entry:      &CacheObject[int]{Value: 1, ExpireAtMillis: 1100},
			random:     0,
			wantLoads:  1,
			wantReason: LoadReasonRevalidation,
		},
		{
			name:   "fresh",
			entry:  &CacheObject[int]{Value: 1, ExpireAtMillis: 2000},
			random: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
			if tt.entry != nil {
				provider.items["answer"] = *tt.entry
			}
			metrics := &testMetricsProvider{}
			cache := NewCache(
				provider,
				NoopCacheStorageCodec[int]{},
				WithMetricsProvider(metrics),
			)
			impl := cache.(*cacheImpl[int, CacheObject[int]])
			impl.now = func() time.Time { return time.UnixMilli(1000) }
			impl.random = fakeRandom(tt.random)

			if _, err := cache.GetOrLoad(context.Background(), "answer", time.Second, func(context.Context) (int, error) {
				return 42, nil
			}); err != nil {
				t.Fatalf("get or load: %v", err)
			}

			if got := metrics.loads.Load(); got != tt.wantLoads {
				t.Fatalf("loads = %d, want %d", got, tt.wantLoads)
			}
			reasons := metrics.recordedReasons()
			if tt.wantLoads == 0 {
				if len(reasons) != 0 {
					t.Fatalf("reasons = %v, want none", reasons)
				}

				return
			}
			if len(reasons) != 1 || reasons[0] != tt.wantReason {
				t.Fatalf("reasons = %v, want [%v]", reasons, tt.wantReason)
			}
		})
	}
}

func TestCache_GetOrLoadRecordsLoadError(t *testing.T) {
	t.Parallel()

	metrics := &testMetricsProvider{}
	cache := NewCache(
		&testMemoryProvider[int]{items: make(map[string]CacheObject[int])},
		NoopCacheStorageCodec[int]{},
		WithMetricsProvider(metrics),
	)
	expectErr := errors.New("loader failed")

	if _, err := cache.GetOrLoad(context.Background(), "answer", time.Second, func(context.Context) (int, error) {
		return 0, expectErr
	}); err != expectErr {
		t.Fatalf("expected error %v, got %v", expectErr, err)
	}
	if got := metrics.loadErrors.Load(); got != 1 {
		t.Fatalf("load errors = %d, want 1", got)
	}
}

func TestCache_GetOrLoadRevalidationFallbackReturnsCachedValue(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	provider.items["answer"] = CacheObject[int]{
		Value:          42,
		ExpireAtMillis: 2000,
	}
	metrics := &countingMetricsProvider{}
	logs := &bytes.Buffer{}
	cache := NewCache(provider, NoopCacheStorageCodec[int]{},
		WithRevalidationFallback(true),
		WithDirectLoader(),
		WithMetricsProvider(metrics),
		WithLogger(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn}))),
	)
	impl := cache.(*cacheImpl[int, CacheObject[int]])
	impl.now = func() time.Time { return time.UnixMilli(1000) }
	impl.random = fakeRandom(0)

	expectErr := errors.New("loader failed")
	value, err := cache.GetOrLoad(context.Background(), "answer", time.Second, func(context.Context) (int, error) {
		return 0, expectErr
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if value != 42 {
		t.Fatalf("expected stale value 42, got %d", value)
	}
	if got := atomic.LoadInt32(&metrics.loadErrors); got != 1 {
		t.Fatalf("expected 1 load error recorded, got %d", got)
	}
	if !strings.Contains(logs.String(), "falling back to cached value") {
		t.Fatalf("expected fallback warning, got %q", logs.String())
	}
	stored, ok := provider.items["answer"]
	if !ok || stored.Value != 42 || stored.ExpireAtMillis != 2000 {
		t.Fatalf("expected cache entry to be untouched, got %+v (ok=%t)", stored, ok)
	}
}

func TestCache_GetOrLoadRevalidationFallbackExpiredReturnsError(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	provider.items["answer"] = CacheObject[int]{
		Value:          42,
		ExpireAtMillis: 900,
	}
	metrics := &countingMetricsProvider{}
	cache := NewCache(provider, NoopCacheStorageCodec[int]{},
		WithRevalidationFallback(true),
		WithMetricsProvider(metrics),
	)
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
	if got := atomic.LoadInt32(&metrics.loadErrors); got != 1 {
		t.Fatalf("expected 1 load error recorded, got %d", got)
	}
}

func TestCache_GetOrLoadRevalidationFallbackExpiresDuringLoad(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: map[string]CacheObject[int]{
		"answer": {Value: 42, ExpireAtMillis: 2000},
	}}
	cache := NewCache(provider, NoopCacheStorageCodec[int]{})
	impl := cache.(*cacheImpl[int, CacheObject[int]])
	var nowMillis atomic.Int64
	nowMillis.Store(1000)
	impl.now = func() time.Time { return time.UnixMilli(nowMillis.Load()) }
	impl.random = fakeRandom(0)

	expectErr := errors.New("loader failed")
	value, err := cache.GetOrLoad(context.Background(), "answer", time.Second, func(context.Context) (int, error) {
		nowMillis.Store(2000)

		return 0, expectErr
	})
	if err != expectErr {
		t.Fatalf("expected error %v, got %v", expectErr, err)
	}
	if value != 0 {
		t.Fatalf("expected zero value, got %d", value)
	}
}

func TestCache_GetOrLoadRevalidationFallbackPropagatesCancellation(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: map[string]CacheObject[int]{
		"answer": {Value: 42, ExpireAtMillis: 2000},
	}}
	cache := NewCache(provider, NoopCacheStorageCodec[int]{}, WithDirectLoader())
	impl := cache.(*cacheImpl[int, CacheObject[int]])
	impl.now = func() time.Time { return time.UnixMilli(1000) }
	impl.random = fakeRandom(0)

	ctx, cancel := context.WithCancel(context.Background())
	value, err := cache.GetOrLoad(ctx, "answer", time.Second, func(context.Context) (int, error) {
		cancel()

		return 0, context.Canceled
	})
	if err != context.Canceled {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if value != 0 {
		t.Fatalf("expected zero value, got %d", value)
	}
}

func TestCache_GetOrLoadRevalidationFallbackEnabledByDefault(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	provider.items["answer"] = CacheObject[int]{
		Value:          42,
		ExpireAtMillis: 2000,
	}
	cache := NewCache(provider, NoopCacheStorageCodec[int]{})
	impl := cache.(*cacheImpl[int, CacheObject[int]])
	impl.now = func() time.Time { return time.UnixMilli(1000) }
	impl.random = fakeRandom(0)

	value, err := cache.GetOrLoad(context.Background(), "answer", time.Second, func(context.Context) (int, error) {
		return 0, errors.New("loader failed")
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if value != 42 {
		t.Fatalf("expected stale value 42, got %d", value)
	}
}

func TestCache_GetOrLoadRevalidationFallbackDisabledPropagatesError(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	provider.items["answer"] = CacheObject[int]{
		Value:          42,
		ExpireAtMillis: 2000,
	}
	cache := NewCache(provider, NoopCacheStorageCodec[int]{}, WithRevalidationFallback(false))
	impl := cache.(*cacheImpl[int, CacheObject[int]])
	impl.now = func() time.Time { return time.UnixMilli(1000) }
	impl.random = fakeRandom(0)

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
}

func TestCache_GetPropagatesProviderGetError(t *testing.T) {
	t.Parallel()

	expectErr := errors.New("get failed")
	provider := &errorProvider[CacheObject[int]]{getErr: expectErr}
	cache := NewCache(provider, NoopCacheStorageCodec[int]{})

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
	metrics := &testMetricsProvider{}
	cache := NewCache(
		provider,
		NoopCacheStorageCodec[int]{},
		WithMetricsProvider(metrics),
	)
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
	reasons := metrics.recordedReasons()
	if len(reasons) != 2 || reasons[0] != LoadReasonGetError || reasons[1] != LoadReasonGetError {
		t.Fatalf("reasons = %v, want two get errors", reasons)
	}
}

func TestCache_SetPropagatesProviderSetError(t *testing.T) {
	t.Parallel()

	expectErr := errors.New("set failed")
	provider := &errorProvider[CacheObject[int]]{setErr: expectErr}
	cache := NewCache(provider, NoopCacheStorageCodec[int]{})
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
	cache := NewCache(provider, NoopCacheStorageCodec[int]{})

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
	cache := NewCache(provider, NoopCacheStorageCodec[int]{})

	if err := cache.Delete(context.Background(), "key"); err != expectErr {
		t.Fatalf("expected error %v, got %v", expectErr, err)
	}
}

func TestCache_GetOrLoadSetErrorReturnsValue(t *testing.T) {
	t.Parallel()

	expectErr := errors.New("set failed")
	provider := &errorProvider[CacheObject[int]]{setErr: expectErr}
	cache := NewCache(provider, NoopCacheStorageCodec[int]{})
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

func TestCache_ShouldRevalidateProbabilityBoundaries(t *testing.T) {
	t.Parallel()

	steepness, window := calculateSteepnessAndRevalidationWindow(1000)
	cache := &cacheImpl[int, CacheObject[int]]{
		steepness:                      steepness,
		revalidationWindowMilliseconds: window,
	}

	cache.random = fakeRandom(0)
	if cache.shouldRevalidate(0, window) {
		t.Fatalf("expected no revalidation at the window boundary")
	}

	for _, draw := range []float64{0.1, 0.5, 0.9} {
		cache.random = fakeRandom(draw)
		started := false
		for remain := window; remain > 0; remain-- {
			if cache.shouldRevalidate(0, remain) {
				started = true

				continue
			}
			if started {
				t.Fatalf("expected probability to grow toward expiry, draw %f stopped revalidating at remain %d", draw, remain)
			}
		}
		if !started {
			t.Fatalf("expected draw %f to revalidate before expiry", draw)
		}
	}

	cache.random = fakeRandom(0.998)
	if !cache.shouldRevalidate(0, 1) {
		t.Fatalf("expected revalidation just before expiry")
	}
	cache.random = fakeRandom(0.9995)
	if cache.shouldRevalidate(0, 1) {
		t.Fatalf("expected no revalidation when random draw exceeds probability")
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

func TestCalculateSteepnessAndRevalidationWindow_UsesConfiguredWindow(t *testing.T) {
	t.Parallel()

	const configuredWindow = int64(1000)
	steepness, window := calculateSteepnessAndRevalidationWindow(configuredWindow)
	if window != configuredWindow {
		t.Fatalf("expected window %d, got %d", configuredWindow, window)
	}

	const targetProbability = 0.999
	probabilityAtExpiry := 1 - math.Exp(-steepness*float64(window))
	if math.Abs(probabilityAtExpiry-targetProbability) > 1e-12 {
		t.Fatalf("expected probability %f, got %f", targetProbability, probabilityAtExpiry)
	}
}

func TestWithLogger_SetsLogger(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	custom := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))

	cache := NewCache(provider, NoopCacheStorageCodec[int]{}, WithLogger(custom))
	impl := cache.(*cacheImpl[int, CacheObject[int]])

	if impl.logger != custom {
		t.Fatalf("expected custom logger to be set")
	}
}

func TestWithMetricsProvider_SetsMetrics(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	metrics := &testMetricsProvider{}

	cache := NewCache(provider, NoopCacheStorageCodec[int]{}, WithMetricsProvider(metrics))
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
		NoopCacheStorageCodec[int]{},
		WithDirectLoader(),
		WithMetricsProvider(metrics),
	)
	impl := cache.(*cacheImpl[int, CacheObject[int]])

	if impl.metrics != metrics {
		t.Fatalf("expected custom metrics provider to be set")
	}
	loader, ok := impl.internalLoader.(directLoader[int])
	if !ok {
		t.Fatalf("expected internal loader to be directLoader")
	}
	if loader.metrics != metrics {
		t.Fatalf("expected loader metrics to be set")
	}
}

func TestWithMetricsProvider_BeforeLoaderOptions(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	metrics := &testMetricsProvider{}

	cache := NewCache(
		provider,
		NoopCacheStorageCodec[int]{},
		WithMetricsProvider(metrics),
		WithMaxLoadTimeout(time.Minute),
	)
	impl := cache.(*cacheImpl[int, CacheObject[int]])

	loader, ok := impl.internalLoader.(*singleflightLoader[int])
	if !ok {
		t.Fatalf("expected internal loader to be singleflightLoader")
	}
	if loader.metrics != metrics {
		t.Fatalf("expected loader metrics to be set regardless of option order")
	}
	if loader.maxLoadTimeout != time.Minute {
		t.Fatalf("expected max load timeout to be set, got %v", loader.maxLoadTimeout)
	}
}

func TestDirectLoader_RecordsLoadMetrics(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	provider.items["answer"] = CacheObject[int]{
		Value:          1,
		ExpireAtMillis: 900,
	}
	metrics := &testMetricsProvider{}
	cache := NewCache(
		provider,
		NoopCacheStorageCodec[int]{},
		WithDirectLoader(),
		WithMetricsProvider(metrics),
	)
	impl := cache.(*cacheImpl[int, CacheObject[int]])
	impl.now = func() time.Time { return time.UnixMilli(1000) }

	expectErr := errors.New("loader failed")
	_, err := cache.GetOrLoad(context.Background(), "answer", time.Second, func(context.Context) (int, error) {
		return 0, expectErr
	})
	if err != expectErr {
		t.Fatalf("expected error %v, got %v", expectErr, err)
	}

	if got := metrics.loads.Load(); got != 1 {
		t.Fatalf("expected 1 load, got %d", got)
	}
	if got := metrics.loadErrors.Load(); got != 1 {
		t.Fatalf("expected 1 load error, got %d", got)
	}
	if reasons := metrics.recordedReasons(); len(reasons) != 1 || reasons[0] != LoadReasonExpired {
		t.Fatalf("expected reasons [%v], got %v", LoadReasonExpired, reasons)
	}
	if got := metrics.recordedConcurrency(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("expected concurrency [1], got %v", got)
	}
}

func TestWithMetricsProvider_NilFallsBackToNoop(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	cache := NewCache(provider, NoopCacheStorageCodec[int]{}, WithMetricsProvider(nil))
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
	cache := NewCache(provider, NoopCacheStorageCodec[int]{}, WithDirectLoader())
	impl := cache.(*cacheImpl[int, CacheObject[int]])

	if _, ok := impl.internalLoader.(directLoader[int]); !ok {
		t.Fatalf("expected internal loader to be directLoader")
	}
}

func TestWithMaxLoadTimeout_SetsSingleflightTimeout(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	timeout := 1500 * time.Millisecond
	cache := NewCache(provider, NoopCacheStorageCodec[int]{}, WithMaxLoadTimeout(timeout))
	impl := cache.(*cacheImpl[int, CacheObject[int]])

	if impl.maxLoadTimeout != timeout {
		t.Fatalf("expected maxLoadTimeout %v, got %v", timeout, impl.maxLoadTimeout)
	}
	loader, ok := impl.internalLoader.(*singleflightLoader[int])
	if !ok {
		t.Fatalf("expected internal loader to be singleflightLoader")
	}
	if loader.maxLoadTimeout != timeout {
		t.Fatalf("expected loader maxLoadTimeout %v, got %v", timeout, loader.maxLoadTimeout)
	}
}

func TestWithRevalidationWindow_SetsValues(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	target := 1500 * time.Millisecond
	expectedSteepness, expectedWindow := calculateSteepnessAndRevalidationWindow(target.Milliseconds())

	cache := NewCache(provider, NoopCacheStorageCodec[int]{}, WithRevalidationWindow(target))
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
	cache := NewCache(provider, NoopCacheStorageCodec[int]{}, WithRevalidationWindow(0))
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

	cache := NewCache(provider, NoopCacheStorageCodec[int]{}, nil)
	impl := cache.(*cacheImpl[int, CacheObject[int]])

	if impl.internalLoader == nil {
		t.Fatalf("expected internal loader to be set")
	}
	if impl.logger == nil {
		t.Fatalf("expected logger to be set")
	}
}
