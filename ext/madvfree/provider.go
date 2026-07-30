//go:build (linux && !386 && !arm && !mips && !mipsle) || darwin

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

	hits             atomic.Uint64
	misses           atomic.Uint64
	reclaimedMisses  atomic.Uint64
	generationMisses atomic.Uint64
	expiredMisses    atomic.Uint64

	idleCalls        atomic.Uint64
	idleErrors       atomic.Uint64
	reactivateCalls  atomic.Uint64
	reactivateErrors atomic.Uint64
	discardCalls     atomic.Uint64
	discardErrors    atomic.Uint64

	allocations       atomic.Uint64
	allocationFails   atomic.Uint64
	smallAllocations  atomic.Uint64
	extentAllocations atomic.Uint64
}

// Provider is a concurrent, best-effort byte cache backed by a single
// anonymous mmap arena.
//
// Idle allocations are marked reclaimable through the platform memoryBackend, so
// the kernel may discard them under memory pressure. Get therefore may miss
// before an entry's TTL expires.
type Provider struct {
	lifecycle sync.RWMutex
	closed    bool

	backend    memoryBackend
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

	expiryMu    sync.Mutex
	expirations expiryHeap
	expiryWake  chan struct{}
	expiryStop  chan struct{}
	expiryDone  chan struct{}

	stats counters
}

var _ crema.CacheProvider[[]byte] = (*Provider)(nil)

// NewProvider creates an anonymous-mmap cache for the running platform.
//
// The zero-value Config is valid. Initialization probes the platform's lazy-free
// mechanism and applies the configured arena advice before starting the TTL
// worker. The caller must call Close when the provider is no longer needed. On
// unsupported platforms NewProvider returns an error wrapping ErrUnsupported.
//
//nolint:cyclop,funlen // Initialization keeps backend setup and cleanup paths together.
func NewProvider(config Config) (*Provider, error) {
	if config.CapacityBytes < 0 || config.ShardCount < 0 {
		return nil, fmt.Errorf("%w: capacity and shard count must be non-negative", ErrInvalidConfig)
	}

	backend, err := newBackend(config)
	if err != nil {
		return nil, err
	}

	capacityBytes := config.CapacityBytes
	if capacityBytes == 0 {
		capacityBytes = DefaultCapacityBytes
	}
	pageSize := backend.pageSize()
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

	arena, err := backend.mapArena(capacity)
	if err != nil {
		return nil, err
	}

	shards := make([]indexShard, shardCount)
	for i := range shards {
		shards[i].entries = make(map[string]*cacheEntry)
	}
	pageCount := uint32(capacity / pageSize)

	provider := &Provider{
		backend:     backend,
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
//
//nolint:cyclop // Miss classification and reactivation errors require distinct outcomes.
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

	reclaimed, acquired, err := p.acquire(item)
	if err != nil {
		return nil, false, err
	}
	if !acquired {
		p.removeIfSame(key, item)
		p.retire(item)
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
		region := p.extentBytes(item.startPage, item.pageCount)
		if err := p.markActive(region); err != nil {
			p.allocatorMu.Lock()
			p.allocator.release(startPage, pageCount)
			p.stats.entries.Add(-1)
			p.stats.logicalBytes.Add(-int64(len(value)))
			p.stats.reservedBytes.Add(-int64(pageCount) * int64(p.pageSize))
			p.allocatorMu.Unlock()
			p.stats.allocationFails.Add(1)

			return err
		}
		p.writeExtent(item, value)
		p.stats.allocations.Add(1)
		p.stats.extentAllocations.Add(1)

		p.markIdle(region)
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
	for _, item := range p.drainIndex() {
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
	p.discard(p.arena)
	if err := p.backend.unmap(p.arena); err != nil {
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
		Entries:           entries,
		LogicalBytes:      logicalBytes,
		ReservedBytes:     reservedBytes,
		FreeBytes:         freeBytes,
		Hits:              p.stats.hits.Load(),
		Misses:            p.stats.misses.Load(),
		ReclaimedMisses:   p.stats.reclaimedMisses.Load(),
		GenerationMisses:  p.stats.generationMisses.Load(),
		ExpiredMisses:     p.stats.expiredMisses.Load(),
		IdleCalls:         p.stats.idleCalls.Load(),
		IdleErrors:        p.stats.idleErrors.Load(),
		ReactivateCalls:   p.stats.reactivateCalls.Load(),
		ReactivateErrors:  p.stats.reactivateErrors.Load(),
		DiscardCalls:      p.stats.discardCalls.Load(),
		DiscardErrors:     p.stats.discardErrors.Load(),
		Allocations:       p.stats.allocations.Load(),
		AllocationFails:   p.stats.allocationFails.Load(),
		SmallAllocations:  p.stats.smallAllocations.Load(),
		ExtentAllocations: p.stats.extentAllocations.Load(),
	}
}

func (p *Provider) acquire(item *cacheEntry) (bool, bool, error) {
	if item.kind == allocationSmall {
		return p.acquireSmall(item)
	}

	return p.acquireExtent(item)
}

func (p *Provider) acquireExtent(item *cacheEntry) (bool, bool, error) {
	item.mu.Lock()
	defer item.mu.Unlock()
	if item.state != entryLive {
		return false, false, nil
	}
	if item.refs == 0 {
		reclaimed, valid, err := p.activateAndValidateExtent(item)
		if err != nil {
			return false, false, err
		}
		if !valid {
			item.state = entryDead

			return reclaimed, false, nil
		}
	}
	item.refs++

	return false, true, nil
}

func (p *Provider) activateAndValidateExtent(item *cacheEntry) (bool, bool, error) {
	if p.backend.canReadIdle() {
		return p.precheckAndActivateExtent(item)
	}

	return p.activateThenValidateExtent(item)
}

func (p *Provider) activateThenValidateExtent(item *cacheEntry) (bool, bool, error) {
	if err := p.markActive(p.extentBytes(item.startPage, item.pageCount)); err != nil {
		return false, false, err
	}
	for pageIndex := uint32(0); pageIndex < item.pageCount; pageIndex++ {
		generation, storedIndex := pageHeader(p.page(item.startPage + pageIndex))
		if generation != item.generation || storedIndex != pageIndex {
			return generation == 0, false, nil
		}
	}

	return false, true, nil
}

func (p *Provider) precheckAndActivateExtent(item *cacheEntry) (bool, bool, error) {
	activated := false
	for pageIndex := uint32(0); pageIndex < item.pageCount; pageIndex++ {
		page := p.page(item.startPage + pageIndex)
		generation := pageGeneration(page)
		if generation != item.generation {
			if activated {
				p.stats.reactivateCalls.Add(1)
			}

			return generation == 0, false, nil
		}
		if err := p.reactivate(page); err != nil {
			return false, false, err
		}
		activated = true

		// The page may have been reclaimed before the write-touch.
		generation, storedIndex := pageHeader(page)
		if generation != item.generation || storedIndex != pageIndex {
			p.stats.reactivateCalls.Add(1)

			return generation == 0, false, nil
		}
	}
	if activated {
		p.stats.reactivateCalls.Add(1)
	}

	return false, true, nil
}

func pageHeader(page []byte) (uint64, uint32) {
	return pageGeneration(page),
		binary.LittleEndian.Uint32(page[pageIndexOffset:touchOffset])
}

func pageGeneration(page []byte) uint64 {
	return binary.LittleEndian.Uint64(page[generationOffset:pageIndexOffset])
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

		p.finalizeExtent(item)

		return
	}
	p.markIdle(p.extentBytes(item.startPage, item.pageCount))
	item.mu.Unlock()
}

// retire marks item for removal and frees it once no reader holds a reference.
//
// It is also the tail of a failed acquire: acquire leaves the entry in a
// non-live state, so retiring it there frees it as soon as its last reader
// leaves.
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
		p.finalizeExtent(item)
	}
}

func (p *Provider) finalizeExtent(item *cacheEntry) {
	item.mu.Lock()
	if item.freed || item.refs != 0 {
		item.mu.Unlock()

		return
	}
	item.freed = true
	item.state = entryDead
	p.discard(p.extentBytes(item.startPage, item.pageCount))
	item.mu.Unlock()

	p.allocatorMu.Lock()
	p.allocator.release(item.startPage, item.pageCount)
	p.stats.reservedBytes.Add(-int64(item.pageCount) * int64(p.pageSize))
	p.stats.entries.Add(-1)
	p.stats.logicalBytes.Add(-int64(item.length))
	p.allocatorMu.Unlock()
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
	return &p.shards[maphash.String(p.hashSeed, key)%uint64(len(p.shards))]
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
	wakeExpiry := p.rescheduleExpiry(old, item)
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
	wakeExpiry := p.cancelExpiry(item)
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
	wakeExpiry := p.cancelExpiry(expected)
	delete(shard.entries, key)
	shard.mu.Unlock()
	if wakeExpiry {
		p.wakeExpiryLoop()
	}

	return true
}

// drainIndex removes every indexed entry and returns them for retirement.
func (p *Provider) drainIndex() []*cacheEntry {
	var result []*cacheEntry
	for shardIndex := range p.shards {
		shard := &p.shards[shardIndex]
		shard.mu.Lock()
		for key, item := range shard.entries {
			wakeExpiry := p.cancelExpiry(item)
			delete(shard.entries, key)
			result = append(result, item)
			if wakeExpiry {
				p.wakeExpiryLoop()
			}
		}
		shard.mu.Unlock()
	}

	return result
}

func (p *Provider) popAny() *cacheEntry {
	for shardIndex := range p.shards {
		shard := &p.shards[shardIndex]
		shard.mu.Lock()
		for key, item := range shard.entries {
			wakeExpiry := p.cancelExpiry(item)
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
