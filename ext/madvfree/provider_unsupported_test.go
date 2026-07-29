//go:build !darwin && (!linux || (linux && (386 || arm || mips || mipsle)))

package madvfree

import (
	"errors"
	"testing"
)

func TestNewProviderUnsupported(t *testing.T) {
	t.Parallel()

	if DefaultCapacityBytes != 64<<30 {
		t.Fatalf("DefaultCapacityBytes = %d", uint64(DefaultCapacityBytes))
	}
	provider, err := NewProvider(Config{CapacityBytes: 4096})
	if provider != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("NewProvider() = (%v, %v), want (nil, ErrUnsupported)", provider, err)
	}
}
