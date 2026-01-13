package example

import (
	"context"
	"fmt"
	"time"

	"github.com/abema/crema"
	cremavalkey "github.com/abema/crema/ext/valkey-go"
	"github.com/valkey-io/valkey-go"
)

type GreetingMessage struct {
	Message string `json:"message"`
}

func ExampleValkeyCacheProvider() {
	client, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{"127.0.0.1:6379"}})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer client.Close()

	provider := cremavalkey.NewValkeyCacheProvider(client)
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
