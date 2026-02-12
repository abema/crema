package crema

import (
	"testing"
	"time"
)

func TestNoopCacheProvider_GetSetDelete(t *testing.T) {
	t.Parallel()

	provider := NewNoopCacheProvider[string]()

	if err := provider.Set(t.Context(), "key", "value", time.Second); err != nil {
		t.Fatalf("set: %v", err)
	}

	value, ok, err := provider.Get(t.Context(), "key")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if ok {
		t.Fatal("expected cache miss")
	}
	if value != "" {
		t.Fatalf("expected zero value, got %q", value)
	}

	if err := provider.Delete(t.Context(), "key"); err != nil {
		t.Fatalf("delete: %v", err)
	}
}
