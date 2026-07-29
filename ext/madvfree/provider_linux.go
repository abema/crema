//go:build linux && !386 && !arm && !mips && !mipsle

package madvfree

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/maphash"
	"sync"
	"sync/atomic"
	"time"

	"github.com/abema/crema"
	"golang.org/x/sys/unix"
)

const (
	generationOffset = 0
	pageIndexOffset  = 8
	touchOffset      = 12
	pageHeaderSize   = 16
	defaultShards    = 64
)

type entryState uint8

const (
	entryLive entryState = iota
	entryDeleting
	entryDead
)

type allocationKind uint8

const (
	allocationExtent allocationKind = iota
	allocationSmall
)

type cacheEntry struct {
	mu sync.Mutex

	smallMeta      *smallPageMeta
	key            string
	kind           allocationKind
	startPage      uint32
	pageCount      uint32
	classID        uint16
	slot           uint16
	length         int
	generation     uint64
	slotGeneration uint64
	expiresAt      int64
	expiry         *expiryRecord

	refs  uint32
	state entryState
	freed bool
}

type indexShard struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry
}

type counters struct {
	entries       atomic.Int64
	logicalBytes  atomic.Int64
	reservedBytes atomic.Int64

	hits               atomic.Uint64
	misses             atomic.Uint64
	reclaimedMisses    atomic.Uint64
	generationMisses   atomic.Uint64
	expiredMisses      atomic.Uint64
	madvFreeCalls      atomic.Uint64
	madvDontNeedCalls  atomic.Uint64
	madvFreeErrors     atomic.Uint64
	madvDontNeedErrors atomic.Uint64
	allocations        atomic.Uint64
	allocationFails    atomic.Uint64
	smallAllocations   atomic.Uint64
	extentAllocations  atomic.Uint64
}

// Provider is a concurrent, best-effort byte cache backed by a single
// anonymous mmap arena.
//
// Idle extents are marked MADV_FREE, so Linux may discard them under memory
// pressure. Get therefore may miss before an entry's TTL expires.
type Provider struct {
	lifecycle sync.RWMutex
	closed    bool

	arena      []byte
	pageSize   int
	capacity   int
	shards     []indexShard
	hashSeed   maphash.Seed
	generation atomic.Uint64

	allocatorMu sync.Mutex
	allocator   extentAllocator

	smallMu     sync.RWMutex
	smallPages  map[uint32]*smallPageMeta
	classPages  [][]smallPageRef
	classCreate []sync.Mutex
	smallRefs   sync.Pool
	layouts     []smallPageLayout
	madvise     madviseFunc

	expiryMu    sync.Mutex
	expirations expiryHeap
	expiryWake  chan struct{}
	expiryStop  chan struct{}
	expiryDone  chan struct{}

	stats counters
}

var _ crema.CacheProvider[[]byte] = (*Provider)(nil)

// NewProvider creates a Linux anonymous-mmap cache.
//
// The zero-value Config is valid. Initialization probes MADV_FREE and applies
// the configured arena advice before starting the TTL worker. The caller must
// call Close when the provider is no longer needed.
//
//nolint:cyclop,funlen // Initialization keeps all mmap cleanup paths together.
func NewProvider(config Config) (*Provider, error) {
	if config.CapacityBytes < 0 || config.ShardCount < 0 {
		return nil, fmt.Errorf("%w: capacity and shard count must be non-negative", ErrInvalidConfig)
	}

	capacityBytes := config.CapacityBytes
	if capacityBytes == 0 {
		capacityBytes = DefaultCapacityBytes
	}
	pageSize := unix.Getpagesize()
	capacity, ok := roundUp(capacityBytes, pageSize)
	if !ok || capacity < pageSize {
		return nil, fmt.Errorf("%w: capacity overflows int", ErrInvalidConfig)
	}
	shardCount := config.ShardCount
	if shardCount == 0 {
		shardCount = defaultShards
	}
	if capacity/pageSize > int(^uint32(0)) {
		return nil, fmt.Errorf("%w: capacity exceeds the 32-bit page index", ErrInvalidConfig)
	}
	layouts, err := makeSmallPageLayouts(pageSize, config.SizeClasses)
	if err != nil {
		return nil, err
	}
	if err := probeMADVFree(pageSize); err != nil {
		return nil, err
	}

	arena, err := unix.Mmap(
		-1,
		0,
		capacity,
		unix.PROT_READ|unix.PROT_WRITE,
		unix.MAP_PRIVATE|unix.MAP_ANONYMOUS|unix.MAP_NORESERVE,
	)
	if err != nil {
		return nil, fmt.Errorf("madvfree: mmap: %w", err)
	}
	cleanup := func(cause error) (*Provider, error) {
		if unmapErr := unix.Munmap(arena); unmapErr != nil {
			return nil, errors.Join(cause, fmt.Errorf("madvfree: munmap after initialization failure: %w", unmapErr))
		}

		return nil, cause
	}

	if !config.EnableHugePages {
		if err := unix.Madvise(arena, unix.MADV_NOHUGEPAGE); err != nil {
			return cleanup(fmt.Errorf("madvfree: MADV_NOHUGEPAGE: %w", err))
		}
	}
	if !config.IncludeInCoreDump {
		if err := unix.Madvise(arena, unix.MADV_DONTDUMP); err != nil {
			return cleanup(fmt.Errorf("madvfree: MADV_DONTDUMP: %w", err))
		}
	}

	shards := make([]indexShard, shardCount)
	for i := range shards {
		shards[i].entries = make(map[string]*cacheEntry)
	}
	pageCount := uint32(capacity / pageSize)

	provider := &Provider{
		arena:       arena,
		pageSize:    pageSize,
		capacity:    capacity,
		shards:      shards,
		hashSeed:    maphash.MakeSeed(),
		allocator:   newExtentAllocator(pageCount),
		smallPages:  make(map[uint32]*smallPageMeta),
		classPages:  make([][]smallPageRef, len(layouts)),
		classCreate: make([]sync.Mutex, len(layouts)),
		smallRefs: sync.Pool{
			New: func() any {
				return new(smallPageRefBuffer)
			},
		},
		layouts:    layouts,
		madvise:    unix.Madvise,
		expiryWake: make(chan struct{}, 1),
		expiryStop: make(chan struct{}),
		expiryDone: make(chan struct{}),
	}
	go provider.expirationLoop()

	return provider, nil
}

// Get retrieves a Go-managed copy of the value for key.
//
// It returns (nil, false, nil) for an absent, expired, or reclaimed entry.
// Cancellation of ctx and use after Close are returned as errors.
func (p *Provider) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	p.lifecycle.RLock()
	defer p.lifecycle.RUnlock()
	if p.closed {
		return nil, false, ErrClosed
	}

	item := p.lookup(key)
	if item == nil {
		p.stats.misses.Add(1)

		return nil, false, nil
	}
	if item.expiresAt != 0 && time.Now().UnixNano() >= item.expiresAt {
		if p.removeIfSame(key, item) {
			p.stats.expiredMisses.Add(1)
			p.retire(item)
		}
		p.stats.misses.Add(1)

		return nil, false, nil
	}

	reclaimed, acquired := p.acquire(item)
	if !acquired {
		p.removeIfSame(key, item)
		p.finalize(item)
		if reclaimed {
			p.stats.reclaimedMisses.Add(1)
		} else {
			p.stats.generationMisses.Add(1)
		}
		p.stats.misses.Add(1)

		return nil, false, nil
	}

	result := make([]byte, item.length)
	if item.kind == allocationSmall {
		p.copyFromSmall(result, item)
	} else {
		p.copyFromExtent(result, item)
	}
	p.release(item)

	p.stats.hits.Add(1)

	return result, true, nil
}

// Set stores a copy of value under key.
//
// A zero TTL means no expiration. A negative TTL returns ErrInvalidConfig.
// The caller may reuse or mutate value after Set returns. Replacing an existing
// key allocates the new value before retiring the old value, so the operation
// can return ErrCapacity when the arena lacks temporary replacement space.
//
//nolint:cyclop,funlen // Small and extent allocation share replacement accounting.
func (p *Provider) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ttl < 0 {
		return fmt.Errorf("%w: ttl must be non-negative", ErrInvalidConfig)
	}

	p.lifecycle.RLock()
	defer p.lifecycle.RUnlock()
	if p.closed {
		return ErrClosed
	}

	var expiresAt int64
	if ttl > 0 {
		now := time.Now().UnixNano()
		if int64(ttl) > int64(^uint64(0)>>1)-now {
			expiresAt = int64(^uint64(0) >> 1)
		} else {
			expiresAt = now + int64(ttl)
		}
	}

	item, small, err := p.allocateSmall(key, value, expiresAt)
	if err != nil {
		p.stats.allocationFails.Add(1)

		return err
	}
	if !small {
		pageCount, ok := p.pagesForValue(len(value))
		if !ok {
			p.stats.allocationFails.Add(1)

			return ErrCapacity
		}
		p.allocatorMu.Lock()
		startPage, allocated := p.allocator.allocate(pageCount)
		if allocated {
			p.stats.entries.Add(1)
			p.stats.logicalBytes.Add(int64(len(value)))
			p.stats.reservedBytes.Add(int64(pageCount) * int64(p.pageSize))
		}
		p.allocatorMu.Unlock()
		if !allocated {
			p.stats.allocationFails.Add(1)

			return ErrCapacity
		}

		item = &cacheEntry{
			key:        key,
			kind:       allocationExtent,
			startPage:  startPage,
			pageCount:  pageCount,
			length:     len(value),
			generation: p.nextGeneration(),
			expiresAt:  expiresAt,
		}
		p.writeExtent(item, value)
		p.stats.allocations.Add(1)
		p.stats.extentAllocations.Add(1)

		p.madviseExtent(item, unix.MADV_FREE)
	}

	old := p.replace(key, item)
	if old != nil {
		p.retire(old)
	}
	if small {
		p.release(item)
	}

	return nil
}

// Delete removes key. Missing keys are ignored.
//
// In-flight readers finish before the entry's pages are discarded.
func (p *Provider) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.lifecycle.RLock()
	defer p.lifecycle.RUnlock()
	if p.closed {
		return ErrClosed
	}

	item := p.remove(key)
	if item == nil {
		return nil
	}

	p.retire(item)

	return nil
}

// Purge removes all indexed entries and pending expiration records.
func (p *Provider) Purge() error {
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()
	if p.closed {
		return ErrClosed
	}

	p.clearExpirations()
	for _, item := range p.drainIndex(0) {
		p.retire(item)
	}

	return nil
}

// Trim evicts entries until at least targetBytes of reserved arena space has
// been released, or until no indexed entries remain.
//
// Entries are selected in arbitrary map iteration order, not by recency.
// Trim holds the provider's exclusive lifecycle lock, so Get, Set, Delete,
// Purge, Close, and other Trim calls wait until it completes.
//
// It returns the number of bytes actually released. A non-positive target
// performs no work. The result can exceed targetBytes because slabs and extents
// are released at their allocation granularity.
func (p *Provider) Trim(targetBytes int64) (int64, error) {
	if targetBytes <= 0 {
		return 0, nil
	}
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()
	if p.closed {
		return 0, ErrClosed
	}

	startReserved := p.stats.reservedBytes.Load()
	for startReserved-p.stats.reservedBytes.Load() < targetBytes {
		item := p.popAny()
		if item == nil {
			break
		}
		p.retire(item)
	}

	return startReserved - p.stats.reservedBytes.Load(), nil
}

// Close stops expiration processing and removes the arena mapping.
//
// Close is idempotent. Get, Set, Delete, Purge, and Trim return ErrClosed after
// it succeeds; Stats remains available.
func (p *Provider) Close() error {
	p.lifecycle.Lock()
	if p.closed && p.arena == nil {
		p.lifecycle.Unlock()
		<-p.expiryDone

		return nil
	}
	if !p.closed {
		p.closed = true
		close(p.expiryStop)
	}
	p.clearExpirations()

	for shardIndex := range p.shards {
		shard := &p.shards[shardIndex]
		shard.mu.Lock()
		clear(shard.entries)
		shard.mu.Unlock()
	}
	var result error
	p.advise(p.arena, unix.MADV_DONTNEED)
	if err := unix.Munmap(p.arena); err != nil {
		result = errors.Join(result, fmt.Errorf("madvfree: munmap: %w", err))
	} else {
		p.arena = nil
	}
	p.allocatorMu.Lock()
	p.allocator = newExtentAllocator(0)
	p.stats.entries.Store(0)
	p.stats.logicalBytes.Store(0)
	p.stats.reservedBytes.Store(0)
	p.allocatorMu.Unlock()
	p.lifecycle.Unlock()
	<-p.expiryDone

	return result
}

// Stats returns a point-in-time snapshot of gauges and cumulative counters.
func (p *Provider) Stats() Stats {
	p.allocatorMu.Lock()
	freeBytes := int64(p.allocator.freePages()) * int64(p.pageSize)
	entries := p.stats.entries.Load()
	logicalBytes := p.stats.logicalBytes.Load()
	reservedBytes := p.stats.reservedBytes.Load()
	p.allocatorMu.Unlock()

	return Stats{
		Entries:            entries,
		LogicalBytes:       logicalBytes,
		ReservedBytes:      reservedBytes,
		FreeBytes:          freeBytes,
		Hits:               p.stats.hits.Load(),
		Misses:             p.stats.misses.Load(),
		ReclaimedMisses:    p.stats.reclaimedMisses.Load(),
		GenerationMisses:   p.stats.generationMisses.Load(),
		ExpiredMisses:      p.stats.expiredMisses.Load(),
		MadvFreeCalls:      p.stats.madvFreeCalls.Load(),
		MadvDontNeedCalls:  p.stats.madvDontNeedCalls.Load(),
		MadvFreeErrors:     p.stats.madvFreeErrors.Load(),
		MadvDontNeedErrors: p.stats.madvDontNeedErrors.Load(),
		Allocations:        p.stats.allocations.Load(),
		AllocationFails:    p.stats.allocationFails.Load(),
		SmallAllocations:   p.stats.smallAllocations.Load(),
		ExtentAllocations:  p.stats.extentAllocations.Load(),
	}
}

func (p *Provider) acquire(item *cacheEntry) (bool, bool) {
	if item.kind == allocationSmall {
		return p.acquireSmall(item)
	}

	return p.acquireExtent(item)
}

func (p *Provider) acquireExtent(item *cacheEntry) (bool, bool) {
	item.mu.Lock()
	defer item.mu.Unlock()
	if item.state != entryLive {
		return false, false
	}
	if item.refs == 0 {
		reclaimed := false
		// A mismatch on a later page can occur after earlier pages were touched
		// and re-pinned. The failed acquire retires the whole extent, whose
		// MADV_DONTNEED makes that transient re-pin harmless.
		for pageIndex := uint32(0); pageIndex < item.pageCount; pageIndex++ {
			page := p.page(item.startPage + pageIndex)
			page[touchOffset] ^= 1
			generation := binary.LittleEndian.Uint64(page[generationOffset:pageIndexOffset])
			storedIndex := binary.LittleEndian.Uint32(page[pageIndexOffset:touchOffset])
			if generation != item.generation || storedIndex != pageIndex {
				reclaimed = reclaimed || generation == 0
				item.state = entryDead

				return reclaimed, false
			}
		}
	}
	item.refs++

	return false, true
}

func (p *Provider) release(item *cacheEntry) {
	if item.kind == allocationSmall {
		p.releaseSmall(item)

		return
	}

	p.releaseExtent(item)
}

func (p *Provider) releaseExtent(item *cacheEntry) {
	item.mu.Lock()
	if item.refs == 0 {
		panic("madvfree: extent reference count underflow")
	}
	item.refs--
	if item.refs != 0 {
		item.mu.Unlock()

		return
	}
	if item.state != entryLive {
		item.mu.Unlock()

		p.finalize(item)

		return
	}
	p.madviseExtentLocked(item, unix.MADV_FREE)
	item.mu.Unlock()
}

func (p *Provider) retire(item *cacheEntry) {
	if item.kind == allocationSmall {
		p.retireSmall(item)

		return
	}

	p.retireExtent(item)
}

func (p *Provider) retireExtent(item *cacheEntry) {
	item.mu.Lock()
	if item.state == entryLive {
		item.state = entryDeleting
	}
	ready := item.refs == 0
	item.mu.Unlock()
	if ready {
		p.finalize(item)
	}
}

func (p *Provider) finalize(item *cacheEntry) {
	if item.kind == allocationSmall {
		p.retireSmall(item)

		return
	}

	p.finalizeExtent(item)
}

func (p *Provider) finalizeExtent(item *cacheEntry) {
	item.mu.Lock()
	if item.freed || item.refs != 0 {
		item.mu.Unlock()

		return
	}
	item.freed = true
	item.state = entryDead
	p.madviseExtentLocked(item, unix.MADV_DONTNEED)
	item.mu.Unlock()

	p.allocatorMu.Lock()
	p.allocator.release(item.startPage, item.pageCount)
	p.stats.entries.Add(-1)
	p.stats.logicalBytes.Add(-int64(item.length))
	p.stats.reservedBytes.Add(-int64(item.pageCount) * int64(p.pageSize))
	p.allocatorMu.Unlock()
}

func (p *Provider) madviseExtent(item *cacheEntry, advice int) {
	item.mu.Lock()
	p.madviseExtentLocked(item, advice)
	item.mu.Unlock()
}

func (p *Provider) madviseExtentLocked(item *cacheEntry, advice int) {
	p.advise(p.extentBytes(item.startPage, item.pageCount), advice)
}

func (p *Provider) writeExtent(item *cacheEntry, value []byte) {
	remaining := value
	for pageIndex := uint32(0); pageIndex < item.pageCount; pageIndex++ {
		page := p.page(item.startPage + pageIndex)
		binary.LittleEndian.PutUint64(page[generationOffset:pageIndexOffset], item.generation)
		binary.LittleEndian.PutUint32(page[pageIndexOffset:touchOffset], pageIndex)
		page[touchOffset] = 0
		payload := page[pageHeaderSize:]
		written := copy(payload, remaining)
		remaining = remaining[written:]
	}
}

func (p *Provider) copyFromExtent(destination []byte, item *cacheEntry) {
	remaining := destination
	for pageIndex := uint32(0); pageIndex < item.pageCount && len(remaining) > 0; pageIndex++ {
		payload := p.page(item.startPage + pageIndex)[pageHeaderSize:]
		copied := copy(remaining, payload)
		remaining = remaining[copied:]
	}
}

func (p *Provider) pagesForValue(length int) (uint32, bool) {
	payloadPerPage := p.pageSize - pageHeaderSize
	if length == 0 {
		return 1, true
	}
	pageCount := 1 + (length-1)/payloadPerPage
	if pageCount > p.capacity/p.pageSize || pageCount > int(^uint32(0)) {
		return 0, false
	}

	return uint32(pageCount), true
}

func (p *Provider) page(pageID uint32) []byte {
	start := int(pageID) * p.pageSize

	return p.arena[start : start+p.pageSize]
}

func (p *Provider) extentBytes(startPage, pageCount uint32) []byte {
	start := int(startPage) * p.pageSize
	end := start + int(pageCount)*p.pageSize

	return p.arena[start:end]
}

func (p *Provider) nextGeneration() uint64 {
	for {
		generation := p.generation.Add(1)
		if generation != 0 {
			return generation
		}
	}
}

func (p *Provider) shard(key string) *indexShard {
	var hash maphash.Hash
	hash.SetSeed(p.hashSeed)
	_, _ = hash.WriteString(key)

	return &p.shards[hash.Sum64()%uint64(len(p.shards))]
}

func (p *Provider) lookup(key string) *cacheEntry {
	shard := p.shard(key)
	shard.mu.RLock()
	item := shard.entries[key]
	shard.mu.RUnlock()

	return item
}

func (p *Provider) replace(key string, item *cacheEntry) *cacheEntry {
	shard := p.shard(key)
	shard.mu.Lock()
	old := shard.entries[key]
	p.expiryMu.Lock()
	wakeExpiry := p.cancelExpiryLocked(old)
	if item.expiresAt != 0 {
		wakeExpiry = p.addExpiryLocked(item) || wakeExpiry
	}
	p.expiryMu.Unlock()
	shard.entries[key] = item
	shard.mu.Unlock()
	if wakeExpiry {
		p.wakeExpiryLoop()
	}

	return old
}

func (p *Provider) remove(key string) *cacheEntry {
	shard := p.shard(key)
	shard.mu.Lock()
	item := shard.entries[key]
	p.expiryMu.Lock()
	wakeExpiry := p.cancelExpiryLocked(item)
	p.expiryMu.Unlock()
	delete(shard.entries, key)
	shard.mu.Unlock()
	if wakeExpiry {
		p.wakeExpiryLoop()
	}

	return item
}

func (p *Provider) removeIfSame(key string, expected *cacheEntry) bool {
	shard := p.shard(key)
	shard.mu.Lock()
	if shard.entries[key] != expected {
		shard.mu.Unlock()

		return false
	}
	p.expiryMu.Lock()
	wakeExpiry := p.cancelExpiryLocked(expected)
	p.expiryMu.Unlock()
	delete(shard.entries, key)
	shard.mu.Unlock()
	if wakeExpiry {
		p.wakeExpiryLoop()
	}

	return true
}

func (p *Provider) drainIndex(limit int64) []*cacheEntry {
	var result []*cacheEntry
	var reserved int64
	for shardIndex := range p.shards {
		shard := &p.shards[shardIndex]
		shard.mu.Lock()
		for key, item := range shard.entries {
			p.expiryMu.Lock()
			wakeExpiry := p.cancelExpiryLocked(item)
			p.expiryMu.Unlock()
			delete(shard.entries, key)
			result = append(result, item)
			reserved += int64(item.pageCount) * int64(p.pageSize)
			if wakeExpiry {
				p.wakeExpiryLoop()
			}
			if limit > 0 && reserved >= limit {
				break
			}
		}
		shard.mu.Unlock()
		if limit > 0 && reserved >= limit {
			break
		}
	}

	return result
}

func (p *Provider) popAny() *cacheEntry {
	for shardIndex := range p.shards {
		shard := &p.shards[shardIndex]
		shard.mu.Lock()
		for key, item := range shard.entries {
			p.expiryMu.Lock()
			wakeExpiry := p.cancelExpiryLocked(item)
			p.expiryMu.Unlock()
			delete(shard.entries, key)
			shard.mu.Unlock()
			if wakeExpiry {
				p.wakeExpiryLoop()
			}

			return item
		}
		shard.mu.Unlock()
	}

	return nil
}

func roundUp(value, alignment int) (int, bool) {
	remainder := value % alignment
	if remainder == 0 {
		return value, true
	}
	increment := alignment - remainder
	if value > int(^uint(0)>>1)-increment {
		return 0, false
	}

	return value + increment, true
}
