//go:build (linux && !386 && !arm && !mips && !mipsle) || darwin

package madvfree

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestSmallAllocatorPacksSlotsIntoPages(t *testing.T) {
	provider := newTestProvider(t, 4)
	layout := provider.layouts[0]
	value := []byte("small")

	for slot := 0; slot < layout.slotCount; slot++ {
		if err := provider.Set(context.Background(), fmt.Sprintf("key-%d", slot), value, 0); err != nil {
			t.Fatal(err)
		}
	}
	stats := provider.Stats()
	if stats.Entries != int64(layout.slotCount) ||
		stats.ReservedBytes != int64(provider.pageSize) ||
		stats.SmallAllocations != uint64(layout.slotCount) {
		t.Fatalf("packed Stats() = %+v, slots=%d", stats, layout.slotCount)
	}

	if err := provider.Set(context.Background(), "overflow", value, 0); err != nil {
		t.Fatal(err)
	}
	if got := provider.Stats().ReservedBytes; got != int64(2*provider.pageSize) {
		t.Fatalf("reserved bytes after overflow slot = %d, want %d", got, 2*provider.pageSize)
	}

	if err := provider.Delete(context.Background(), "key-0"); err != nil {
		t.Fatal(err)
	}
	if got := provider.Stats().ReservedBytes; got != int64(2*provider.pageSize) {
		t.Fatalf("deleting one packed slot released its page: reserved=%d", got)
	}

	for slot := 1; slot < layout.slotCount; slot++ {
		if err := provider.Delete(context.Background(), fmt.Sprintf("key-%d", slot)); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.Delete(context.Background(), "overflow"); err != nil {
		t.Fatal(err)
	}
	stats = provider.Stats()
	if stats.Entries != 0 || stats.ReservedBytes != 0 || stats.FreeBytes != int64(provider.capacity) {
		t.Fatalf("Stats() after deleting packed slots = %+v", stats)
	}
	assertNoSmallPageMetadata(t, provider)
}

func TestSmallAllocatorReleasedMetadataRejectsStaleReference(t *testing.T) {
	provider := newTestProvider(t, 1)
	ctx := context.Background()
	value := []byte("value")

	if err := provider.Set(ctx, "old", value, 0); err != nil {
		t.Fatal(err)
	}
	old := provider.lookup("old")
	oldReference := smallPageRef{
		pageID:     old.startPage,
		generation: old.generation,
		meta:       old.smallMeta,
	}
	if err := provider.Delete(ctx, "old"); err != nil {
		t.Fatal(err)
	}
	assertNoSmallPageMetadata(t, provider)

	if err := provider.Set(ctx, "new", value, 0); err != nil {
		t.Fatal(err)
	}
	current := provider.lookup("new")
	if current.startPage != old.startPage {
		t.Fatalf("reused page=%d, want %d", current.startPage, old.startPage)
	}
	if current.smallMeta == old.smallMeta {
		t.Fatal("released page reused stale metadata")
	}

	classID, ok := provider.smallClass(len(value))
	if !ok {
		t.Fatal("value has no small class")
	}
	item, stale, allocated, err := provider.allocateSmallSlot(
		oldReference,
		classID,
		provider.layouts[classID],
		"stale",
		value,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if item != nil || len(stale) != 0 || allocated {
		t.Fatalf("stale allocation = (%#v, %d, %v), want rejected", item, len(stale), allocated)
	}

	got, found, err := provider.Get(ctx, "new")
	if err != nil || !found || !bytes.Equal(got, value) {
		t.Fatalf("Get(new) = (%q, %v, %v)", got, found, err)
	}
}

func TestSmallAllocatorCoordinatesConcurrentSlabCreation(t *testing.T) {
	skipUnless4KiBPages(t)
	provider := newTestProvider(t, 64)
	ctx := context.Background()
	layout := provider.layouts[0]
	value := []byte("value")

	for slot := 0; slot < layout.slotCount; slot++ {
		if err := provider.Set(ctx, fmt.Sprintf("full-%d", slot), value, 0); err != nil {
			t.Fatal(err)
		}
	}

	workers := min(layout.slotCount, 16)
	start := make(chan struct{})
	var wait sync.WaitGroup
	errs := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			<-start
			errs <- provider.Set(ctx, fmt.Sprintf("concurrent-%d", worker), value, 0)
		}(worker)
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	if got := provider.Stats().ReservedBytes; got != int64(2*provider.pageSize) {
		t.Fatalf("concurrent creation reserved=%d, want %d", got, 2*provider.pageSize)
	}
	provider.smallMu.RLock()
	pages := len(provider.classPages[0])
	provider.smallMu.RUnlock()
	if pages != 2 {
		t.Fatalf("class pages=%d, want 2", pages)
	}
}

func TestSmallAllocatorSnapshotBufferIsReused(t *testing.T) {
	skipUnless4KiBPages(t)
	provider := newTestProvider(t, 4)
	ctx := context.Background()
	value := []byte("value")
	if err := provider.Set(ctx, "key", value, 0); err != nil {
		t.Fatal(err)
	}

	allocations := testing.AllocsPerRun(100, func() {
		if err := provider.Set(ctx, "key", value, 0); err != nil {
			panic(err)
		}
	})
	if allocations > 1 {
		t.Fatalf("Set replacement allocations=%f, want at most 1", allocations)
	}
}

func TestSmallAllocatorReclaimInvalidatesWholePage(t *testing.T) {
	provider := newTestProvider(t, 2)
	keys := []string{"one", "two", "three"}
	for _, key := range keys {
		if err := provider.Set(context.Background(), key, []byte(key), 0); err != nil {
			t.Fatal(err)
		}
	}
	item := provider.lookup(keys[0])
	if item == nil || item.kind != allocationSmall {
		t.Fatal("small entry was not indexed")
	}
	if err := simulateReclaim(provider.page(item.startPage)); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := provider.Get(context.Background(), keys[0]); err != nil || ok {
		t.Fatalf("Get() after page reclaim = (_, %v, %v), want miss", ok, err)
	}
	for _, key := range keys {
		if provider.lookup(key) != nil {
			t.Fatalf("reclaimed sibling %q remained indexed", key)
		}
	}
	stats := provider.Stats()
	if stats.Entries != 0 ||
		stats.ReservedBytes != 0 ||
		stats.FreeBytes != int64(provider.capacity) ||
		stats.ReclaimedMisses != 1 ||
		stats.GenerationMisses != 0 {
		t.Fatalf("Stats() after small-page reclaim = %+v", stats)
	}
}

func TestMultiPageSlabPacksSixKiBValues(t *testing.T) {
	provider := newTestProvider(t, 64)
	size, layout := multiPageValueSize(t, provider)
	if layout.pageCount <= 1 || layout.slotCount < 2 {
		t.Fatalf("layout is not multi-page packed: %#v", layout)
	}
	extentPages, ok := provider.pagesForValue(size)
	if !ok {
		t.Fatal("multi-page extent does not fit")
	}
	if layout.slabBytes()/layout.slotCount >= int(extentPages)*provider.pageSize {
		t.Fatalf(
			"slab does not improve reservation: slab=%d slots=%d extent=%d",
			layout.slabBytes(),
			layout.slotCount,
			int(extentPages)*provider.pageSize,
		)
	}

	value := bytes.Repeat([]byte{0xa5}, size)
	for slot := 0; slot < layout.slotCount; slot++ {
		key := fmt.Sprintf("medium-%d", slot)
		if err := provider.Set(context.Background(), key, value, 0); err != nil {
			t.Fatal(err)
		}
		got, found, err := provider.Get(context.Background(), key)
		if err != nil || !found || !bytes.Equal(got, value) {
			t.Fatalf("Get(%q) = (len=%d, %v, %v)", key, len(got), found, err)
		}
	}

	stats := provider.Stats()
	if stats.Entries != int64(layout.slotCount) ||
		stats.ReservedBytes != int64(layout.slabBytes()) {
		t.Fatalf("packed medium Stats() = %+v, layout=%#v", stats, layout)
	}
	first := provider.lookup("medium-0")
	if first.pageCount != layout.pageCount {
		t.Fatalf("entry pages=%d, want %d", first.pageCount, layout.pageCount)
	}
}

//nolint:gocyclo // One scenario verifies all affected and unaffected slot outcomes.
func TestMultiPageSlabPartialReclaimPreservesUnaffectedSlots(t *testing.T) {
	provider := newTestProvider(t, 64)
	size, layout := multiPageValueSize(t, provider)
	keys := make([]string, layout.slotCount)
	values := make([][]byte, layout.slotCount)
	items := make([]*cacheEntry, layout.slotCount)
	for slot := 0; slot < layout.slotCount; slot++ {
		key := fmt.Sprintf("medium-%d", slot)
		value := bytes.Repeat([]byte{byte(slot + 1)}, size)
		keys[slot] = key
		values[slot] = value
		if err := provider.Set(context.Background(), key, value, 0); err != nil {
			t.Fatal(err)
		}
		items[slot] = provider.lookup(key)
	}

	if layout.pageCount <= 1 {
		t.Fatalf("test value used single-page layout: %#v", layout)
	}
	target := items[0]
	targetPages, valid := layout.slotPayloadPageMask(int(target.slot), target.length)
	if !valid {
		t.Fatal("target payload page mask is invalid")
	}
	var reclaimedOffset uint32
	foundReclaimPage := false
	for pageOffset := uint32(0); pageOffset < layout.pageCount; pageOffset++ {
		pageMask := uint32(1) << pageOffset
		if targetPages&pageMask != 0 {
			continue
		}
		for _, item := range items[1:] {
			payloadPages, maskValid := layout.slotPayloadPageMask(int(item.slot), item.length)
			if maskValid && payloadPages&pageMask != 0 {
				reclaimedOffset = pageOffset
				foundReclaimPage = true

				break
			}
		}
		if foundReclaimPage {
			break
		}
	}
	if !foundReclaimPage {
		t.Fatalf("layout has no page that isolates target payload: %#v", layout)
	}
	reclaimedMask := uint32(1) << reclaimedOffset
	if err := simulateReclaim(provider.page(target.startPage + reclaimedOffset)); err != nil {
		t.Fatal(err)
	}

	got, found, err := provider.Get(context.Background(), keys[0])
	if err != nil || !found || !bytes.Equal(got, values[0]) {
		t.Fatalf("Get(target) after partial reclaim = (len=%d, %v, %v), want hit", len(got), found, err)
	}

	unaffected := 0
	reclaimedSlot := -1
	for slot, item := range items {
		payloadPages, maskValid := layout.slotPayloadPageMask(int(item.slot), item.length)
		if !maskValid {
			t.Fatalf("slot %d payload page mask is invalid", slot)
		}
		if payloadPages&reclaimedMask != 0 {
			if reclaimedSlot < 0 {
				reclaimedSlot = slot
			}
			if provider.lookup(keys[slot]) != nil {
				t.Fatalf("reclaimed slot %q remained indexed", keys[slot])
			}

			continue
		}
		unaffected++
		got, found, err := provider.Get(context.Background(), keys[slot])
		if err != nil || !found || !bytes.Equal(got, values[slot]) {
			t.Fatalf("Get(%q) after sibling reclaim = (len=%d, %v, %v)", keys[slot], len(got), found, err)
		}
	}
	stats := provider.Stats()
	if stats.Entries != int64(unaffected) ||
		stats.ReservedBytes != int64(layout.slabBytes()) ||
		stats.ReclaimedMisses != 0 ||
		stats.GenerationMisses != 0 {
		t.Fatalf("Stats() after partial slab reclaim = %+v", stats)
	}

	replacementKey := "replacement"
	replacementValue := bytes.Repeat([]byte{0x7f}, size)
	if err := provider.Set(context.Background(), replacementKey, replacementValue, 0); err != nil {
		t.Fatal(err)
	}
	replacement := provider.lookup(replacementKey)
	if replacement == nil ||
		replacement.startPage != target.startPage ||
		int(replacement.slot) != reclaimedSlot {
		t.Fatalf(
			"replacement = %#v, want slab=%d slot=%d",
			replacement,
			target.startPage,
			reclaimedSlot,
		)
	}
	if got := provider.Stats().ReservedBytes; got != int64(layout.slabBytes()) {
		t.Fatalf("replacement reserved bytes=%d, want %d", got, layout.slabBytes())
	}
}

func TestMultiPageSlabAllocationRepairsPartialReclaim(t *testing.T) {
	provider := newTestProvider(t, 64)
	size, layout := multiPageValueSize(t, provider)
	value := bytes.Repeat([]byte{0x5a}, size)
	items := make([]*cacheEntry, layout.slotCount)
	for slot := range layout.slotCount {
		key := fmt.Sprintf("old-%d", slot)
		if err := provider.Set(context.Background(), key, value, 0); err != nil {
			t.Fatal(err)
		}
		items[slot] = provider.lookup(key)
	}

	targetPages, valid := layout.slotPayloadPageMask(int(items[0].slot), items[0].length)
	if !valid {
		t.Fatal("target payload page mask is invalid")
	}
	reclaimedOffset := uint32(layout.pageCount)
	for pageOffset := uint32(0); pageOffset < layout.pageCount; pageOffset++ {
		pageMask := uint32(1) << pageOffset
		if targetPages&pageMask != 0 {
			continue
		}
		for _, item := range items[1:] {
			payloadPages, maskValid := layout.slotPayloadPageMask(int(item.slot), item.length)
			if maskValid && payloadPages&pageMask != 0 {
				reclaimedOffset = pageOffset

				break
			}
		}
		if reclaimedOffset != layout.pageCount {
			break
		}
	}
	if reclaimedOffset == layout.pageCount {
		t.Fatalf("layout has no page that isolates target payload: %#v", layout)
	}
	reclaimedMask := uint32(1) << reclaimedOffset
	if err := simulateReclaim(provider.page(items[0].startPage + reclaimedOffset)); err != nil {
		t.Fatal(err)
	}

	if err := provider.Set(context.Background(), "replacement", value, 0); err != nil {
		t.Fatal(err)
	}
	replacement := provider.lookup("replacement")
	if replacement == nil || replacement.startPage != items[0].startPage {
		t.Fatalf("replacement did not reuse repaired slab: %#v", replacement)
	}
	wantEntries := int64(1)
	for slot, item := range items {
		payloadPages, maskValid := layout.slotPayloadPageMask(int(item.slot), item.length)
		if !maskValid {
			t.Fatalf("slot %d payload page mask is invalid", slot)
		}
		old := provider.lookup(fmt.Sprintf("old-%d", slot))
		if payloadPages&reclaimedMask != 0 {
			if old != nil {
				t.Fatalf("reclaimed slot %d remained indexed", slot)
			}

			continue
		}
		wantEntries++
		if old == nil {
			t.Fatalf("unaffected slot %d was removed", slot)
		}
	}
	stats := provider.Stats()
	if stats.Entries != wantEntries || stats.ReservedBytes != int64(layout.slabBytes()) {
		t.Fatalf("Stats() after allocation repair = %+v, want entries=%d", stats, wantEntries)
	}
	got, found, err := provider.Get(context.Background(), "old-0")
	if err != nil || !found || !bytes.Equal(got, value) {
		t.Fatalf("Get(unaffected) = (len=%d, %v, %v)", len(got), found, err)
	}
}

func TestSmallSlabRetireRepairsPartialReclaim(t *testing.T) {
	provider := newTestProvider(t, 8)
	ctx := context.Background()
	if err := provider.Set(ctx, "empty", []byte{}, 0); err != nil {
		t.Fatal(err)
	}
	if err := provider.Set(ctx, "retired", []byte("retired"), 0); err != nil {
		t.Fatal(err)
	}
	if err := provider.Set(ctx, "lost", []byte("lost"), 0); err != nil {
		t.Fatal(err)
	}
	empty := provider.lookup("empty")
	retired := provider.lookup("retired")
	if empty == nil || retired == nil || empty.startPage != retired.startPage {
		t.Fatal("entries did not share one slab")
	}
	layout := provider.layouts[empty.classID]
	mask, valid := layout.slotPayloadPageMask(int(empty.slot), empty.length)
	if !valid || mask != 0 {
		t.Fatalf("zero-length payload page mask = %#b (valid=%v), want 0", mask, valid)
	}
	if err := simulateReclaim(provider.page(empty.startPage)); err != nil {
		t.Fatal(err)
	}

	if err := provider.Delete(ctx, "retired"); err != nil {
		t.Fatal(err)
	}

	got, found, err := provider.Get(ctx, "empty")
	if err != nil || !found || len(got) != 0 {
		t.Fatalf("Get(\"empty\") after sibling retire = (%q, %v, %v), want hit", got, found, err)
	}
	if _, found, err := provider.Get(ctx, "lost"); err != nil || found {
		t.Fatalf("Get(\"lost\") after payload reclaim = (_, %v, %v), want miss", found, err)
	}
	stats := provider.Stats()
	if stats.Entries != 1 ||
		stats.LogicalBytes != 0 ||
		stats.ReservedBytes != int64(layout.slabBytes()) {
		t.Fatalf("Stats() after sibling retire = %+v, want one zero-length entry", stats)
	}
}

func TestMultiPageSlabRetireRepairsPartialReclaim(t *testing.T) {
	provider := newTestProvider(t, 64)
	size, layout := multiPageValueSize(t, provider)
	keys := make([]string, layout.slotCount)
	values := make([][]byte, layout.slotCount)
	items := make([]*cacheEntry, layout.slotCount)
	for slot := 0; slot < layout.slotCount; slot++ {
		keys[slot] = fmt.Sprintf("medium-%d", slot)
		values[slot] = bytes.Repeat([]byte{byte(slot + 1)}, size)
		if err := provider.Set(context.Background(), keys[slot], values[slot], 0); err != nil {
			t.Fatal(err)
		}
		items[slot] = provider.lookup(keys[slot])
	}

	const reclaimedMask = uint32(1)
	retiredSlot := -1
	for slot, item := range items {
		payloadPages, maskValid := layout.slotPayloadPageMask(int(item.slot), item.length)
		if !maskValid {
			t.Fatalf("slot %d payload page mask is invalid", slot)
		}
		if payloadPages&reclaimedMask == 0 {
			retiredSlot = slot

			break
		}
	}
	if retiredSlot < 0 {
		t.Skipf("layout has no slot whose payload avoids the first page: %#v", layout)
	}
	if err := simulateReclaim(provider.page(items[0].startPage)); err != nil {
		t.Fatal(err)
	}
	if err := provider.Delete(context.Background(), keys[retiredSlot]); err != nil {
		t.Fatal(err)
	}

	survivors := int64(0)
	for slot, item := range items {
		if slot == retiredSlot {
			continue
		}
		payloadPages, maskValid := layout.slotPayloadPageMask(int(item.slot), item.length)
		if !maskValid {
			t.Fatalf("slot %d payload page mask is invalid", slot)
		}
		got, found, err := provider.Get(context.Background(), keys[slot])
		if payloadPages&reclaimedMask != 0 {
			if err != nil || found {
				t.Fatalf("Get(%q) after payload reclaim = (_, %v, %v), want miss", keys[slot], found, err)
			}

			continue
		}
		survivors++
		if err != nil || !found || !bytes.Equal(got, values[slot]) {
			t.Fatalf("Get(%q) after unrelated retire = (len=%d, %v, %v), want hit", keys[slot], len(got), found, err)
		}
	}
	if survivors == 0 {
		t.Skipf("layout has no slot that outlives the retire: %#v", layout)
	}
	stats := provider.Stats()
	if stats.Entries != survivors || stats.ReservedBytes != int64(layout.slabBytes()) {
		t.Fatalf("Stats() after repaired retire = %+v, want entries=%d", stats, survivors)
	}
}

func TestMultiPageSlabFallsBackToExtent(t *testing.T) {
	skipUnless4KiBPages(t)
	pageSize := unix.Getpagesize()
	provider, err := NewProvider(Config{CapacityBytes: pageSize * 2})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()

	value := bytes.Repeat([]byte{0xa5}, 6<<10)
	if err := provider.Set(context.Background(), "key", value, 0); err != nil {
		t.Fatal(err)
	}
	item := provider.lookup("key")
	if item.kind != allocationExtent {
		t.Fatalf("allocation kind=%d, want extent fallback", item.kind)
	}
	got, found, err := provider.Get(context.Background(), "key")
	if err != nil || !found || !bytes.Equal(got, value) {
		t.Fatalf("Get() = (len=%d, %v, %v)", len(got), found, err)
	}
}

func TestMultiPageSlabAvoidsInefficientClassRounding(t *testing.T) {
	skipUnless4KiBPages(t)
	provider := newTestProvider(t, 16)
	value := bytes.Repeat([]byte{0xa5}, 7<<10)
	if err := provider.Set(context.Background(), "key", value, 0); err != nil {
		t.Fatal(err)
	}
	item := provider.lookup("key")
	if item.kind != allocationExtent {
		t.Fatalf("allocation kind=%d, want extent because 8 KiB class wastes more", item.kind)
	}
	if item.pageCount != 2 {
		t.Fatalf("extent pages=%d, want 2", item.pageCount)
	}
}

func TestMultiPageSlabTTLAndActiveReader(t *testing.T) {
	provider := newTestProvider(t, 32)
	value := bytes.Repeat([]byte{0x6b}, 6<<10)
	if err := provider.Set(context.Background(), "keeper", value, 0); err != nil {
		t.Fatal(err)
	}
	if err := provider.Set(context.Background(), "expired", value, time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	provider.expireReady()

	keeper := provider.lookup("keeper")
	if keeper == nil {
		t.Fatal("TTL expiration removed sibling slot")
	}
	layout := provider.layouts[keeper.classID]
	stats := provider.Stats()
	if stats.Entries != 1 || stats.ReservedBytes != int64(layout.slabBytes()) {
		t.Fatalf("Stats() after medium TTL expiration = %+v", stats)
	}
	if _, acquired, err := provider.acquireSmall(keeper); err != nil || !acquired {
		t.Fatal("acquireSmall() failed")
	}
	if err := provider.Delete(context.Background(), "keeper"); err != nil {
		t.Fatal(err)
	}
	if keeper.freed {
		t.Fatal("Delete finalized medium slot with active reader")
	}
	got := make([]byte, len(value))
	provider.copyFromSmall(got, keeper)
	if !bytes.Equal(got, value) {
		t.Fatal("active reader observed corrupted medium value")
	}
	provider.releaseSmall(keeper)
	if stats := provider.Stats(); stats.Entries != 0 || stats.ReservedBytes != 0 {
		t.Fatalf("Stats() after final medium release = %+v", stats)
	}
}

func TestMultiPageSlabRepeatedLifecycleReleasesArena(t *testing.T) {
	value := bytes.Repeat([]byte{0x7c}, 6<<10)
	for iteration := 0; iteration < 50; iteration++ {
		provider, err := NewProvider(Config{CapacityBytes: unix.Getpagesize() * 10})
		if err != nil {
			t.Fatal(err)
		}
		for slot := 0; slot < 3; slot++ {
			if err := provider.Set(context.Background(), fmt.Sprintf("key-%d", slot), value, 0); err != nil {
				t.Fatal(err)
			}
		}
		if err := provider.Purge(); err != nil {
			t.Fatal(err)
		}
		stats := provider.Stats()
		if stats.Entries != 0 ||
			stats.LogicalBytes != 0 ||
			stats.ReservedBytes != 0 ||
			stats.FreeBytes != int64(provider.capacity) {
			t.Fatalf("iteration %d Stats() after Purge = %+v", iteration, stats)
		}
		assertNoSmallPageMetadata(t, provider)
		if err := provider.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSmallAllocatorSlotGenerationChangesOnReuse(t *testing.T) {
	provider := newTestProvider(t, 1)
	if err := provider.Set(context.Background(), "keeper", []byte("keeper"), 0); err != nil {
		t.Fatal(err)
	}
	if err := provider.Set(context.Background(), "old", []byte("old"), 0); err != nil {
		t.Fatal(err)
	}
	old := provider.lookup("old")
	oldSlot := old.slot
	oldGeneration := old.slotGeneration
	if err := provider.Delete(context.Background(), "old"); err != nil {
		t.Fatal(err)
	}
	if err := provider.Set(context.Background(), "new", []byte("new"), 0); err != nil {
		t.Fatal(err)
	}
	replacement := provider.lookup("new")
	if replacement.slot != oldSlot {
		t.Fatalf("replacement slot = %d, want reused slot %d", replacement.slot, oldSlot)
	}
	if replacement.slotGeneration == oldGeneration || replacement.slotGeneration == 0 {
		t.Fatalf("replacement generation = %d, old = %d", replacement.slotGeneration, oldGeneration)
	}

	for key, want := range map[string]string{"keeper": "keeper", "new": "new"} {
		got, ok, err := provider.Get(context.Background(), key)
		if err != nil || !ok || string(got) != want {
			t.Fatalf("Get(%q) = (%q, %v, %v), want %q", key, got, ok, err, want)
		}
	}
}

func TestSmallAllocatorDeleteWaitsForReader(t *testing.T) {
	provider := newTestProvider(t, 1)
	if err := provider.Set(context.Background(), "key", []byte("value"), 0); err != nil {
		t.Fatal(err)
	}
	item := provider.lookup("key")
	if _, ok, err := provider.acquireSmall(item); err != nil || !ok {
		t.Fatal("acquireSmall() failed")
	}
	if err := provider.Delete(context.Background(), "key"); err != nil {
		t.Fatal(err)
	}
	if item.freed {
		t.Fatal("Delete finalized a slot with an active reader")
	}
	provider.smallMu.RLock()
	metadata := len(provider.smallPages)
	provider.smallMu.RUnlock()
	if metadata != 1 {
		t.Fatalf("metadata with active reader=%d, want 1", metadata)
	}

	got := make([]byte, item.length)
	provider.copyFromSmall(got, item)
	if string(got) != "value" {
		t.Fatalf("reader copied %q, want value", got)
	}
	provider.releaseSmall(item)
	if !item.freed {
		t.Fatal("last release did not finalize deleted slot")
	}
	stats := provider.Stats()
	if stats.Entries != 0 || stats.ReservedBytes != 0 {
		t.Fatalf("Stats() after final release = %+v", stats)
	}
	assertNoSmallPageMetadata(t, provider)
}

func assertNoSmallPageMetadata(t *testing.T, provider *Provider) {
	t.Helper()
	provider.smallMu.RLock()
	defer provider.smallMu.RUnlock()
	if len(provider.smallPages) != 0 {
		t.Fatalf("small page metadata entries=%d, want 0", len(provider.smallPages))
	}
	for classID, pages := range provider.classPages {
		if len(pages) != 0 {
			t.Fatalf("class %d page references=%d, want 0", classID, len(pages))
		}
	}
}

func TestTrimReleasesPackedSmallPage(t *testing.T) {
	provider := newTestProvider(t, 2)
	layout := provider.layouts[0]
	for index := 0; index <= layout.slotCount; index++ {
		if err := provider.Set(context.Background(), fmt.Sprintf("key-%d", index), []byte("value"), 0); err != nil {
			t.Fatal(err)
		}
	}

	freed, err := provider.Trim(int64(provider.pageSize))
	if err != nil {
		t.Fatal(err)
	}
	if freed < int64(provider.pageSize) {
		t.Fatalf("Trim() freed %d, want at least one page", freed)
	}
	if got := provider.Stats().ReservedBytes; got > int64(provider.pageSize) {
		t.Fatalf("reserved bytes after Trim = %d, want at most one page", got)
	}
}

func TestSmallAllocatorCanBeDisabled(t *testing.T) {
	provider, err := NewProvider(Config{
		CapacityBytes: unix.Getpagesize() * 2,
		SizeClasses:   []int{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	if err := provider.Set(context.Background(), "key", []byte("value"), 0); err != nil {
		t.Fatal(err)
	}
	item := provider.lookup("key")
	if item.kind != allocationExtent {
		t.Fatalf("allocation kind = %d, want extent", item.kind)
	}
}
