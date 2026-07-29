//go:build darwin

package madvfree

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestProbeReusableSequence(t *testing.T) {
	t.Parallel()

	page := make([]byte, unix.Getpagesize())
	var advice []int
	unmapped := false
	err := probeReusableWith(
		len(page),
		func(fd int, offset int64, length, prot, flags int) ([]byte, error) {
			if fd != -1 || offset != 0 || length != len(page) {
				t.Fatalf("mmap arguments = (%d, %d, %d)", fd, offset, length)
			}
			if prot != unix.PROT_READ|unix.PROT_WRITE {
				t.Fatalf("mmap prot = %d", prot)
			}
			if flags != unix.MAP_PRIVATE|unix.MAP_ANON {
				t.Fatalf("mmap flags = %d", flags)
			}

			return page, nil
		},
		func(region []byte, current int) error {
			if !bytes.Equal(region, page) {
				t.Fatal("madvise received a different mapping")
			}
			advice = append(advice, current)

			return nil
		},
		func([]byte) error {
			unmapped = true

			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{unix.MADV_FREE_REUSABLE, unix.MADV_FREE_REUSE, unix.MADV_FREE_REUSABLE}
	if len(advice) != len(want) {
		t.Fatalf("madvise sequence = %v, want %v", advice, want)
	}
	for i := range want {
		if advice[i] != want[i] {
			t.Fatalf("madvise sequence = %v, want %v", advice, want)
		}
	}
	if !unmapped {
		t.Fatal("probe mapping was not unmapped")
	}
}

func TestProbeReusableUnsupported(t *testing.T) {
	t.Parallel()

	unmapped := false
	err := probeReusableWith(
		unix.Getpagesize(),
		func(_ int, _ int64, length, _, _ int) ([]byte, error) {
			return make([]byte, length), nil
		},
		func(_ []byte, advice int) error {
			if advice == unix.MADV_FREE_REUSABLE {
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

func TestProbeReusableDetectsLostContents(t *testing.T) {
	t.Parallel()

	err := probeReusableWith(
		unix.Getpagesize(),
		func(_ int, _ int64, length, _, _ int) ([]byte, error) {
			return make([]byte, length), nil
		},
		func(region []byte, advice int) error {
			// Simulate a kernel that drops idle pages during the no-pressure
			// round trip: zero the marker on REUSE.
			if advice == unix.MADV_FREE_REUSE {
				binary.LittleEndian.PutUint64(region[:8], 0)
			}

			return nil
		},
		func([]byte) error { return nil },
	)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("probe error = %v, want ErrUnsupported", err)
	}
}

func TestRuntimeAdviceFailureHandlingDarwin(t *testing.T) {
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
	reuseCalls := 0
	provider.backend.(injectableBackend).injectMadvise(func(_ []byte, advice int) error {
		if advice == unix.MADV_FREE_REUSE {
			reuseCalls++
			if reuseCalls == 1 {
				return nil
			}
		}

		return unix.EIO
	})

	value := bytes.Repeat([]byte{0xa5}, unix.Getpagesize())
	if err := provider.Set(context.Background(), "key", value, time.Minute); err != nil {
		t.Fatalf("Set(): %v", err)
	}
	got, ok, err := provider.Get(context.Background(), "key")
	if !errors.Is(err, unix.EIO) || ok || got != nil {
		t.Fatalf("Get() = (len=%d, %v, %v), want reactivation error", len(got), ok, err)
	}
	if err := provider.Delete(context.Background(), "key"); err != nil {
		t.Fatalf("Delete(): %v", err)
	}

	stats := provider.Stats()
	if stats.IdleErrors == 0 || stats.ReactivateErrors == 0 || stats.DiscardErrors == 0 {
		t.Fatalf("advice error counters = %+v, want all positive", stats)
	}
	if stats.Entries != 0 || stats.ReservedBytes != 0 {
		t.Fatalf("accounting after failed advice = %+v", stats)
	}
}

func TestReactivateFailureRollsBackAllocationDarwin(t *testing.T) {
	tests := []struct {
		name        string
		sizeClasses []int
		value       []byte
	}{
		{
			name:        "extent",
			sizeClasses: []int{},
			value:       bytes.Repeat([]byte{0xa5}, unix.Getpagesize()),
		},
		{
			name:  "small",
			value: []byte("value"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider, err := NewProvider(Config{
				CapacityBytes: unix.Getpagesize() * 8,
				SizeClasses:   test.sizeClasses,
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

			err = provider.Set(context.Background(), "key", test.value, 0)
			if !errors.Is(err, unix.EIO) {
				t.Fatalf("Set() error = %v, want EIO", err)
			}
			stats := provider.Stats()
			if stats.Entries != 0 ||
				stats.LogicalBytes != 0 ||
				stats.ReservedBytes != 0 ||
				stats.FreeBytes != int64(provider.capacity) ||
				stats.AllocationFails != 1 ||
				stats.ReactivateErrors != 1 {
				t.Fatalf("Stats() after failed reactivation = %+v", stats)
			}
			assertNoSmallPageMetadata(t, provider)
		})
	}
}
