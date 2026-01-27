package rueidis

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/rueidis"
)

func TestRedisCacheProvider_GetSetDelete(t *testing.T) {
	t.Parallel()

	_, _, provider := newTestRedisProvider(t)
	ctx := context.Background()

	if err := provider.Set(ctx, "key", []byte("value"), 0); err != nil {
		t.Fatalf("set: %v", err)
	}

	value, ok, err := provider.Get(ctx, "key")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ok {
		t.Fatal("expected value to exist")
	}
	if string(value) != "value" {
		t.Fatalf("unexpected value: %q", value)
	}

	if err := provider.Delete(ctx, "key"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	_, ok, err = provider.Get(ctx, "key")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if ok {
		t.Fatal("expected value to be deleted")
	}
}

func TestRedisCacheProvider_TTL(t *testing.T) {
	t.Parallel()

	server, _, provider := newTestRedisProvider(t)
	ctx := context.Background()

	if err := provider.Set(ctx, "key", []byte("value"), 50*time.Millisecond); err != nil {
		t.Fatalf("set: %v", err)
	}

	if _, err := server.Get("key"); err != nil {
		t.Fatal("expected key to exist in redis")
	}

	server.FastForward(60 * time.Millisecond)

	_, ok, err := provider.Get(ctx, "key")
	if err != nil {
		t.Fatalf("get after ttl: %v", err)
	}
	if ok {
		t.Fatal("expected value to expire")
	}
}

func TestRedisCacheProvider_GetWrongType(t *testing.T) {
	t.Parallel()

	_, client, provider := newTestRedisProvider(t)
	ctx := context.Background()
	if err := client.Do(ctx, client.B().Hset().Key("key").FieldValue().FieldValue("field", "value").Build()).Error(); err != nil {
		t.Fatalf("hset: %v", err)
	}

	_, ok, err := provider.Get(ctx, "key")
	if err == nil {
		t.Fatal("expected error")
	}
	if ok {
		t.Fatal("expected ok to be false")
	}
}

func newTestRedisProvider(t *testing.T) (*miniredis.Miniredis, rueidis.Client, *RedisCacheProvider) {
	t.Helper()

	server := miniredis.RunT(t)
	client, err := rueidis.NewClient(rueidis.ClientOption{
		InitAddress:  []string{server.Addr()},
		DisableCache: true,
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	return server, client, NewRedisCacheProvider(client)
}
