package gomemcache

import (
	"context"
	"errors"
	"math"
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

func TestMemcachedCacheProvider_LongTTLDoesNotExpireImmediately(t *testing.T) {
	t.Parallel()

	client := newTestMemcacheClient()
	provider := NewMemcachedCacheProvider(client)
	ctx := context.Background()

	if err := provider.Set(ctx, "key", []byte("value"), 60*24*time.Hour); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Set must feed ttlSeconds the real clock. Which of the two representations
	// wins depends on how close the wall clock is to 2038, so accept either --
	// but reject anything that is neither, such as a timestamp in the past.
	switch got := client.LastSetTTL(); {
	case got == maxRelativeExpirationSeconds:
	case int64(got) > time.Now().Unix():
	default:
		t.Fatalf("expiration %d is neither a future timestamp nor the 30-day fallback", got)
	}

	_, ok, err := provider.Get(ctx, "key")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ok {
		t.Fatal("expected value to exist")
	}
}

func TestTTLSeconds(t *testing.T) {
	t.Parallel()

	const maxRelative = maxRelativeExpirationSeconds

	// base is comfortably before 2038, so absolute timestamps are exact there.
	base := time.Unix(1_700_000_000, 0)
	// tieBreak is the instant where the maximum absolute timestamp and a relative
	// 30-day TTL express exactly the same expiry.
	tieBreak := time.Unix(math.MaxInt32-maxRelative, 0)

	tests := []struct {
		name string
		ttl  time.Duration
		now  time.Time
		want int32
	}{
		{name: "zero", ttl: 0, now: base, want: 1},
		{name: "negative", ttl: -time.Second, now: base, want: 1},
		{name: "fractional", ttl: 1500 * time.Millisecond, now: base, want: 2},
		{
			name: "just below thirty days stays relative",
			ttl:  maxRelative*time.Second - time.Second,
			now:  base,
			want: maxRelative - 1,
		},
		{
			name: "exactly thirty days stays relative",
			ttl:  maxRelative * time.Second,
			now:  base,
			want: maxRelative,
		},
		{
			name: "just above thirty days becomes absolute",
			ttl:  maxRelative*time.Second + time.Second,
			now:  base,
			want: int32(base.Unix()) + maxRelative + 1,
		},
		{
			name: "fractional above thirty days rounds up",
			ttl:  maxRelative*time.Second + 1500*time.Millisecond,
			now:  base,
			want: int32(base.Unix()) + maxRelative + 2,
		},
		{
			name: "sixty days becomes absolute",
			ttl:  60 * 24 * time.Hour,
			now:  base,
			want: int32(base.Unix()) + 60*24*60*60,
		},
		{
			name: "beyond 2038 takes the absolute maximum",
			ttl:  100 * 365 * 24 * time.Hour,
			now:  base,
			want: math.MaxInt32,
		},
		{
			name: "max duration takes the absolute maximum",
			ttl:  math.MaxInt64,
			now:  base,
			want: math.MaxInt32,
		},
		{
			// A tie: both representations land on the same instant, so either is
			// equally accurate and the absolute maximum is returned.
			name: "absolute maximum still reaches thirty days out",
			ttl:  100 * 365 * 24 * time.Hour,
			now:  tieBreak,
			want: math.MaxInt32,
		},
		{
			name: "relative wins once the absolute maximum is nearer than thirty days",
			ttl:  100 * 365 * 24 * time.Hour,
			now:  tieBreak.Add(time.Second),
			want: maxRelative,
		},
		{
			name: "relative wins at the int32 boundary",
			ttl:  60 * 24 * time.Hour,
			now:  time.Unix(math.MaxInt32, 0),
			want: maxRelative,
		},
		{
			name: "relative wins after the int32 boundary",
			ttl:  60 * 24 * time.Hour,
			now:  time.Unix(math.MaxInt32, 0).Add(365 * 24 * time.Hour),
			want: maxRelative,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := ttlSeconds(tt.ttl, tt.now)
			if got != tt.want {
				t.Fatalf("ttlSeconds(%v, %d) = %d, want %d", tt.ttl, tt.now.Unix(), got, tt.want)
			}
			// Whatever the representation, the entry must never be born expired.
			if got > maxRelative && int64(got) <= tt.now.Unix() {
				t.Fatalf("ttlSeconds(%v, %d) = %d, which is already in the past", tt.ttl, tt.now.Unix(), got)
			}
		})
	}
}
