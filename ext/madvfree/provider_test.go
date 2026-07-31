//go:build (linux && !386 && !arm && !mips && !mipsle) || darwin

package madvfree

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func newTestProvider(t *testing.T, pages int) *Provider {
	t.Helper()
	provider, err := NewProvider(Config{
		CapacityBytes: unix.Getpagesize() * pages,
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

// skipUnless4KiBPages skips tests whose expectations are tied to the 4 KiB slab
// arithmetic of x86-64 Linux (for example exact slab counts or class rounding).
func skipUnless4KiBPages(t *testing.T) {
	t.Helper()
	if unix.Getpagesize() != 4096 {
		t.Skipf("test assumes 4 KiB pages, host uses %d", unix.Getpagesize())
	}
}

// multiPageValueSize returns a value length that resolves to a multi-page slab
// class for the host page size, along with that class. It picks the class with
// the most slots so reclaim tests can isolate a single slot's pages.
func multiPageValueSize(t *testing.T, provider *Provider) (int, smallPageLayout) {
	t.Helper()
	best := -1
	for classID := range provider.layouts {
		layout := provider.layouts[classID]
		if layout.pageCount > 1 && layout.slotCount >= 2 &&
			(best < 0 || layout.slotCount > provider.layouts[best].slotCount) {
			best = classID
		}
	}
	if best < 0 {
		t.Skipf("no multi-page size class for page size %d", provider.pageSize)
	}

	return provider.layouts[best].classSize, provider.layouts[best]
}

func TestProviderDefaults(t *testing.T) {
	provider, err := NewProvider(Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := provider.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})

	if provider.capacity != DefaultCapacityBytes {
		t.Fatalf("default capacity=%d, want %d", provider.capacity, DefaultCapacityBytes)
	}
	if len(provider.shards) != defaultShards {
		t.Fatalf("default shards=%d, want %d", len(provider.shards), defaultShards)
	}
	if len(provider.smallPages) != 0 {
		t.Fatalf("default provider eagerly allocated %d small-page metadata entries", len(provider.smallPages))
	}
}

func TestProviderSetGetCopiesValue(t *testing.T) {
	provider := newTestProvider(t, 4)
	input := []byte("value")
	if err := provider.Set(context.Background(), "key", input, 0); err != nil {
		t.Fatal(err)
	}
	input[0] = 'X'

	first, ok, err := provider.Get(context.Background(), "key")
	if err != nil || !ok || string(first) != "value" {
		t.Fatalf("Get() = (%q, %v, %v)", first, ok, err)
	}
	first[0] = 'Y'
	second, ok, err := provider.Get(context.Background(), "key")
	if err != nil || !ok || string(second) != "value" {
		t.Fatalf("second Get() = (%q, %v, %v)", second, ok, err)
	}
}

func TestProviderLargeExtentAndReplacement(t *testing.T) {
	provider := newTestProvider(t, 8)
	value := bytes.Repeat([]byte{0xa5}, unix.Getpagesize()*2)
	if err := provider.Set(context.Background(), "key", []byte("old"), 0); err != nil {
		t.Fatal(err)
	}
	if err := provider.Set(context.Background(), "key", value, 0); err != nil {
		t.Fatal(err)
	}

	got, ok, err := provider.Get(context.Background(), "key")
	if err != nil || !ok || !bytes.Equal(got, value) {
		t.Fatalf("Get() = (len %d, %v, %v)", len(got), ok, err)
	}
	if stats := provider.Stats(); stats.Entries != 1 || stats.LogicalBytes != int64(len(value)) {
		t.Fatalf("Stats() = %+v", stats)
	}
}

func TestProviderTTLAndDelete(t *testing.T) {
	provider := newTestProvider(t, 4)
	if err := provider.Set(context.Background(), "expired", []byte("value"), time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if _, ok, err := provider.Get(context.Background(), "expired"); err != nil || ok {
		t.Fatalf("expired Get() = (_, %v, %v), want miss", ok, err)
	}

	if err := provider.Set(context.Background(), "deleted", []byte("value"), 0); err != nil {
		t.Fatal(err)
	}
	if err := provider.Delete(context.Background(), "deleted"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := provider.Get(context.Background(), "deleted"); err != nil || ok {
		t.Fatalf("deleted Get() = (_, %v, %v), want miss", ok, err)
	}
}

func TestProviderExpiryRecordsDoNotAccumulate(t *testing.T) {
	provider := newTestProvider(t, 16)
	ctx := context.Background()

	for iteration := 0; iteration < 1000; iteration++ {
		if err := provider.Set(ctx, "key", []byte{byte(iteration)}, time.Hour); err != nil {
			t.Fatalf("Set() iteration %d: %v", iteration, err)
		}
	}
	assertSingleExpiryRecord(t, provider, "key")

	if err := provider.Set(ctx, "key", []byte("without TTL"), 0); err != nil {
		t.Fatalf("Set() without TTL: %v", err)
	}
	assertExpiryRecordCount(t, provider, 0)

	if err := provider.Set(ctx, "key", []byte("with TTL"), time.Hour); err != nil {
		t.Fatalf("Set() before Delete: %v", err)
	}
	if err := provider.Delete(ctx, "key"); err != nil {
		t.Fatalf("Delete(): %v", err)
	}
	assertExpiryRecordCount(t, provider, 0)
}

func TestProviderExpiryReplacementRacesExpiration(t *testing.T) {
	provider := newTestProvider(t, 16)
	ctx := context.Background()

	for iteration := 0; iteration < 1000; iteration++ {
		if err := provider.Set(ctx, "key", []byte{byte(iteration)}, time.Nanosecond); err != nil {
			t.Fatalf("expiring Set() iteration %d: %v", iteration, err)
		}
	}
	if err := provider.Set(ctx, "key", []byte("final"), time.Hour); err != nil {
		t.Fatalf("final Set(): %v", err)
	}

	// Allow an expiry record already popped by the worker to finish its
	// removeIfSame attempt against the final entry.
	time.Sleep(10 * time.Millisecond)

	value, found, err := provider.Get(ctx, "key")
	if err != nil || !found || string(value) != "final" {
		t.Fatalf("Get() = (%q, %v, %v), want final value", value, found, err)
	}
	assertSingleExpiryRecord(t, provider, "key")
}

func TestProviderExpiryRemovesNonRootRecord(t *testing.T) {
	provider := newTestProvider(t, 16)
	ctx := context.Background()

	for index, ttl := range []time.Duration{time.Hour, 2 * time.Hour, 3 * time.Hour} {
		key := string(rune('a' + index))
		if err := provider.Set(ctx, key, []byte(key), ttl); err != nil {
			t.Fatalf("Set(%q): %v", key, err)
		}
	}
	assertExpiryHeapInvariants(t, provider, 3)

	if err := provider.Delete(ctx, "b"); err != nil {
		t.Fatalf("Delete non-root record: %v", err)
	}
	assertExpiryHeapInvariants(t, provider, 2)

	if err := provider.Set(ctx, "c", []byte("without TTL"), 0); err != nil {
		t.Fatalf("replace non-root record: %v", err)
	}
	assertSingleExpiryRecord(t, provider, "a")
}

func assertExpiryRecordCount(t *testing.T, provider *Provider, want int) {
	t.Helper()
	provider.expiryMu.Lock()
	defer provider.expiryMu.Unlock()
	if got := len(provider.expirations); got != want {
		t.Fatalf("expiration records = %d, want %d", got, want)
	}
}

func assertSingleExpiryRecord(t *testing.T, provider *Provider, key string) {
	t.Helper()
	item := provider.lookup(key)
	if item == nil {
		t.Fatalf("lookup(%q) returned nil", key)
	}

	provider.expiryMu.Lock()
	defer provider.expiryMu.Unlock()
	if len(provider.expirations) != 1 {
		t.Fatalf("expiration records = %d, want 1", len(provider.expirations))
	}
	record := provider.expirations[0]
	if record.index != 0 || record.item != item || item.expiry != record {
		t.Fatalf(
			"expiration record = {index:%d item:%p}, current item=%p item.expiry=%p",
			record.index,
			record.item,
			item,
			item.expiry,
		)
	}
}

func assertExpiryHeapInvariants(t *testing.T, provider *Provider, want int) {
	t.Helper()
	provider.expiryMu.Lock()
	defer provider.expiryMu.Unlock()
	if len(provider.expirations) != want {
		t.Fatalf("expiration records = %d, want %d", len(provider.expirations), want)
	}
	for index, record := range provider.expirations {
		if record.index != index || record.item.expiry != record {
			t.Fatalf(
				"record %d = {index:%d item:%p}, item.expiry=%p",
				index,
				record.index,
				record.item,
				record.item.expiry,
			)
		}
		if index > 0 {
			parent := (index - 1) / 2
			if provider.expirations[parent].expiresAt > record.expiresAt {
				t.Fatalf("record %d expires before parent %d", index, parent)
			}
		}
	}
}

func TestProviderExpiryHeapConsistentUnderMixedTTL(t *testing.T) {
	provider := newTestProvider(t, 64)
	ctx := context.Background()

	const (
		workers    = 8
		iterations = 200
	)
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			ttl := time.Duration(0)
			if worker%2 == 0 {
				ttl = time.Hour
			}
			key := string(rune('a' + worker))
			for iteration := 0; iteration < iterations; iteration++ {
				if err := provider.Set(ctx, key, []byte{byte(iteration)}, ttl); err != nil &&
					!errors.Is(err, ErrCapacity) {
					t.Errorf("Set(%q): %v", key, err)

					return
				}
				if _, _, err := provider.Get(ctx, key); err != nil {
					t.Errorf("Get(%q): %v", key, err)

					return
				}
				if iteration%8 == 7 {
					if err := provider.Delete(ctx, key); err != nil {
						t.Errorf("Delete(%q): %v", key, err)

						return
					}
				}
			}
		}(worker)
	}
	group.Wait()

	assertExpiryHeapInvariants(t, provider, countIndexedWithTTL(provider))
}

func countIndexedWithTTL(provider *Provider) int {
	var total int
	for shardIndex := range provider.shards {
		shard := &provider.shards[shardIndex]
		shard.mu.RLock()
		for _, item := range shard.entries {
			if item.expiresAt != 0 {
				total++
			}
		}
		shard.mu.RUnlock()
	}

	return total
}

func TestProviderBackgroundExpirationReleasesCapacity(t *testing.T) {
	provider := newTestProvider(t, 1)
	if err := provider.Set(context.Background(), "expired", []byte("value"), time.Millisecond); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	for provider.Stats().Entries != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := provider.Stats().Entries; got != 0 {
		t.Fatalf("entries after expiration = %d, want 0", got)
	}
	if err := provider.Set(context.Background(), "replacement", []byte("value"), 0); err != nil {
		t.Fatalf("Set() after expiration: %v", err)
	}
}

func TestProviderDetectsReclaimedMiddlePage(t *testing.T) {
	provider := newTestProvider(t, 8)
	value := bytes.Repeat([]byte{1}, unix.Getpagesize()*2)
	if err := provider.Set(context.Background(), "key", value, 0); err != nil {
		t.Fatal(err)
	}
	item := provider.lookup("key")
	middle := provider.page(item.startPage + 1)
	if err := simulateReclaim(middle); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := provider.Get(context.Background(), "key"); err != nil || ok {
		t.Fatalf("Get() = (_, %v, %v), want reclaimed miss", ok, err)
	}
	if got := provider.Stats().ReclaimedMisses; got != 1 {
		t.Fatalf("ReclaimedMisses = %d, want 1", got)
	}
}

func TestProviderConcurrentReplacementAndGet(t *testing.T) {
	provider := newTestProvider(t, 64)
	if err := provider.Set(context.Background(), "key", []byte("0"), 0); err != nil {
		t.Fatal(err)
	}

	var wait sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for iteration := 0; iteration < 100; iteration++ {
				value := []byte{byte(worker + 1), byte(iteration)}
				if err := provider.Set(context.Background(), "key", value, 0); err != nil {
					t.Errorf("Set(): %v", err)

					return
				}
				_, _, _ = provider.Get(context.Background(), "key")
			}
		}(worker)
	}
	wait.Wait()
}

func TestProviderReleaseRefcountUnderflowPanics(t *testing.T) {
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

	defer func() {
		if recover() == nil {
			t.Fatal("release with zero references did not panic")
		}
	}()
	provider.release(item)
}

func TestGenerationStartsAtOneAndSkipsZero(t *testing.T) {
	provider := newTestProvider(t, 1)
	if got := provider.nextGeneration(); got != 1 {
		t.Fatalf("first generation = %d, want 1", got)
	}
	provider.generation.Store(^uint64(0))
	if got := provider.nextGeneration(); got != 1 {
		t.Fatalf("generation after wrap = %d, want 1", got)
	}
}

func TestProviderCapacityAndClose(t *testing.T) {
	provider, err := NewProvider(Config{
		CapacityBytes: unix.Getpagesize(),
		SizeClasses:   []int{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Set(context.Background(), "one", []byte("one"), 0); err != nil {
		t.Fatal(err)
	}
	if err := provider.Set(context.Background(), "two", []byte("two"), 0); !errors.Is(err, ErrCapacity) {
		t.Fatalf("Set() error = %v, want ErrCapacity", err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := provider.Get(context.Background(), "one"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Get() after Close error = %v, want ErrClosed", err)
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("second Close(): %v", err)
	}
}

func TestProviderRepeatedLifecycleReleasesResources(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		provider, err := NewProvider(Config{
			CapacityBytes: unix.Getpagesize() * 4,
			ShardCount:    2,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := provider.Set(context.Background(), "key", []byte("value"), time.Hour); err != nil {
			t.Fatal(err)
		}
		if err := provider.Purge(); err != nil {
			t.Fatal(err)
		}
		if stats := provider.Stats(); stats.Entries != 0 ||
			stats.LogicalBytes != 0 ||
			stats.ReservedBytes != 0 ||
			stats.FreeBytes != int64(provider.capacity) {
			t.Fatalf("Stats() after Purge = %+v", stats)
		}
		provider.expiryMu.Lock()
		expirations := len(provider.expirations)
		provider.expiryMu.Unlock()
		if expirations != 0 {
			t.Fatalf("expiration records after Purge = %d, want 0", expirations)
		}
		if err := provider.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-provider.expiryDone:
		default:
			t.Fatal("expiration worker still running after Close")
		}
		if provider.arena != nil {
			t.Fatal("arena retained after Close")
		}
	}
}
