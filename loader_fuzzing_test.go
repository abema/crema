package crema

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func FuzzSingleflightLoaderLoad(f *testing.F) {
	f.Add([]byte("abca"))
	f.Add([]byte{0, 1, 2, 3, 4, 5})
	f.Add([]byte{7, 7, 7, 7})

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}

		loaderImpl := newSingleflightLoader[int](NoopMetricsProvider{}, 0)
		expectErr := errors.New("loader error")
		callCount := int(data[0]%16) + 1
		calls := make([]string, callCount)

		for i := 0; i < callCount; i++ {
			b := data[(i+1)%len(data)]
			calls[i] = fmt.Sprintf("k%d", b%8)
		}

		expectedCounts := make(map[string]int)
		for _, key := range calls {
			expectedCounts[key]++
		}

		errorForKey := func(key string) bool {
			if len(key) < 2 {
				return false
			}

			return (key[1]-'0')%2 == 1
		}
		valueForKey := func(key string) int {
			if len(key) < 2 {
				return 0
			}

			return int(key[1]-'0') * 10
		}

		callCounts := make(map[string]*int32)
		loaders := make(map[string]CacheLoadFunc[int])
		release := make(chan struct{})
		for key := range expectedCounts {
			k := key
			wantErr := errorForKey(k)
			val := valueForKey(k)
			var count int32
			callCounts[k] = &count
			loaders[k] = func(context.Context) (int, error) {
				atomic.AddInt32(callCounts[k], 1)
				<-release
				if wantErr {
					return 0, expectErr
				}

				return val, nil
			}
		}

		ready := make(chan struct{}, callCount)
		start := make(chan struct{})
		results := make(chan struct {
			key    string
			val    int
			leader bool
			err    error
		}, callCount)

		for _, key := range calls {
			k := key
			go func() {
				ready <- struct{}{}
				<-start
				val, leader, err := loaderImpl.load(context.Background(), k, loaders[k])
				results <- struct {
					key    string
					val    int
					leader bool
					err    error
				}{key: k, val: val, leader: leader, err: err}
			}()
		}

		for i := 0; i < callCount; i++ {
			<-ready
		}
		close(start)

		for key, wantRefs := range expectedCounts {
			deadline := time.After(2 * time.Second)
			shard := loaderImpl.shardFor(key)
			for {
				shard.mu.Lock()
				inf := shard.inflight[key]
				refs := 0
				if inf != nil {
					refs = inf.refs
				}
				shard.mu.Unlock()
				if refs >= wantRefs {
					break
				}
				select {
				case <-deadline:
					t.Fatalf("timed out waiting for callers to join key %q", key)
				default:
					time.Sleep(1 * time.Millisecond)
				}
			}
		}

		close(release)

		leaderCounts := make(map[string]int, len(expectedCounts))
		for i := 0; i < callCount; i++ {
			select {
			case res := <-results:
				wantErr := errorForKey(res.key)
				if wantErr {
					if res.err != expectErr {
						t.Fatalf("expected error %v, got %v", expectErr, res.err)
					}
					if res.val != 0 {
						t.Fatalf("expected zero value, got %d", res.val)
					}
				} else {
					if res.err != nil {
						t.Fatalf("unexpected error: %v", res.err)
					}
					if res.val != valueForKey(res.key) {
						t.Fatalf("expected value %d, got %d", valueForKey(res.key), res.val)
					}
				}
				if res.leader {
					leaderCounts[res.key]++
				}
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for results")
			}
		}

		for key := range expectedCounts {
			if leaderCounts[key] != 1 {
				t.Fatalf("expected exactly one leader for key %q, got %d", key, leaderCounts[key])
			}
			if atomic.LoadInt32(callCounts[key]) != 1 {
				t.Fatalf("expected loader to be called once for key %q, got %d", key, atomic.LoadInt32(callCounts[key]))
			}
		}
	})
}
