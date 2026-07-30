//go:build (linux && !386 && !arm && !mips && !mipsle) || darwin

package madvfree

import (
	"math"
	"time"
)

// idleQueueCompactLimit is the number of consumed deferral slots tolerated
// before the queue is compacted in place.
const idleQueueCompactLimit = 64

// idleState is the deferred idle-marking state of one arena region.
//
// A region is not marked reclaimable as soon as its last reader releases it.
// The release path bumps useSeq and queues at most one deferral, and the sweeper
// issues the idle advice only once Config.IdleDelay has elapsed with useSeq
// unchanged. Repeated access to the same region therefore costs neither the idle
// nor the reactivate advice, at the price of the region staying unreclaimable
// for up to two delay periods after its last access.
//
// idleState is guarded by the mutex of the region's owner: cacheEntry.mu for
// extents and smallPageMeta.mu for small slabs.
type idleState struct {
	// useSeq counts the times the region's last reader released it.
	useSeq uint64
	// reclaimable reports that the idle advice has been issued for the region,
	// so it must be reactivated before its bytes are read again.
	reclaimable bool
	// pending reports that the sweeper owns a deferral for the region. Only one
	// deferral exists per region; later releases only bump useSeq.
	pending bool
}

// idleCandidate is one queued deferred idle marking. Exactly one of entry and
// meta is set, and startPage identifies the region's first arena page.
type idleCandidate struct {
	entry     *cacheEntry
	meta      *smallPageMeta
	useSeq    uint64
	deadline  int64
	startPage uint32
}

// deferIdleLocked records that the region described by candidate has no readers
// left.
//
// It reports whether the caller must issue the idle advice itself, which happens
// only when the hysteresis is disabled. The caller must hold the mutex guarding
// state.
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

// reactivateLocked re-pins a region before it is read.
//
// Regions that the hysteresis has kept pinned need no advice, which is where the
// deferral pays off. The caller must hold the mutex guarding state.
func (p *Provider) reactivateLocked(state *idleState, region []byte) error {
	if !state.reclaimable {
		return nil
	}
	if err := p.markActive(region); err != nil {
		return err
	}
	state.reclaimable = false

	return nil
}

// requeueIdleLocked reschedules a deferral whose region was accessed during the
// delay window. The caller must hold the mutex guarding state, which keeps
// state.pending set.
func (p *Provider) requeueIdleLocked(state *idleState, candidate idleCandidate) {
	candidate.useSeq = state.useSeq
	p.queueIdle(candidate)
}

// queueIdle appends candidate with a fresh deadline.
//
// Every deferral uses the same delay, so appending keeps the queue ordered by
// deadline and the sweeper only has to look at its head.
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

// applyDeferredIdle issues, reschedules, or cancels one queued idle marking.
//
// It holds the lifecycle read lock so the arena cannot be unmapped underneath
// the advice.
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
	// The decision only looks at the region's current state, never at the
	// identity the deferral was queued for. A reader, a replacement, or a discard
	// resolves the region on its own, and the next release queues a new deferral.
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
	// A re-initialized slab keeps its page, so the queued startPage still
	// describes the live slab; its length comes from the current class.
	p.markIdle(p.smallSlab(candidate.startPage, p.layouts[meta.classID]))
}

// clearIdleQueue drops every queued deferral. The caller must hold the exclusive
// lifecycle lock.
func (p *Provider) clearIdleQueue() {
	p.idleMu.Lock()
	clear(p.idleQueue)
	p.idleQueue = p.idleQueue[:0]
	p.idleHead = 0
	p.idleMu.Unlock()
}
