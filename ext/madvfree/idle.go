//go:build (linux && !386 && !arm && !mips && !mipsle) || darwin

package madvfree

import (
	"math"
	"time"
)

// idleQueueCompactLimit bounds consumed queue slots.
const idleQueueCompactLimit = 64

// idleState is guarded by cacheEntry.mu or smallPageMeta.mu.
type idleState struct {
	useSeq      uint64
	reclaimable bool
	pending     bool
}

// idleCandidate identifies one extent or slab.
type idleCandidate struct {
	entry     *cacheEntry
	meta      *smallPageMeta
	useSeq    uint64
	deadline  int64
	startPage uint32
}

// deferIdleLocked returns whether the caller must mark the region idle now.
func (p *Provider) deferIdleLocked(state *idleState, candidate idleCandidate) bool {
	if p.idleDelay <= 0 {
		state.reclaimable = true

		return true
	}

	state.useSeq++
	if state.reclaimable || state.pending {
		return false
	}
	state.pending = true
	candidate.useSeq = state.useSeq
	p.queueIdle(candidate)
	p.stats.idleDeferrals.Add(1)

	return false
}

// requeueIdleLocked reschedules a deferral after access during its delay.
func (p *Provider) requeueIdleLocked(state *idleState, candidate idleCandidate) {
	candidate.useSeq = state.useSeq
	p.queueIdle(candidate)
}

// queueIdle appends a candidate with a fresh deadline.
func (p *Provider) queueIdle(candidate idleCandidate) {
	p.idleMu.Lock()
	empty := p.idleHead == len(p.idleQueue)
	if empty || p.idleHead >= idleQueueCompactLimit {
		remaining := copy(p.idleQueue, p.idleQueue[p.idleHead:])
		clear(p.idleQueue[remaining:])
		p.idleQueue = p.idleQueue[:remaining]
		p.idleHead = 0
	}
	now := time.Now().UnixNano()
	if int64(p.idleDelay) > math.MaxInt64-now {
		candidate.deadline = math.MaxInt64
	} else {
		candidate.deadline = now + int64(p.idleDelay)
	}
	p.idleQueue = append(p.idleQueue, candidate)
	p.idleMu.Unlock()

	if empty {
		p.wakeIdleLoop()
	}
}

func (p *Provider) wakeIdleLoop() {
	select {
	case p.idleWake <- struct{}{}:
	default:
	}
}

// idleLoop issues deferred idle advice once each region's delay has elapsed.
//
//nolint:cyclop // Timer reset and shutdown paths are intentionally handled in one loop.
func (p *Provider) idleLoop() {
	defer close(p.idleDone)
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	for {
		p.idleMu.Lock()
		if p.idleHead == len(p.idleQueue) {
			p.idleMu.Unlock()
			select {
			case <-p.idleWake:
				continue
			case <-p.idleStop:
				return
			}
		}
		wait := time.Until(time.Unix(0, p.idleQueue[p.idleHead].deadline))
		p.idleMu.Unlock()
		if wait < 0 {
			wait = 0
		}
		timer.Reset(wait)

		select {
		case <-timer.C:
			p.sweepIdle()
		case <-p.idleWake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-p.idleStop:
			return
		}
	}
}

func (p *Provider) sweepIdle() {
	now := time.Now().UnixNano()
	for {
		p.idleMu.Lock()
		if p.idleHead == len(p.idleQueue) || p.idleQueue[p.idleHead].deadline > now {
			p.idleMu.Unlock()

			return
		}
		candidate := p.idleQueue[p.idleHead]
		p.idleQueue[p.idleHead] = idleCandidate{}
		p.idleHead++
		p.idleMu.Unlock()

		p.applyDeferredIdle(candidate)
	}
}

// applyDeferredIdle processes one queued idle marking.
func (p *Provider) applyDeferredIdle(candidate idleCandidate) {
	p.lifecycle.RLock()
	defer p.lifecycle.RUnlock()
	if p.closed {
		return
	}

	if candidate.entry != nil {
		p.applyDeferredIdleExtent(candidate)

		return
	}

	p.applyDeferredIdleSmall(candidate)
}

func (p *Provider) applyDeferredIdleExtent(candidate idleCandidate) {
	item := candidate.entry
	item.mu.Lock()
	defer item.mu.Unlock()
	if !item.idle.pending {
		return
	}
	if item.freed || item.state != entryLive || item.refs != 0 || item.idle.reclaimable {
		item.idle.pending = false
		p.stats.idleCancellations.Add(1)

		return
	}
	if item.idle.useSeq != candidate.useSeq {
		p.requeueIdleLocked(&item.idle, candidate)

		return
	}
	item.idle.pending = false
	item.idle.reclaimable = true
	p.markIdle(p.extentBytes(item.startPage, item.pageCount))
}

func (p *Provider) applyDeferredIdleSmall(candidate idleCandidate) {
	meta := candidate.meta
	meta.mu.Lock()
	defer meta.mu.Unlock()
	if !meta.idle.pending {
		return
	}
	if meta.state != smallPageLive || meta.refs != 0 || meta.idle.reclaimable {
		meta.idle.pending = false
		p.stats.idleCancellations.Add(1)

		return
	}
	if meta.idle.useSeq != candidate.useSeq {
		p.requeueIdleLocked(&meta.idle, candidate)

		return
	}
	meta.idle.pending = false
	meta.idle.reclaimable = true
	p.markIdle(p.smallSlab(candidate.startPage, p.layouts[meta.classID]))
}

// clearIdleQueue drops queued deferrals.
func (p *Provider) clearIdleQueue() {
	p.idleMu.Lock()
	clear(p.idleQueue)
	p.idleQueue = p.idleQueue[:0]
	p.idleHead = 0
	p.idleMu.Unlock()
}
