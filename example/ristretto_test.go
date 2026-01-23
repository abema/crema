package example

import (
	"context"
	"fmt"
	"time"

	"github.com/abema/crema"
	cremaristretto "github.com/abema/crema/ext/ristretto"
	dgraphristretto "github.com/dgraph-io/ristretto"
)

func ExampleRistrettoCacheProvider() {
	cache, err := dgraphristretto.NewCache(&dgraphristretto.Config{
		NumCounters: 1e6,
		MaxCost:     1 << 30,
		BufferItems: 64,
	})
	if err != nil {
		fmt.Println(err)

		return
	}

	provider, err := cremaristretto.NewRistrettoCacheProvider[crema.CacheObject[string]](cache)
	if err != nil {
		fmt.Println(err)

		return
	}

	cremaCache := crema.NewCache(provider, crema.NoopCacheStorageCodec[string]{})
	value, err := cremaCache.GetOrLoad(context.Background(), "greeting", time.Minute, func(ctx context.Context) (string, error) {
		return "hello", nil
	})
	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(value)
	// Output: hello
}
