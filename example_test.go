package crema

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func ExampleCache() {
	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	codec := NoopCacheStorageCodec[int]{}
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

func ExampleWithNegativeCache() {
	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	cache := NewCache(
		provider,
		NoopCacheStorageCodec[int]{},
		WithNegativeCache[int, CacheObject[int]](time.Second),
	)

	backendDown := errors.New("backend down")
	calls := 0
	loader := func(context.Context) (int, error) {
		calls++

		return 0, backendDown
	}

	for range 3 {
		_, err := cache.GetOrLoad(context.Background(), "answer", time.Minute, loader)
		fmt.Println(errors.Is(err, backendDown))
	}
	fmt.Println("loader calls:", calls)
	// Output:
	// true
	// true
	// true
	// loader calls: 1
}
