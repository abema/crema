//go:build (linux && !386 && !arm && !mips && !mipsle) || darwin

package example

import (
	"context"
	"fmt"
	"time"

	"github.com/abema/crema"
	"github.com/abema/crema/ext/madvfree"
)

func Example_madvfree() {
	provider, err := madvfree.NewProvider(madvfree.Config{})
	if err != nil {
		fmt.Println(err)

		return
	}
	defer func() {
		if err := provider.Close(); err != nil {
			panic(err)
		}
	}()

	cache := crema.NewCache(provider, crema.JSONByteStringCodec[GreetingMessage]{})
	value, err := cache.GetOrLoad(
		context.Background(),
		"greeting",
		time.Minute,
		func(context.Context) (GreetingMessage, error) {
			return GreetingMessage{Message: "hello"}, nil
		},
	)
	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(value.Message)
	// Output: hello
}
