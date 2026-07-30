package golanglru

import (
	"context"
	"time"

	"github.com/abema/crema"
	"github.com/hashicorp/golang-lru/v2/expirable"
)

// CacheProvider stores cache entries in a hashicorp/golang-lru cache.
// Entries use the provider-wide TTL configured by NewCacheProvider.
type CacheProvider[S any] struct {
	cache *expirable.LRU[string, S]
}

var _ crema.CacheProvider[any] = (*CacheProvider[any])(nil)

// NewCacheProvider constructs a CacheProvider with the given max size and
// provider-wide TTL. A non-positive defaultTTL disables time-based eviction.
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

// Set stores a value. The ttl argument is ignored.
func (c *CacheProvider[S]) Set(_ context.Context, key string, value S, _ time.Duration) error {
	c.cache.Add(key, value)

	return nil
}

// Delete removes a value from the cache by key.
func (c *CacheProvider[S]) Delete(_ context.Context, key string) error {
	c.cache.Remove(key)

	return nil
}
