# ext/gomemcache

Memcached cache provider for `crema` using `gomemcache`.

## Features

- `MemcachedCacheProvider` for storing cache data in Memcached with TTL handling

## Usage

```go
client := memcache.New("127.0.0.1:11211")
provider := gomemcache.NewMemcachedCacheProvider(client)
```

## TTL handling

[Memcached][protocol] interprets expiration values over 30 days as absolute
UNIX timestamps. This provider converts longer TTLs accordingly instead of
letting them expire immediately.

`gomemcache` stores expiration in an `int32`, whose latest absolute timestamp is
2038-01-19T03:14:07Z. If a requested expiry exceeds that limit, the provider
uses whichever expires later: that timestamp or a relative 30-day TTL. The
entry therefore expires earlier than requested, without being stored already
expired. Positive fractional seconds round up; non-positive TTLs disable
expiration.

[protocol]: https://github.com/memcached/memcached/blob/master/doc/protocol.txt
