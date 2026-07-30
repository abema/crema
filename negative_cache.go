package crema

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

const (
	negativeCacheMaxShardCount   = 64
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
	_        noCopy
	mu       sync.RWMutex
	entries  map[string]negativeCacheEntry
	capacity int
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

func newNegativeCacheStore(capacity int) *negativeCacheStore {
	shardCount := min(capacity, negativeCacheMaxShardCount)
	shards := make([]negativeCacheShard, shardCount)
	shardCapacity := capacity / shardCount
	remainder := capacity % shardCount
	for i := range shards {
		capacity := shardCapacity
		if i < remainder {
			capacity++
		}
		shards[i] = negativeCacheShard{
			entries:  make(map[string]negativeCacheEntry),
			capacity: capacity,
		}
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
	if !shard.setIfAbsent(key, err, nowMillis, expireAtMillis, token.generation) {
		return false
	}
	if s.generations[token.index].Load() == token.generation {
		return true
	}

	shard.deleteGeneration(key, token.generation)

	return false
}

func (s *negativeCacheShard) setIfAbsent(
	key string,
	err error,
	nowMillis int64,
	expireAtMillis int64,
	generation uint64,
) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entry, ok := s.entries[key]; ok &&
		entry.expireAtMillis > nowMillis &&
		entry.generation == generation {
		return false
	}
	if _, ok := s.entries[key]; !ok && len(s.entries) >= s.capacity {
		for victim := range s.entries {
			delete(s.entries, victim)

			break
		}
	}
	s.entries[key] = negativeCacheEntry{
		err:            err,
		expireAtMillis: expireAtMillis,
		generation:     generation,
	}

	return true
}

func (s *negativeCacheShard) deleteGeneration(key string, generation uint64) {
	s.mu.Lock()
	if entry, ok := s.entries[key]; ok && entry.generation == generation {
		delete(s.entries, key)
	}
	s.mu.Unlock()
}

func (s *negativeCacheStore) invalidate(key string) {
	token := s.tokenFor(key)
	s.generations[token.index].Add(1)

	shard := s.shardFor(key)
	shard.mu.Lock()
	delete(shard.entries, key)
	shard.mu.Unlock()
}
