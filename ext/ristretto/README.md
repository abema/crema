# ext/ristretto

Ristretto-backed cache provider for `crema`.

## Features

- `RistrettoCacheProvider` for storing encoded cache entries in ristretto

## Usage

```go
import (
	dgraphristretto "github.com/dgraph-io/ristretto/v2"
	cremaristretto "github.com/abema/crema/ext/ristretto"
)

cache, err := dgraphristretto.NewCache(&dgraphristretto.Config[string, []byte]{
	NumCounters: 1e6,
	MaxCost:     1 << 30,
	BufferItems: 64,
})
if err != nil {
	panic(err)
}

provider, err := cremaristretto.NewRistrettoCacheProvider[[]byte](cache)
if err != nil {
	panic(err)
}
```
