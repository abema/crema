package valkeygo

import (
	"context"
	"errors"
	"time"

	"github.com/abema/crema"
	"github.com/valkey-io/valkey-go"
)

// ValkeyCacheProvider stores cache entries in Valkey.
type ValkeyCacheProvider struct {
	client valkey.Client
}

var _ crema.CacheProvider[[]byte] = (*ValkeyCacheProvider)(nil)

// NewValkeyCacheProvider builds a Valkey-backed cache provider.
func NewValkeyCacheProvider(client valkey.Client) *ValkeyCacheProvider {
	return &ValkeyCacheProvider{client: client}
}

// Get retrieves a cached value from Valkey.
func (p *ValkeyCacheProvider) Get(ctx context.Context, key string) ([]byte, bool, error) {
	result := p.client.Do(ctx, p.client.B().Get().Key(key).Build())
	msg, err := result.ToMessage()

	return parseValkeyGetMessage(msg, err)
}

// Set stores a cache entry in Valkey with the given TTL.
func (p *ValkeyCacheProvider) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	builder := p.client.B().Set().Key(key).Value(valkey.BinaryString(value))
	if ttl > 0 {
		return p.client.Do(ctx, builder.Px(ttl).Build()).Error()
	}

	return p.client.Do(ctx, builder.Build()).Error()
}

// Delete removes a cached value from Valkey.
func (p *ValkeyCacheProvider) Delete(ctx context.Context, key string) error {
	return p.client.Do(ctx, p.client.B().Del().Key(key).Build()).Error()
}

func parseValkeyGetMessage(msg valkey.ValkeyMessage, err error) ([]byte, bool, error) {
	if msg.IsNil() {
		return nil, false, nil
	}
	if err != nil {
		if errors.Is(err, valkey.Nil) {
			return nil, false, nil
		}

		return nil, false, err
	}
	value, err := msg.AsBytes()
	if err != nil {
		return nil, false, err
	}

	return value, true, nil
}
