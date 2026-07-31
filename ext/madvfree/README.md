# ext/madvfree

Best-effort `crema.CacheProvider[[]byte]` backed by one anonymous `mmap` arena
on 64-bit Linux and macOS.

Idle allocations use `MADV_FREE` on Linux and `MADV_FREE_REUSABLE` on macOS.
The kernel may discard them under memory pressure, so `Get` can return a cache
miss before TTL expiry. Linux uses `MADV_DONTNEED` for pages returned to the
internal allocator; macOS keeps them reusable until the allocator needs them
again. Returned values are always copied into Go-managed memory.

Linux creates the arena with `MAP_NORESERVE`, allowing its virtual size to
exceed the immediately available physical memory without reserving swap for
every page. macOS anonymous mappings are lazily backed without that flag.

Values through 32 KiB are eligible for fixed-size slab classes. Sub-page
classes use one-page slabs; larger classes use the smallest multi-page slab
that stores at least two values and reduces reserved bytes per value compared
with individual extents. A value falls back to a contiguous extent when class
rounding would lose that saving or no complete slab fits. Larger values always
use extents. A slab is marked reclaimable only when it has no active readers.
During `Get` and slot allocation, reclaim is detected per physical page. Only
slots whose actual payload overlaps a reclaimed page are invalidated; metadata
for unaffected slots is rebuilt from Go-owned bookkeeping.

## Usage

```go
provider, err := madvfree.NewProvider(madvfree.Config{})
if err != nil {
    // handle error
}
defer provider.Close()

cache := crema.NewCache(provider, crema.JSONByteStringCodec[MyValue]{})
```

The zero-value `Config` uses a 64 GiB virtual arena, the default size classes,
and 64 index shards. Linux also applies `MADV_NOHUGEPAGE` and `MADV_DONTDUMP`;
macOS has no equivalent per-region advice and ignores those options. Set
`CapacityBytes`, `SizeClasses`, or `ShardCount` to override their defaults.
Set `EnableHugePages` or `IncludeInCoreDump` to opt out of the corresponding
Linux advice.

Lazy backing does not guarantee that the default 64 GiB mapping will succeed.
A strict Linux overcommit policy, a low `RLIMIT_AS`, or an equivalent container
limit can make `NewProvider` fail with an `mmap` `ENOMEM`. Set `CapacityBytes`
explicitly below that environment's virtual-address-space limit.

The provider supports 64-bit Linux and macOS. Other platforms return
`madvfree.ErrUnsupported`. `NewProvider` probes the platform mechanism against
a real anonymous private mapping: `MADV_FREE` on Linux and the
`MADV_FREE_REUSABLE` / `MADV_FREE_REUSE` pair on macOS. It returns an error
wrapping `madvfree.ErrUnsupported` when the running kernel cannot use the
required mechanism.

After initialization, failures to mark a region idle or discard it are soft:
cache indexing, generations, and arena accounting continue normally, but RSS
may remain higher. Reactivation is different on macOS because
`MADV_FREE_REUSE` must succeed before reusable pages can be accessed safely; a
reactivation failure aborts the current `Get` or `Set` and is returned as an
error. `Stats.IdleErrors`, `Stats.DiscardErrors`, and
`Stats.ReactivateErrors` expose these failures.

## Capacity and eviction

Kernel reclaim and allocator capacity are separate. Reclaiming an idle page
reduces physical memory pressure, but does not by itself return the
corresponding extent or slab slot to the provider's allocator. Reclaimed
entries are removed when `Get` encounters them; small-slab allocation normally
skips full slabs and probes them for reclaim when the class population doubles
or when it needs to recover logical capacity. `Delete`, TTL expiration, `Purge`,
and `Trim` release entries explicitly.

`Set` does not run LRU or any other automatic eviction. A workload that only
writes new keys and never reads or expires old keys can therefore fill the
logical arena and receive `madvfree.ErrCapacity`, even after the kernel has
reclaimed physical pages and reduced RSS. Use TTLs for time-bounded entries, or
call `Trim` before retrying when the application requires space immediately.

`Trim` takes the provider's exclusive lifecycle lock, so concurrent `Get`,
`Set`, `Delete`, `Purge`, and `Close` wait until it finishes. It removes
arbitrary indexed entries, not the least recently used entries, until the
requested number of reserved arena bytes has been released or the index is
empty. The returned byte count may exceed the target because allocation is
released at slab or extent granularity.

The provider intentionally has no background reclaim scanner. Determining
whether an idle page was reclaimed requires reactivating it: a write-touch on
Linux or `MADV_FREE_REUSE` on macOS. Reactivating a page that has not been
reclaimed removes its reclaimable state.

## Comparison benchmarks

Benchmarks on Linux and macOS compare this provider with `ext/ristretto` at
64 B, 4 KiB, 6 KiB, and 64 KiB:

```sh
go test ./bench -run '^$' -bench '^BenchmarkProvider' -benchmem
```

The benchmark names make the different completion and ownership semantics
explicit:

- `madvfree_copy`: synchronous access; `Get` returns a copy.
- `madvfree_extent_copy`: the same provider with slabs disabled.
- `ristretto_zero_copy`: the regular Ristretto adapter; `Get` returns the
  stored slice.
- `ristretto_copy`: copies Ristretto values on `Get` for a closer comparison.
- `ristretto_async`: measures Ristretto's asynchronous `Set`.
- `ristretto_wait`: calls `Cache.Wait` after every `Set`.
- `madvfree_extent_sync`: synchronous `Set` with slabs disabled.

Ristretto is configured with byte-length cost accounting and the same 256 MiB
logical capacity. These benchmarks measure provider API cost; they do not
simulate kernel reclaim or compare eviction quality.

One reference run produced the following 6 KiB `Get` results:

| Provider | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| `madvfree_copy` | 4,118–4,604 | 6,144 | 1 |
| `madvfree_extent_copy` | 2,177–2,405 | 6,144 | 1 |
| `ristretto_copy` | 441.8–468.9 | 6,166 | 2 |
| `ristretto_zero_copy` | 33.72–33.95 | 17 | 1 |

Environment: Linux/arm64, 4 vCPUs, Go 1.25, 256 MiB configured capacity,
`-benchtime=1s -count=3`, measured on 2026-07-29. The slab layout reserved five
4 KiB pages for three 6 KiB values, or about 6.67 KiB per value, compared with
8 KiB per value for extents. The slab therefore traded lower reserved memory
for higher `Get` cost in this run. Treat these numbers as an order-of-magnitude
reference rather than a portable performance guarantee.

## Memory-pressure integration test

The opt-in integration test measures the effective available memory at runtime.
Linux prefers the cgroup v2 or v1 memory limit and falls back to
`MemAvailable`; macOS uses the system-wide free percentage reported by
`memory_pressure -Q`. It writes 1.5 times that amount into the arena, then
verifies that the kernel reclaimed at least one idle extent and that every
cache hit still returns the correct bytes.

```sh
MADVFREE_PRESSURE_TEST=1 go test -run TestMADVFreeUnderMemoryPressure -v
```

The default safety ceiling is 4 GiB on Linux and 96 GiB on macOS. A larger
environment is skipped unless the ceiling is explicitly raised:

```sh
MADVFREE_PRESSURE_TEST=1 \
MADVFREE_PRESSURE_TEST_MAX_BYTES=8589934592 \
go test -run TestMADVFreeUnderMemoryPressure -v
```

The test is excluded by `-short`. Run it in an environment where temporary
memory pressure is acceptable.
