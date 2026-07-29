//go:build linux && !386 && !arm && !mips && !mipsle

package madvfree

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

type (
	mmapFunc   func(fd int, offset int64, length, prot, flags int) ([]byte, error)
	munmapFunc func([]byte) error
)

// linuxBackend implements memoryBackend with the MADV_FREE lazy-free mechanism.
//
// markActive re-pins a region by dirtying one byte of every page, which cancels
// a pending MADV_FREE without a system call. markIdle issues MADV_FREE and
// discard issues MADV_DONTNEED.
type linuxBackend struct {
	ps                int
	madvise           madviseFunc
	enableHugePages   bool
	includeInCoreDump bool
}

var (
	_ memoryBackend     = (*linuxBackend)(nil)
	_ injectableBackend = (*linuxBackend)(nil)
)

func newBackend(config Config) (memoryBackend, error) {
	pageSize := unix.Getpagesize()
	if err := probeMADVFree(pageSize); err != nil {
		return nil, err
	}

	return &linuxBackend{
		ps:                pageSize,
		madvise:           unix.Madvise,
		enableHugePages:   config.EnableHugePages,
		includeInCoreDump: config.IncludeInCoreDump,
	}, nil
}

func (b *linuxBackend) pageSize() int { return b.ps }

func (b *linuxBackend) injectMadvise(fn madviseFunc) { b.madvise = fn }

func (b *linuxBackend) mapArena(size int) ([]byte, error) {
	arena, err := unix.Mmap(
		-1,
		0,
		size,
		unix.PROT_READ|unix.PROT_WRITE,
		unix.MAP_PRIVATE|unix.MAP_ANONYMOUS|unix.MAP_NORESERVE,
	)
	if err != nil {
		return nil, fmt.Errorf("madvfree: mmap: %w", err)
	}
	cleanup := func(cause error) ([]byte, error) {
		if unmapErr := unix.Munmap(arena); unmapErr != nil {
			return nil, errors.Join(cause, fmt.Errorf("madvfree: munmap after initialization failure: %w", unmapErr))
		}

		return nil, cause
	}
	if !b.enableHugePages {
		if err := b.madvise(arena, unix.MADV_NOHUGEPAGE); err != nil {
			return cleanup(fmt.Errorf("madvfree: MADV_NOHUGEPAGE: %w", err))
		}
	}
	if !b.includeInCoreDump {
		if err := b.madvise(arena, unix.MADV_DONTDUMP); err != nil {
			return cleanup(fmt.Errorf("madvfree: MADV_DONTDUMP: %w", err))
		}
	}

	return arena, nil
}

func (b *linuxBackend) markActive(region []byte) error {
	// A single-byte write per page cancels a pending MADV_FREE. If the page was
	// already reclaimed the write faults in a fresh zero page, which the caller's
	// generation check then detects.
	for offset := 0; offset < len(region); offset += b.ps {
		region[offset+touchOffset] ^= 1
	}

	return nil
}

func (b *linuxBackend) markIdle(region []byte) error {
	return b.madvise(region, unix.MADV_FREE)
}

func (b *linuxBackend) discard(region []byte) error {
	return b.madvise(region, unix.MADV_DONTNEED)
}

func (b *linuxBackend) unmap(region []byte) error {
	return unix.Munmap(region)
}

func probeMADVFree(pageSize int) error {
	return probeMADVFreeWith(pageSize, unix.Mmap, unix.Madvise, unix.Munmap)
}

func probeMADVFreeWith(
	pageSize int,
	mmap mmapFunc,
	madvise madviseFunc,
	munmap munmapFunc,
) (result error) {
	page, err := mmap(
		-1,
		0,
		pageSize,
		unix.PROT_READ|unix.PROT_WRITE,
		unix.MAP_PRIVATE|unix.MAP_ANONYMOUS,
	)
	if err != nil {
		return fmt.Errorf("madvfree: MADV_FREE probe mmap: %w", err)
	}
	defer func() {
		if err := munmap(page); err != nil {
			result = errors.Join(result, fmt.Errorf("madvfree: MADV_FREE probe munmap: %w", err))
		}
	}()

	page[0] = 1
	if err := madvise(page, unix.MADV_FREE); err != nil {
		return fmt.Errorf("%w: MADV_FREE probe: %w", ErrUnsupported, err)
	}
	page[0] ^= 1 // Write-touch cancels lazy-free before the probe page is discarded.
	if err := madvise(page, unix.MADV_DONTNEED); err != nil {
		return fmt.Errorf("madvfree: MADV_FREE probe cleanup MADV_DONTNEED: %w", err)
	}

	return nil
}
