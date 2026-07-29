// Package madvfree provides a byte-cache provider backed by one anonymous mmap
// arena, supported on 64-bit Linux and macOS.
//
// Provider implements crema.CacheProvider[[]byte]. Set copies values into the
// arena, and Get returns a new Go-managed copy, so callers retain ownership of
// both input and output slices.
//
// # Best-effort retention
//
// Idle allocations are marked reclaimable through the platform memoryBackend
// (Linux MADV_FREE, macOS MADV_FREE_REUSABLE). The kernel may discard those
// pages under memory pressure, causing Get to report a cache miss before the
// entry's TTL expires. This is normal cache behavior, not data corruption. A
// reclaimed page returns zero-filled, and each page stores a generation stamp,
// so the provider detects reclamation and reports a miss rather than serving
// stale bytes. On macOS the provider re-pins a region with MADV_FREE_REUSE
// before validating it; on Linux a single-byte write per page serves the same
// purpose without a system call.
//
// The arena is created with MAP_NORESERVE on Linux; macOS anonymous mappings are
// already lazily backed. Consequently CapacityBytes controls virtual address
// space and allocator capacity; it does not eagerly reserve the same amount of
// RAM or swap. ReservedBytes in Stats is allocator bookkeeping and is not the
// process RSS.
//
// MAP_NORESERVE does not guarantee that a large mapping will succeed. A strict
// overcommit policy, a low RLIMIT_AS, or an equivalent container limit can
// cause NewProvider to fail with an mmap error. Set CapacityBytes explicitly
// below the applicable virtual-address-space limit in such environments.
//
// # Capacity and eviction
//
// Kernel reclaim of an idle page does not itself release the corresponding
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
// # Platform and advice behavior
//
// NewProvider probes the platform's lazy-free mechanism using a real anonymous
// private mapping and returns an error wrapping ErrUnsupported when the running
// kernel cannot provide the required semantics. Unsupported platforms expose
// the same API but NewProvider returns ErrUnsupported.
//
// Advice maps to the host as follows. "Idle" is MADV_FREE on Linux and
// MADV_FREE_REUSABLE on macOS. "Reactivate" is a page write on Linux and
// MADV_FREE_REUSE on macOS. "Discard" is MADV_DONTNEED on Linux; because macOS
// MADV_DONTNEED neither zeroes nor releases anonymous pages, macOS discards with
// MADV_FREE_REUSABLE instead. Config.EnableHugePages and Config.IncludeInCoreDump
// apply MADV_NOHUGEPAGE and MADV_DONTDUMP on Linux only; macOS has no equivalent
// per-region advice and ignores them.
//
// Idle and discard advice failures after initialization are soft: logical cache
// operations continue, while physical memory may remain resident longer.
// Reactivation failure aborts the current Get or Set because accessing a region
// that remains reusable is unsafe on macOS. Stats exposes these failures through
// IdleErrors, ReactivateErrors, and DiscardErrors.
package madvfree
