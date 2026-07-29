//go:build linux && !386 && !arm && !mips && !mipsle

package madvfree

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const defaultPressureTestMaxBytes = uint64(4 << 30)

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
