//go:build linux && !386 && !arm && !mips && !mipsle

package madvfree

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type reclaimOnActivateBackend struct {
	memoryBackend

	touches int
}

func (b *reclaimOnActivateBackend) markActive(page []byte) error {
	b.touches++
	clear(page)
	page[touchOffset] ^= 1

	return nil
}

func (b *reclaimOnActivateBackend) canReadIdle() bool { return true }

func TestProbeMADVFreeSequence(t *testing.T) {
	t.Parallel()

	page := make([]byte, unix.Getpagesize())
	var advice []int
	unmapped := false
	err := probeMADVFreeWith(
		len(page),
		func(fd int, offset int64, length, prot, flags int) ([]byte, error) {
			if fd != -1 || offset != 0 || length != len(page) {
				t.Fatalf("mmap arguments = (%d, %d, %d)", fd, offset, length)
			}
			if prot != unix.PROT_READ|unix.PROT_WRITE {
				t.Fatalf("mmap prot = %d", prot)
			}
			if flags != unix.MAP_PRIVATE|unix.MAP_ANONYMOUS {
				t.Fatalf("mmap flags = %d", flags)
			}

			return page, nil
		},
		func(region []byte, current int) error {
			if !bytes.Equal(region, page) {
				t.Fatal("madvise received a different mapping")
			}
			if current == unix.MADV_FREE && region[0] != 1 {
				t.Fatalf("MADV_FREE page byte = %d, want 1", region[0])
			}
			if current == unix.MADV_DONTNEED && region[0] != 0 {
				t.Fatalf("write-touch did not cancel lazy-free: byte = %d", region[0])
			}
			advice = append(advice, current)

			return nil
		},
		func(region []byte) error {
			unmapped = true

			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(advice) != 2 {
		t.Fatalf("madvise sequence = %v, want two calls", advice)
	}
	if !bytes.Equal(
		[]byte{byte(advice[0]), byte(advice[1])},
		[]byte{byte(unix.MADV_FREE), byte(unix.MADV_DONTNEED)},
	) {
		t.Fatalf("madvise sequence = %v", advice)
	}
	if !unmapped {
		t.Fatal("probe mapping was not unmapped")
	}
}

func TestProbeMADVFreeUnsupported(t *testing.T) {
	t.Parallel()

	unmapped := false
	err := probeMADVFreeWith(
		unix.Getpagesize(),
		func(_ int, _ int64, length, _, _ int) ([]byte, error) {
			return make([]byte, length), nil
		},
		func(_ []byte, advice int) error {
			if advice == unix.MADV_FREE {
				return unix.EINVAL
			}

			return nil
		},
		func([]byte) error {
			unmapped = true

			return nil
		},
	)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("probe error = %v, want ErrUnsupported", err)
	}
	if !unmapped {
		t.Fatal("failed probe mapping was not unmapped")
	}
}

func TestRuntimeMadviseFailuresAreSoftForExtent(t *testing.T) {
	provider, err := NewProvider(Config{
		CapacityBytes: unix.Getpagesize() * 8,
		SizeClasses:   []int{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = provider.Close()
	})
	provider.backend.(injectableBackend).injectMadvise(func([]byte, int) error {
		return unix.EIO
	})

	value := bytes.Repeat([]byte{0xa5}, unix.Getpagesize())
	if err := provider.Set(context.Background(), "key", value, 0); err != nil {
		t.Fatalf("Set(): %v", err)
	}
	got, ok, err := provider.Get(context.Background(), "key")
	if err != nil || !ok || !bytes.Equal(got, value) {
		t.Fatalf("Get() = (len=%d, %v, %v)", len(got), ok, err)
	}
	if err := provider.Delete(context.Background(), "key"); err != nil {
		t.Fatalf("Delete(): %v", err)
	}

	stats := provider.Stats()
	if stats.IdleErrors != 2 || stats.DiscardErrors != 1 {
		t.Fatalf("madvise error counters = %+v", stats)
	}
	if stats.Entries != 0 || stats.ReservedBytes != 0 {
		t.Fatalf("accounting after failed madvise = %+v", stats)
	}
}

func TestExtentPrecheckDoesNotTouchReclaimedOrLaterPages(t *testing.T) {
	pageSize := unix.Getpagesize()
	provider, err := NewProvider(Config{
		CapacityBytes: pageSize * 8,
		SizeClasses:   []int{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = provider.Close()
	})

	value := bytes.Repeat([]byte{0xa5}, pageSize*2)
	if err := provider.Set(context.Background(), "key", value, 0); err != nil {
		t.Fatal(err)
	}
	item := provider.lookup("key")
	if item == nil || item.pageCount < 3 {
		t.Fatalf("extent = %#v, want at least three pages", item)
	}
	reclaimedPage := provider.page(item.startPage + 1)
	laterPage := provider.page(item.startPage + 2)
	if err := simulateReclaim(reclaimedPage); err != nil {
		t.Fatal(err)
	}

	reclaimed, acquired, err := provider.acquireExtent(item)
	if err != nil || acquired || !reclaimed {
		t.Fatalf("acquireExtent() = (%v, %v, %v), want (true, false, nil)", reclaimed, acquired, err)
	}
	if got := reclaimedPage[touchOffset]; got != 0 {
		t.Fatalf("reclaimed page touch byte = %d, want 0", got)
	}
	if got := laterPage[touchOffset]; got != 0 {
		t.Fatalf("page after reclaimed page touch byte = %d, want 0", got)
	}
	if got := provider.page(item.startPage)[touchOffset]; got != 1 {
		t.Fatalf("valid page before reclaimed page touch byte = %d, want 1", got)
	}

	provider.finalizeExtent(item)
}

func TestExtentPrecheckRevalidatesAfterTouch(t *testing.T) {
	provider, err := NewProvider(Config{
		CapacityBytes: unix.Getpagesize() * 2,
		SizeClasses:   []int{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = provider.Close()
	})
	if err := provider.Set(context.Background(), "key", []byte("value"), 0); err != nil {
		t.Fatal(err)
	}
	item := provider.lookup("key")
	backend := &reclaimOnActivateBackend{memoryBackend: provider.backend}
	provider.backend = backend

	reclaimed, acquired, err := provider.acquireExtent(item)
	if err != nil || acquired || !reclaimed {
		t.Fatalf("acquireExtent() = (%v, %v, %v), want (true, false, nil)", reclaimed, acquired, err)
	}
	if backend.touches != 1 {
		t.Fatalf("touch calls = %d, want 1", backend.touches)
	}

	provider.finalizeExtent(item)
}

func TestSmallSlabPrecheckDoesNotTouchReclaimedPage(t *testing.T) {
	provider := newTestProvider(t, 64)
	size, layout := multiPageValueSize(t, provider)
	value := bytes.Repeat([]byte{0xa5}, size)
	if err := provider.Set(context.Background(), "key", value, 0); err != nil {
		t.Fatal(err)
	}
	item := provider.lookup("key")
	if item == nil || item.kind != allocationSmall || layout.pageCount < 2 {
		t.Fatalf("small item = %#v, layout = %#v", item, layout)
	}
	const reclaimedOffset = uint32(1)
	reclaimedPage := provider.page(item.startPage + reclaimedOffset)
	if err := simulateReclaim(reclaimedPage); err != nil {
		t.Fatal(err)
	}

	validPages, err := provider.touchAndValidateSmallSlabPages(
		item.startPage,
		item.smallMeta,
		layout,
	)
	if err != nil {
		t.Fatal(err)
	}
	if validPages&(uint32(1)<<reclaimedOffset) != 0 {
		t.Fatalf("valid page mask %#x includes reclaimed page %d", validPages, reclaimedOffset)
	}
	if got := reclaimedPage[touchOffset]; got != 0 {
		t.Fatalf("reclaimed page touch byte = %d, want 0", got)
	}
	if got := provider.page(item.startPage)[touchOffset]; got != 1 {
		t.Fatalf("valid slab page touch byte = %d, want 1", got)
	}
}

func TestRuntimeMadviseFailuresAreSoftForSmallAndTTL(t *testing.T) {
	provider := newTestProvider(t, 4)
	provider.backend.(injectableBackend).injectMadvise(func([]byte, int) error {
		return unix.EIO
	})

	if err := provider.Set(context.Background(), "key", []byte("value"), time.Nanosecond); err != nil {
		t.Fatalf("Set(): %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for provider.Stats().Entries != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	stats := provider.Stats()
	if stats.Entries != 0 || stats.ReservedBytes != 0 {
		t.Fatalf("TTL cleanup after failed madvise = %+v", stats)
	}
	if stats.IdleErrors == 0 || stats.DiscardErrors == 0 {
		t.Fatalf("madvise error counters = %+v", stats)
	}
}

func TestCloseIgnoresMadviseFailure(t *testing.T) {
	provider, err := NewProvider(Config{CapacityBytes: unix.Getpagesize()})
	if err != nil {
		t.Fatal(err)
	}
	provider.backend.(injectableBackend).injectMadvise(func([]byte, int) error {
		return unix.EIO
	})

	if err := provider.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	if got := provider.Stats().DiscardErrors; got != 1 {
		t.Fatalf("DiscardErrors = %d, want 1", got)
	}
}
