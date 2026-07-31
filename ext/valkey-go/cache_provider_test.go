package valkeygo

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/valkey-io/valkey-go"
)

func TestValkeyCacheProvider_GetSetDelete(t *testing.T) {
	t.Parallel()

	_, _, provider := newTestValkeyProvider(t)
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

func TestValkeyCacheProvider_TTL(t *testing.T) {
	t.Parallel()

	server, _, provider := newTestValkeyProvider(t)
	ctx := context.Background()

	if err := provider.Set(ctx, "key", []byte("value"), 50*time.Millisecond); err != nil {
		t.Fatalf("set: %v", err)
	}

	if _, err := server.Get("key"); err != nil {
		t.Fatal("expected key to exist in valkey")
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

func TestValkeyCacheProvider_SubMillisecondTTL(t *testing.T) {
	t.Parallel()

	server, _, provider := newTestValkeyProvider(t)
	if err := provider.Set(context.Background(), "key", []byte("value"), time.Nanosecond); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, err := server.Get("key"); err != nil {
		t.Fatal("expected key to exist before the rounded TTL expires")
	}

	server.FastForward(time.Millisecond)
	if _, found, err := provider.Get(context.Background(), "key"); err != nil || found {
		t.Fatalf("Get() after ttl = (_, %v, %v), want miss", found, err)
	}
}

func TestValkeyCacheProvider_GetWrongType(t *testing.T) {
	t.Parallel()

	_, client, provider := newTestValkeyProvider(t)
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

func newTestValkeyProvider(t *testing.T) (*miniredis.Miniredis, valkey.Client, *ValkeyCacheProvider) {
	t.Helper()

	server := miniredis.RunT(t)
	client, err := valkey.NewClient(valkey.ClientOption{
		InitAddress:  []string{server.Addr()},
		DisableCache: true,
	})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	return server, client, NewValkeyCacheProvider(client)
}
