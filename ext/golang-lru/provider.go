package golanglru

import (
	"context"
	"time"

	"github.com/abema/crema"
	"github.com/hashicorp/golang-lru/v2/expirable"
)

// CacheProvider stores cache entries in a hashicorp/golang-lru cache.
//
// Every entry expires after the defaultTTL passed to NewCacheProvider; per-entry
// TTLs are ignored. See NewCacheProvider and Set for details.
type CacheProvider[S any] struct {
	cache *expirable.LRU[string, S]
}

var _ crema.CacheProvider[any] = (*CacheProvider[any])(nil)

// NewCacheProvider constructs a CacheProvider with the given max size and default TTL.
//
// defaultTTL is the only TTL this provider honors: expirable.LRU fixes the
// expiration of every entry at construction time and exposes no per-entry TTL,
// so the TTL passed to Set is ignored. Logical expiration is still correct
// because crema stores the absolute expiry in crema.CacheObject.ExpireAtMillis
// and revalidates on read; defaultTTL only controls how long an entry physically
// stays resident.
//
// Set defaultTTL to at least the largest TTL passed to Cache.GetOrLoad (or
// Cache.Set). With a shorter defaultTTL, entries are evicted before their
// logical expiry, causing extra loader calls; with a longer one, logically
// expired entries keep occupying memory (and an LRU slot) until eviction.
func NewCacheProvider[S any](size int, defaultTTL time.Duration) *CacheProvider[S] {
	return &CacheProvider[S]{
		cache: expirable.NewLRU[string, S](size, nil, defaultTTL),
	}
}

// Get retrieves a value from the cache by key.
func (c *CacheProvider[S]) Get(_ context.Context, key string) (S, bool, error) {
	value, ok := c.cache.Get(key)
	if !ok {
		var zero S

		return zero, false, nil
	}

	return value, true, nil
}

// Set stores a value in the cache with the specified key.
//
// The ttl argument is ignored: expirable.LRU applies the defaultTTL given to
// NewCacheProvider to every entry and provides no per-entry TTL. Callers relying
// on per-entry expiration should size defaultTTL accordingly; logical expiration
// itself is unaffected because it is derived from
// crema.CacheObject.ExpireAtMillis rather than from the LRU's own expiry.
func (c *CacheProvider[S]) Set(_ context.Context, key string, value S, _ time.Duration) error {
	c.cache.Add(key, value)

	return nil
}

// Delete removes a value from the cache by key.
func (c *CacheProvider[S]) Delete(_ context.Context, key string) error {
	c.cache.Remove(key)

	return nil
}
