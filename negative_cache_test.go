package crema

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type testNegativeProvider struct {
	mu      sync.Mutex
	entries map[string]error
	ttls    []time.Duration
}

func (p *testNegativeProvider) Get(_ context.Context, key string) (error, bool, error) {
	p.mu.Lock()
	err, found := p.entries[key]
	p.mu.Unlock()

	return err, found, nil
}

func (p *testNegativeProvider) Set(_ context.Context, key string, err error, ttl time.Duration) error {
	p.mu.Lock()
	p.entries[key] = err
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

func newNegativeTestCache(t *testing.T, provider *testNegativeProvider) Cache[int] {
	t.Helper()

	return NewCache(
		&testMemoryProvider[int]{items: make(map[string]CacheObject[int])},
		NoopCacheStorageCodec[int]{},
		WithDirectLoader(),
		WithLoadErrorCacheProvider(provider, time.Second, func(error) bool { return true }),
	)
}

func TestCache_NegativeCacheProviderSuppressesReload(t *testing.T) {
	negative := &testNegativeProvider{entries: make(map[string]error)}
	cache := newNegativeTestCache(t, negative)
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

func TestCache_NegativeCacheProviderInvalidatedBySetAndDelete(t *testing.T) {
	for _, operation := range []string{"set", "delete"} {
		t.Run(operation, func(t *testing.T) {
			negative := &testNegativeProvider{entries: map[string]error{"key": errors.New("old")}}
			cache := newNegativeTestCache(t, negative)
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
	negative := &testNegativeProvider{entries: make(map[string]error)}
	cache := newNegativeTestCache(t, negative)
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
	if cache.(*cacheImpl[int, CacheObject[int]]).negativeCache != nil {
		t.Fatal("negative cache enabled without provider")
	}
}

func TestCache_NegativeCacheProviderCachesOnlyAbsentErrors(t *testing.T) {
	negative := &testNegativeProvider{entries: make(map[string]error)}
	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	notFound := errors.New("not found")
	cache := NewCache(
		provider,
		NoopCacheStorageCodec[int]{},
		WithDirectLoader(),
		WithNegativeCacheProvider(
			negative,
			time.Second,
			func(err error) bool { return errors.Is(err, notFound) },
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
