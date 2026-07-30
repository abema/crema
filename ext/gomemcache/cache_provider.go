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
		item.Expiration = memcachedExpiration(ttl, time.Now())
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

// Expiration values above 30 days are interpreted as UNIX timestamps.
const maxRelativeExpirationSeconds = 60 * 60 * 24 * 30

// memcachedExpiration converts a TTL to a relative or absolute expiration.
func memcachedExpiration(ttl time.Duration, now time.Time) int32 {
	seconds := int64(ttl / time.Second)
	if ttl%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		return 1
	}
	if seconds <= maxRelativeExpirationSeconds {
		return int32(seconds)
	}

	unixNow := now.Unix()
	if expiresAt := unixNow + seconds; expiresAt <= math.MaxInt32 {
		return int32(expiresAt)
	}

	if math.MaxInt32-unixNow >= maxRelativeExpirationSeconds {
		return math.MaxInt32
	}

	return maxRelativeExpirationSeconds
}
