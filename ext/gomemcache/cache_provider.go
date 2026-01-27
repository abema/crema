package gomemcache

import (
	"context"
	"math"
	"time"

	"github.com/abema/crema"
	"github.com/bradfitz/gomemcache/memcache"
)

// MemcachedCacheProvider stores cache entries in Memcached.
type MemcachedCacheProvider struct {
	client memcacheClient
}

var _ crema.CacheProvider[[]byte] = (*MemcachedCacheProvider)(nil)

// NewMemcachedCacheProvider builds a Memcached-backed cache provider.
func NewMemcachedCacheProvider(client memcacheClient) *MemcachedCacheProvider {
	return &MemcachedCacheProvider{client: client}
}

// Get retrieves a cached value from Memcached.
func (p *MemcachedCacheProvider) Get(_ context.Context, key string) ([]byte, bool, error) {
	item, err := p.client.Get(key)
	if err != nil {
		if err == memcache.ErrCacheMiss {
			return nil, false, nil
		}

		return nil, false, err
	}
	if item == nil {
		return nil, false, nil
	}

	return item.Value, true, nil
}

// Set stores a cache entry in Memcached with the given TTL.
func (p *MemcachedCacheProvider) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	item := &memcache.Item{Key: key, Value: value}
	if ttl > 0 {
		item.Expiration = ttlSeconds(ttl)
	}

	return p.client.Set(item)
}

// Delete removes a cached value from Memcached.
func (p *MemcachedCacheProvider) Delete(_ context.Context, key string) error {
	if err := p.client.Delete(key); err != nil && err != memcache.ErrCacheMiss {
		return err
	}

	return nil
}

type memcacheClient interface {
	Get(key string) (*memcache.Item, error)
	Set(item *memcache.Item) error
	Delete(key string) error
}

func ttlSeconds(ttl time.Duration) int32 {
	seconds := int32(math.Ceil(ttl.Seconds()))
	if seconds < 1 {
		return 1
	}

	return seconds
}
