// Package madvfree provides a Linux byte-cache provider backed by one
// anonymous mmap arena.
//
// Provider implements crema.CacheProvider[[]byte]. Set copies values into the
// arena, and Get returns a new Go-managed copy, so callers retain ownership of
// both input and output slices.
//
// # Best-effort retention
//
// Idle allocations are marked MADV_FREE. Linux may discard those pages under
// memory pressure, causing Get to report a cache miss before the entry's TTL
// expires. This is normal cache behavior, not data corruption. Pages returned
// to the internal allocator are discarded with MADV_DONTNEED.
//
// The arena uses MAP_NORESERVE. Consequently, CapacityBytes controls virtual
// address space and allocator capacity; it does not eagerly reserve the same
// amount of RAM or swap. ReservedBytes in Stats is allocator bookkeeping and
// is not the process RSS.
//
// MAP_NORESERVE does not guarantee that a large mapping will succeed. A strict
// overcommit policy, a low RLIMIT_AS, or an equivalent container limit can
// cause NewProvider to fail with an mmap error. Set CapacityBytes explicitly
// below the applicable virtual-address-space limit in such environments.
//
// # Capacity and eviction
//
// Kernel reclaim of a MADV_FREE page does not itself release the corresponding
// allocator slot. Reclaimed entries are removed when encountered by Get;
// small-slab allocation can also detect and repair reclaimed slab pages.
// Delete, TTL expiration, Purge, and Trim release entries explicitly.
//
// The provider has no LRU or automatic eviction on Set. A write-only workload
// can therefore exhaust logical arena capacity and receive ErrCapacity even
// when the kernel has already reduced the process RSS. Callers that require
// space immediately should use Trim or assign TTLs. Trim chooses arbitrary
// indexed entries rather than ordering them by recency.
//
// # Platform and madvise behavior
//
// The provider is supported on 64-bit Linux. NewProvider probes MADV_FREE using
// a real anonymous private mapping and returns an error wrapping
// ErrUnsupported when the running kernel cannot provide the required
// semantics. Unsupported platforms expose the same API but NewProvider returns
// ErrUnsupported.
//
// MADV_FREE and MADV_DONTNEED failures after initialization are soft failures:
// logical cache operations continue, while physical memory may remain resident
// longer. Stats exposes these failures.
package madvfree
