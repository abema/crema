//go:build (linux && !386 && !arm && !mips && !mipsle) || darwin

package madvfree

import (
	"container/heap"
	"time"
)

type expiryRecord struct {
	expiresAt int64
	item      *cacheEntry
	index     int
}

type expiryHeap []*expiryRecord

func (h expiryHeap) Len() int           { return len(h) }
func (h expiryHeap) Less(i, j int) bool { return h[i].expiresAt < h[j].expiresAt }
func (h expiryHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *expiryHeap) Push(value any) {
	record := value.(*expiryRecord)
	record.index = len(*h)
	*h = append(*h, record)
}

func (h *expiryHeap) Pop() any {
	old := *h
	last := len(old) - 1
	value := old[last]
	old[last] = nil
	*h = old[:last]
	value.index = -1

	return value
}

// addExpiryLocked registers item while expiryMu is held.
func (p *Provider) addExpiryLocked(item *cacheEntry) bool {
	earliest := len(p.expirations) == 0 || item.expiresAt < p.expirations[0].expiresAt
	record := &expiryRecord{
		expiresAt: item.expiresAt,
		item:      item,
		index:     -1,
	}
	item.expiry = record
	heap.Push(&p.expirations, record)

	return earliest
}

// cancelExpiryLocked unregisters item while expiryMu is held.
func (p *Provider) cancelExpiryLocked(item *cacheEntry) bool {
	if item == nil || item.expiry == nil {
		return false
	}

	record := item.expiry
	item.expiry = nil
	if record.index < 0 {
		return false
	}
	earliest := record.index == 0
	heap.Remove(&p.expirations, record.index)

	return earliest
}

func (p *Provider) wakeExpiryLoop() {
	select {
	case p.expiryWake <- struct{}{}:
	default:
	}
}

func (p *Provider) expirationLoop() { //nolint:cyclop // Timer reset and shutdown paths are intentionally handled in one loop.
	defer close(p.expiryDone)
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	for {
		p.expiryMu.Lock()
		if len(p.expirations) == 0 {
			p.expiryMu.Unlock()
			select {
			case <-p.expiryWake:
				continue
			case <-p.expiryStop:
				return
			}
		}
		wait := time.Until(time.Unix(0, p.expirations[0].expiresAt))
		p.expiryMu.Unlock()
		if wait < 0 {
			wait = 0
		}
		timer.Reset(wait)

		select {
		case <-timer.C:
			p.expireReady()
		case <-p.expiryWake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-p.expiryStop:
			return
		}
	}
}

func (p *Provider) expireReady() {
	now := time.Now().UnixNano()
	for {
		p.expiryMu.Lock()
		if len(p.expirations) == 0 || p.expirations[0].expiresAt > now {
			p.expiryMu.Unlock()

			return
		}
		record := heap.Pop(&p.expirations).(*expiryRecord)
		if record.item.expiry == record {
			record.item.expiry = nil
		}
		p.expiryMu.Unlock()

		p.lifecycle.RLock()
		if p.closed {
			p.lifecycle.RUnlock()

			return
		}
		if p.removeIfSame(record.item.key, record.item) {
			p.stats.expiredMisses.Add(1)
			p.retire(record.item)
		}
		p.lifecycle.RUnlock()
	}
}

func (p *Provider) clearExpirations() {
	p.expiryMu.Lock()
	for _, record := range p.expirations {
		if record.item.expiry == record {
			record.item.expiry = nil
		}
		record.index = -1
	}
	clear(p.expirations)
	p.expirations = p.expirations[:0]
	p.expiryMu.Unlock()
}
