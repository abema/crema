package crema

import (
	"context"
	"sync"
	"time"
)

const negativeCacheFenceCount = 4096

// LoadErrorCacheErrorPredicate reports whether a load error should be cached.
// Implementations must be safe for concurrent use.
type LoadErrorCacheErrorPredicate func(err error) bool

// NegativeCacheErrorPredicate reports whether an error represents an absent value.
type NegativeCacheErrorPredicate func(err error) bool

type negativeCache struct {
	provider CacheProvider[error]
	fences   []negativeCacheFence
}

type negativeCacheFence struct {
	mu         sync.RWMutex
	generation uint64
}

type negativeCacheToken struct {
	index      uint64
	generation uint64
}

func newNegativeCache(provider CacheProvider[error]) *negativeCache {
	return &negativeCache{
		provider: provider,
		fences:   make([]negativeCacheFence, negativeCacheFenceCount),
	}
}

func (c *negativeCache) fence(key string) *negativeCacheFence {
	return &c.fences[hashKey(key)%uint64(len(c.fences))]
}

func (c *negativeCache) token(key string) negativeCacheToken {
	index := hashKey(key) % uint64(len(c.fences))
	fence := &c.fences[index]
	fence.mu.RLock()
	generation := fence.generation
	fence.mu.RUnlock()

	return negativeCacheToken{index: index, generation: generation}
}

func (c *negativeCache) get(ctx context.Context, key string) (error, bool, error) {
	return c.provider.Get(ctx, key)
}

func (c *negativeCache) set(
	ctx context.Context,
	key string,
	err error,
	ttl time.Duration,
	token negativeCacheToken,
) (bool, error) {
	if ttl <= 0 {
		return false, nil
	}
	fence := &c.fences[token.index]
	fence.mu.RLock()
	defer fence.mu.RUnlock()
	if fence.generation != token.generation {
		return false, nil
	}
	if err := c.provider.Set(ctx, key, err, ttl); err != nil {
		return false, err
	}

	return true, nil
}

func (c *negativeCache) invalidate(ctx context.Context, key string) error {
	fence := c.fence(key)
	fence.mu.Lock()
	defer fence.mu.Unlock()
	fence.generation++

	return c.provider.Delete(ctx, key)
}
