package ristretto

import (
	"context"
	"testing"
	"time"

	dgraphristretto "github.com/dgraph-io/ristretto"
)

func newTestCache(t *testing.T) *dgraphristretto.Cache {
	t.Helper()

	cache, err := dgraphristretto.NewCache(&dgraphristretto.Config{
		NumCounters: 1e4,
		MaxCost:     1 << 20,
		BufferItems: 64,
	})
	if err != nil {
		t.Fatalf("create cache: %v", err)
	}

	return cache
}

func TestNewRistrettoCacheProvider_NilCache(t *testing.T) {
	t.Parallel()

	if _, err := NewRistrettoCacheProvider[[]byte](nil); err == nil {
		t.Fatal("expected error for nil cache")
	}
}

func TestNewRistrettoCacheProvider_NilOption(t *testing.T) {
	t.Parallel()

	cache := newTestCache(t)

	provider, err := NewRistrettoCacheProvider[[]byte](cache, nil)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if provider == nil {
		t.Fatal("expected provider to be created")
	}
}

func TestRistrettoCacheProvider_GetSetDelete(t *testing.T) {
	t.Parallel()

	cache := newTestCache(t)
	provider, err := NewRistrettoCacheProvider[[]byte](cache)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}

	ctx := context.Background()
	if err := provider.Set(ctx, "key", []byte("value"), 0); err != nil {
		t.Fatalf("set: %v", err)
	}
	cache.Wait()

	value, ok, err := provider.Get(ctx, "key")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ok {
		t.Fatal("expected value to exist")
	}
	if string(value) != "value" {
		t.Fatalf("unexpected value: %q", value)
	}

	if err := provider.Delete(ctx, "key"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	cache.Wait()

	_, ok, err = provider.Get(ctx, "key")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if ok {
		t.Fatal("expected value to be deleted")
	}
}

func TestRistrettoCacheProvider_TTL(t *testing.T) {
	t.Parallel()

	cache := newTestCache(t)
	provider, err := NewRistrettoCacheProvider[[]byte](cache)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}

	ctx := context.Background()
	if err := provider.Set(ctx, "key", []byte("value"), 20*time.Millisecond); err != nil {
		t.Fatalf("set: %v", err)
	}
	cache.Wait()

	time.Sleep(40 * time.Millisecond)

	_, ok, err := provider.Get(ctx, "key")
	if err != nil {
		t.Fatalf("get after ttl: %v", err)
	}
	if ok {
		t.Fatal("expected value to expire")
	}
}

func TestRistrettoCacheProvider_UnexpectedType(t *testing.T) {
	t.Parallel()

	cache := newTestCache(t)
	provider, err := NewRistrettoCacheProvider[[]byte](cache)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}

	cache.Set("key", "value", 1)
	cache.Wait()

	_, ok, err := provider.Get(context.Background(), "key")
	if err != ErrUnexpectedCacheValueType {
		t.Fatalf("expected ErrUnexpectedCacheValueType, got %v", err)
	}
	if ok {
		t.Fatal("expected ok=false for unexpected type")
	}
}

func TestRistrettoCacheProvider_WithCostFunc(t *testing.T) {
	t.Parallel()

	cache := newTestCache(t)

	var called bool
	provider, err := NewRistrettoCacheProvider[[]byte](cache, WithCostFunc(func(value []byte) int64 {
		called = true

		return int64(len(value))
	}))
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}

	if err := provider.Set(context.Background(), "key", []byte("value"), 0); err != nil {
		t.Fatalf("set: %v", err)
	}
	cache.Wait()

	if !called {
		t.Fatal("expected cost func to be called")
	}
}

func TestRistrettoCacheProvider_ZeroCostUsesDefault(t *testing.T) {
	t.Parallel()

	cache := newTestCache(t)
	provider, err := NewRistrettoCacheProvider[[]byte](cache, WithCostFunc(func([]byte) int64 {
		return 0
	}))
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}

	if err := provider.Set(context.Background(), "key", []byte("value"), 0); err != nil {
		t.Fatalf("set: %v", err)
	}
	cache.Wait()

	_, ok, err := provider.Get(context.Background(), "key")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ok {
		t.Fatal("expected value to exist")
	}
}

func TestRistrettoCacheProvider_SetRejected(t *testing.T) {
	t.Parallel()

	cache := newTestCache(t)
	cache.Close()

	provider, err := NewRistrettoCacheProvider[[]byte](cache)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}

	if err := provider.Set(context.Background(), "key", []byte("value"), 0); err != nil {
		t.Fatalf("set: %v", err)
	}

	_, ok, err := provider.Get(context.Background(), "key")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if ok {
		t.Fatal("expected value to be rejected")
	}
}
