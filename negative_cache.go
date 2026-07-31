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

// CacheLoadResult is the value and error returned by a cache loader.
type CacheLoadResult[V any] struct {
	Value V
	Err   error
}

// NegativeCachePredicate reports whether a loader result represents an absent value.
// Implementations must be safe for concurrent use.
type NegativeCachePredicate[V any] func(value V, err error) bool

type fencedCache[S any] struct {
	provider CacheProvider[S]
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

func newFencedCache[S any](provider CacheProvider[S]) *fencedCache[S] {
	return &fencedCache[S]{
		provider: provider,
		fences:   make([]negativeCacheFence, negativeCacheFenceCount),
	}
}

func (c *fencedCache[S]) fence(key string) *negativeCacheFence {
	return &c.fences[hashKey(key)%uint64(len(c.fences))]
}

func (c *fencedCache[S]) token(key string) negativeCacheToken {
	index := hashKey(key) % uint64(len(c.fences))
	fence := &c.fences[index]
	fence.mu.RLock()
	generation := fence.generation
	fence.mu.RUnlock()

	return negativeCacheToken{index: index, generation: generation}
}

func (c *fencedCache[S]) get(ctx context.Context, key string) (S, bool, error) {
	return c.provider.Get(ctx, key)
}

func (c *fencedCache[S]) set(
	ctx context.Context,
	key string,
	value S,
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
	if err := c.provider.Set(ctx, key, value, ttl); err != nil {
		return false, err
	}

	return true, nil
}

func (c *fencedCache[S]) invalidate(ctx context.Context, key string) error {
	fence := c.fence(key)
	fence.mu.Lock()
	defer fence.mu.Unlock()
	fence.generation++

	return c.provider.Delete(ctx, key)
}
