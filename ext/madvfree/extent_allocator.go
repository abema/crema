//go:build (linux && !386 && !arm && !mips && !mipsle) || darwin

package madvfree

type extent struct {
	start uint32
	count uint32
}

type extentAllocator struct {
	free []extent
}

func newExtentAllocator(pageCount uint32) extentAllocator {
	return extentAllocator{free: []extent{{count: pageCount}}}
}

func (a *extentAllocator) allocate(count uint32) (uint32, bool) {
	for i := range a.free {
		current := &a.free[i]
		if current.count < count {
			continue
		}

		start := current.start
		current.start += count
		current.count -= count
		if current.count == 0 {
			a.free = append(a.free[:i], a.free[i+1:]...)
		}

		return start, true
	}

	return 0, false
}

//nolint:cyclop // Overlap validation and adjacent coalescing belong to one atomic mutation.
func (a *extentAllocator) release(start, count uint32) {
	if count == 0 {
		return
	}
	end := start + count
	if end < start {
		panic("madvfree: released extent overflows page index")
	}

	at := 0
	for at < len(a.free) && a.free[at].start < start {
		at++
	}
	if at > 0 {
		previous := a.free[at-1]
		if previous.start+previous.count > start {
			panic("madvfree: released extent overlaps previous free extent")
		}
	}
	if at < len(a.free) && end > a.free[at].start {
		panic("madvfree: released extent overlaps next free extent")
	}
	a.free = append(a.free, extent{})
	copy(a.free[at+1:], a.free[at:])
	a.free[at] = extent{start: start, count: count}

	if at > 0 {
		previous := &a.free[at-1]
		if previous.start+previous.count == a.free[at].start {
			previous.count += a.free[at].count
			a.free = append(a.free[:at], a.free[at+1:]...)
			at--
		}
	}
	if at+1 < len(a.free) {
		current := &a.free[at]
		next := a.free[at+1]
		if current.start+current.count == next.start {
			current.count += next.count
			a.free = append(a.free[:at+1], a.free[at+2:]...)
		}
	}
}

func (a *extentAllocator) freePages() uint32 {
	var pages uint32
	for _, item := range a.free {
		pages += item.count
	}

	return pages
}
