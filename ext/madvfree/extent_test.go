//go:build (linux && !386 && !arm && !mips && !mipsle) || darwin

package madvfree

import (
	"bytes"
	"container/heap"
	"context"
	"fmt"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func newExtentTestProvider(t *testing.T, pages int) *Provider {
	t.Helper()
	provider, err := NewProvider(Config{
		CapacityBytes: unix.Getpagesize() * pages,
		SizeClasses:   []int{},
		ShardCount:    4,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := provider.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})

	return provider
}

func setExtent(t *testing.T, provider *Provider, key string, value []byte, ttl time.Duration) *cacheEntry {
	t.Helper()
	if err := provider.Set(context.Background(), key, value, ttl); err != nil {
		t.Fatalf("Set(%q, %d bytes): %v", key, len(value), err)
	}
	item := provider.lookup(key)
	if item == nil {
		t.Fatalf("Set(%q) did not index an entry", key)
	}
	if item.kind != allocationExtent {
		t.Fatalf("Set(%q) allocation kind = %d, want extent", key, item.kind)
	}

	return item
}

func acquireExtentReader(t *testing.T, provider *Provider, item *cacheEntry) {
	t.Helper()
	reclaimed, acquired, err := provider.acquireExtent(item)
	if err != nil || !acquired || reclaimed {
		t.Fatalf("acquireExtent() = (%v, %v, %v), want (false, true, nil)", reclaimed, acquired, err)
	}
}

func assertExtentReaderSeesValue(t *testing.T, provider *Provider, item *cacheEntry, want []byte) {
	t.Helper()
	got := make([]byte, item.length)
	provider.copyFromExtent(got, item)
	if !bytes.Equal(got, want) {
		t.Fatalf("active reader copied %d bytes that do not match the stored value", len(got))
	}
}

func forceExpiryNow(t *testing.T, provider *Provider, item *cacheEntry) {
	t.Helper()
	provider.expiryMu.Lock()
	record := item.expiry
	if record == nil || record.index < 0 {
		provider.expiryMu.Unlock()
		t.Fatalf("entry %q has no pending expiration record", item.key)
	}
	record.expiresAt = 1
	heap.Fix(&provider.expirations, record.index)
	provider.expiryMu.Unlock()

	provider.expireReady()
}

func idleAttempts(provider *Provider) uint64 {
	stats := provider.Stats()

	return stats.IdleCalls + stats.IdleErrors
}

func TestExtentDeleteWaitsForReader(t *testing.T) {
	provider := newExtentTestProvider(t, 8)
	ctx := context.Background()
	value := bytes.Repeat([]byte{0x3c}, provider.pageSize*2)
	item := setExtent(t, provider, "key", value, 0)
	acquireExtentReader(t, provider, item)

	if err := provider.Delete(ctx, "key"); err != nil {
		t.Fatalf("Delete(): %v", err)
	}
	if item.freed {
		t.Fatal("Delete finalized an extent with an active reader")
	}
	if item.state != entryDeleting {
		t.Fatalf("entry state = %d, want entryDeleting", item.state)
	}
	if _, found, err := provider.Get(ctx, "key"); err != nil || found {
		t.Fatalf("Get() after Delete = (_, %v, %v), want miss", found, err)
	}

	reserved := int64(item.pageCount) * int64(provider.pageSize)
	stats := provider.Stats()
	if stats.Entries != 1 || stats.ReservedBytes != reserved {
		t.Fatalf("Stats() with active reader = %+v, want 1 entry and %d reserved bytes", stats, reserved)
	}
	assertExtentReaderSeesValue(t, provider, item, value)

	provider.releaseExtent(item)
	if !item.freed {
		t.Fatal("last release did not finalize the deleted extent")
	}
	stats = provider.Stats()
	if stats.Entries != 0 || stats.ReservedBytes != 0 || stats.FreeBytes != int64(provider.capacity) {
		t.Fatalf("Stats() after final release = %+v", stats)
	}
}

func TestExtentReplacementWaitsForReader(t *testing.T) {
	provider := newExtentTestProvider(t, 16)
	ctx := context.Background()
	first := bytes.Repeat([]byte{0x11}, provider.pageSize*2)
	second := bytes.Repeat([]byte{0x22}, provider.pageSize*3)

	old := setExtent(t, provider, "key", first, 0)
	acquireExtentReader(t, provider, old)
	current := setExtent(t, provider, "key", second, 0)
	if current == old {
		t.Fatal("replacement reused the pinned entry")
	}
	if old.freed {
		t.Fatal("replacement finalized the old extent with an active reader")
	}
	assertExtentReaderSeesValue(t, provider, old, first)

	got, found, err := provider.Get(ctx, "key")
	if err != nil || !found || !bytes.Equal(got, second) {
		t.Fatalf("Get() after replacement = (len=%d, %v, %v), want the new value", len(got), found, err)
	}
	reserved := int64(old.pageCount+current.pageCount) * int64(provider.pageSize)
	stats := provider.Stats()
	if stats.Entries != 2 || stats.ReservedBytes != reserved {
		t.Fatalf(
			"Stats() while the replaced extent is pinned = %+v, want 2 entries and %d reserved bytes",
			stats,
			reserved,
		)
	}

	provider.releaseExtent(old)
	if !old.freed {
		t.Fatal("last release did not finalize the replaced extent")
	}
	reserved = int64(current.pageCount) * int64(provider.pageSize)
	stats = provider.Stats()
	if stats.Entries != 1 || stats.ReservedBytes != reserved {
		t.Fatalf("Stats() after releasing the replaced extent = %+v, want %d reserved bytes", stats, reserved)
	}
	got, found, err = provider.Get(ctx, "key")
	if err != nil || !found || !bytes.Equal(got, second) {
		t.Fatalf("Get() after release = (len=%d, %v, %v), want the new value", len(got), found, err)
	}
}

func TestExtentTTLExpiryWaitsForReader(t *testing.T) {
	provider := newExtentTestProvider(t, 8)
	value := bytes.Repeat([]byte{0x5e}, provider.pageSize*2)
	item := setExtent(t, provider, "key", value, time.Hour)
	acquireExtentReader(t, provider, item)

	forceExpiryNow(t, provider, item)
	if provider.lookup("key") != nil {
		t.Fatal("TTL expiration left the entry indexed")
	}
	if item.freed {
		t.Fatal("TTL expiration finalized an extent with an active reader")
	}
	stats := provider.Stats()
	reserved := int64(item.pageCount) * int64(provider.pageSize)
	if stats.Entries != 1 || stats.ReservedBytes != reserved || stats.ExpiredMisses != 1 {
		t.Fatalf("Stats() after expiring a pinned extent = %+v, want %d reserved bytes", stats, reserved)
	}
	assertExtentReaderSeesValue(t, provider, item, value)

	provider.releaseExtent(item)
	if !item.freed {
		t.Fatal("last release did not finalize the expired extent")
	}
	stats = provider.Stats()
	if stats.Entries != 0 || stats.ReservedBytes != 0 || stats.FreeBytes != int64(provider.capacity) {
		t.Fatalf("Stats() after final release = %+v", stats)
	}
	if _, found, err := provider.Get(context.Background(), "key"); err != nil || found {
		t.Fatalf("Get() after expiration = (_, %v, %v), want miss", found, err)
	}
}

func TestExtentIdleDeferredUntilLastReader(t *testing.T) {
	provider := newConfiguredTestProvider(t, Config{
		CapacityBytes:    unix.Getpagesize() * 8,
		SizeClasses:      []int{},
		ShardCount:       4,
		DisableIdleDelay: true,
	})
	value := bytes.Repeat([]byte{0x7a}, provider.pageSize*2)
	item := setExtent(t, provider, "key", value, 0)

	acquireExtentReader(t, provider, item)
	acquireExtentReader(t, provider, item)
	if item.refs != 2 {
		t.Fatalf("references after two acquires = %d, want 2", item.refs)
	}

	before := idleAttempts(provider)
	provider.releaseExtent(item)
	if got := idleAttempts(provider); got != before {
		t.Fatalf("idle attempts after releasing one of two readers = %d, want %d", got, before)
	}
	provider.releaseExtent(item)
	if got := idleAttempts(provider); got != before+1 {
		t.Fatalf("idle attempts after the last release = %d, want %d", got, before+1)
	}

	got, found, err := provider.Get(context.Background(), "key")
	if err != nil || !found || !bytes.Equal(got, value) {
		t.Fatalf("Get() after the extent went idle = (len=%d, %v, %v)", len(got), found, err)
	}
}

func TestTrimReleasesMultipleExtents(t *testing.T) {
	provider := newExtentTestProvider(t, 16)
	const entries = 6
	value := bytes.Repeat([]byte{0x2b}, provider.pageSize+1)

	var perEntry int64
	for index := 0; index < entries; index++ {
		item := setExtent(t, provider, fmt.Sprintf("key-%d", index), value, 0)
		if item.pageCount != 2 {
			t.Fatalf("entry pages = %d, want 2", item.pageCount)
		}
		perEntry = int64(item.pageCount) * int64(provider.pageSize)
	}
	startReserved := provider.Stats().ReservedBytes
	if startReserved != entries*perEntry {
		t.Fatalf("reserved bytes = %d, want %d", startReserved, entries*perEntry)
	}

	target := 3 * int64(provider.pageSize)
	freed, err := provider.Trim(target)
	if err != nil {
		t.Fatalf("Trim(%d): %v", target, err)
	}
	if freed < target {
		t.Fatalf("Trim(%d) released %d bytes, want at least the target", target, freed)
	}
	if freed%perEntry != 0 {
		t.Fatalf("Trim released %d bytes, want a multiple of the %d byte extent granularity", freed, perEntry)
	}
	stats := provider.Stats()
	if stats.ReservedBytes != startReserved-freed {
		t.Fatalf("reserved bytes after Trim = %d, want %d", stats.ReservedBytes, startReserved-freed)
	}
	if stats.Entries != entries-freed/perEntry {
		t.Fatalf("entries after Trim = %d, want %d", stats.Entries, entries-freed/perEntry)
	}
	if stats.ReservedBytes+stats.FreeBytes != int64(provider.capacity) {
		t.Fatalf("arena accounting after Trim = %+v, capacity=%d", stats, provider.capacity)
	}

	remaining := stats.ReservedBytes
	freed, err = provider.Trim(int64(provider.capacity))
	if err != nil {
		t.Fatalf("exhaustive Trim(): %v", err)
	}
	if freed != remaining {
		t.Fatalf("exhaustive Trim released %d bytes, want the remaining %d", freed, remaining)
	}
	stats = provider.Stats()
	if stats.Entries != 0 || stats.ReservedBytes != 0 || stats.FreeBytes != int64(provider.capacity) {
		t.Fatalf("Stats() after exhaustive Trim = %+v", stats)
	}

	if freed, err := provider.Trim(int64(provider.pageSize)); err != nil || freed != 0 {
		t.Fatalf("Trim() on an empty index = (%d, %v), want (0, nil)", freed, err)
	}
	if freed, err := provider.Trim(0); err != nil || freed != 0 {
		t.Fatalf("Trim(0) = (%d, %v), want (0, nil)", freed, err)
	}
}

func TestTrimKeepsExtentWithActiveReader(t *testing.T) {
	provider := newExtentTestProvider(t, 16)
	value := bytes.Repeat([]byte{0x4d}, provider.pageSize*2)
	pinned := setExtent(t, provider, "pinned", value, 0)
	evicted := setExtent(t, provider, "evicted", value, 0)
	acquireExtentReader(t, provider, pinned)

	perEntry := int64(pinned.pageCount) * int64(provider.pageSize)
	freed, err := provider.Trim(int64(provider.capacity))
	if err != nil {
		t.Fatalf("Trim(): %v", err)
	}
	if freed != perEntry {
		t.Fatalf("Trim released %d bytes, want only the unreferenced extent (%d)", freed, perEntry)
	}
	if !evicted.freed {
		t.Fatal("Trim did not finalize the unreferenced extent")
	}
	if pinned.freed {
		t.Fatal("Trim finalized an extent with an active reader")
	}
	if provider.lookup("pinned") != nil || provider.lookup("evicted") != nil {
		t.Fatal("Trim left an evicted key indexed")
	}
	stats := provider.Stats()
	if stats.Entries != 1 || stats.ReservedBytes != perEntry {
		t.Fatalf("Stats() with a pinned extent = %+v, want 1 entry and %d reserved bytes", stats, perEntry)
	}
	assertExtentReaderSeesValue(t, provider, pinned, value)

	if freed, err := provider.Trim(int64(provider.pageSize)); err != nil || freed != 0 {
		t.Fatalf("Trim() with only a pinned extent = (%d, %v), want (0, nil)", freed, err)
	}

	provider.releaseExtent(pinned)
	if !pinned.freed {
		t.Fatal("last release did not finalize the trimmed extent")
	}
	stats = provider.Stats()
	if stats.Entries != 0 || stats.ReservedBytes != 0 || stats.FreeBytes != int64(provider.capacity) {
		t.Fatalf("Stats() after releasing the pinned extent = %+v", stats)
	}
}
