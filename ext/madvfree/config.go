package madvfree

import "errors"

// DefaultCapacityBytes is the virtual arena size selected by a zero-value Config.
const DefaultCapacityBytes = 64 << 30

var (
	// ErrUnsupported is returned on platforms where MADV_FREE anonymous mappings
	// are not supported by this provider.
	ErrUnsupported = errors.New("madvfree: unsupported platform")
	// ErrClosed is returned after the provider has been closed.
	ErrClosed = errors.New("madvfree: provider is closed")
	// ErrInvalidConfig is returned when Config cannot describe a usable arena.
	ErrInvalidConfig = errors.New("madvfree: invalid config")
	// ErrCapacity is returned when the arena allocator cannot satisfy a Set.
	// Set does not automatically evict another entry.
	ErrCapacity = errors.New("madvfree: cache capacity exhausted")
)

// Config controls the anonymous mmap arena.
//
// Its zero value is ready to use.
type Config struct {
	// CapacityBytes is the arena size in bytes. Zero selects
	// DefaultCapacityBytes. It is rounded up to the host page size. This is
	// logical allocator capacity, not resident memory. Environments with a
	// strict overcommit policy or a low RLIMIT_AS may need a smaller explicit
	// value because even a MAP_NORESERVE mapping can fail with ENOMEM.
	CapacityBytes int
	// SizeClasses controls fixed-size slots used for values that fit in one
	// size-class slab. Nil selects defaults through 32 KiB. An empty slice
	// disables slabs. Custom classes must be strictly increasing and no larger
	// than 32 KiB.
	SizeClasses []int
	// ShardCount is the number of independently locked key-index shards.
	// Zero selects 64.
	ShardCount int
	// EnableHugePages skips the default MADV_NOHUGEPAGE advice.
	EnableHugePages bool
	// IncludeInCoreDump skips the default MADV_DONTDUMP advice.
	IncludeInCoreDump bool
}

// Stats is a point-in-time snapshot of provider activity.
//
// Counter fields are cumulative for the lifetime of a Provider. Gauge fields
// describe the state observed during the call to Provider.Stats.
type Stats struct {
	// Entries is the number of currently indexed cache entries.
	Entries int64
	// LogicalBytes is the sum of the value lengths of current entries.
	LogicalBytes int64
	// ReservedBytes is arena space assigned to current slabs and extents.
	// It is not resident-set size.
	ReservedBytes int64
	// FreeBytes is currently unassigned arena space.
	FreeBytes int64

	// Hits is the number of successful Get calls.
	Hits uint64
	// Misses includes absent, expired, reclaimed, and generation-invalid Get
	// calls.
	Misses uint64

	// ReclaimedMisses is the number of Get misses identified as kernel page
	// reclamation.
	ReclaimedMisses uint64
	// GenerationMisses is the number of Get misses caused by stale or
	// inconsistent allocation generations.
	GenerationMisses uint64
	// ExpiredMisses is the number of Get misses caused by TTL expiration.
	ExpiredMisses uint64

	// MadvFreeCalls and MadvDontNeedCalls count runtime advice attempts. They do
	// not include the initialization probe.
	MadvFreeCalls     uint64
	MadvDontNeedCalls uint64
	// MadvFreeErrors and MadvDontNeedErrors count soft runtime advice failures.
	MadvFreeErrors     uint64
	MadvDontNeedErrors uint64

	// Allocations counts successful slab-slot and extent allocations.
	Allocations uint64
	// AllocationFails counts Set calls that could not allocate arena space.
	AllocationFails uint64

	// SmallAllocations and ExtentAllocations partition Allocations by allocator.
	SmallAllocations  uint64
	ExtentAllocations uint64
}
