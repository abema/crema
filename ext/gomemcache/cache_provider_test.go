package gomemcache

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemcachedCacheProvider_GetSetDelete(t *testing.T) {
	t.Parallel()

	client := newTestMemcacheClient()
	provider := NewMemcachedCacheProvider(client)
	ctx := context.Background()

	if err := provider.Set(ctx, "key", []byte("value"), 0); err != nil {
		t.Fatalf("set: %v", err)
	}

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

	_, ok, err = provider.Get(ctx, "key")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if ok {
		t.Fatal("expected value to be deleted")
	}
}

func TestMemcachedCacheProvider_TTL(t *testing.T) {
	t.Parallel()

	client := newTestMemcacheClient()
	provider := NewMemcachedCacheProvider(client)
	ctx := context.Background()

	if err := provider.Set(ctx, "key", []byte("value"), time.Second); err != nil {
		t.Fatalf("set: %v", err)
	}

	_, ok, err := provider.Get(ctx, "key")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ok {
		t.Fatal("expected value to exist")
	}

	time.Sleep(1100 * time.Millisecond)

	_, ok, err = provider.Get(ctx, "key")
	if err != nil {
		t.Fatalf("get after ttl: %v", err)
	}
	if ok {
		t.Fatal("expected value to expire")
	}
}

func TestMemcachedCacheProvider_GetError(t *testing.T) {
	t.Parallel()

	provider := &MemcachedCacheProvider{
		client: &testMemcacheClient{getErr: errors.New("get failed")},
	}

	_, ok, err := provider.Get(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error")
	}
	if ok {
		t.Fatal("expected ok to be false")
	}
}

func TestMemcachedCacheProvider_GetNilItem(t *testing.T) {
	t.Parallel()

	provider := &MemcachedCacheProvider{
		client: &testMemcacheClient{getItem: nil},
	}

	_, ok, err := provider.Get(context.Background(), "key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected ok to be false")
	}
}

func TestMemcachedCacheProvider_DeleteError(t *testing.T) {
	t.Parallel()

	provider := &MemcachedCacheProvider{
		client: &testMemcacheClient{deleteErr: errors.New("delete failed")},
	}

	if err := provider.Delete(context.Background(), "key"); err == nil {
		t.Fatal("expected error")
	}
}

func TestTTLSeconds_RoundsUpAndClamps(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ttl  time.Duration
		want int32
	}{
		{name: "zero", ttl: 0, want: 1},
		{name: "negative", ttl: -time.Second, want: 1},
		{name: "fractional", ttl: 1500 * time.Millisecond, want: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ttlSeconds(tt.ttl); got != tt.want {
				t.Fatalf("ttlSeconds(%v) = %d, want %d", tt.ttl, got, tt.want)
			}
		})
	}
}
