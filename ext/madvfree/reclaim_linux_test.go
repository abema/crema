//go:build linux && !386 && !arm && !mips && !mipsle

package madvfree

import "golang.org/x/sys/unix"

// simulateReclaim forces the kernel to drop region so a later read observes the
// zero-fill that real MADV_FREE reclamation produces. On Linux MADV_DONTNEED
// zeroes anonymous pages immediately.
func simulateReclaim(region []byte) error {
	return unix.Madvise(region, unix.MADV_DONTNEED)
}
