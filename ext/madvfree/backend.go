//go:build (linux && !386 && !arm && !mips && !mipsle) || darwin

package madvfree

import "fmt"

// madviseFunc is the signature of unix.Madvise. It is stored on each platform
// backend so tests can inject failures without issuing real system calls.
type madviseFunc func([]byte, int) error

// memoryBackend isolates the OS-specific virtual-memory operations from the
// platform-independent cache core.
//
// A region is a page-aligned sub-slice of the arena. markActive and markIdle
// move a region between the "resident, referenced" and "reclaimable, idle"
// states; discard forgets a region's contents entirely. The concrete meaning of
// each transition differs per platform (see backend_linux.go and
// backend_darwin.go), but the core relies only on the following contract:
//
//   - After markActive succeeds, reads of a region that the kernel has NOT
//     reclaimed observe the last bytes written to it; reads of a reclaimed
//     region observe zeroes. markActive must succeed before reading or writing
//     a region that may currently be idle.
//   - markIdle makes a region reclaimable under memory pressure without
//     immediately discarding it.
//   - discard releases a region's physical pages; its logical contents are
//     undefined afterwards and callers must reinitialize before reuse.
type memoryBackend interface {
	// pageSize returns the host page size in bytes.
	pageSize() int
	// mapArena reserves size bytes of anonymous memory and applies the
	// configured arena-wide advice. size is a multiple of pageSize.
	mapArena(size int) ([]byte, error)
	// markActive re-pins region so subsequent access is safe, restoring
	// accounting on platforms that require it. It runs before validation reads.
	markActive(region []byte) error
	// canReadIdle reports whether idle pages may be read before markActive.
	canReadIdle() bool
	// markIdle marks region reclaimable once it has no active readers.
	markIdle(region []byte) error
	// discard forgets region's contents and releases its pages where supported.
	discard(region []byte) error
	// unmap removes a mapping returned by mapArena.
	unmap(region []byte) error
}

// injectableBackend is implemented by the real backends so in-package tests can
// substitute the madvise system call. It is never used in production paths.
type injectableBackend interface {
	injectMadvise(fn madviseFunc)
}

// markActive re-pins region and records the operation for Stats.
func (p *Provider) markActive(region []byte) error {
	if err := p.reactivate(region); err != nil {
		return err
	}
	p.stats.reactivateCalls.Add(1)

	return nil
}

// reactivate performs the platform transition without incrementing ReactivateCalls.
func (p *Provider) reactivate(region []byte) error {
	if err := p.backend.markActive(region); err != nil {
		p.stats.reactivateErrors.Add(1)

		return fmt.Errorf("madvfree: reactivate region: %w", err)
	}

	return nil
}

// markIdle marks region reclaimable and records the operation for Stats.
func (p *Provider) markIdle(region []byte) {
	if err := p.backend.markIdle(region); err != nil {
		p.stats.idleErrors.Add(1)

		return
	}
	p.stats.idleCalls.Add(1)
}

// discard forgets region and records the operation for Stats.
func (p *Provider) discard(region []byte) {
	if err := p.backend.discard(region); err != nil {
		p.stats.discardErrors.Add(1)

		return
	}
	p.stats.discardCalls.Add(1)
}
