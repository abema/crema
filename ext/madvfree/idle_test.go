//go:build (linux && !386 && !arm && !mips && !mipsle) || darwin

package madvfree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// idleTestCase covers both allocators, whose idle state lives on different
// objects: cacheEntry for extents and smallPageMeta for slabs.
type idleTestCase struct {
	name        string
	sizeClasses []int
	value       []byte
}

func idleTestCases() []idleTestCase {
	return []idleTestCase{
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
}

// forceIdleSweep runs the sweeper from the calling goroutine. Callers configure a
// delay long enough that the provider's own sweeper cannot run concurrently.
func forceIdleSweep(t *testing.T, provider *Provider) {
	t.Helper()
	provider.idleMu.Lock()
	for index := provider.idleHead; index < len(provider.idleQueue); index++ {
		provider.idleQueue[index].deadline = 0
	}
	provider.idleMu.Unlock()
	provider.sweepIdle()
}

func queuedIdleDeferrals(provider *Provider) int {
	provider.idleMu.Lock()
	defer provider.idleMu.Unlock()

	return len(provider.idleQueue) - provider.idleHead
}

func TestIdleHysteresisKeepsRepeatedGetsPinned(t *testing.T) {
	for _, test := range idleTestCases() {
		t.Run(test.name, func(t *testing.T) {
			provider := newConfiguredTestProvider(t, Config{
				CapacityBytes: unix.Getpagesize() * 8,
				SizeClasses:   test.sizeClasses,
				IdleDelay:     time.Minute,
			})
			if err := provider.Set(context.Background(), "key", test.value, 0); err != nil {
				t.Fatalf("Set(): %v", err)
			}
			for range 16 {
				got, ok, err := provider.Get(context.Background(), "key")
				if err != nil || !ok || !bytes.Equal(got, test.value) {
					t.Fatalf("Get() = (len=%d, %v, %v)", len(got), ok, err)
				}
			}

			// One deferral covers the whole burst: Set queues it and every release
			// only postpones it.
			stats := provider.Stats()
			if stats.IdleCalls != 0 ||
				stats.ReactivateCalls != 1 ||
				stats.IdleDeferrals != 1 ||
				stats.IdleCancellations != 0 {
				t.Fatalf("Stats() during a Get burst = %+v", stats)
			}
			if got := queuedIdleDeferrals(provider); got != 1 {
				t.Fatalf("queued deferrals = %d, want 1", got)
			}
		})
	}
}

func TestIdleHysteresisReschedulesAccessedRegion(t *testing.T) {
	for _, test := range idleTestCases() {
		t.Run(test.name, func(t *testing.T) {
			provider := newConfiguredTestProvider(t, Config{
				CapacityBytes: unix.Getpagesize() * 8,
				SizeClasses:   test.sizeClasses,
				IdleDelay:     time.Minute,
			})
			if err := provider.Set(context.Background(), "key", test.value, 0); err != nil {
				t.Fatalf("Set(): %v", err)
			}
			if _, ok, err := provider.Get(context.Background(), "key"); err != nil || !ok {
				t.Fatalf("Get() = (_, %v, %v)", ok, err)
			}

			// The Get moved the region's use sequence, so the queued deferral is
			// rescheduled instead of issuing the advice.
			forceIdleSweep(t, provider)
			if stats := provider.Stats(); stats.IdleCalls != 0 || stats.IdleDeferrals != 1 {
				t.Fatalf("Stats() after a rescheduled sweep = %+v", stats)
			}
			if got := queuedIdleDeferrals(provider); got != 1 {
				t.Fatalf("queued deferrals after rescheduling = %d, want 1", got)
			}

			// Nothing touched the region since the reschedule, so the second sweep
			// hands it to the kernel.
			forceIdleSweep(t, provider)
			stats := provider.Stats()
			if stats.IdleCalls != 1 || stats.IdleCancellations != 0 {
				t.Fatalf("Stats() after an expired sweep = %+v", stats)
			}
			if got := queuedIdleDeferrals(provider); got != 0 {
				t.Fatalf("queued deferrals after the advice = %d, want 0", got)
			}

			// The reclaimable region is reactivated on the next read and still
			// returns its bytes.
			got, ok, err := provider.Get(context.Background(), "key")
			if err != nil || !ok || !bytes.Equal(got, test.value) {
				t.Fatalf("Get() after idle marking = (len=%d, %v, %v)", len(got), ok, err)
			}
			if stats := provider.Stats(); stats.ReactivateCalls != 2 {
				t.Fatalf("ReactivateCalls after idle marking = %d, want 2", stats.ReactivateCalls)
			}
		})
	}
}

func TestIdleHysteresisCancelsDeferralForDeletedEntry(t *testing.T) {
	for _, test := range idleTestCases() {
		t.Run(test.name, func(t *testing.T) {
			provider := newConfiguredTestProvider(t, Config{
				CapacityBytes: unix.Getpagesize() * 8,
				SizeClasses:   test.sizeClasses,
				IdleDelay:     time.Minute,
			})
			if err := provider.Set(context.Background(), "key", test.value, 0); err != nil {
				t.Fatalf("Set(): %v", err)
			}
			if err := provider.Delete(context.Background(), "key"); err != nil {
				t.Fatalf("Delete(): %v", err)
			}

			forceIdleSweep(t, provider)
			stats := provider.Stats()
			if stats.IdleCalls != 0 || stats.IdleCancellations != 1 || stats.DiscardCalls == 0 {
				t.Fatalf("Stats() after sweeping a discarded region = %+v", stats)
			}
			if stats.Entries != 0 || stats.ReservedBytes != 0 {
				t.Fatalf("accounting after Delete = %+v", stats)
			}
		})
	}
}

func TestDisableIdleDelayMarksIdleOnRelease(t *testing.T) {
	for _, test := range idleTestCases() {
		t.Run(test.name, func(t *testing.T) {
			provider := newConfiguredTestProvider(t, Config{
				CapacityBytes:    unix.Getpagesize() * 8,
				SizeClasses:      test.sizeClasses,
				DisableIdleDelay: true,
			})
			if err := provider.Set(context.Background(), "key", test.value, 0); err != nil {
				t.Fatalf("Set(): %v", err)
			}
			got, ok, err := provider.Get(context.Background(), "key")
			if err != nil || !ok || !bytes.Equal(got, test.value) {
				t.Fatalf("Get() = (len=%d, %v, %v)", len(got), ok, err)
			}

			// Set and the Get's release each mark the region reclaimable, and the
			// Get has to reactivate it in between.
			stats := provider.Stats()
			if stats.IdleCalls != 2 ||
				stats.ReactivateCalls != 2 ||
				stats.IdleDeferrals != 0 ||
				stats.IdleCancellations != 0 {
				t.Fatalf("Stats() without the hysteresis = %+v", stats)
			}
			if got := queuedIdleDeferrals(provider); got != 0 {
				t.Fatalf("queued deferrals without the hysteresis = %d, want 0", got)
			}
		})
	}
}

func TestNewProviderRejectsNegativeIdleDelay(t *testing.T) {
	t.Parallel()

	provider, err := NewProvider(Config{IdleDelay: -time.Second})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewProvider() error = %v, want ErrInvalidConfig", err)
	}
	if provider != nil {
		t.Fatal("NewProvider() returned a provider for an invalid config")
	}
}

func TestIdleQueueCompactsConsumedDeferrals(t *testing.T) {
	const (
		swept   = idleQueueCompactLimit + 8
		pending = 8
	)
	provider := newConfiguredTestProvider(t, Config{
		CapacityBytes: unix.Getpagesize() * (swept + pending + 1),
		SizeClasses:   []int{},
		IdleDelay:     time.Minute,
	})

	// Every key owns a single-page extent, so each Set queues its own deferral.
	for index := range swept + pending {
		if err := provider.Set(context.Background(), fmt.Sprint(index), []byte("value"), 0); err != nil {
			t.Fatalf("Set(): %v", err)
		}
	}
	if got := queuedIdleDeferrals(provider); got != swept+pending {
		t.Fatalf("queued deferrals = %d, want %d", got, swept+pending)
	}

	// Sweeping only part of the queue leaves a consumed prefix behind.
	provider.idleMu.Lock()
	for index := range swept {
		provider.idleQueue[index].deadline = 0
	}
	provider.idleMu.Unlock()
	provider.sweepIdle()
	provider.idleMu.Lock()
	head, length := provider.idleHead, len(provider.idleQueue)
	provider.idleMu.Unlock()
	if head != swept || length != swept+pending {
		t.Fatalf("queue head = %d, length = %d, want %d and %d", head, length, swept, swept+pending)
	}

	// The next deferral compacts that prefix away instead of growing the queue.
	if err := provider.Set(context.Background(), "extra", []byte("value"), 0); err != nil {
		t.Fatalf("Set(): %v", err)
	}
	provider.idleMu.Lock()
	head, length = provider.idleHead, len(provider.idleQueue)
	provider.idleMu.Unlock()
	if head != 0 || length != pending+1 {
		t.Fatalf("queue head = %d, length = %d after compaction, want 0 and %d", head, length, pending+1)
	}
}
