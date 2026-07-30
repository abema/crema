//go:build (linux && !386 && !arm && !mips && !mipsle) || darwin

package madvfreebench

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/abema/crema"
	"github.com/abema/crema/ext/madvfree"
	cremaristretto "github.com/abema/crema/ext/ristretto"
	dgraphristretto "github.com/dgraph-io/ristretto/v2"
)

const comparisonCapacity = 256 << 20

type benchmarkProvider struct {
	name     string
	provider crema.CacheProvider[[]byte]
	wait     func()
}

type copyingProvider struct {
	crema.CacheProvider[[]byte]
}

func (p copyingProvider) Get(ctx context.Context, key string) ([]byte, bool, error) {
	value, ok, err := p.CacheProvider.Get(ctx, key)
	if err != nil || !ok {
		return value, ok, err
	}

	return append([]byte(nil), value...), true, nil
}

type waitingProvider struct {
	crema.CacheProvider[[]byte]

	wait func()
}

func (p waitingProvider) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := p.CacheProvider.Set(ctx, key, value, ttl); err != nil {
		return err
	}
	p.wait()

	return nil
}

func BenchmarkProviderGetHit(b *testing.B) {
	for _, size := range []int{64, 4 << 10, 6 << 10, 64 << 10} {
		b.Run(fmt.Sprintf("%dB", size), func(b *testing.B) {
			value := benchmarkValue(size)
			for _, fixture := range newComparisonProviders(b, false) {
				b.Run(fixture.name, func(b *testing.B) {
					benchmarkGetHit(b, fixture, value)
				})
			}
		})
	}
}

func BenchmarkProviderGetHitParallel(b *testing.B) {
	for _, size := range []int{64, 4 << 10, 6 << 10, 64 << 10} {
		b.Run(fmt.Sprintf("%dB", size), func(b *testing.B) {
			value := benchmarkValue(size)
			for _, fixture := range newComparisonProviders(b, false) {
				b.Run(fixture.name, func(b *testing.B) {
					ctx := context.Background()
					if err := fixture.provider.Set(ctx, "key", value, 0); err != nil {
						b.Fatal(err)
					}
					fixture.wait()

					b.SetBytes(int64(size))
					b.ReportAllocs()
					b.ResetTimer()
					b.RunParallel(func(pb *testing.PB) {
						for pb.Next() {
							got, ok, err := fixture.provider.Get(ctx, "key")
							if err != nil || !ok || len(got) != len(value) {
								b.Fatalf("Get() = (len %d, %v, %v)", len(got), ok, err)
							}
						}
					})
				})
			}
		})
	}
}

func BenchmarkProviderSetReplacement(b *testing.B) {
	for _, size := range []int{64, 4 << 10, 6 << 10, 64 << 10} {
		b.Run(fmt.Sprintf("%dB", size), func(b *testing.B) {
			value := benchmarkValue(size)
			for _, fixture := range newComparisonProviders(b, true) {
				b.Run(fixture.name, func(b *testing.B) {
					ctx := context.Background()
					b.SetBytes(int64(size))
					b.ReportAllocs()
					b.ResetTimer()
					for range b.N {
						if err := fixture.provider.Set(ctx, "key", value, 0); err != nil {
							b.Fatal(err)
						}
					}
					b.StopTimer()
					fixture.wait()
				})
			}
		})
	}
}

// BenchmarkMADVFreeIdleHysteresis measures repeated Get calls on the same key
// with and without the deferred idle marking.
//
// The reported madv-idle/op and repin/op metrics are the advice calls the
// provider issued per Get: one pair per call without the hysteresis, and one pair
// per idle period with it.
func BenchmarkMADVFreeIdleHysteresis(b *testing.B) {
	variants := []struct {
		name   string
		config madvfree.Config
	}{
		{name: "hysteresis", config: madvfree.Config{}},
		{name: "immediate", config: madvfree.Config{DisableIdleDelay: true}},
	}
	for _, size := range []int{64, 6 << 10, 64 << 10} {
		b.Run(fmt.Sprintf("%dB", size), func(b *testing.B) {
			value := benchmarkValue(size)
			for _, variant := range variants {
				b.Run(variant.name, func(b *testing.B) {
					benchmarkIdleHysteresis(b, variant.config, value)
				})
			}
		})
	}
}

func benchmarkIdleHysteresis(b *testing.B, config madvfree.Config, value []byte) {
	b.Helper()
	config.CapacityBytes = comparisonCapacity
	config.ShardCount = 64
	provider, err := madvfree.NewProvider(config)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := provider.Close(); err != nil {
			b.Errorf("madvfree Close(): %v", err)
		}
	})
	ctx := context.Background()
	if err := provider.Set(ctx, "key", value, 0); err != nil {
		b.Fatal(err)
	}
	before := provider.Stats()

	b.SetBytes(int64(len(value)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		got, ok, err := provider.Get(ctx, "key")
		if err != nil || !ok || len(got) != len(value) {
			b.Fatalf("Get() = (len %d, %v, %v)", len(got), ok, err)
		}
	}
	b.StopTimer()

	after := provider.Stats()
	operations := float64(b.N)
	b.ReportMetric(float64(after.IdleCalls-before.IdleCalls)/operations, "madv-idle/op")
	b.ReportMetric(float64(after.ReactivateCalls-before.ReactivateCalls)/operations, "repin/op")
}

func benchmarkGetHit(b *testing.B, fixture benchmarkProvider, value []byte) {
	b.Helper()
	ctx := context.Background()
	if err := fixture.provider.Set(ctx, "key", value, 0); err != nil {
		b.Fatal(err)
	}
	fixture.wait()

	b.SetBytes(int64(len(value)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		got, ok, err := fixture.provider.Get(ctx, "key")
		if err != nil || !ok || len(got) != len(value) {
			b.Fatalf("Get() = (len %d, %v, %v)", len(got), ok, err)
		}
	}
}

func newComparisonProviders(b *testing.B, includeWaiting bool) []benchmarkProvider {
	b.Helper()

	mmapProvider, err := madvfree.NewProvider(madvfree.Config{
		CapacityBytes: comparisonCapacity,
		ShardCount:    64,
	})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := mmapProvider.Close(); err != nil {
			b.Errorf("madvfree Close(): %v", err)
		}
	})
	extentProvider, err := madvfree.NewProvider(madvfree.Config{
		CapacityBytes: comparisonCapacity,
		SizeClasses:   []int{},
		ShardCount:    64,
	})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := extentProvider.Close(); err != nil {
			b.Errorf("madvfree extent Close(): %v", err)
		}
	})

	newRistretto := func() (*cremaristretto.RistrettoCacheProvider[[]byte], *dgraphristretto.Cache[string, []byte]) {
		cache, err := dgraphristretto.NewCache(&dgraphristretto.Config[string, []byte]{
			NumCounters: 1e5,
			MaxCost:     comparisonCapacity,
			BufferItems: 64,
		})
		if err != nil {
			b.Fatal(err)
		}
		b.Cleanup(cache.Close)
		provider, err := cremaristretto.NewRistrettoCacheProvider(
			cache,
			cremaristretto.WithCostFunc(func(value []byte) int64 {
				return int64(len(value))
			}),
		)
		if err != nil {
			b.Fatal(err)
		}

		return provider, cache
	}

	rawRistretto, rawCache := newRistretto()
	if includeWaiting {
		waitRistretto, waitCache := newRistretto()

		return []benchmarkProvider{
			{
				name:     "madvfree_sync",
				provider: mmapProvider,
				wait:     func() {},
			},
			{
				name:     "madvfree_extent_sync",
				provider: extentProvider,
				wait:     func() {},
			},
			{
				name:     "ristretto_async",
				provider: rawRistretto,
				wait:     rawCache.Wait,
			},
			{
				name: "ristretto_wait",
				provider: waitingProvider{
					CacheProvider: waitRistretto,
					wait:          waitCache.Wait,
				},
				wait: waitCache.Wait,
			},
		}
	}

	copyRistretto, copyCache := newRistretto()

	return []benchmarkProvider{
		{
			name:     "madvfree_copy",
			provider: mmapProvider,
			wait:     func() {},
		},
		{
			name:     "madvfree_extent_copy",
			provider: extentProvider,
			wait:     func() {},
		},
		{
			name:     "ristretto_zero_copy",
			provider: rawRistretto,
			wait:     rawCache.Wait,
		},
		{
			name: "ristretto_copy",
			provider: copyingProvider{
				CacheProvider: copyRistretto,
			},
			wait: copyCache.Wait,
		},
	}
}

func benchmarkValue(size int) []byte {
	value := make([]byte, size)
	for index := range value {
		value[index] = byte(index)
	}

	return value
}
