//go:build (linux && !386 && !arm && !mips && !mipsle) || darwin

package madvfree

import (
	"encoding/binary"
	"testing"
)

func BenchmarkHeaderReadWrite(b *testing.B) {
	page := make([]byte, 4096)
	b.ReportAllocs()
	for b.Loop() {
		binary.LittleEndian.PutUint64(page[generationOffset:pageIndexOffset], 42)
		if binary.LittleEndian.Uint64(page[generationOffset:pageIndexOffset]) != 42 {
			b.Fatal("generation mismatch")
		}
	}
}
