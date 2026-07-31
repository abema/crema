//go:build darwin

package madvfree

import (
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

type (
	mmapFunc   func(fd int, offset int64, length, prot, flags int) ([]byte, error)
	munmapFunc func([]byte) error
)

// darwinBackend implements memoryBackend with the XNU MADV_FREE_REUSABLE /
// MADV_FREE_REUSE pair.
//
// markIdle marks a region reusable by the system (MADV_FREE_REUSABLE); the
// kernel may then reclaim its pages under memory pressure. markActive reclaims
// ownership with MADV_FREE_REUSE before the region is read or written: pages the
// kernel did not reclaim keep their contents, while reclaimed pages return
// zero-filled, which the caller's generation check detects as a miss.
//
// Darwin's MADV_DONTNEED neither zeroes nor releases anonymous pages, so discard
// uses MADV_FREE_REUSABLE as well; a subsequent allocation re-pins the pages
// with markActive before writing.
type darwinBackend struct {
	ps      int
	madvise madviseFunc
}

var (
	_ memoryBackend     = (*darwinBackend)(nil)
	_ injectableBackend = (*darwinBackend)(nil)
)

func newBackend(_ Config) (memoryBackend, error) {
	pageSize := unix.Getpagesize()
	if err := probeReusable(pageSize); err != nil {
		return nil, err
	}

	return &darwinBackend{
		ps:      pageSize,
		madvise: unix.Madvise,
	}, nil
}

func (b *darwinBackend) pageSize() int { return b.ps }

func (b *darwinBackend) injectMadvise(fn madviseFunc) { b.madvise = fn }

func (b *darwinBackend) canReadIdle() bool { return false }

func (b *darwinBackend) mapArena(size int) ([]byte, error) {
	// Darwin has no MAP_NORESERVE; anonymous mappings are already lazily backed.
	// It also has no per-region transparent-huge-page or core-dump advice, so
	// Config.EnableHugePages and Config.IncludeInCoreDump have no effect here.
	arena, err := unix.Mmap(
		-1,
		0,
		size,
		unix.PROT_READ|unix.PROT_WRITE,
		unix.MAP_PRIVATE|unix.MAP_ANON,
	)
	if err != nil {
		return nil, fmt.Errorf("madvfree: mmap: %w", err)
	}

	return arena, nil
}

func (b *darwinBackend) markActive(region []byte) error {
	return b.madvise(region, unix.MADV_FREE_REUSE)
}

func (b *darwinBackend) markIdle(region []byte) error {
	return b.madvise(region, unix.MADV_FREE_REUSABLE)
}

func (b *darwinBackend) discard(region []byte) error {
	return b.madvise(region, unix.MADV_FREE_REUSABLE)
}

func (b *darwinBackend) unmap(region []byte) error {
	return unix.Munmap(region)
}

func probeReusable(pageSize int) error {
	return probeReusableWith(pageSize, unix.Mmap, unix.Madvise, unix.Munmap)
}

func probeReusableWith(
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
		unix.MAP_PRIVATE|unix.MAP_ANON,
	)
	if err != nil {
		return fmt.Errorf("madvfree: MADV_FREE_REUSABLE probe mmap: %w", err)
	}
	defer func() {
		if err := munmap(page); err != nil {
			result = errors.Join(result, fmt.Errorf("madvfree: MADV_FREE_REUSABLE probe munmap: %w", err))
		}
	}()

	const marker = 0x6d6164766672 // "madvfr"
	binary.LittleEndian.PutUint64(page[:8], marker)
	if err := madvise(page, unix.MADV_FREE_REUSABLE); err != nil {
		return fmt.Errorf("%w: MADV_FREE_REUSABLE probe: %w", ErrUnsupported, err)
	}
	if err := madvise(page, unix.MADV_FREE_REUSE); err != nil {
		return fmt.Errorf("%w: MADV_FREE_REUSE probe: %w", ErrUnsupported, err)
	}
	// Without memory pressure the pages are not reclaimed, so the marker must
	// survive the reusable/reuse round trip. A kernel that fails this cannot
	// support the generation-based reclaim detection.
	if binary.LittleEndian.Uint64(page[:8]) != marker {
		return fmt.Errorf("%w: MADV_FREE_REUSE did not preserve idle pages", ErrUnsupported)
	}
	if err := madvise(page, unix.MADV_FREE_REUSABLE); err != nil {
		return fmt.Errorf("madvfree: MADV_FREE_REUSABLE probe cleanup: %w", err)
	}

	return nil
}
