//go:build darwin

package madvfree

import "testing"

func TestParseMemoryPressureFreePercentage(t *testing.T) {
	t.Parallel()

	const output = `The system has 68719476736 (4194304 pages with a page size of 16384).
System-wide memory free percentage: 80%
`
	got, err := parseMemoryPressureFreePercentage(output)
	if err != nil {
		t.Fatal(err)
	}
	if got != 80 {
		t.Fatalf("free percentage = %d, want 80", got)
	}
}
