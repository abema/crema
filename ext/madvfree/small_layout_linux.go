//go:build linux && !386 && !arm && !mips && !mipsle

package madvfree

import (
	"encoding/binary"
	"fmt"
)

const (
	smallSlotGenerationSize = 8
	smallSlotLengthSize     = 4
	smallBitmapBitsPerByte  = 8
	smallSearchDivisor      = 2
	maxSmallClassSize       = 32 << 10
	maxSmallSlabPages       = 32
	maxSmallSlotCount       = 1<<16 - 1
)

var defaultSmallSizeClasses = [...]int{
	64,
	96,
	128,
	192,
	256,
	384,
	512,
	768,
	1024,
	1536,
	2048,
	3072,
	4096,
	6144,
	8192,
	12288,
	16384,
	24576,
	32768,
}

// smallPageLayout describes a fixed-size slot slab. pageCount is one for
// sub-page classes and grows only when sharing pages saves space over extents.
//
// Every physical page reserves pageHeaderSize bytes for reclaim detection.
// Metadata and payloads use a logical address space formed by concatenating
// the remaining bytes from every page.
type smallPageLayout struct {
	pageSize          int
	pageCount         uint32
	payloadPerPage    int
	classSize         int
	slotCount         int
	bitmapOffset      int
	bitmapBytes       int
	generationsOffset int
	lengthsOffset     int
	payloadOffset     int
	usedBytes         int
}

func makeSmallPageLayouts(pageSize int, sizeClasses []int) ([]smallPageLayout, error) {
	if sizeClasses == nil {
		sizeClasses = defaultSmallSizeClasses[:]
	}

	layouts := make([]smallPageLayout, len(sizeClasses))
	previous := 0
	for index, classSize := range sizeClasses {
		if classSize <= previous {
			return nil, fmt.Errorf("%w: size classes must be positive and strictly increasing", ErrInvalidConfig)
		}
		if classSize > maxSmallClassSize {
			return nil, fmt.Errorf(
				"%w: size class %d exceeds maximum %d",
				ErrInvalidConfig,
				classSize,
				maxSmallClassSize,
			)
		}

		layout, ok := newSmallClassLayout(pageSize, classSize)
		if !ok {
			return nil, fmt.Errorf("%w: size class %d cannot form an efficient slab", ErrInvalidConfig, classSize)
		}
		layouts[index] = layout
		previous = classSize
	}

	return layouts, nil
}

func newSmallClassLayout(pageSize, classSize int) (smallPageLayout, bool) {
	onePage, ok := newSmallPageLayout(pageSize, classSize)
	if ok {
		return onePage, true
	}
	if pageSize <= pageHeaderSize || classSize <= 0 || classSize > maxSmallClassSize {
		return smallPageLayout{}, false
	}

	payloadPerPage := pageSize - pageHeaderSize
	extentPages := 1 + (classSize-1)/payloadPerPage
	extentBytes := extentPages * pageSize
	for pageCount := 2; pageCount <= maxSmallSlabPages; pageCount++ {
		layout, fits := newSmallSlabLayout(pageSize, classSize, uint32(pageCount))
		if !fits || layout.slotCount < 2 {
			continue
		}
		if uint64(pageCount)*uint64(pageSize) <
			uint64(extentBytes)*uint64(layout.slotCount) {
			return layout, true
		}
	}

	return smallPageLayout{}, false
}

func newSmallPageLayout(pageSize, classSize int) (smallPageLayout, bool) {
	return newSmallSlabLayout(pageSize, classSize, 1)
}

func newSmallSlabLayout(pageSize, classSize int, pageCount uint32) (smallPageLayout, bool) {
	if !validSmallSlabParameters(pageSize, classSize, pageCount) {
		return smallPageLayout{}, false
	}

	payloadPerPage := pageSize - pageHeaderSize
	available := int(pageCount) * payloadPerPage
	perSlot := classSize + smallSlotGenerationSize + smallSlotLengthSize
	if perSlot <= 0 {
		return smallPageLayout{}, false
	}

	upper := available / perSlot
	low, high := 0, upper
	for low < high {
		middle := low + (high-low+1)/smallSearchDivisor
		if smallSlotsFit(available, perSlot, middle) {
			low = middle
		} else {
			high = middle - 1
		}
	}
	if low == 0 || low > maxSmallSlotCount {
		return smallPageLayout{}, false
	}

	slotCount := low
	bitmapBytes := (slotCount + smallBitmapBitsPerByte - 1) / smallBitmapBitsPerByte
	generationsOffset := bitmapBytes
	lengthsOffset := generationsOffset + slotCount*smallSlotGenerationSize
	payloadOffset := lengthsOffset + slotCount*smallSlotLengthSize
	usedBytes := payloadOffset + slotCount*classSize

	return smallPageLayout{
		pageSize:          pageSize,
		pageCount:         pageCount,
		payloadPerPage:    payloadPerPage,
		classSize:         classSize,
		slotCount:         slotCount,
		bitmapOffset:      0,
		bitmapBytes:       bitmapBytes,
		generationsOffset: generationsOffset,
		lengthsOffset:     lengthsOffset,
		payloadOffset:     payloadOffset,
		usedBytes:         usedBytes,
	}, true
}

func validSmallSlabParameters(pageSize, classSize int, pageCount uint32) bool {
	return pageSize > pageHeaderSize &&
		classSize > 0 &&
		uint64(classSize) <= uint64(^uint32(0)) &&
		pageCount > 0 &&
		uint64(pageCount) <= uint64(int(^uint(0)>>1)/pageSize)
}

func smallSlotsFit(available, perSlot, slotCount int) bool {
	if available < 0 || perSlot <= 0 || slotCount < 0 {
		return false
	}
	if slotCount > available/perSlot {
		return false
	}

	metadataAndPayload := slotCount * perSlot
	bitmapBytes := (slotCount + smallBitmapBitsPerByte - 1) / smallBitmapBitsPerByte

	return bitmapBytes <= available-metadataAndPayload
}

func (l smallPageLayout) slabBytes() int {
	return int(l.pageCount) * l.pageSize
}

func (l smallPageLayout) logicalCapacity() int {
	return int(l.pageCount) * l.payloadPerPage
}

func (l smallPageLayout) allPageMask() uint32 {
	if l.pageCount >= uint32(maxSmallSlabPages) {
		return ^uint32(0)
	}

	return (uint32(1) << l.pageCount) - 1
}

func (l smallPageLayout) slotPayloadPageMask(slot, length int) (uint32, bool) {
	if slot < 0 || slot >= l.slotCount || length < 0 || length > l.classSize {
		return 0, false
	}

	return l.logicalRangePageMask(l.payloadOffset+slot*l.classSize, length), true
}

func (l smallPageLayout) logicalRangePageMask(offset, length int) uint32 {
	if length <= 0 {
		return 0
	}

	firstPage := offset / l.payloadPerPage
	lastPage := (offset + length - 1) / l.payloadPerPage
	var mask uint32
	for pageIndex := firstPage; pageIndex <= lastPage; pageIndex++ {
		mask |= uint32(1) << uint(pageIndex)
	}

	return mask
}

func (l smallPageLayout) slotAllocated(slab []byte, slot int) (bool, bool) {
	if !l.validSlot(slab, slot) {
		return false, false
	}

	value, ok := l.logicalByte(slab, l.bitmapOffset+slot/smallBitmapBitsPerByte)
	if !ok {
		return false, false
	}
	mask := byte(1 << uint(slot%smallBitmapBitsPerByte))

	return value&mask != 0, true
}

func (l smallPageLayout) setSlotAllocated(slab []byte, slot int, allocated bool) bool {
	if !l.validSlot(slab, slot) {
		return false
	}

	offset := l.bitmapOffset + slot/smallBitmapBitsPerByte
	value, ok := l.logicalByte(slab, offset)
	if !ok {
		return false
	}
	mask := byte(1 << uint(slot%smallBitmapBitsPerByte))
	if allocated {
		value |= mask
	} else {
		value &^= mask
	}

	return l.setLogicalByte(slab, offset, value)
}

func (l smallPageLayout) slotGeneration(slab []byte, slot int) (uint64, bool) {
	if !l.validSlot(slab, slot) {
		return 0, false
	}

	var encoded [smallSlotGenerationSize]byte
	if !l.copyLogicalOut(encoded[:], slab, l.generationsOffset+slot*smallSlotGenerationSize) {
		return 0, false
	}

	return binary.LittleEndian.Uint64(encoded[:]), true
}

func (l smallPageLayout) setSlotGeneration(slab []byte, slot int, generation uint64) bool {
	if !l.validSlot(slab, slot) {
		return false
	}

	var encoded [smallSlotGenerationSize]byte
	binary.LittleEndian.PutUint64(encoded[:], generation)

	return l.copyLogicalIn(slab, l.generationsOffset+slot*smallSlotGenerationSize, encoded[:])
}

func (l smallPageLayout) slotLength(slab []byte, slot int) (uint32, bool) {
	if !l.validSlot(slab, slot) {
		return 0, false
	}

	var encoded [smallSlotLengthSize]byte
	if !l.copyLogicalOut(encoded[:], slab, l.lengthsOffset+slot*smallSlotLengthSize) {
		return 0, false
	}

	return binary.LittleEndian.Uint32(encoded[:]), true
}

func (l smallPageLayout) setSlotLength(slab []byte, slot int, length uint32) bool {
	if !l.validSlot(slab, slot) || uint64(length) > uint64(l.classSize) {
		return false
	}

	var encoded [smallSlotLengthSize]byte
	binary.LittleEndian.PutUint32(encoded[:], length)

	return l.copyLogicalIn(slab, l.lengthsOffset+slot*smallSlotLengthSize, encoded[:])
}

func (l smallPageLayout) copySlotIn(slab []byte, slot int, value []byte) bool {
	if !l.validSlot(slab, slot) || len(value) > l.classSize {
		return false
	}

	return l.copyLogicalIn(slab, l.payloadOffset+slot*l.classSize, value)
}

func (l smallPageLayout) copySlotOut(destination, slab []byte, slot int) bool {
	if !l.validSlot(slab, slot) || len(destination) > l.classSize {
		return false
	}

	return l.copyLogicalOut(destination, slab, l.payloadOffset+slot*l.classSize)
}

func (l smallPageLayout) clearSlotMetadata(slab []byte, slot int) bool {
	if !l.setSlotAllocated(slab, slot, false) {
		return false
	}
	if !l.setSlotGeneration(slab, slot, 0) {
		return false
	}

	return l.setSlotLength(slab, slot, 0)
}

func (l smallPageLayout) validSlot(slab []byte, slot int) bool {
	return slot >= 0 &&
		slot < l.slotCount &&
		l.pageSize > pageHeaderSize &&
		l.pageCount > 0 &&
		l.payloadPerPage == l.pageSize-pageHeaderSize &&
		len(slab) >= l.slabBytes() &&
		l.usedBytes <= l.logicalCapacity()
}

func (l smallPageLayout) logicalByte(slab []byte, logicalOffset int) (byte, bool) {
	physicalOffset, ok := l.physicalOffset(slab, logicalOffset)
	if !ok {
		return 0, false
	}

	return slab[physicalOffset], true
}

func (l smallPageLayout) setLogicalByte(slab []byte, logicalOffset int, value byte) bool {
	physicalOffset, ok := l.physicalOffset(slab, logicalOffset)
	if !ok {
		return false
	}
	slab[physicalOffset] = value

	return true
}

func (l smallPageLayout) physicalOffset(slab []byte, logicalOffset int) (int, bool) {
	if logicalOffset < 0 || logicalOffset >= l.logicalCapacity() || len(slab) < l.slabBytes() {
		return 0, false
	}
	pageIndex := logicalOffset / l.payloadPerPage
	offsetInPayload := logicalOffset % l.payloadPerPage
	physicalOffset := pageIndex*l.pageSize + pageHeaderSize + offsetInPayload

	return physicalOffset, true
}

func (l smallPageLayout) copyLogicalIn(slab []byte, logicalOffset int, source []byte) bool {
	if logicalOffset < 0 ||
		len(source) > l.logicalCapacity()-logicalOffset ||
		len(slab) < l.slabBytes() {
		return false
	}

	remaining := source
	for len(remaining) > 0 {
		physicalOffset, ok := l.physicalOffset(slab, logicalOffset)
		if !ok {
			return false
		}
		available := l.payloadPerPage - logicalOffset%l.payloadPerPage
		written := min(len(remaining), available)
		copy(slab[physicalOffset:physicalOffset+written], remaining[:written])
		logicalOffset += written
		remaining = remaining[written:]
	}

	return true
}

func (l smallPageLayout) copyLogicalOut(destination, slab []byte, logicalOffset int) bool {
	if logicalOffset < 0 ||
		len(destination) > l.logicalCapacity()-logicalOffset ||
		len(slab) < l.slabBytes() {
		return false
	}

	remaining := destination
	for len(remaining) > 0 {
		physicalOffset, ok := l.physicalOffset(slab, logicalOffset)
		if !ok {
			return false
		}
		available := l.payloadPerPage - logicalOffset%l.payloadPerPage
		copied := min(len(remaining), available)
		copy(remaining[:copied], slab[physicalOffset:physicalOffset+copied])
		logicalOffset += copied
		remaining = remaining[copied:]
	}

	return true
}
