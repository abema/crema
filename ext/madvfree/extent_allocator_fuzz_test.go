package madvfree

import "testing"

func FuzzExtentAllocator(f *testing.F) {
	f.Add([]byte{0, 1, 0, 2, 1, 0, 0, 4})
	f.Add([]byte{0xff, 0, 3, 1, 7, 0, 2, 9})

	f.Fuzz(func(t *testing.T, operations []byte) {
		const pageCount = uint32(32)
		if len(operations) > 4096 {
			operations = operations[:4096]
		}

		allocator := newExtentAllocator(pageCount)
		allocated := make(map[uint32]uint32)
		for offset := 0; offset+1 < len(operations); offset += 2 {
			if operations[offset]&1 == 0 {
				count := uint32(operations[offset+1]%40) + 1
				start, ok := allocator.allocate(count)
				if ok {
					allocated[start] = count
				}
			} else if len(allocated) > 0 {
				selected := int(operations[offset+1]) % len(allocated)
				for start, count := range allocated {
					if selected == 0 {
						allocator.release(start, count)
						delete(allocated, start)

						break
					}
					selected--
				}
			}
			assertAllocatorPartition(t, &allocator, allocated, pageCount)
		}
	})
}

func assertAllocatorPartition(
	t *testing.T,
	allocator *extentAllocator,
	allocated map[uint32]uint32,
	pageCount uint32,
) {
	t.Helper()

	owners := make([]uint8, pageCount)
	mark := func(start, count uint32, owner uint8) {
		if count == 0 || start > pageCount || count > pageCount-start {
			t.Fatalf("invalid extent: start=%d count=%d pages=%d", start, count, pageCount)
		}
		for page := start; page < start+count; page++ {
			if owners[page] != 0 {
				t.Fatalf("page %d has overlapping owners", page)
			}
			owners[page] = owner
		}
	}

	for start, count := range allocated {
		mark(start, count, 1)
	}
	var previousEnd uint32
	for index, item := range allocator.free {
		if index > 0 && item.start <= previousEnd {
			t.Fatalf("free extents are unsorted or unmerged: %#v", allocator.free)
		}
		mark(item.start, item.count, 2)
		previousEnd = item.start + item.count
	}
	for page, owner := range owners {
		if owner == 0 {
			t.Fatalf("page %d is neither allocated nor free", page)
		}
	}
}
