//go:build darwin

package madvfree

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const defaultPressureTestMaxBytes = uint64(96 << 30)

func effectiveAvailableMemory() (uint64, string, error) {
	total, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0, "", fmt.Errorf("madvfree pressure test: read hw.memsize: %w", err)
	}
	output, err := exec.Command("memory_pressure", "-Q").Output()
	if err != nil {
		return 0, "", fmt.Errorf("madvfree pressure test: memory_pressure -Q: %w", err)
	}
	percent, err := parseMemoryPressureFreePercentage(string(output))
	if err != nil {
		return 0, "", err
	}
	if percent != 0 && total > ^uint64(0)/percent {
		return 0, "", fmt.Errorf("madvfree pressure test: available memory calculation overflows")
	}

	return total * percent / 100, "memory_pressure -Q", nil
}

func parseMemoryPressureFreePercentage(output string) (uint64, error) {
	const prefix = "System-wide memory free percentage:"

	for line := range strings.SplitSeq(output, "\n") {
		text, ok := strings.CutPrefix(strings.TrimSpace(line), prefix)
		if !ok {
			continue
		}
		percent, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(text), "%"), 10, 64)
		if err != nil || percent > 100 {
			return 0, fmt.Errorf("madvfree pressure test: invalid free percentage %q", text)
		}

		return percent, nil
	}

	return 0, fmt.Errorf("madvfree pressure test: free percentage missing from memory_pressure output")
}
