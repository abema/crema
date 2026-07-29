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
	provider.madvise = func([]byte, int) error {
		return unix.EIO
	}

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
	if stats.MadvFreeErrors != 2 || stats.MadvDontNeedErrors != 1 {
		t.Fatalf("madvise error counters = %+v", stats)
	}
	if stats.Entries != 0 || stats.ReservedBytes != 0 {
		t.Fatalf("accounting after failed madvise = %+v", stats)
	}
}

func TestRuntimeMadviseFailuresAreSoftForSmallAndTTL(t *testing.T) {
	provider := newTestProvider(t, 4)
	provider.madvise = func([]byte, int) error {
		return unix.EIO
	}

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
	if stats.MadvFreeErrors == 0 || stats.MadvDontNeedErrors == 0 {
		t.Fatalf("madvise error counters = %+v", stats)
	}
}

func TestCloseIgnoresMadviseFailure(t *testing.T) {
	provider, err := NewProvider(Config{CapacityBytes: unix.Getpagesize()})
	if err != nil {
		t.Fatal(err)
	}
	provider.madvise = func([]byte, int) error {
		return unix.EIO
	}

	if err := provider.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	if got := provider.Stats().MadvDontNeedErrors; got != 1 {
		t.Fatalf("MadvDontNeedErrors = %d, want 1", got)
	}
}
