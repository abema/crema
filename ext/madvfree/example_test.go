//go:build (linux && !386 && !arm && !mips && !mipsle) || darwin

package madvfree_test

import (
	"context"
	"fmt"
	"time"

	"github.com/abema/crema/ext/madvfree"
)

func Example() {
	provider, err := madvfree.NewProvider(madvfree.Config{})
	if err != nil {
		panic(err)
	}
	defer func() {
		if err := provider.Close(); err != nil {
			panic(err)
		}
	}()

	ctx := context.Background()
	if err := provider.Set(ctx, "greeting", []byte("hello"), time.Minute); err != nil {
		panic(err)
	}

	value, found, err := provider.Get(ctx, "greeting")
	if err != nil {
		panic(err)
	}
	fmt.Printf("%s, found=%t\n", value, found)

	stats := provider.Stats()
	fmt.Printf("entries=%d, logical-bytes=%d\n", stats.Entries, stats.LogicalBytes)

	// Output:
	// hello, found=true
	// entries=1, logical-bytes=5
}
