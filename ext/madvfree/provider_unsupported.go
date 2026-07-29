//go:build !darwin && (!linux || 386 || arm || mips || mipsle)

package madvfree

import (
	"context"
	"time"
)

// Provider is unavailable on this platform. The cache requires 64-bit Linux or
// macOS.
type Provider struct{}

// NewProvider returns ErrUnsupported on unsupported platforms.
func NewProvider(Config) (*Provider, error) {
	return nil, ErrUnsupported
}

// Get returns ErrUnsupported.
func (*Provider) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, ErrUnsupported
}

// Set returns ErrUnsupported.
func (*Provider) Set(context.Context, string, []byte, time.Duration) error {
	return ErrUnsupported
}

// Delete returns ErrUnsupported.
func (*Provider) Delete(context.Context, string) error {
	return ErrUnsupported
}

// Purge returns ErrUnsupported.
func (*Provider) Purge() error {
	return ErrUnsupported
}

// Trim returns ErrUnsupported.
func (*Provider) Trim(int64) (int64, error) {
	return 0, ErrUnsupported
}

// Close returns ErrUnsupported.
func (*Provider) Close() error {
	return ErrUnsupported
}

// Stats returns an empty snapshot.
func (*Provider) Stats() Stats {
	return Stats{}
}
