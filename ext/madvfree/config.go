package madvfree

import (
	"errors"
	"time"
)

// DefaultCapacityBytes is the virtual arena size selected by a zero-value Config.
const DefaultCapacityBytes = 64 << 30

// DefaultIdleDelay is the idle-marking delay selected by a zero-value Config.
const DefaultIdleDelay = 10 * time.Millisecond

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
	// value because a lazily backed mapping can still fail with ENOMEM.
	CapacityBytes int
	// SizeClasses controls fixed-size slots used for values that fit in one
	// size-class slab. Nil selects defaults through 32 KiB. An empty slice
	// disables slabs. Custom classes must be strictly increasing and no larger
	// than 32 KiB.
	SizeClasses []int
	// ShardCount is the number of independently locked key-index shards.
	// Zero selects 64.
	ShardCount int
	// IdleDelay is how long an allocation whose last reader has released it stays
	// pinned before the provider marks it reclaimable. The advice is issued only
	// after IdleDelay has elapsed without any access, so a key read in a loop pays
	// one idle-plus-reactivate pair per delay period instead of one per call.
	//
	// Zero selects DefaultIdleDelay. A negative value returns ErrInvalidConfig.
	// Longer delays remove more advice calls but keep unreferenced allocations
	// unreclaimable for up to twice IdleDelay after their last access, and an
	// allocation accessed more often than once per IdleDelay is never offered to
	// the kernel while that traffic continues.
	IdleDelay time.Duration
	// DisableIdleDelay marks an allocation reclaimable as soon as its last reader
	// releases it, ignoring IdleDelay. It minimizes the time an unreferenced
	// allocation is unreclaimable at the cost of one idle advice per access.
	DisableIdleDelay bool
	// EnableHugePages skips the default MADV_NOHUGEPAGE advice on Linux.
	// It has no effect on macOS.
	EnableHugePages bool
	// IncludeInCoreDump skips the default MADV_DONTDUMP advice on Linux.
	// It has no effect on macOS.
	IncludeInCoreDump bool
}

// Stats is a point-in-time snapshot of provider activity.
//
// Counter fields are cumulative for the lifetime of a Provider. Gauge fields
// describe the state observed during the call to Provider.Stats.
type Stats struct {
	// Entries is the number of allocated cache entries, including entries that
	// were removed from the index but remain pinned by in-flight readers.
	Entries int64
	// LogicalBytes is the sum of value lengths represented by Entries.
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

	// IdleCalls counts regions marked reclaimable (Linux MADV_FREE, Darwin
	// MADV_FREE_REUSABLE). ReactivateCalls counts regions re-pinned before use
	// (a page write on Linux, MADV_FREE_REUSE on Darwin). DiscardCalls counts
	// regions released (Linux MADV_DONTNEED, Darwin MADV_FREE_REUSABLE). None
	// include the initialization probe.
	IdleCalls       uint64
	ReactivateCalls uint64
	DiscardCalls    uint64
	// IdleErrors, ReactivateErrors, and DiscardErrors count runtime advice
	// failures for the corresponding operation. Idle and discard failures are
	// soft; a reactivation failure aborts the current Get or Set.
	IdleErrors       uint64
	ReactivateErrors uint64
	DiscardErrors    uint64

	// IdleDeferrals counts allocations queued for deferred idle marking instead
	// of being marked reclaimable as soon as their last reader released them. It
	// stays zero when Config.DisableIdleDelay is set.
	IdleDeferrals uint64
	// IdleCancellations counts queued deferrals resolved without any idle advice
	// because the allocation was in use again, already reclaimable, or discarded
	// before its delay elapsed. A deferral rescheduled by further access counts
	// in neither counter.
	IdleCancellations uint64

	// Allocations counts successful slab-slot and extent allocations.
	Allocations uint64
	// AllocationFails counts Set calls that could not allocate arena space.
	AllocationFails uint64

	// SmallAllocations and ExtentAllocations partition Allocations by allocator.
	SmallAllocations  uint64
	ExtentAllocations uint64
}
