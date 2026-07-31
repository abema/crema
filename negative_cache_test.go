package crema

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type testLoadErrorProvider struct {
	mu      sync.Mutex
	entries map[string]error
	ttls    []time.Duration
}

func (p *testLoadErrorProvider) Get(_ context.Context, key string) (error, bool, error) {
	p.mu.Lock()
	err, found := p.entries[key]
	p.mu.Unlock()

	return err, found, nil
}

func (p *testLoadErrorProvider) Set(_ context.Context, key string, err error, ttl time.Duration) error {
	p.mu.Lock()
	p.entries[key] = err
	p.ttls = append(p.ttls, ttl)
	p.mu.Unlock()

	return nil
}

func (p *testLoadErrorProvider) Delete(_ context.Context, key string) error {
	p.mu.Lock()
	delete(p.entries, key)
	p.mu.Unlock()

	return nil
}

func newLoadErrorTestCache(t *testing.T, provider *testLoadErrorProvider) Cache[int] {
	t.Helper()

	return NewCache(
		&testMemoryProvider[int]{items: make(map[string]CacheObject[int])},
		NoopCacheStorageCodec[int]{},
		WithDirectLoader(),
		WithLoadErrorCacheProvider(provider, time.Second, func(error) bool { return true }),
	)
}

func TestCache_NegativeCacheProviderSuppressesReload(t *testing.T) {
	negative := &testLoadErrorProvider{entries: make(map[string]error)}
	cache := newLoadErrorTestCache(t, negative)
	want := errors.New("backend down")
	calls := 0
	loader := func(context.Context) (int, error) {
		calls++

		return 0, want
	}
	for range 2 {
		if _, err := cache.GetOrLoad(context.Background(), "key", time.Minute, loader); !errors.Is(err, want) {
			t.Fatalf("GetOrLoad() error = %v, want %v", err, want)
		}
	}
	if calls != 1 || len(negative.ttls) != 1 || negative.ttls[0] != time.Second {
		t.Fatalf("calls = %d, ttls = %v", calls, negative.ttls)
	}
}

func TestCache_LoadErrorCacheInvalidatedBySetAndDelete(t *testing.T) {
	for _, operation := range []string{"set", "delete"} {
		t.Run(operation, func(t *testing.T) {
			negative := &testLoadErrorProvider{entries: map[string]error{"key": errors.New("old")}}
			cache := newLoadErrorTestCache(t, negative)
			if operation == "set" {
				err := cache.Set(context.Background(), "key", CacheObject[int]{Value: 1, ExpireAtMillis: time.Now().Add(time.Minute).UnixMilli()})
				if err != nil {
					t.Fatal(err)
				}
			} else if err := cache.Delete(context.Background(), "key"); err != nil {
				t.Fatal(err)
			}
			if _, found, _ := negative.Get(context.Background(), "key"); found {
				t.Fatal("negative entry remained")
			}
		})
	}
}

func TestCache_NegativeCacheProviderInvalidatedBySetAndDelete(t *testing.T) {
	for _, operation := range []string{"set", "delete"} {
		t.Run(operation, func(t *testing.T) {
			negative := &testNegativeProvider{entries: map[string]CacheLoadResult[int]{"key": {Value: 0}}}
			cache := NewCache(
				&testMemoryProvider[int]{items: make(map[string]CacheObject[int])},
				NoopCacheStorageCodec[int]{},
				WithNegativeCacheProvider(negative, time.Second, func(value int, err error) bool {
					return err == nil && value == 0
				}),
			)
			if operation == "set" {
				err := cache.Set(context.Background(), "key", CacheObject[int]{Value: 1, ExpireAtMillis: time.Now().Add(time.Minute).UnixMilli()})
				if err != nil {
					t.Fatal(err)
				}
			} else if err := cache.Delete(context.Background(), "key"); err != nil {
				t.Fatal(err)
			}
			if _, found, _ := negative.Get(context.Background(), "key"); found {
				t.Fatal("negative entry remained")
			}
		})
	}
}

func TestCache_NegativeCacheProviderRejectsInvalidatedLoad(t *testing.T) {
	negative := &testLoadErrorProvider{entries: make(map[string]error)}
	cache := newLoadErrorTestCache(t, negative)
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	want := errors.New("backend down")
	go func() {
		_, err := cache.GetOrLoad(context.Background(), "key", time.Minute, func(context.Context) (int, error) {
			close(started)
			<-release

			return 0, want
		})
		done <- err
	}()
	<-started
	if err := cache.Delete(context.Background(), "key"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, want) {
		t.Fatalf("loader error = %v, want %v", err, want)
	}
	if _, found, _ := negative.Get(context.Background(), "key"); found {
		t.Fatal("stale loader error was cached")
	}
}

func TestWithNegativeCacheProviderDisabled(t *testing.T) {
	cache := NewCache(
		&testMemoryProvider[int]{items: make(map[string]CacheObject[int])},
		NoopCacheStorageCodec[int]{},
		WithLoadErrorCacheProvider(nil, time.Second, nil),
	)
	if cache.(*cacheImpl[int, CacheObject[int]]).loadErrorCache != nil {
		t.Fatal("load-error cache enabled without provider")
	}
}

type testNegativeProvider struct {
	mu      sync.Mutex
	entries map[string]CacheLoadResult[int]
	ttls    []time.Duration
}

func (p *testNegativeProvider) Get(_ context.Context, key string) (CacheLoadResult[int], bool, error) {
	p.mu.Lock()
	result, found := p.entries[key]
	p.mu.Unlock()

	return result, found, nil
}

func (p *testNegativeProvider) Set(_ context.Context, key string, result CacheLoadResult[int], ttl time.Duration) error {
	p.mu.Lock()
	p.entries[key] = result
	p.ttls = append(p.ttls, ttl)
	p.mu.Unlock()

	return nil
}

func (p *testNegativeProvider) Delete(_ context.Context, key string) error {
	p.mu.Lock()
	delete(p.entries, key)
	p.mu.Unlock()

	return nil
}

func TestCache_NegativeCacheProviderCachesOnlyAbsentResults(t *testing.T) {
	negative := &testNegativeProvider{entries: make(map[string]CacheLoadResult[int])}
	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	notFound := errors.New("not found")
	cache := NewCache(
		provider,
		NoopCacheStorageCodec[int]{},
		WithDirectLoader(),
		WithNegativeCacheProvider(
			negative,
			time.Second,
			func(_ int, err error) bool { return errors.Is(err, notFound) },
		),
	)
	for _, test := range []struct {
		key string
		err error
	}{
		{key: "absent", err: notFound},
		{key: "failed", err: errors.New("backend down")},
	} {
		calls := 0
		for range 2 {
			_, err := cache.GetOrLoad(context.Background(), test.key, time.Minute, func(context.Context) (int, error) {
				calls++

				return 0, test.err
			})
			if !errors.Is(err, test.err) {
				t.Fatalf("GetOrLoad(%q) error = %v, want %v", test.key, err, test.err)
			}
		}
		wantCalls := 2
		if errors.Is(test.err, notFound) {
			wantCalls = 1
		}
		if calls != wantCalls {
			t.Fatalf("loader calls for %q = %d, want %d", test.key, calls, wantCalls)
		}
	}
}

func TestCache_NegativeCacheProviderCachesAbsentValue(t *testing.T) {
	negative := &testNegativeProvider{entries: make(map[string]CacheLoadResult[int])}
	cache := NewCache(
		&testMemoryProvider[int]{items: make(map[string]CacheObject[int])},
		NoopCacheStorageCodec[int]{},
		WithDirectLoader(),
		WithNegativeCacheProvider(negative, time.Second, func(value int, err error) bool {
			return err == nil && value == 0
		}),
	)

	calls := 0
	for range 2 {
		value, err := cache.GetOrLoad(context.Background(), "missing", time.Minute, func(context.Context) (int, error) {
			calls++

			return 0, nil
		})
		if err != nil || value != 0 {
			t.Fatalf("GetOrLoad() = (%d, %v), want (0, nil)", value, err)
		}
	}
	if calls != 1 {
		t.Fatalf("loader calls = %d, want 1", calls)
	}
}

func TestCache_NegativeAndLoadErrorCachesCoexist(t *testing.T) {
	negative := &testNegativeProvider{entries: make(map[string]CacheLoadResult[int])}
	loadErrors := &testLoadErrorProvider{entries: make(map[string]error)}
	backendDown := errors.New("backend down")
	cache := NewCache(
		&testMemoryProvider[int]{items: make(map[string]CacheObject[int])},
		NoopCacheStorageCodec[int]{},
		WithDirectLoader(),
		WithNegativeCacheProvider(negative, time.Second, func(value int, err error) bool {
			return err == nil && value == 0
		}),
		WithLoadErrorCacheProvider(loadErrors, time.Second, func(err error) bool {
			return errors.Is(err, backendDown)
		}),
	)

	for _, test := range []struct {
		key  string
		load CacheLoadFunc[int]
	}{
		{
			key: "missing",
			load: func(context.Context) (int, error) {
				return 0, nil
			},
		},
		{
			key: "failed",
			load: func(context.Context) (int, error) {
				return 0, backendDown
			},
		},
	} {
		calls := 0
		for range 2 {
			_, _ = cache.GetOrLoad(context.Background(), test.key, time.Minute, func(ctx context.Context) (int, error) {
				calls++

				return test.load(ctx)
			})
		}
		if calls != 1 {
			t.Fatalf("loader calls for %q = %d, want 1", test.key, calls)
		}
	}
}
