package crema

import (
	"context"
	"time"
)

// CacheProvider abstracts storage for encoded cache entries.
// Implementations must be safe for concurrent use by multiple goroutines.
type CacheProvider[S any] interface {
	// Get retrieves a value from the cache by key.
	Get(ctx context.Context, key string) (S, bool, error)
	// Set stores a value in the cache with the specified key.
	Set(ctx context.Context, key string, value S, ttl time.Duration) error
	// Delete removes a value from the cache by key.
	Delete(ctx context.Context, key string) error
}

// NoopCacheProvider is a cache provider that does nothing.
// All Get calls return a cache miss, and Set/Delete calls are no-ops.
// Useful for tests or when caching should be explicitly disabled.
type NoopCacheProvider[S any] struct{}

var _ CacheProvider[any] = (*NoopCacheProvider[any])(nil)

// NewNoopCacheProvider constructs a NoopCacheProvider.
func NewNoopCacheProvider[S any]() *NoopCacheProvider[S] {
	return &NoopCacheProvider[S]{}
}

// Get always returns a cache miss.
func (n *NoopCacheProvider[S]) Get(_ context.Context, _ string) (S, bool, error) {
	var zero S

	return zero, false, nil
}

// Set does nothing and returns nil.
func (n *NoopCacheProvider[S]) Set(_ context.Context, _ string, _ S, _ time.Duration) error {
	return nil
}

// Delete does nothing and returns nil.
func (n *NoopCacheProvider[S]) Delete(_ context.Context, _ string) error {
	return nil
}
