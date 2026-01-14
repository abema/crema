package crema

import (
	"context"
	"fmt"
	"time"
)

func ExampleCache() {
	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	codec := NoopSerializationCodec[int]{}
	cache := NewCache(provider, codec)

	value, err := cache.GetOrLoad(context.Background(), "answer", time.Minute, func(ctx context.Context) (int, error) {
		// Database or computation logic here
		return 42, nil
	})
	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(value)
	// Output: 42
}
