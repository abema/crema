//go:build linux && !386 && !arm && !mips && !mipsle

package madvfree

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"testing"
)

func TestMakeSmallPageLayouts(t *testing.T) {
	t.Parallel()

	defaults, err := makeSmallPageLayouts(4096, nil)
	if err != nil {
		t.Fatalf("make default layouts: %v", err)
	}
	if len(defaults) != len(defaultSmallSizeClasses) {
		t.Fatalf("default layout count=%d, want %d", len(defaults), len(defaultSmallSizeClasses))
	}
	for index, layout := range defaults {
		if layout.classSize != defaultSmallSizeClasses[index] {
			t.Fatalf("layout %d class=%d, want %d", index, layout.classSize, defaultSmallSizeClasses[index])
		}
	}
	medium := defaults[13] // 6 KiB
	if medium.classSize != 6144 || medium.pageCount <= 1 || medium.slotCount < 2 {
		t.Fatalf("6 KiB layout is not a packed multi-page slab: %#v", medium)
	}

	disabled, err := makeSmallPageLayouts(4096, []int{})
	if err != nil {
		t.Fatalf("empty layouts: %v", err)
	}
	if len(disabled) != 0 {
		t.Fatalf("empty layout count=%d, want 0", len(disabled))
	}

	for _, classes := range [][]int{
		{0},
		{-1},
		{64, 64},
		{128, 64},
		{64, maxSmallClassSize + 1},
	} {
		if _, err := makeSmallPageLayouts(4096, classes); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("classes %v error=%v, want ErrInvalidConfig", classes, err)
		}
	}
}

func TestSmallPageLayout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		pageSize  int
		sizeClass int
		wantOK    bool
	}{
		{name: "64 byte class", pageSize: 4096, sizeClass: 64, wantOK: true},
		{name: "one slot exactly", pageSize: 30, sizeClass: 1, wantOK: true},
		{name: "header only", pageSize: pageHeaderSize, sizeClass: 1, wantOK: false},
		{name: "slot cannot fit", pageSize: 29, sizeClass: 1, wantOK: false},
		{name: "zero class", pageSize: 4096, sizeClass: 0, wantOK: false},
		{name: "negative class", pageSize: 4096, sizeClass: -1, wantOK: false},
		{name: "class exceeds length field", pageSize: math.MaxInt, sizeClass: int(uint64(math.MaxUint32) + 1), wantOK: false},
		{name: "overflowing class", pageSize: 4096, sizeClass: math.MaxInt, wantOK: false},
		{name: "slot count exceeds entry field", pageSize: 1 << 20, sizeClass: 1, wantOK: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			layout, ok := newSmallPageLayout(test.pageSize, test.sizeClass)
			if ok != test.wantOK {
				t.Fatalf("newSmallPageLayout(%d, %d) ok=%v, want %v", test.pageSize, test.sizeClass, ok, test.wantOK)
			}
			if !ok {
				return
			}

			assertSmallLayoutInvariants(t, layout)
		})
	}
}

func TestSmallPageLayoutMaximizesSlots(t *testing.T) {
	t.Parallel()

	for _, sizeClass := range []int{1, 8, 16, 64, 256, 1024, 2048, 4067} {
		layout, ok := newSmallPageLayout(4096, sizeClass)
		if !ok {
			t.Fatalf("size class %d did not fit", sizeClass)
		}
		assertSmallLayoutInvariants(t, layout)

		available := layout.pageSize - pageHeaderSize
		perSlot := layout.classSize + smallSlotGenerationSize + smallSlotLengthSize
		if smallSlotsFit(available, perSlot, layout.slotCount+1) {
			t.Fatalf("size class %d left room for another slot: %#v", sizeClass, layout)
		}
	}
}

func TestSmallPageMetadataRoundTripAndIsolation(t *testing.T) {
	t.Parallel()

	layout, ok := newSmallPageLayout(4096, 64)
	if !ok {
		t.Fatal("layout did not fit")
	}
	page := make([]byte, layout.slabBytes())
	slot := layout.slotCount / 2

	if !layout.setSlotAllocated(page, slot, true) {
		t.Fatal("set allocated failed")
	}
	if !layout.setSlotGeneration(page, slot, 0x0102030405060708) {
		t.Fatal("set generation failed")
	}
	if !layout.setSlotLength(page, slot, 63) {
		t.Fatal("set length failed")
	}
	wantPayload := bytes.Repeat([]byte{0x3c}, layout.classSize)
	if !layout.copySlotIn(page, slot, wantPayload) {
		t.Fatal("payload write failed")
	}
	gotPayload := make([]byte, layout.classSize)
	if !layout.copySlotOut(gotPayload, page, slot) || !bytes.Equal(gotPayload, wantPayload) {
		t.Fatal("payload round trip failed")
	}

	allocated, valid := layout.slotAllocated(page, slot)
	if !valid || !allocated {
		t.Fatalf("allocated=(%v, %v), want (true, true)", allocated, valid)
	}
	generation, valid := layout.slotGeneration(page, slot)
	if !valid || generation != 0x0102030405060708 {
		t.Fatalf("generation=(%x, %v)", generation, valid)
	}
	length, valid := layout.slotLength(page, slot)
	if !valid || length != 63 {
		t.Fatalf("length=(%d, %v)", length, valid)
	}

	generationOffset := layout.generationsOffset + slot*smallSlotGenerationSize
	var generationBytes [smallSlotGenerationSize]byte
	if !layout.copyLogicalOut(generationBytes[:], page, generationOffset) {
		t.Fatal("generation byte lookup failed")
	}
	if got := generationBytes[:]; !bytes.Equal(got, []byte{8, 7, 6, 5, 4, 3, 2, 1}) {
		t.Fatalf("generation bytes=%x, want little endian", got)
	}
	lengthOffset := layout.lengthsOffset + slot*smallSlotLengthSize
	var lengthBytes [smallSlotLengthSize]byte
	if !layout.copyLogicalOut(lengthBytes[:], page, lengthOffset) {
		t.Fatal("length byte lookup failed")
	}
	if got := binary.LittleEndian.Uint32(lengthBytes[:]); got != 63 {
		t.Fatalf("encoded length=%d, want 63", got)
	}

	neighbor := slot + 1
	if neighbor < layout.slotCount {
		allocated, valid = layout.slotAllocated(page, neighbor)
		if !valid || !allocated {
			// The page started filled with 0xa5, whose selected bitmap bit may be
			// zero. Verify only that clearing the target did not mutate it.
			before, _ := layout.logicalByte(page, layout.bitmapOffset+neighbor/8)
			if !layout.clearSlotMetadata(page, slot) {
				t.Fatal("clear metadata failed")
			}
			after, _ := layout.logicalByte(page, layout.bitmapOffset+neighbor/8)
			mask := byte(1 << uint(neighbor%8))
			if before&mask != after&mask {
				t.Fatal("clearing slot changed neighboring allocation bit")
			}
		}
	}

	if !layout.clearSlotMetadata(page, slot) {
		t.Fatal("clear metadata failed")
	}
	allocated, valid = layout.slotAllocated(page, slot)
	if !valid || allocated {
		t.Fatalf("allocated after clear=(%v, %v)", allocated, valid)
	}
	generation, valid = layout.slotGeneration(page, slot)
	if !valid || generation != 0 {
		t.Fatalf("generation after clear=(%d, %v)", generation, valid)
	}
	length, valid = layout.slotLength(page, slot)
	if !valid || length != 0 {
		t.Fatalf("length after clear=(%d, %v)", length, valid)
	}
}

func TestSmallPageMetadataRejectsInvalidBounds(t *testing.T) {
	t.Parallel()

	layout, ok := newSmallPageLayout(4096, 64)
	if !ok {
		t.Fatal("layout did not fit")
	}
	shortPage := make([]byte, layout.slabBytes()-1)
	page := make([]byte, layout.slabBytes())

	for _, slot := range []int{-1, layout.slotCount} {
		if _, valid := layout.slotAllocated(page, slot); valid {
			t.Fatalf("slotAllocated accepted slot %d", slot)
		}
		if layout.setSlotAllocated(page, slot, true) {
			t.Fatalf("setSlotAllocated accepted slot %d", slot)
		}
		if _, valid := layout.slotGeneration(page, slot); valid {
			t.Fatalf("slotGeneration accepted slot %d", slot)
		}
		if layout.setSlotGeneration(page, slot, 1) {
			t.Fatalf("setSlotGeneration accepted slot %d", slot)
		}
		if _, valid := layout.slotLength(page, slot); valid {
			t.Fatalf("slotLength accepted slot %d", slot)
		}
		if layout.setSlotLength(page, slot, 1) {
			t.Fatalf("setSlotLength accepted slot %d", slot)
		}
		if layout.copySlotOut(make([]byte, 1), page, slot) {
			t.Fatalf("copySlotOut accepted slot %d", slot)
		}
	}

	if layout.setSlotLength(page, 0, uint32(layout.classSize+1)) {
		t.Fatal("setSlotLength accepted oversized payload")
	}
	if layout.setSlotAllocated(shortPage, 0, true) {
		t.Fatal("setSlotAllocated accepted short page")
	}
	if layout.copySlotOut(make([]byte, 1), shortPage, 0) {
		t.Fatal("copySlotOut accepted short page")
	}
}

func TestMultiPageSlabMetadataPreservesPageHeaders(t *testing.T) {
	t.Parallel()

	layout, ok := newSmallClassLayout(4096, 6<<10)
	if !ok || layout.pageCount <= 1 {
		t.Fatalf("6 KiB layout = (%#v, %v)", layout, ok)
	}
	slab := make([]byte, layout.slabBytes())
	for pageIndex := uint32(0); pageIndex < layout.pageCount; pageIndex++ {
		header := slab[int(pageIndex)*layout.pageSize : int(pageIndex)*layout.pageSize+pageHeaderSize]
		clear(header)
		for index := range header {
			header[index] = byte(pageIndex + 1)
		}
	}

	value := bytes.Repeat([]byte{0x3c}, layout.classSize)
	for slot := 0; slot < layout.slotCount; slot++ {
		if !layout.setSlotAllocated(slab, slot, true) ||
			!layout.setSlotGeneration(slab, slot, uint64(slot+1)) ||
			!layout.setSlotLength(slab, slot, uint32(len(value))) ||
			!layout.copySlotIn(slab, slot, value) {
			t.Fatalf("metadata write failed for slot %d", slot)
		}
	}

	for pageIndex := uint32(0); pageIndex < layout.pageCount; pageIndex++ {
		header := slab[int(pageIndex)*layout.pageSize : int(pageIndex)*layout.pageSize+pageHeaderSize]
		for _, value := range header {
			if value != byte(pageIndex+1) {
				t.Fatalf("page %d header changed: %x", pageIndex, header)
			}
		}
	}
}

func TestMultiPageSlabPayloadPageMasks(t *testing.T) {
	t.Parallel()

	layout, ok := newSmallClassLayout(4096, 6<<10)
	if !ok || layout.pageCount <= 1 || layout.slotCount < 2 {
		t.Fatalf("6 KiB layout = (%#v, %v)", layout, ok)
	}
	if got, want := layout.allPageMask(), (uint32(1)<<layout.pageCount)-1; got != want {
		t.Fatalf("allPageMask()=%#x, want %#x", got, want)
	}

	union := uint32(0)
	for slot := 0; slot < layout.slotCount; slot++ {
		mask, valid := layout.slotPayloadPageMask(slot, layout.classSize)
		if !valid || mask == 0 || mask&^layout.allPageMask() != 0 {
			t.Fatalf("slot %d payload mask=(%#x, %v)", slot, mask, valid)
		}
		union |= mask
	}
	if union != layout.allPageMask() {
		t.Fatalf("payload page union=%#x, want %#x", union, layout.allPageMask())
	}

	if mask, valid := layout.slotPayloadPageMask(-1, 1); valid || mask != 0 {
		t.Fatalf("invalid slot mask=(%#x, %v)", mask, valid)
	}
	if mask, valid := layout.slotPayloadPageMask(0, layout.classSize+1); valid || mask != 0 {
		t.Fatalf("oversized payload mask=(%#x, %v)", mask, valid)
	}
}

func FuzzSmallPageLayout(f *testing.F) {
	f.Add(uint32(4096), uint32(64))
	f.Add(uint32(30), uint32(1))
	f.Add(uint32(4096), uint32(4068))
	f.Add(^uint32(0), uint32(1))

	f.Fuzz(func(t *testing.T, pageSizeInput, sizeClassInput uint32) {
		pageSize := int(pageSizeInput)
		sizeClass := int(sizeClassInput)
		layout, ok := newSmallPageLayout(pageSize, sizeClass)
		if !ok {
			return
		}

		assertSmallLayoutInvariants(t, layout)
		available := layout.pageSize - pageHeaderSize
		perSlot := layout.classSize + smallSlotGenerationSize + smallSlotLengthSize
		if smallSlotsFit(available, perSlot, layout.slotCount+1) {
			t.Fatalf("layout was not maximal: %#v", layout)
		}
	})
}

func FuzzSmallPageMetadata(f *testing.F) {
	f.Add(uint16(4096), uint16(64), uint16(0), uint64(1), uint32(32), true)
	f.Add(uint16(4096), uint16(256), uint16(15), ^uint64(0), uint32(256), false)

	f.Fuzz(func(
		t *testing.T,
		pageSizeInput uint16,
		sizeClassInput uint16,
		slotInput uint16,
		generation uint64,
		length uint32,
		allocated bool,
	) {
		pageSize := int(pageSizeInput)
		sizeClass := int(sizeClassInput)
		layout, ok := newSmallPageLayout(pageSize, sizeClass)
		if !ok {
			return
		}

		page := make([]byte, layout.slabBytes())
		slot := int(slotInput) % layout.slotCount
		if !layout.setSlotAllocated(page, slot, allocated) {
			t.Fatal("setSlotAllocated rejected valid slot")
		}
		if !layout.setSlotGeneration(page, slot, generation) {
			t.Fatal("setSlotGeneration rejected valid slot")
		}

		wantLengthAccepted := uint64(length) <= uint64(layout.classSize)
		if got := layout.setSlotLength(page, slot, length); got != wantLengthAccepted {
			t.Fatalf("setSlotLength acceptance=%v, want %v", got, wantLengthAccepted)
		}

		gotAllocated, valid := layout.slotAllocated(page, slot)
		if !valid || gotAllocated != allocated {
			t.Fatalf("allocated=(%v, %v), want (%v, true)", gotAllocated, valid, allocated)
		}
		gotGeneration, valid := layout.slotGeneration(page, slot)
		if !valid || gotGeneration != generation {
			t.Fatalf("generation=(%d, %v), want (%d, true)", gotGeneration, valid, generation)
		}
		if wantLengthAccepted {
			gotLength, valid := layout.slotLength(page, slot)
			if !valid || gotLength != length {
				t.Fatalf("length=(%d, %v), want (%d, true)", gotLength, valid, length)
			}
		}
	})
}

func FuzzSmallSlabMetadata(f *testing.F) {
	f.Add(uint16(6144), uint8(5), uint16(0), uint32(6144))
	f.Add(uint16(32768), uint8(17), uint16(1), uint32(32768))

	f.Fuzz(func(
		t *testing.T,
		sizeClassInput uint16,
		pageCountInput uint8,
		slotInput uint16,
		length uint32,
	) {
		pageCount := uint32(pageCountInput%maxSmallSlabPages + 1)
		layout, ok := newSmallSlabLayout(4096, int(sizeClassInput), pageCount)
		if !ok {
			return
		}
		slab := make([]byte, layout.slabBytes())
		slot := int(slotInput) % layout.slotCount
		valueLength := min(int(length), layout.classSize)
		value := bytes.Repeat([]byte{byte(length)}, valueLength)
		if !layout.copySlotIn(slab, slot, value) {
			t.Fatal("copySlotIn rejected valid value")
		}
		got := make([]byte, valueLength)
		if !layout.copySlotOut(got, slab, slot) || !bytes.Equal(got, value) {
			t.Fatal("multi-page payload round trip failed")
		}
	})
}

func assertSmallLayoutInvariants(t *testing.T, layout smallPageLayout) {
	t.Helper()

	if layout.slotCount <= 0 {
		t.Fatalf("slotCount=%d", layout.slotCount)
	}
	if layout.bitmapOffset != 0 {
		t.Fatalf("bitmapOffset=%d, want 0", layout.bitmapOffset)
	}
	if layout.bitmapBytes != (layout.slotCount+7)/8 {
		t.Fatalf("bitmapBytes=%d", layout.bitmapBytes)
	}
	if layout.generationsOffset != layout.bitmapOffset+layout.bitmapBytes {
		t.Fatalf("generationsOffset=%d", layout.generationsOffset)
	}
	if layout.lengthsOffset != layout.generationsOffset+layout.slotCount*smallSlotGenerationSize {
		t.Fatalf("lengthsOffset=%d", layout.lengthsOffset)
	}
	if layout.payloadOffset != layout.lengthsOffset+layout.slotCount*smallSlotLengthSize {
		t.Fatalf("payloadOffset=%d", layout.payloadOffset)
	}
	if layout.usedBytes != layout.payloadOffset+layout.slotCount*layout.classSize {
		t.Fatalf("usedBytes=%d", layout.usedBytes)
	}
	if layout.usedBytes > layout.logicalCapacity() {
		t.Fatalf("usedBytes=%d exceeds logicalCapacity=%d", layout.usedBytes, layout.logicalCapacity())
	}
	if layout.slabBytes() != int(layout.pageCount)*layout.pageSize {
		t.Fatalf("slabBytes=%d", layout.slabBytes())
	}
}
