//go:build !linux || (linux && (386 || arm || mips || mipsle))

package madvfree

import (
	"context"
	"time"
)

// Provider is unavailable on this platform.
type Provider struct{}

// NewProvider returns ErrUnsupported on non-Linux and 32-bit platforms.
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
