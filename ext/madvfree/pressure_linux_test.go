//go:build linux && !386 && !arm && !mips && !mipsle

package madvfree

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

const (
	pressureTestEnabledEnv  = "MADVFREE_PRESSURE_TEST"
	pressureTestMaxBytesEnv = "MADVFREE_PRESSURE_TEST_MAX_BYTES"
	defaultPressureTestMax  = uint64(4 << 30)
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
			"no MADV_FREE pages were reclaimed after writing %d bytes with %d bytes initially available",
			stats.ReservedBytes,
			available,
		)
	}
	if stats.GenerationMisses != 0 {
		t.Fatalf("unexpected non-reclaim generation misses: %d", stats.GenerationMisses)
	}
	if stats.MadvFreeErrors != 0 || stats.MadvDontNeedErrors != 0 {
		t.Fatalf("unexpected madvise errors: %+v", stats)
	}
}

func effectiveAvailableMemory() (uint64, string, error) {
	type candidate struct {
		bytes  uint64
		source string
	}
	var candidates []candidate

	for _, paths := range [][2]string{
		{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory.current"},
		{"/sys/fs/cgroup/memory/memory.limit_in_bytes", "/sys/fs/cgroup/memory/memory.usage_in_bytes"},
	} {
		limit, limitOK := readMemoryNumber(paths[0])
		used, usedOK := readMemoryNumber(paths[1])
		if limitOK && usedOK && limit > used && limit < 1<<60 {
			candidates = append(candidates, candidate{
				bytes:  limit - used,
				source: paths[0],
			})
		}
	}

	meminfo, err := os.ReadFile("/proc/meminfo")
	if err == nil {
		for line := range strings.SplitSeq(string(meminfo), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 3 && fields[0] == "MemAvailable:" && fields[2] == "kB" {
				value, parseErr := strconv.ParseUint(fields[1], 10, 64)
				if parseErr == nil && value <= ^uint64(0)/1024 {
					candidates = append(candidates, candidate{
						bytes:  value * 1024,
						source: "/proc/meminfo:MemAvailable",
					})
				}

				break
			}
		}
	}
	if len(candidates) == 0 {
		return 0, "", fmt.Errorf("madvfree pressure test: cannot determine available memory")
	}

	selected := candidates[0]
	for _, item := range candidates[1:] {
		if item.bytes < selected.bytes {
			selected = item
		}
	}

	return selected.bytes, selected.source, nil
}

func readMemoryNumber(path string) (uint64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	text := strings.TrimSpace(string(data))
	if text == "" || text == "max" {
		return 0, false
	}
	value, err := strconv.ParseUint(text, 10, 64)

	return value, err == nil
}

func pressureTestMaxBytes() (uint64, error) {
	text := os.Getenv(pressureTestMaxBytesEnv)
	if text == "" {
		return defaultPressureTestMax, nil
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
