package crema

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func BenchmarkLoader(b *testing.B) {
	benchCases := []struct {
		name      string
		newLoader func() internalLoader[int]
	}{
		{
			name: "singleflight",
			newLoader: func() internalLoader[int] {
				return newSingleflightLoader[int](NoopMetricsProvider{})
			},
		},
		{
			name: "direct",
			newLoader: func() internalLoader[int] {
				return directLoader[int]{}
			},
		},
	}
	collisionRates := []int{0, 90}
	sleepDurations := []time.Duration{0, 1 * time.Millisecond}
	parallelisms := []int{1, 100}

	for _, sleepDuration := range sleepDurations {
		for _, bc := range benchCases {
			for _, collision := range collisionRates {
				for _, parallelism := range parallelisms {
					b.Run(fmt.Sprintf("%s/sleep_%s/collision_%d/parallel_%d", bc.name, sleepDuration, collision, parallelism), func(b *testing.B) {
						loader := bc.newLoader()
						b.ReportAllocs()
						var counter uint64
						ctx := context.Background()
						b.SetParallelism(parallelism)
						b.RunParallel(func(pb *testing.PB) {
							for pb.Next() {
								idx := atomic.AddUint64(&counter, 1) - 1
								key := selectKey(idx, collision)
								loadFunc := func(ctx context.Context) (int, error) {
									if sleepDuration > 0 {
										time.Sleep(sleepDuration)
									}

									return len(key), nil
								}
								if _, _, err := loader.load(ctx, key, loadFunc); err != nil {
									b.Fatal(err)
								}
							}
						})
					})
				}
			}
		}
	}
}

func selectKey(idx uint64, collisionPercent int) string {
	if idx%100 < uint64(collisionPercent) {
		return keyFor(0)
	}

	return keyFor(idx)
}

func keyFor(idx uint64) string {
	const prefix = "key-"
	const digits = 20
	var buf [len(prefix) + digits]byte
	copy(buf[:len(prefix)], prefix)
	for i := len(prefix); i < len(buf); i++ {
		buf[i] = '0'
	}
	for i := len(buf) - 1; i >= len(prefix); i-- {
		buf[i] = byte('0' + idx%10)
		idx /= 10
		if idx == 0 {
			break
		}
	}

	return string(buf[:])
}
