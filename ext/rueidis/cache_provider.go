package rueidis

import (
	"context"
	"errors"
	"time"

	"github.com/abema/crema"
	"github.com/redis/rueidis"
)

// RedisCacheProvider stores cache entries in Redis using rueidis.
type RedisCacheProvider struct {
	client rueidis.Client
}

var _ crema.CacheProvider[[]byte] = (*RedisCacheProvider)(nil)

// NewRedisCacheProvider builds a Redis-backed cache provider.
func NewRedisCacheProvider(client rueidis.Client) *RedisCacheProvider {
	return &RedisCacheProvider{client: client}
}

// Get retrieves a cached value from Redis.
func (p *RedisCacheProvider) Get(ctx context.Context, key string) ([]byte, bool, error) {
	result := p.client.Do(ctx, p.client.B().Get().Key(key).Build())
	msg, err := result.ToMessage()

	return parseRedisGetMessage(msg, err)
}

// Set stores a cache entry in Redis with the given TTL.
func (p *RedisCacheProvider) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	builder := p.client.B().Set().Key(key).Value(rueidis.BinaryString(value))
	if ttl > 0 {
		return p.client.Do(ctx, builder.Px(ttl).Build()).Error()
	}

	return p.client.Do(ctx, builder.Build()).Error()
}

// Delete removes a cached value from Redis.
func (p *RedisCacheProvider) Delete(ctx context.Context, key string) error {
	return p.client.Do(ctx, p.client.B().Del().Key(key).Build()).Error()
}

func parseRedisGetMessage(msg rueidis.RedisMessage, err error) ([]byte, bool, error) {
	if msg.IsNil() {
		return nil, false, nil
	}
	if err != nil {
		if errors.Is(err, rueidis.Nil) {
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
