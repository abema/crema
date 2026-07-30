# ext/golang-lru

Cache provider for `crema` using `hashicorp/golang-lru`.

## Usage

```go
provider := golanglru.NewCacheProvider[[]byte](1024, 5*time.Minute)
cache := crema.NewCache(provider, crema.JSONByteStringCodec[any]{})
```

## TTL behavior

This provider ignores per-entry TTLs. `expirable.LRU` fixes the expiration of
every entry to the `defaultTTL` given to `NewCacheProvider` and exposes no
per-entry TTL, so the TTL that `crema` passes to `Set` has no effect.

Logical expiration is still correct: `crema` stores the absolute expiry in
`crema.CacheObject.ExpireAtMillis` and revalidates on read, so an entry past its
logical expiry is reloaded even if it is still resident in the LRU. `defaultTTL`
only controls how long an entry physically stays in memory.

### Choosing `defaultTTL`

Set `defaultTTL` to at least the largest TTL passed to `Cache.GetOrLoad` (or
`Cache.Set`).

- **`defaultTTL` shorter than the entry TTL**: entries are evicted before their
  logical expiry, so reads miss and the loader runs more often than intended.
- **`defaultTTL` longer than the entry TTL**: logically expired entries keep
  occupying memory and an LRU slot until they expire or are pushed out, reducing
  the effective cache size.
