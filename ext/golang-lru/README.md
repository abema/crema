# ext/golang-lru

Cache provider for `crema` using `hashicorp/golang-lru`.

## Usage

```go
provider := golanglru.NewCacheProvider[[]byte](1024, 5*time.Minute)
cache := crema.NewCache(provider, crema.JSONByteStringCodec[any]{})
```

## TTL behavior

Every entry uses the `defaultTTL` passed to `NewCacheProvider`; the TTL passed
to `CacheProvider.Set` is ignored. A non-positive `defaultTTL` disables
time-based eviction by the provider.

When used through `crema.Cache`, logical expiration is tracked separately in
`crema.CacheObject`. Set `defaultTTL` to at least the longest logical TTL, or
disable time-based eviction, to avoid premature cache misses. A longer
`defaultTTL` may retain logically expired entries until they expire or are
evicted by the LRU.
