# ext/gomemcache

Memcached cache provider for `crema` using `gomemcache`.

## Features

- `MemcachedCacheProvider` for storing cache data in Memcached with TTL handling

## Usage

```go
client := memcache.New("127.0.0.1:11211")
provider := gomemcache.NewMemcachedCacheProvider(client)
```

## TTL handling and the year 2038

Memcached overloads a single expiration field: a value of up to 30 days
(2,592,000 seconds) is a number of seconds from now, and anything larger is an
absolute UNIX timestamp. Passing a TTL longer than 30 days through as a relative
count is therefore read as a timestamp in the distant past, which expires the
entry immediately. That is why `Set` converts long TTLs rather than passing them
along verbatim.

The absolute form is capped as well. The protocol specifies it as
"Unix time (number of seconds since January 1, 1970, as a 32-bit value)"
([memcached protocol.txt][protocol]), so the latest expiry that can be expressed
is `math.MaxInt32`, i.e. 2038-01-19T03:14:07Z. `gomemcache` mirrors this with an
`int32` field, and because the value travels as a decimal number in the text
protocol, a wider client-side type would not help: the server would not accept
it. The limit can only be worked around, not lifted.

A TTL is therefore mapped to the closest expiry Memcached can represent:

| Requested TTL | Expiration sent |
| --- | --- |
| Up to 30 days | Relative seconds, exactly as requested |
| Over 30 days, and `now + TTL` fits in `int32` | Absolute timestamp, exactly as requested |
| Over 30 days, and `now + TTL` overflows `int32` | The later of `math.MaxInt32` and a relative 30-day TTL |

Only the last row is lossy, and it loses as little as possible. Until late 2037
the maximum absolute timestamp is the closer of the two, so it wins; after that a
relative 30-day TTL reaches further, so the mapping degrades to it and keeps
working past 2038 without ever sending a timestamp in the past. Sub-second TTLs
round up to one second, and a zero or negative TTL is stored with no expiration.

The consequence to be aware of: a TTL beyond the `int32` range is **silently
shortened**. Nothing is logged and no error is returned, because an entry that
expires earlier than requested is a safe outcome for a cache, while one that
expires immediately is not.

[protocol]: https://github.com/memcached/memcached/blob/master/doc/protocol.txt
