//go:build linux && !386 && !arm && !mips && !mipsle

package madvfree

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

type (
	mmapFunc    func(fd int, offset int64, length, prot, flags int) ([]byte, error)
	madviseFunc func([]byte, int) error
	munmapFunc  func([]byte) error
)

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

func (p *Provider) advise(region []byte, advice int) {
	if err := p.madvise(region, advice); err != nil {
		switch advice {
		case unix.MADV_FREE:
			p.stats.madvFreeErrors.Add(1)
		case unix.MADV_DONTNEED:
			p.stats.madvDontNeedErrors.Add(1)
		}

		return
	}

	switch advice {
	case unix.MADV_FREE:
		p.stats.madvFreeCalls.Add(1)
	case unix.MADV_DONTNEED:
		p.stats.madvDontNeedCalls.Add(1)
	}
}
