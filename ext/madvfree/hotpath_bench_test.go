//go:build (linux && !386 && !arm && !mips && !mipsle) || darwin

package madvfree

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

const (
	hotPathCapacity = 256 << 20
	hotPathKeyRing  = 16
)

var hotPathTTLs = []struct {
	name string
	ttl  time.Duration
}{
	{name: "no_ttl", ttl: 0},
	{name: "ttl", ttl: time.Hour},
}

// BenchmarkProviderSetParallel measures replacement Sets from many goroutines.
func BenchmarkProviderSetParallel(b *testing.B) {
	for _, size := range []int{64, 4 << 10} {
		for _, test := range hotPathTTLs {
			b.Run(fmt.Sprintf("%dB/%s", size, test.name), func(b *testing.B) {
				provider := newHotPathProvider(b)
				value := benchmarkPayload(size)
				ctx := context.Background()
				var workers atomic.Uint64

				b.SetBytes(int64(size))
				b.ReportAllocs()
				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					keys := hotPathKeys(&workers)
					index := 0
					for pb.Next() {
						if err := provider.Set(ctx, keys[index], value, test.ttl); err != nil {
							b.Fatalf("Set(%q): %v", keys[index], err)
						}
						index = (index + 1) % len(keys)
					}
				})
			})
		}
	}
}

// BenchmarkProviderMixedParallel measures a read-heavy mixed workload.
func BenchmarkProviderMixedParallel(b *testing.B) {
	for _, size := range []int{64, 4 << 10} {
		for _, test := range hotPathTTLs {
			b.Run(fmt.Sprintf("%dB/%s", size, test.name), func(b *testing.B) {
				provider := newHotPathProvider(b)
				value := benchmarkPayload(size)
				ctx := context.Background()
				var workers atomic.Uint64

				b.SetBytes(int64(size))
				b.ReportAllocs()
				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					keys := hotPathKeys(&workers)
					for index := range keys {
						if err := provider.Set(ctx, keys[index], value, test.ttl); err != nil {
							b.Fatalf("Set(%q): %v", keys[index], err)
						}
					}
					index := 0
					for pb.Next() {
						key := keys[index%len(keys)]
						if index%4 == 0 {
							if err := provider.Set(ctx, key, value, test.ttl); err != nil {
								b.Fatalf("Set(%q): %v", key, err)
							}
						} else if _, _, err := provider.Get(ctx, key); err != nil {
							b.Fatalf("Get(%q): %v", key, err)
						}
						index++
					}
				})
			})
		}
	}
}

func newHotPathProvider(b *testing.B) *Provider {
	b.Helper()
	provider, err := NewProvider(Config{
		CapacityBytes: hotPathCapacity,
		ShardCount:    defaultShards,
	})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := provider.Close(); err != nil {
			b.Errorf("Close(): %v", err)
		}
	})

	return provider
}

func hotPathKeys(workers *atomic.Uint64) []string {
	prefix := "w" + strconv.FormatUint(workers.Add(1), 10) + "-"
	keys := make([]string, hotPathKeyRing)
	for index := range keys {
		keys[index] = prefix + strconv.Itoa(index)
	}

	return keys
}

func benchmarkPayload(size int) []byte {
	value := make([]byte, size)
	for index := range value {
		value[index] = byte(index)
	}

	return value
}
