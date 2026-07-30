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
		item.Expiration = ttlSeconds(ttl, time.Now())
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

// maxRelativeExpirationSeconds is the largest expiration Memcached treats as a
// relative number of seconds (30 days). Larger values are interpreted as an
// absolute UNIX timestamp instead.
const maxRelativeExpirationSeconds = 60 * 60 * 24 * 30

// ttlSeconds converts a TTL into a Memcached expiration value.
//
// Memcached reads an expiration above 30 days as an absolute UNIX timestamp, so
// passing a longer TTL through as a relative count would be read as a timestamp
// in the past and expire the entry immediately. Such TTLs are converted into an
// absolute timestamp instead.
//
// Absolute timestamps are limited to int32, i.e. 2038-01-19. Once the requested
// expiry no longer fits, only two values remain expressible: the largest
// absolute timestamp, and a relative 30-day TTL. Whichever of the two lands
// closer to the requested expiry wins, so the result degrades gradually and
// never becomes a timestamp in the past.
func ttlSeconds(ttl time.Duration, now time.Time) int32 {
	seconds := int64(math.Ceil(ttl.Seconds()))
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

	// Both candidates fall short of the requested expiry, so the closer one is
	// simply the later one. math.MaxInt32-unixNow is the lifetime the maximum
	// absolute timestamp still buys us, and it goes negative once that timestamp
	// is in the past -- at which point the relative TTL is the only safe answer.
	if math.MaxInt32-unixNow >= maxRelativeExpirationSeconds {
		return math.MaxInt32
	}

	return maxRelativeExpirationSeconds
}
