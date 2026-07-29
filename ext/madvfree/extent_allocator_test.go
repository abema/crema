package madvfree

import "testing"

func TestExtentAllocatorSplitAndMerge(t *testing.T) {
	t.Parallel()

	allocator := newExtentAllocator(10)
	first, ok := allocator.allocate(3)
	if !ok || first != 0 {
		t.Fatalf("first allocation = (%d, %v), want (0, true)", first, ok)
	}
	second, ok := allocator.allocate(4)
	if !ok || second != 3 {
		t.Fatalf("second allocation = (%d, %v), want (3, true)", second, ok)
	}
	if _, ok := allocator.allocate(4); ok {
		t.Fatal("oversized allocation unexpectedly succeeded")
	}

	allocator.release(first, 3)
	allocator.release(second, 4)
	if got := allocator.freePages(); got != 10 {
		t.Fatalf("free pages = %d, want 10", got)
	}
	if len(allocator.free) != 1 || allocator.free[0] != (extent{count: 10}) {
		t.Fatalf("merged extents = %#v, want one ten-page extent", allocator.free)
	}
}

func TestExtentAllocatorFirstFit(t *testing.T) {
	t.Parallel()

	allocator := newExtentAllocator(12)
	a, _ := allocator.allocate(2)
	_, _ = allocator.allocate(3)
	c, _ := allocator.allocate(2)
	allocator.release(a, 2)
	allocator.release(c, 2)

	got, ok := allocator.allocate(2)
	if !ok || got != 0 {
		t.Fatalf("first-fit allocation = (%d, %v), want (0, true)", got, ok)
	}
}

func TestExtentAllocatorRejectsOverlappingRelease(t *testing.T) {
	t.Parallel()

	allocator := newExtentAllocator(4)
	start, ok := allocator.allocate(2)
	if !ok {
		t.Fatal("allocate failed")
	}
	allocator.release(start, 2)

	defer func() {
		if recover() == nil {
			t.Fatal("overlapping release did not panic")
		}
	}()
	allocator.release(start, 2)
}
