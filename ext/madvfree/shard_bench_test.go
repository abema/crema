//go:build (linux && !386 && !arm && !mips && !mipsle) || darwin

package madvfree

import (
	"hash/maphash"
	"strings"
	"testing"
)

var benchmarkShardSink *indexShard

func BenchmarkProviderShard(b *testing.B) {
	provider := &Provider{
		hashSeed: maphash.MakeSeed(),
		shards:   make([]indexShard, defaultShards),
	}

	for _, test := range []struct {
		name string
		size int
	}{
		{name: "8B", size: 8},
		{name: "32B", size: 32},
		{name: "128B", size: 128},
	} {
		key := strings.Repeat("k", test.size)
		b.Run(test.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				benchmarkShardSink = provider.shard(key)
			}
		})
	}
}
