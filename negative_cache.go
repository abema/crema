package crema

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

const (
	negativeCacheShardCount      = 64
	negativeCacheShardCapacity   = 512
	negativeCacheGenerationCount = 4096
)

// NegativeCacheErrorPredicate reports whether a load error should be cached.
// Implementations must be safe for concurrent use.
type NegativeCacheErrorPredicate func(err error) bool

// DefaultNegativeCacheErrorPredicate caches every non-context error.
func DefaultNegativeCacheErrorPredicate(err error) bool {
	if err == nil {
		return false
	}

	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

type negativeCacheStore struct {
	_           noCopy
	shards      []negativeCacheShard
	generations []atomic.Uint64
}

type negativeCacheShard struct {
	_       noCopy
	mu      sync.RWMutex
	entries map[string]negativeCacheEntry
}

type negativeCacheEntry struct {
	err            error
	expireAtMillis int64
	generation     uint64
}

type negativeCacheToken struct {
	index      uint64
	generation uint64
}

func newNegativeCacheStore() *negativeCacheStore {
	shards := make([]negativeCacheShard, negativeCacheShardCount)
	for i := range shards {
		shards[i].entries = make(map[string]negativeCacheEntry)
	}

	return &negativeCacheStore{
		shards:      shards,
		generations: make([]atomic.Uint64, negativeCacheGenerationCount),
	}
}

func (s *negativeCacheStore) shardFor(key string) *negativeCacheShard {
	return &s.shards[hashKey(key)%uint64(len(s.shards))]
}

func (s *negativeCacheStore) tokenFor(key string) negativeCacheToken {
	index := hashKey(key) % uint64(len(s.generations))

	return negativeCacheToken{
		index:      index,
		generation: s.generations[index].Load(),
	}
}

func (s *negativeCacheStore) get(key string, nowMillis int64) (error, bool) {
	shard := s.shardFor(key)
	shard.mu.RLock()
	entry, ok := shard.entries[key]
	shard.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if entry.expireAtMillis > nowMillis {
		return entry.err, true
	}

	shard.mu.Lock()
	if current, ok := shard.entries[key]; ok && current.expireAtMillis <= nowMillis {
		delete(shard.entries, key)
	}
	shard.mu.Unlock()

	return nil, false
}

func (s *negativeCacheStore) set(
	key string,
	err error,
	nowMillis int64,
	expireAtMillis int64,
	token negativeCacheToken,
) bool {
	if expireAtMillis <= nowMillis || s.generations[token.index].Load() != token.generation {
		return false
	}

	shard := s.shardFor(key)
	shard.mu.Lock()
	if entry, ok := shard.entries[key]; ok &&
		entry.expireAtMillis > nowMillis &&
		entry.generation == token.generation {
		shard.mu.Unlock()

		return false
	}
	if _, ok := shard.entries[key]; !ok && len(shard.entries) >= negativeCacheShardCapacity {
		for victim := range shard.entries {
			delete(shard.entries, victim)

			break
		}
	}
	shard.entries[key] = negativeCacheEntry{
		err:            err,
		expireAtMillis: expireAtMillis,
		generation:     token.generation,
	}
	shard.mu.Unlock()

	if s.generations[token.index].Load() == token.generation {
		return true
	}

	shard.mu.Lock()
	if entry, ok := shard.entries[key]; ok && entry.generation == token.generation {
		delete(shard.entries, key)
	}
	shard.mu.Unlock()

	return false
}

func (s *negativeCacheStore) invalidate(key string) {
	token := s.tokenFor(key)
	s.generations[token.index].Add(1)

	shard := s.shardFor(key)
	shard.mu.Lock()
	delete(shard.entries, key)
	shard.mu.Unlock()
}
