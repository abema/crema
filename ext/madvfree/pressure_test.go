//go:build (linux && !386 && !arm && !mips && !mipsle) || darwin

package madvfree

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
)

const (
	pressureTestEnabledEnv  = "MADVFREE_PRESSURE_TEST"
	pressureTestMaxBytesEnv = "MADVFREE_PRESSURE_TEST_MAX_BYTES"
	pressureValueSize       = 1 << 20
)

func TestMADVFreeUnderMemoryPressure(t *testing.T) {
	if os.Getenv(pressureTestEnabledEnv) != "1" {
		t.Skipf("set %s=1 to run the memory-pressure integration test", pressureTestEnabledEnv)
	}
	if testing.Short() {
		t.Skip("memory-pressure integration test is disabled by -short")
	}

	available, source, err := effectiveAvailableMemory()
	if err != nil {
		t.Fatal(err)
	}
	maxBytes, err := pressureTestMaxBytes()
	if err != nil {
		t.Fatal(err)
	}
	writeTarget := available + available/2
	if writeTarget < available {
		t.Fatal("memory-pressure target overflows uint64")
	}
	if writeTarget > maxBytes {
		t.Skipf(
			"target %d bytes from %s exceeds safety limit %d; raise %s to run",
			writeTarget,
			source,
			maxBytes,
			pressureTestMaxBytesEnv,
		)
	}
	if writeTarget < 64<<20 {
		t.Skipf("only %d bytes available from %s; at least 64 MiB required", available, source)
	}
	capacity := writeTarget + 4*pressureValueSize
	if capacity > uint64(maxInt()) {
		t.Fatalf("arena capacity %d exceeds int", capacity)
	}

	provider, err := NewProvider(Config{
		CapacityBytes: int(capacity),
		ShardCount:    64,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := provider.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	}()

	value := bytes.Repeat([]byte{0xa5}, pressureValueSize)
	keys := make([]string, 0, int(writeTarget/pressureValueSize)+1)
	ctx := context.Background()
	for provider.Stats().ReservedBytes < int64(writeTarget) {
		key := fmt.Sprintf("pressure-%08d", len(keys))
		if err := provider.Set(ctx, key, value, 0); err != nil {
			t.Fatalf(
				"Set after %d logical bytes and %d reserved bytes: %v",
				len(keys)*len(value),
				provider.Stats().ReservedBytes,
				err,
			)
		}
		keys = append(keys, key)
	}

	hits := 0
	for _, key := range keys {
		got, ok, err := provider.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get(%q): %v", key, err)
		}
		if !ok {
			continue
		}
		hits++
		if !bytes.Equal(got, value) {
			t.Fatalf("Get(%q) returned corrupted data", key)
		}
	}

	stats := provider.Stats()
	t.Logf(
		"memory source=%s available=%d target=%d entries=%d hits=%d reclaimed=%d",
		source,
		available,
		writeTarget,
		len(keys),
		hits,
		stats.ReclaimedMisses,
	)
	if stats.ReclaimedMisses == 0 {
		t.Fatalf(
			"no reclaimable pages were reclaimed after writing %d bytes with %d bytes initially available",
			stats.ReservedBytes,
			available,
		)
	}
	if stats.GenerationMisses != 0 {
		t.Fatalf("unexpected non-reclaim generation misses: %d", stats.GenerationMisses)
	}
	if stats.IdleErrors != 0 || stats.ReactivateErrors != 0 || stats.DiscardErrors != 0 {
		t.Fatalf("unexpected memory advice errors: %+v", stats)
	}
}

func pressureTestMaxBytes() (uint64, error) {
	text := os.Getenv(pressureTestMaxBytesEnv)
	if text == "" {
		return defaultPressureTestMaxBytes, nil
	}
	value, err := strconv.ParseUint(text, 10, 64)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("%s must be a positive byte count", pressureTestMaxBytesEnv)
	}

	return value, nil
}

func maxInt() int {
	return int(^uint(0) >> 1)
}
