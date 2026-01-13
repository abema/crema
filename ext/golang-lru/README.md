# ext/golang-lru

Cache provider for `crema` using `hashicorp/golang-lru`.

## Usage

```go
provider := golanglru.NewCacheProvider[[]byte](1024, 5*time.Minute)
cache := crema.NewCache(provider, crema.JSONByteStringCodec[any]{})
```
