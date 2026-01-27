package example

import (
	"context"
	"fmt"
	"time"

	"github.com/abema/crema"
	cremarueidis "github.com/abema/crema/ext/rueidis"
	"github.com/redis/rueidis"
)

func ExampleRedisCacheProvider() {
	client, err := rueidis.NewClient(rueidis.ClientOption{InitAddress: []string{"127.0.0.1:6379"}})
	if err != nil {
		fmt.Println(err)

		return
	}
	defer client.Close()

	provider := cremarueidis.NewRedisCacheProvider(client)
	cache := crema.NewCache(provider, crema.JSONByteStringCodec[GreetingMessage]{})

	value, err := cache.GetOrLoad(context.Background(), "greeting", time.Minute, func(ctx context.Context) (GreetingMessage, error) {
		return GreetingMessage{Message: "hello"}, nil
	})
	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println(value.Message)
	// Output: hello
}
