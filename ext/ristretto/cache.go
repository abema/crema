package ristretto

import (
	"context"
	"errors"
	"time"

	"github.com/abema/crema"
	dgraphristretto "github.com/dgraph-io/ristretto"
)

// CostFunc returns the cost associated with a cache entry.
type CostFunc[S any] func(value S) int64

// CacheProviderOption customizes the RistrettoCacheProvider.
type CacheProviderOption[S any] func(*RistrettoCacheProvider[S])

// RistrettoCacheProvider stores encoded cache entries in ristretto.
type RistrettoCacheProvider[S any] struct {
	cache    *dgraphristretto.Cache
	costFunc CostFunc[S]
}

// ErrUnexpectedCacheValueType indicates a non-matching value type stored in ristretto.
var ErrUnexpectedCacheValueType = errors.New("ristretto cache returned unexpected value type")

const defaultCost = int64(1)

var _ crema.CacheProvider[any] = (*RistrettoCacheProvider[any])(nil)

// NewRistrettoCacheProvider wraps an existing ristretto cache.
func NewRistrettoCacheProvider[S any](cache *dgraphristretto.Cache, opts ...CacheProviderOption[S]) (*RistrettoCacheProvider[S], error) {
	if cache == nil {
		return nil, errors.New("ristretto cache is nil")
	}
	provider := &RistrettoCacheProvider[S]{
		cache: cache,
		costFunc: func(S) int64 {
			return defaultCost
		},
	}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		opt(provider)
	}

	return provider, nil
}

// WithCostFunc overrides the default cost function.
func WithCostFunc[S any](costFunc CostFunc[S]) CacheProviderOption[S] {
	return func(provider *RistrettoCacheProvider[S]) {
		if costFunc != nil {
			provider.costFunc = costFunc
		}
	}
}

// Get retrieves a value from the cache by key.
func (r *RistrettoCacheProvider[S]) Get(_ context.Context, key string) (S, bool, error) {
	value, ok := r.cache.Get(key)
	if !ok {
		var zero S

		return zero, false, nil
	}
	castValue, ok := value.(S)
	if !ok {
		var zero S

		return zero, false, ErrUnexpectedCacheValueType
	}

	return castValue, true, nil
}

// Set stores a value in the cache with the specified key.
func (r *RistrettoCacheProvider[S]) Set(_ context.Context, key string, value S, ttl time.Duration) error {
	cost := r.costFunc(value)
	if cost <= 0 {
		cost = defaultCost
	}
	if ok := r.cache.SetWithTTL(key, value, cost, ttl); !ok {
		// rejected by TinyLFU algorithm, but not an error
		return nil
	}

	return nil
}

// Delete removes a value from the cache by key.
func (r *RistrettoCacheProvider[S]) Delete(_ context.Context, key string) error {
	r.cache.Del(key)

	return nil
}
