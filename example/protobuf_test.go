package example

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/abema/crema"
	cremaprotobuf "github.com/abema/crema/ext/protobuf"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type byteProvider struct {
	mu    sync.Mutex
	items map[string][]byte
}

func (b *byteProvider) Get(_ context.Context, key string) ([]byte, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	value, ok := b.items[key]
	return value, ok, nil
}

func (b *byteProvider) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.items[key] = value
	return nil
}

func (b *byteProvider) Delete(_ context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.items, key)
	return nil
}

func ExampleProtobufCodec() {
	provider := &byteProvider{items: make(map[string][]byte)}
	// Provide a zero-value instance of the message type to decode into.
	codec, err := cremaprotobuf.NewProtobufCodec(&wrapperspb.StringValue{})
	if err != nil {
		fmt.Println(err)
		return
	}

	cache := crema.NewCache(provider, codec)
	value, err := cache.GetOrLoad(context.Background(), "greeting", time.Minute, func(ctx context.Context) (*wrapperspb.StringValue, error) {
		return wrapperspb.String("hello"), nil
	})
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Println(value.Value)
	// Output: hello
}
