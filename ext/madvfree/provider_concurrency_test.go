//go:build (linux && !386 && !arm && !mips && !mipsle) || darwin

package madvfree

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The sustained concurrency tests are bounded by wall-clock time rather than by
// an iteration count, so they take the same time on every CI job including the
// race-enabled matrix. The race detector reduces the number of operations
// completed within the budget instead of extending the test.
const (
	stressBudget      = 750 * time.Millisecond
	shortStressBudget = 100 * time.Millisecond

	// stressKeyCount keeps several keys per index shard so writers, deleters,
	// and the expiration worker collide on the same shard locks.
	stressKeyCount = 48
)

// stressSizes mixes sub-page slab classes, a multi-page slab class, and values
// above the 32 KiB class cap that always fall back to contiguous extents.
var stressSizes = []int{0, 1, 48, 700, 3 << 10, 6 << 10, 40 << 10}

func stressTestBudget() time.Duration {
	if testing.Short() {
		return shortStressBudget
	}

	return stressBudget
}

// stressValue returns a value whose bytes are derived from its own length, so a
// reader can validate whatever it observes without knowing which writer stored
// it. Torn reads, payload bleed between entries, and reclaimed (zeroed) pages
// all break the pattern.
func stressValue(size int) []byte {
	value := make([]byte, size)
	for index := range value {
		value[index] = byte(size + index)
	}

	return value
}

func validStressValue(value []byte) bool {
	for index := range value {
		if value[index] != byte(len(value)+index) {
			return false
		}
	}

	return true
}

func stressKey(index int) string {
	return fmt.Sprintf("stress-%02d", index%stressKeyCount)
}

// stressWorkload drives one provider from many goroutines until its deadline.
//
// Workers report failures with Errorf from their own goroutine and then stop;
// failed also stops the remaining workers so a broken provider does not keep
// the whole budget running.
type stressWorkload struct {
	t        *testing.T
	provider *Provider
	deadline time.Time
	failed   atomic.Bool
}

func (w *stressWorkload) running() bool {
	return !w.failed.Load() && time.Now().Before(w.deadline)
}

func (w *stressWorkload) failf(format string, args ...any) {
	w.failed.Store(true)
	w.t.Errorf(format, args...)
}

func (w *stressWorkload) key(random *rand.Rand) string {
	return stressKey(random.IntN(stressKeyCount))
}

func (w *stressWorkload) runWriter(random *rand.Rand) {
	ctx := context.Background()
	for w.running() {
		key := w.key(random)
		value := stressValue(stressSizes[random.IntN(len(stressSizes))])
		var ttl time.Duration
		if random.IntN(4) == 0 {
			ttl = time.Duration(random.IntN(2000)+1) * time.Microsecond
		}
		if err := w.provider.Set(ctx, key, value, ttl); err != nil && !errors.Is(err, ErrCapacity) {
			w.failf("Set(%q, %d bytes, ttl=%v): %v", key, len(value), ttl, err)

			return
		}
	}
}

func (w *stressWorkload) runReader(random *rand.Rand) {
	ctx := context.Background()
	for w.running() {
		key := w.key(random)
		value, found, err := w.provider.Get(ctx, key)
		if err != nil {
			w.failf("Get(%q): %v", key, err)

			return
		}
		if found && !validStressValue(value) {
			w.failf("Get(%q) returned a corrupted %d byte value", key, len(value))

			return
		}
	}
}

func (w *stressWorkload) runDeleter(random *rand.Rand) {
	ctx := context.Background()
	for w.running() {
		key := w.key(random)
		if err := w.provider.Delete(ctx, key); err != nil {
			w.failf("Delete(%q): %v", key, err)

			return
		}
		// Deleting as fast as the loop allows would keep the index nearly empty
		// and starve the readers of hits.
		time.Sleep(50 * time.Microsecond)
	}
}

func (w *stressWorkload) runTrimmer(random *rand.Rand) {
	for w.running() {
		target := int64(random.IntN(4)+1) * int64(w.provider.pageSize)
		freed, err := w.provider.Trim(target)
		if err != nil {
			w.failf("Trim(%d): %v", target, err)

			return
		}
		if freed < 0 {
			w.failf("Trim(%d) released %d bytes", target, freed)

			return
		}
		// Trim holds the exclusive lifecycle lock, so pause between evictions to
		// let the rest of the workload make progress.
		time.Sleep(500 * time.Microsecond)
	}
}

func (w *stressWorkload) runStatsMonitor() {
	for w.running() {
		if err := checkProviderStats(w.provider); err != nil {
			w.failf("concurrent stats invariant: %v", err)

			return
		}
		time.Sleep(200 * time.Microsecond)
	}
}

// TestProviderConcurrentMixedOperationsStress runs Get, Set, Delete, Trim, and
// TTL expiration against one provider at the same time.
//
// The workload crosses every lock in the provider: index shards, entry and slab
// metadata, the expiration heap, the extent allocator, and the exclusive
// lifecycle lock taken by Trim. It is meant to run under -race, where the
// checks below (value integrity, error classification, and arena accounting)
// turn an interleaving bug into a test failure instead of silent corruption.
func TestProviderConcurrentMixedOperationsStress(t *testing.T) {
	provider, err := NewProvider(Config{
		CapacityBytes: unix.Getpagesize() * 512,
		ShardCount:    8,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := provider.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})

	workload := &stressWorkload{
		t:        t,
		provider: provider,
		deadline: time.Now().Add(stressTestBudget()),
	}
	parallelism := max(2, runtime.GOMAXPROCS(0)/2)

	var wait sync.WaitGroup
	launch := func(count int, role func(random *rand.Rand)) {
		for worker := 0; worker < count; worker++ {
			wait.Add(1)
			go func(worker int) {
				defer wait.Done()
				role(rand.New(rand.NewPCG(uint64(worker)+1, 0x9e3779b97f4a7c15)))
			}(worker)
		}
	}

	launch(parallelism, workload.runWriter)
	launch(parallelism, workload.runReader)
	launch(1, workload.runDeleter)
	launch(1, workload.runTrimmer)
	wait.Add(1)
	go func() {
		defer wait.Done()
		workload.runStatsMonitor()
	}()
	wait.Wait()

	stats := provider.Stats()
	if stats.Hits == 0 {
		t.Fatalf("workload never observed a hit: %+v", stats)
	}
	if stats.SmallAllocations == 0 || stats.ExtentAllocations == 0 {
		t.Fatalf("workload did not exercise both allocators: %+v", stats)
	}
	assertProviderStats(t, provider)

	if err := provider.Purge(); err != nil {
		t.Fatalf("Purge() after stress: %v", err)
	}
	waitForDrainedArena(t, provider)
}

// waitForDrainedArena waits for the arena to return to its empty state.
//
// An entry retired by the expiration worker can still be between its index
// removal and its final release when Purge returns, so the drained state is
// reached shortly after rather than exactly at that point.
func waitForDrainedArena(t *testing.T, provider *Provider) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		stats := provider.Stats()
		if stats.Entries == 0 && stats.LogicalBytes == 0 &&
			stats.ReservedBytes == 0 && stats.FreeBytes == int64(provider.capacity) {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("arena not drained: %+v; state=%s", stats, debugProviderState(provider))
		}
		time.Sleep(time.Millisecond)
	}
}

// closeRace exercises operations that are in flight while Close unmaps the arena.
type closeRace struct {
	t        *testing.T
	provider *Provider
	deadline time.Time
	ops      atomic.Uint64
}

// awaitProgress waits until the workers have completed enough operations that
// Close is guaranteed to land on a running workload rather than on goroutines
// that have not been scheduled yet.
//
// A missed target is reported without stopping the test, so the caller still
// closes the provider and joins its workers.
func (r *closeRace) awaitProgress(target uint64) {
	r.t.Helper()
	for r.ops.Load() < target {
		if !time.Now().Before(r.deadline) {
			r.t.Errorf("workers completed %d operations before the deadline, want %d", r.ops.Load(), target)

			return
		}
		time.Sleep(50 * time.Microsecond)
	}
}

// run loops over the public API until it observes ErrClosed.
//
// Every operation must either complete against a live arena or report ErrClosed;
// observing a value read from an unmapped arena, a spurious error, or a crash
// means Close raced an operation that still held the mapping.
func (r *closeRace) run(worker int) {
	ctx := context.Background()
	for iteration := 0; time.Now().Before(r.deadline); iteration++ {
		key := stressKey(worker + iteration)
		var err error
		switch (worker + iteration) % 5 {
		case 0:
			var value []byte
			var found bool
			value, found, err = r.provider.Get(ctx, key)
			if err == nil && found && !validStressValue(value) {
				r.t.Errorf("Get(%q) racing Close returned a corrupted %d byte value", key, len(value))

				return
			}
		case 1:
			err = r.provider.Set(ctx, key, stressValue(stressSizes[iteration%len(stressSizes)]), 0)
		case 2:
			err = r.provider.Delete(ctx, key)
		case 3:
			_, err = r.provider.Trim(int64(r.provider.pageSize))
		case 4:
			// Stats stays available across Close and must not read the mapping.
			_ = r.provider.Stats()
		}
		r.ops.Add(1)
		if errors.Is(err, ErrClosed) {
			return
		}
		if err != nil && !errors.Is(err, ErrCapacity) {
			r.t.Errorf("operation racing Close: %v", err)

			return
		}
	}
	r.t.Error("worker did not observe ErrClosed before its deadline")
}

// TestProviderCloseRacesConcurrentOperations unmaps the arena while Get, Set,
// Delete, Trim, and Stats calls are in flight.
func TestProviderCloseRacesConcurrentOperations(t *testing.T) {
	const (
		iterations = 20
		workers    = 8
	)
	for iteration := 0; iteration < iterations; iteration++ {
		provider, err := NewProvider(Config{
			CapacityBytes: unix.Getpagesize() * 64,
			ShardCount:    4,
		})
		if err != nil {
			t.Fatal(err)
		}
		prefillStressKeys(t, provider)

		race := &closeRace{t: t, provider: provider, deadline: time.Now().Add(5 * time.Second)}
		var wait sync.WaitGroup
		for worker := 0; worker < workers; worker++ {
			wait.Add(1)
			go func(worker int) {
				defer wait.Done()
				race.run(worker)
			}(worker)
		}

		// Vary how far the workload progresses before the arena disappears, so
		// Close lands in different phases of the in-flight operations.
		race.awaitProgress(uint64(workers) * 4)
		time.Sleep(time.Duration(iteration%8) * 50 * time.Microsecond)
		if err := provider.Close(); err != nil {
			t.Fatalf("iteration %d Close(): %v", iteration, err)
		}
		wait.Wait()
		if t.Failed() {
			t.FailNow()
		}
		assertClosedProvider(t, provider)
	}
}

// TestProviderConcurrentCloseIsIdempotent closes one provider from several
// goroutines at once while operations are still in flight.
func TestProviderConcurrentCloseIsIdempotent(t *testing.T) {
	const (
		iterations = 10
		closers    = 4
	)
	for iteration := 0; iteration < iterations; iteration++ {
		provider, err := NewProvider(Config{
			CapacityBytes: unix.Getpagesize() * 32,
			ShardCount:    4,
		})
		if err != nil {
			t.Fatal(err)
		}
		prefillStressKeys(t, provider)

		race := &closeRace{t: t, provider: provider, deadline: time.Now().Add(5 * time.Second)}
		var wait sync.WaitGroup
		for worker := 0; worker < 2; worker++ {
			wait.Add(1)
			go func(worker int) {
				defer wait.Done()
				race.run(worker)
			}(worker)
		}

		race.awaitProgress(8)
		start := make(chan struct{})
		errs := make(chan error, closers)
		var closing sync.WaitGroup
		for closer := 0; closer < closers; closer++ {
			closing.Add(1)
			go func() {
				defer closing.Done()
				<-start
				errs <- provider.Close()
			}()
		}
		close(start)
		closing.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("iteration %d concurrent Close(): %v", iteration, err)
			}
		}
		wait.Wait()
		if t.Failed() {
			t.FailNow()
		}
		assertClosedProvider(t, provider)
	}
}

func prefillStressKeys(t *testing.T, provider *Provider) {
	t.Helper()
	for index := 0; index < len(stressSizes); index++ {
		value := stressValue(stressSizes[index])
		if err := provider.Set(context.Background(), stressKey(index), value, 0); err != nil {
			t.Fatalf("prefill Set(%q, %d bytes): %v", stressKey(index), len(value), err)
		}
	}
}

// assertClosedProvider verifies the post-Close contract. Callers must have
// joined every goroutine that touched the provider, so the unlocked reads of
// arena and expiryDone below are race-free.
func assertClosedProvider(t *testing.T, provider *Provider) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := provider.Get(ctx, "stress-00"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Get() after Close error = %v, want ErrClosed", err)
	}
	if err := provider.Set(ctx, "stress-00", []byte("value"), 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("Set() after Close error = %v, want ErrClosed", err)
	}
	if err := provider.Delete(ctx, "stress-00"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Delete() after Close error = %v, want ErrClosed", err)
	}
	if err := provider.Purge(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Purge() after Close error = %v, want ErrClosed", err)
	}
	if _, err := provider.Trim(int64(provider.pageSize)); !errors.Is(err, ErrClosed) {
		t.Fatalf("Trim() after Close error = %v, want ErrClosed", err)
	}
	if provider.arena != nil {
		t.Fatal("arena retained after Close")
	}
	select {
	case <-provider.expiryDone:
	default:
		t.Fatal("expiration worker still running after Close")
	}
	stats := provider.Stats()
	if stats.Entries != 0 || stats.ReservedBytes != 0 || stats.LogicalBytes != 0 {
		t.Fatalf("Stats() after Close = %+v", stats)
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("Close() after Close: %v", err)
	}
}
