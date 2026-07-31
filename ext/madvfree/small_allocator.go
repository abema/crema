//go:build (linux && !386 && !arm && !mips && !mipsle) || darwin

package madvfree

import (
	"encoding/binary"
	"sync"
)

type smallPageState uint8

const (
	smallPageDead smallPageState = iota
	smallPageLive
)

type smallPageRef struct {
	pageID     uint32
	generation uint64
	meta       *smallPageMeta
}

type smallPageRefBuffer struct {
	references []smallPageRef
}

type smallPageMeta struct {
	mu sync.Mutex

	state      smallPageState
	generation uint64
	classID    uint16
	refs       uint32
	used       int
	entries    []*cacheEntry
}

func (p *Provider) smallClass(length int) (uint16, bool) {
	for classID, layout := range p.layouts {
		if length <= layout.classSize {
			if layout.pageCount > 1 {
				extentPages, ok := p.pagesForValue(length)
				if !ok ||
					uint64(layout.slabBytes()) >=
						uint64(extentPages)*uint64(p.pageSize)*uint64(layout.slotCount) {
					return 0, false
				}
			}

			// Valid size classes are positive, strictly increasing, and capped
			// at 32 KiB, so classID is always representable by uint16.
			return uint16(classID), true
		}
	}

	return 0, false
}

func (p *Provider) allocateSmall(
	key string,
	value []byte,
	expiresAt int64,
) (*cacheEntry, bool, error) {
	classID, ok := p.smallClass(len(value))
	if !ok {
		return nil, false, nil
	}
	layout := p.layouts[classID]

	if item, allocated, err := p.allocateFromSmallPages(classID, layout, key, value, expiresAt); allocated || err != nil {
		return item, true, err
	}

	creation := &p.classCreate[classID]
	creation.Lock()
	defer creation.Unlock()

	// Another Set may have created a slab while this goroutine waited.
	if item, allocated, err := p.allocateFromSmallPages(classID, layout, key, value, expiresAt); allocated || err != nil {
		return item, true, err
	}

	p.allocatorMu.Lock()
	pageID, ok := p.allocator.allocate(layout.pageCount)
	if ok {
		p.stats.reservedBytes.Add(int64(layout.slabBytes()))
	}
	p.allocatorMu.Unlock()
	if !ok {
		return nil, false, nil
	}

	meta := p.smallPageMetadata(pageID)
	meta.mu.Lock()
	generation := p.nextGeneration()
	if err := p.initializeSmallPageLocked(meta, pageID, classID, generation, layout); err != nil {
		meta.mu.Unlock()
		p.removeSmallPage(classID, smallPageRef{pageID: pageID, generation: generation, meta: meta})
		p.allocatorMu.Lock()
		p.allocator.release(pageID, layout.pageCount)
		p.stats.reservedBytes.Add(-int64(layout.slabBytes()))
		p.allocatorMu.Unlock()

		return nil, true, err
	}
	item := p.writeSmallSlotLocked(meta, pageID, classID, layout, key, value, expiresAt)
	meta.mu.Unlock()

	p.smallMu.Lock()
	p.classPages[classID] = append(p.classPages[classID], smallPageRef{
		pageID:     pageID,
		generation: generation,
		meta:       meta,
	})
	p.smallMu.Unlock()

	return item, true, nil
}

func (p *Provider) allocateFromSmallPages(
	classID uint16,
	layout smallPageLayout,
	key string,
	value []byte,
	expiresAt int64,
) (*cacheEntry, bool, error) {
	buffer := p.smallRefs.Get().(*smallPageRefBuffer)
	p.smallMu.RLock()
	buffer.references = append(buffer.references[:0], p.classPages[classID]...)
	p.smallMu.RUnlock()
	defer func() {
		clear(buffer.references)
		buffer.references = buffer.references[:0]
		p.smallRefs.Put(buffer)
	}()

	for _, reference := range buffer.references {
		item, stale, allocated, err := p.allocateSmallSlot(reference, classID, layout, key, value, expiresAt)
		p.cleanupStaleSmall(stale)
		if allocated || err != nil {
			return item, allocated, err
		}
	}

	return nil, false, nil
}

func (p *Provider) allocateSmallSlot(
	reference smallPageRef,
	classID uint16,
	layout smallPageLayout,
	key string,
	value []byte,
	expiresAt int64,
) (*cacheEntry, []*cacheEntry, bool, error) {
	meta := reference.meta
	if meta == nil {
		meta = p.smallPageMetadata(reference.pageID)
	}
	meta.mu.Lock()
	if meta.state != smallPageLive ||
		meta.classID != classID ||
		meta.generation != reference.generation {
		meta.mu.Unlock()

		return nil, nil, false, nil
	}

	var stale []*cacheEntry
	if meta.refs == 0 {
		validPages, err := p.touchAndValidateSmallSlabPages(reference.pageID, meta, layout)
		if err != nil {
			meta.mu.Unlock()

			return nil, nil, false, err
		}
		invalidPages := layout.allPageMask() &^ validPages
		if invalidPages != 0 {
			stale = p.repairSmallSlabPagesLocked(meta, reference.pageID, layout, invalidPages)
		}
	}

	if meta.used >= layout.slotCount {
		if meta.refs == 0 {
			p.markIdleSmallSlabLocked(reference.pageID, layout)
		}
		meta.mu.Unlock()

		return nil, stale, false, nil
	}

	item := p.writeSmallSlotLocked(meta, reference.pageID, classID, layout, key, value, expiresAt)
	meta.mu.Unlock()

	return item, stale, true, nil
}

func (p *Provider) initializeSmallPageLocked(
	meta *smallPageMeta,
	pageID uint32,
	classID uint16,
	generation uint64,
	layout smallPageLayout,
) error {
	slab := p.smallSlab(pageID, layout)
	// Re-pin before writing: the pages may have been left reclaimable by a prior
	// discard, so platforms that require it must re-charge them first.
	if err := p.markActive(slab); err != nil {
		return err
	}
	clear(slab)
	for pageIndex := uint32(0); pageIndex < layout.pageCount; pageIndex++ {
		page := p.page(pageID + pageIndex)
		binary.LittleEndian.PutUint64(page[generationOffset:pageIndexOffset], generation)
		binary.LittleEndian.PutUint32(page[pageIndexOffset:touchOffset], pageIndex)
	}
	meta.state = smallPageLive
	meta.generation = generation
	meta.classID = classID
	meta.refs = 0
	meta.used = 0
	meta.entries = make([]*cacheEntry, layout.slotCount)

	return nil
}

func (p *Provider) writeSmallSlotLocked(
	meta *smallPageMeta,
	pageID uint32,
	classID uint16,
	layout smallPageLayout,
	key string,
	value []byte,
	expiresAt int64,
) *cacheEntry {
	slab := p.smallSlab(pageID, layout)
	slot := -1
	for candidate := 0; candidate < layout.slotCount; candidate++ {
		allocated, valid := layout.slotAllocated(slab, candidate)
		if valid && !allocated {
			slot = candidate

			break
		}
	}
	if slot < 0 {
		panic("madvfree: small page metadata disagrees with used slot count")
	}

	slotGeneration := p.nextGeneration()
	if !layout.copySlotIn(slab, slot, value) ||
		!layout.setSlotLength(slab, slot, uint32(len(value))) ||
		!layout.setSlotGeneration(slab, slot, slotGeneration) ||
		!layout.setSlotAllocated(slab, slot, true) {
		panic("madvfree: failed to write valid small slab slot")
	}

	item := &cacheEntry{
		smallMeta:      meta,
		key:            key,
		kind:           allocationSmall,
		startPage:      pageID,
		pageCount:      layout.pageCount,
		classID:        classID,
		slot:           uint16(slot),
		length:         len(value),
		generation:     meta.generation,
		slotGeneration: slotGeneration,
		expiresAt:      expiresAt,
		refs:           1,
	}
	meta.entries[slot] = item
	meta.used++
	meta.refs++

	p.allocatorMu.Lock()
	p.stats.entries.Add(1)
	p.stats.logicalBytes.Add(int64(len(value)))
	p.allocatorMu.Unlock()
	p.stats.allocations.Add(1)
	p.stats.smallAllocations.Add(1)

	return item
}

func (p *Provider) touchAndValidateSmallSlabPages(
	startPage uint32,
	meta *smallPageMeta,
	layout smallPageLayout,
) (uint32, error) {
	if p.backend.canReadIdle() {
		return p.precheckAndActivateSmallSlabPages(startPage, meta, layout)
	}

	return p.activateThenValidateSmallSlabPages(startPage, meta, layout)
}

func (p *Provider) activateThenValidateSmallSlabPages(
	startPage uint32,
	meta *smallPageMeta,
	layout smallPageLayout,
) (uint32, error) {
	if err := p.markActive(p.smallSlab(startPage, layout)); err != nil {
		return 0, err
	}
	var validPages uint32
	for pageIndex := uint32(0); pageIndex < layout.pageCount; pageIndex++ {
		generation, storedIndex := pageHeader(p.page(startPage + pageIndex))
		if generation == meta.generation && storedIndex == pageIndex {
			validPages |= uint32(1) << pageIndex
		}
	}

	return validPages, nil
}

func (p *Provider) precheckAndActivateSmallSlabPages(
	startPage uint32,
	meta *smallPageMeta,
	layout smallPageLayout,
) (uint32, error) {
	var validPages uint32
	activated := false
	for pageIndex := uint32(0); pageIndex < layout.pageCount; pageIndex++ {
		page := p.page(startPage + pageIndex)
		generation := pageGeneration(page)
		if generation != meta.generation {
			continue
		}
		if err := p.reactivate(page); err != nil {
			return 0, err
		}
		activated = true

		// Revalidate after the write-touch.
		generation, storedIndex := pageHeader(page)
		if generation == meta.generation && storedIndex == pageIndex {
			validPages |= uint32(1) << pageIndex
		}
	}
	if activated {
		p.stats.reactivateCalls.Add(1)
	}

	return validPages, nil
}

//nolint:cyclop,funlen // Every metadata mismatch has different reclaim semantics.
func (p *Provider) acquireSmall(item *cacheEntry) (bool, bool, error) {
	item.mu.Lock()
	if item.state != entryLive {
		item.mu.Unlock()

		return false, false, nil
	}
	meta := item.smallMeta
	meta.mu.Lock()
	layout := p.layouts[item.classID]
	slab := p.smallSlab(item.startPage, layout)
	if meta.state != smallPageLive ||
		meta.generation != item.generation ||
		meta.classID != item.classID {
		item.state = entryDead
		meta.mu.Unlock()
		item.mu.Unlock()
		p.finalizeStaleSmall(item)

		return false, false, nil
	}
	var stale []*cacheEntry
	reclaimed := false
	if meta.refs == 0 {
		validPages, err := p.touchAndValidateSmallSlabPages(item.startPage, meta, layout)
		if err != nil {
			meta.mu.Unlock()
			item.mu.Unlock()

			return false, false, err
		}
		invalidPages := layout.allPageMask() &^ validPages
		if invalidPages != 0 {
			reclaimed = p.smallSlabPagesWereReclaimed(item.startPage, layout, invalidPages)
			stale = p.repairSmallSlabPagesLocked(meta, item.startPage, layout, invalidPages)
			if meta.used == 0 {
				classID := meta.classID
				reference := smallPageRef{
					pageID:     item.startPage,
					generation: meta.generation,
					meta:       meta,
				}
				meta.state = smallPageDead
				meta.entries = nil
				item.state = entryDead
				p.discardSmallSlabLocked(item.startPage, layout)
				meta.mu.Unlock()
				item.mu.Unlock()
				p.releaseDiscardedSmallPage(classID, reference, stale)

				return reclaimed, false, nil
			}
		}
	}

	if containsCacheEntry(stale, item) {
		// Repair already removed this slot.
		item.state = entryDead
		if meta.refs == 0 {
			p.markIdleSmallSlabLocked(item.startPage, layout)
		}
		meta.mu.Unlock()
		item.mu.Unlock()
		p.cleanupStaleSmall(stale)

		return reclaimed, false, nil
	}

	slot := int(item.slot)
	if slot >= len(meta.entries) {
		panic("madvfree: small entry slot ownership mismatch")
	}
	if meta.entries[slot] == nil {
		// A concurrent reclaim repair removed this slot while this entry was
		// waiting for meta.mu. cleanupStaleSmall will finish retirement; this
		// caller must observe the entry as a cache miss.
		item.state = entryDead
		if meta.refs == 0 {
			p.markIdleSmallSlabLocked(item.startPage, layout)
		}
		meta.mu.Unlock()
		item.mu.Unlock()
		p.cleanupStaleSmall(stale)

		return reclaimed, false, nil
	}
	if meta.entries[slot] != item {
		panic("madvfree: small entry slot ownership mismatch")
	}
	allocated, valid := layout.slotAllocated(slab, slot)
	slotGeneration, generationValid := layout.slotGeneration(slab, slot)
	length, lengthValid := layout.slotLength(slab, slot)
	if !valid ||
		!allocated ||
		!generationValid ||
		slotGeneration != item.slotGeneration ||
		!lengthValid ||
		int(length) != item.length {
		panic("madvfree: small slot metadata mismatch")
	}

	item.refs++
	meta.refs++
	meta.mu.Unlock()
	item.mu.Unlock()
	p.cleanupStaleSmall(stale)

	return false, true, nil
}

func containsCacheEntry(entries []*cacheEntry, target *cacheEntry) bool {
	for _, item := range entries {
		if item == target {
			return true
		}
	}

	return false
}

func (p *Provider) copyFromSmall(destination []byte, item *cacheEntry) {
	layout := p.layouts[item.classID]
	if !layout.copySlotOut(destination, p.smallSlab(item.startPage, layout), int(item.slot)) {
		panic("madvfree: failed to copy valid small slab slot")
	}
}

func (p *Provider) releaseSmall(item *cacheEntry) {
	item.mu.Lock()
	meta := item.smallMeta
	meta.mu.Lock()
	if item.refs == 0 || meta.refs == 0 {
		panic("madvfree: small reference count underflow")
	}
	item.refs--
	meta.refs--
	finalize := item.state != entryLive && item.refs == 0
	if finalize {
		p.finalizeSmallLocked(item, meta)

		return
	}

	if meta.refs == 0 {
		p.markIdleSmallSlabLocked(item.startPage, p.layouts[item.classID])
	}
	meta.mu.Unlock()
	item.mu.Unlock()
}

func (p *Provider) retireSmall(item *cacheEntry) {
	item.mu.Lock()
	if item.state == entryLive {
		item.state = entryDeleting
	}
	if item.refs != 0 {
		item.mu.Unlock()

		return
	}

	meta := item.smallMeta
	meta.mu.Lock()

	p.finalizeSmallLocked(item, meta)
}

// finalizeSmallLocked consumes both item.mu and meta.mu.
//
//nolint:cyclop // Slot and page lifecycle transitions must remain in one lock-consuming function.
func (p *Provider) finalizeSmallLocked(item *cacheEntry, meta *smallPageMeta) {
	if item.freed {
		meta.mu.Unlock()
		item.mu.Unlock()

		return
	}

	layout := p.layouts[item.classID]
	slab := p.smallSlab(item.startPage, layout)
	slot := int(item.slot)
	slotMatches := meta.state == smallPageLive &&
		meta.generation == item.generation &&
		meta.classID == item.classID &&
		slot < len(meta.entries) &&
		meta.entries[slot] == item
	var stale []*cacheEntry
	if slotMatches && meta.refs == 0 {
		var finalized bool
		stale, slotMatches, finalized = p.repairRetiredSmallSlabLocked(item, meta, layout)
		if finalized {
			return
		}
	}
	if slotMatches {
		clearSmallSlotLocked(meta, slab, layout, slot)
	}
	item.freed = true
	item.state = entryDead

	releasePage := meta.state == smallPageLive && meta.used == 0 && meta.refs == 0
	if releasePage {
		meta.state = smallPageDead
		p.discardSmallSlabLocked(item.startPage, layout)
	} else if meta.state == smallPageLive && meta.refs == 0 {
		p.markIdleSmallSlabLocked(item.startPage, layout)
	}
	pageID := item.startPage
	classID := item.classID
	pageGeneration := item.generation

	p.allocatorMu.Lock()
	p.stats.entries.Add(-1)
	p.stats.logicalBytes.Add(-int64(item.length))
	p.allocatorMu.Unlock()
	meta.mu.Unlock()
	item.mu.Unlock()
	p.cleanupStaleSmall(stale)
	if releasePage {
		// Drop the page from the index before returning it to the allocator, so a
		// concurrent allocation cannot reuse pageID and re-register this meta
		// between the two steps.
		p.removeSmallPage(classID, smallPageRef{pageID: pageID, generation: pageGeneration, meta: meta})
		p.allocatorMu.Lock()
		p.allocator.release(pageID, layout.pageCount)
		p.stats.reservedBytes.Add(-int64(layout.slabBytes()))
		p.allocatorMu.Unlock()
	}
}

func clearSmallSlotLocked(
	meta *smallPageMeta,
	slab []byte,
	layout smallPageLayout,
	slot int,
) {
	allocated, valid := layout.slotAllocated(slab, slot)
	if !valid || !allocated || meta.used <= 0 {
		panic("madvfree: small bitmap/used metadata mismatch")
	}
	if !layout.clearSlotMetadata(slab, slot) {
		panic("madvfree: failed to clear valid small slot metadata")
	}
	meta.entries[slot] = nil
	meta.used--
}

// repairRetiredSmallSlabLocked repairs reclaimed pages before retiring item.
// It consumes item.mu and meta.mu when reactivation fails.
func (p *Provider) repairRetiredSmallSlabLocked(
	item *cacheEntry,
	meta *smallPageMeta,
	layout smallPageLayout,
) (stale []*cacheEntry, slotMatches, finalized bool) {
	validPages, err := p.touchAndValidateSmallSlabPages(item.startPage, meta, layout)
	if err != nil {
		p.finalizeReclaimedSmallLocked(item, meta)

		return nil, false, true
	}
	invalidPages := layout.allPageMask() &^ validPages
	if invalidPages == 0 {
		return nil, true, false
	}

	stale = p.repairSmallSlabPagesLocked(meta, item.startPage, layout, invalidPages)

	return stale, meta.entries[item.slot] == item, false
}

// finalizeReclaimedSmallLocked consumes both item.mu and meta.mu.
func (p *Provider) finalizeReclaimedSmallLocked(item *cacheEntry, meta *smallPageMeta) {
	pageID := item.startPage
	classID := item.classID
	pageGeneration := item.generation
	stale := p.discardSmallPageLocked(meta, pageID)
	item.freed = true
	item.state = entryDead

	p.allocatorMu.Lock()
	p.stats.entries.Add(-1)
	p.stats.logicalBytes.Add(-int64(item.length))
	p.allocatorMu.Unlock()
	meta.mu.Unlock()
	item.mu.Unlock()

	p.releaseDiscardedSmallPage(
		classID,
		smallPageRef{pageID: pageID, generation: pageGeneration, meta: meta},
		stale,
	)
}

func (p *Provider) discardSmallPageLocked(
	meta *smallPageMeta,
	pageID uint32,
) []*cacheEntry {
	stale := meta.entries
	layout := p.layouts[meta.classID]
	meta.state = smallPageDead
	meta.refs = 0
	meta.used = 0
	meta.entries = nil

	p.discardSmallSlabLocked(pageID, layout)

	return stale
}

func (p *Provider) releaseDiscardedSmallPage(
	classID uint16,
	reference smallPageRef,
	stale []*cacheEntry,
) {
	p.cleanupStaleSmall(stale)
	p.removeSmallPage(classID, reference)
	layout := p.layouts[classID]
	p.allocatorMu.Lock()
	p.allocator.release(reference.pageID, layout.pageCount)
	p.stats.reservedBytes.Add(-int64(layout.slabBytes()))
	p.allocatorMu.Unlock()
}

func (p *Provider) cleanupStaleSmall(stale []*cacheEntry) {
	for _, item := range stale {
		if item == nil {
			continue
		}
		p.removeIfSame(item.key, item)
		p.finalizeStaleSmall(item)
	}
}

func (p *Provider) finalizeStaleSmall(item *cacheEntry) {
	item.mu.Lock()
	if item.freed {
		item.mu.Unlock()

		return
	}
	item.freed = true
	item.state = entryDead

	p.allocatorMu.Lock()
	p.stats.entries.Add(-1)
	p.stats.logicalBytes.Add(-int64(item.length))
	p.allocatorMu.Unlock()
	item.mu.Unlock()
}

func (p *Provider) removeSmallPage(
	classID uint16,
	expected smallPageRef,
) {
	p.smallMu.Lock()
	pages := p.classPages[classID]
	for index, reference := range pages {
		if reference.pageID == expected.pageID &&
			reference.generation == expected.generation &&
			reference.meta == expected.meta {
			p.classPages[classID] = append(pages[:index], pages[index+1:]...)

			break
		}
	}
	if p.smallPages[expected.pageID] == expected.meta {
		delete(p.smallPages, expected.pageID)
	}
	p.smallMu.Unlock()
}

func (p *Provider) markIdleSmallSlabLocked(pageID uint32, layout smallPageLayout) {
	p.markIdle(p.smallSlab(pageID, layout))
}

func (p *Provider) discardSmallSlabLocked(pageID uint32, layout smallPageLayout) {
	p.discard(p.smallSlab(pageID, layout))
}

func (p *Provider) smallSlab(pageID uint32, layout smallPageLayout) []byte {
	return p.extentBytes(pageID, layout.pageCount)
}

func (p *Provider) smallPageMetadata(pageID uint32) *smallPageMeta {
	p.smallMu.Lock()
	meta := p.smallPages[pageID]
	if meta == nil {
		meta = &smallPageMeta{}
		p.smallPages[pageID] = meta
	}
	p.smallMu.Unlock()

	return meta
}

func (p *Provider) smallSlabPagesWereReclaimed(
	pageID uint32,
	layout smallPageLayout,
	invalidPages uint32,
) bool {
	for pageIndex := uint32(0); pageIndex < layout.pageCount; pageIndex++ {
		if invalidPages&(uint32(1)<<pageIndex) == 0 {
			continue
		}
		page := p.page(pageID + pageIndex)
		if binary.LittleEndian.Uint64(page[generationOffset:pageIndexOffset]) == 0 {
			return true
		}
	}

	return false
}

// repairSmallSlabPagesLocked rebuilds reclaim-detection headers and slot
// metadata from the Go-owned slab metadata. Entries are invalidated only when
// their payload overlaps a reclaimed or otherwise invalid physical page.
//
// The caller must hold meta.mu and must have established meta.refs == 0.
//
//nolint:cyclop // Reclaim repair invalidates entries and rebuilds headers in one pass.
func (p *Provider) repairSmallSlabPagesLocked(
	meta *smallPageMeta,
	pageID uint32,
	layout smallPageLayout,
	invalidPages uint32,
) []*cacheEntry {
	if meta.refs != 0 {
		panic("madvfree: attempted to repair a referenced small slab")
	}

	stale := make([]*cacheEntry, 0)
	for slot, item := range meta.entries {
		if item == nil {
			continue
		}
		payloadPages, valid := layout.slotPayloadPageMask(slot, item.length)
		if !valid {
			panic("madvfree: invalid small slot while repairing slab")
		}
		if payloadPages&invalidPages == 0 {
			continue
		}
		if meta.used <= 0 {
			panic("madvfree: small used count underflow while repairing slab")
		}
		meta.entries[slot] = nil
		meta.used--
		stale = append(stale, item)
	}

	for pageIndex := uint32(0); pageIndex < layout.pageCount; pageIndex++ {
		if invalidPages&(uint32(1)<<pageIndex) == 0 {
			continue
		}
		page := p.page(pageID + pageIndex)
		clear(page)
		binary.LittleEndian.PutUint64(page[generationOffset:pageIndexOffset], meta.generation)
		binary.LittleEndian.PutUint32(page[pageIndexOffset:touchOffset], pageIndex)
	}

	slab := p.smallSlab(pageID, layout)
	for offset := 0; offset < layout.payloadOffset; offset++ {
		if !layout.setLogicalByte(slab, offset, 0) {
			panic("madvfree: failed to clear small slab metadata during repair")
		}
	}
	for slot, item := range meta.entries {
		if item == nil {
			continue
		}
		if !layout.setSlotGeneration(slab, slot, item.slotGeneration) ||
			!layout.setSlotLength(slab, slot, uint32(item.length)) ||
			!layout.setSlotAllocated(slab, slot, true) {
			panic("madvfree: failed to rebuild small slab metadata")
		}
	}

	return stale
}
