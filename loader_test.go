package crema

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSingleflightLoader_LoadsOnce(t *testing.T) {
	t.Parallel()

	provider := &testMemoryProvider[int]{items: make(map[string]CacheObject[int])}
	cache := NewCache(provider, NoopSerializationCodec[int]{})
	impl := cache.(*cacheImpl[int, CacheObject[int]])
	impl.now = func() time.Time { return time.UnixMilli(1000) }

	started := make(chan struct{})
	release := make(chan struct{})
	var calls int32
	loader := func(context.Context) (int, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(started)
		}
		<-release

		return 42, nil
	}

	var wg sync.WaitGroup
	results := make([]int, 2)
	wg.Add(2)

	go func() {
		defer wg.Done()
		value, err := cache.GetOrLoad(context.Background(), "key", time.Second, loader)
		if err != nil {
			t.Errorf("first call returned error: %v", err)

			return
		}
		results[0] = value
	}()

	<-started

	go func() {
		defer wg.Done()
		value, err := cache.GetOrLoad(context.Background(), "key", time.Second, loader)
		if err != nil {
			t.Errorf("second call returned error: %v", err)

			return
		}
		results[1] = value
	}()

	deadline := time.After(time.Second)
	loaderImpl, ok := impl.internalLoader.(*singleflightLoader[int])
	if !ok {
		t.Fatal("expected singleflight loader")
	}
	shard := loaderImpl.shardFor("key")
	for {
		shard.mu.Lock()
		inf := shard.inflight["key"]
		refs := 0
		if inf != nil {
			refs = inf.refs
		}
		shard.mu.Unlock()
		if refs >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for second caller to join")
		default:
			time.Sleep(1 * time.Millisecond)
		}
	}

	close(release)
	wg.Wait()

	if calls != 1 {
		t.Fatalf("expected loader to be called once, got %d", calls)
	}
	if results[0] != 42 || results[1] != 42 {
		t.Fatalf("expected both results to be 42, got %v", results)
	}
}

func TestSingleflightLoader_SharedWhenConcurrent(t *testing.T) {
	t.Parallel()

	loaderImpl := newSingleflightLoader[int](NoopMetricsProvider{}, 0)
	started := make(chan struct{})
	unblock := make(chan struct{})
	var calls int32
	loader := func(context.Context) (int, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(started)
		}
		<-unblock

		return 99, nil
	}

	type result struct {
		val    int
		leader bool
		err    error
	}
	results := make(chan result, 2)

	go func() {
		val, leader, err := loaderImpl.load(context.Background(), "key", loader)
		results <- result{val: val, leader: leader, err: err}
	}()

	<-started

	go func() {
		val, leader, err := loaderImpl.load(context.Background(), "key", loader)
		results <- result{val: val, leader: leader, err: err}
	}()

	deadline := time.After(time.Second)
	shard := loaderImpl.shardFor("key")
	for {
		shard.mu.Lock()
		inf := shard.inflight["key"]
		refs := 0
		if inf != nil {
			refs = inf.refs
		}
		shard.mu.Unlock()
		if refs >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for second caller to join")
		default:
			time.Sleep(1 * time.Millisecond)
		}
	}

	close(unblock)

	first := <-results
	second := <-results

	if calls != 1 {
		t.Fatalf("expected loader to be called once, got %d", calls)
	}

	leaderCount := 0
	for _, res := range []result{first, second} {
		if res.err != nil {
			t.Fatalf("unexpected error: %v", res.err)
		}
		if res.val != 99 {
			t.Fatalf("expected value 99, got %d", res.val)
		}
		if res.leader {
			leaderCount++
		}
	}
	if leaderCount != 1 {
		t.Fatalf("expected exactly one leader, got %d", leaderCount)
	}
}

func TestSingleflightLoader_ContextDone(t *testing.T) {
	t.Parallel()

	loaderImpl := newSingleflightLoader[int](NoopMetricsProvider{}, 0)
	started := make(chan struct{})
	unblock := make(chan struct{})
	var calls int32
	loader := func(context.Context) (int, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(started)
		}
		<-unblock

		return 123, nil
	}

	leaderErrCh := make(chan error, 1)
	go func() {
		_, _, err := loaderImpl.load(context.Background(), "key", loader)
		leaderErrCh <- err
	}()

	<-started

	ctx, cancel := context.WithCancel(context.Background())
	followerErrCh := make(chan error, 1)
	followerValCh := make(chan int, 1)
	followerLeaderCh := make(chan bool, 1)
	go func() {
		value, leader, err := loaderImpl.load(ctx, "key", loader)
		followerValCh <- value
		followerLeaderCh <- leader
		followerErrCh <- err
	}()

	cancel()

	select {
	case err := <-followerErrCh:
		if err != context.Canceled {
			t.Fatalf("expected context error, got %v", err)
		}
		if value := <-followerValCh; value != 0 {
			t.Fatalf("expected zero value, got %d", value)
		}
		if leader := <-followerLeaderCh; leader {
			t.Fatalf("expected leader=false, got true")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for context cancellation")
	}

	close(unblock)

	if err := <-leaderErrCh; err != nil {
		t.Fatalf("leader returned error: %v", err)
	}

	if calls != 1 {
		t.Fatalf("expected loader to be called once, got %d", calls)
	}

	shard := loaderImpl.shardFor("key")
	shard.mu.Lock()
	inflightLen := len(shard.inflight)
	shard.mu.Unlock()
	if inflightLen != 0 {
		t.Fatalf("expected inflight map to be empty, got %d", inflightLen)
	}
}

func TestSingleflightLoader_LeaderContextDoneDoesNotBlock(t *testing.T) {
	t.Parallel()

	loaderImpl := newSingleflightLoader[int](NoopMetricsProvider{}, 0)
	started := make(chan struct{})
	block := make(chan struct{})
	loader := func(context.Context) (int, error) {
		close(started)
		<-block

		return 7, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, _, err := loaderImpl.load(ctx, "key", loader)
		errCh <- err
	}()

	<-started
	cancel()

	select {
	case err := <-errCh:
		if err != context.Canceled {
			t.Fatalf("expected context error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for leader cancellation")
	}

	close(block)
}

func TestSingleflightLoader_AcquireAfterDoneReplacesInflight(t *testing.T) {
	t.Parallel()

	loaderImpl := newSingleflightLoader[int](NoopMetricsProvider{}, 0)
	ctx := context.Background()
	inf := loaderImpl.newInflight(ctx)
	shard := loaderImpl.shardFor("key")
	shard.mu.Lock()
	shard.inflight["key"] = inf
	shard.mu.Unlock()

	loaderImpl.finishInflight(inf, shard, 10, nil)

	newInf, leader, _ := loaderImpl.acquireInflight(ctx, "key")
	if !leader {
		t.Fatalf("expected leader=true, got false")
	}
	if newInf == inf {
		t.Fatal("expected new inflight instance")
	}

	shard.mu.Lock()
	current := shard.inflight["key"]
	shard.mu.Unlock()
	if current != newInf {
		t.Fatal("expected inflight map to be replaced with new instance")
	}

	loaderImpl.releaseInflight("key", newInf, shard)
	loaderImpl.releaseInflight("key", inf, shard)
}

func TestSingleflightLoader_PoolPutOnlyAfterDone(t *testing.T) {
	t.Parallel()

	loaderImpl := newSingleflightLoader[int](NoopMetricsProvider{}, 0)
	ctx := context.Background()

	inf := loaderImpl.newInflight(ctx)
	shard := loaderImpl.shardFor("key")
	shard.mu.Lock()
	shard.inflight["key"] = inf
	shard.mu.Unlock()

	loaderImpl.releaseInflight("key", inf, shard)
	if inf.refs != 0 {
		t.Fatalf("expected refs=0 after release, got %d", inf.refs)
	}
	if inf.done {
		t.Fatal("expected done=false before finish")
	}
	if inf.pooled {
		t.Fatal("expected pooled=false before finish")
	}

	loaderImpl.finishInflight(inf, shard, 10, nil)
	if !inf.done {
		t.Fatal("expected done=true after finish")
	}
	if !inf.pooled {
		t.Fatal("expected pooled=true after finish with refs=0")
	}

	loaderImpl.releaseInflight("key", inf, shard)
	if !inf.pooled {
		t.Fatal("expected pooled to remain true after extra release")
	}

	inf2 := loaderImpl.newInflight(ctx)
	shard.mu.Lock()
	shard.inflight["key2"] = inf2
	shard.mu.Unlock()

	loaderImpl.finishInflight(inf2, shard, 20, nil)
	if !inf2.done {
		t.Fatal("expected done=true after finish")
	}
	if inf2.pooled {
		t.Fatal("expected pooled=false before final release")
	}

	loaderImpl.releaseInflight("key2", inf2, shard)
	if !inf2.pooled {
		t.Fatal("expected pooled=true after release with done=true")
	}
}

func TestSingleflightLoader_PropagatesLoaderError(t *testing.T) {
	t.Parallel()

	loaderImpl := newSingleflightLoader[int](NoopMetricsProvider{}, 0)
	expectErr := context.Canceled
	loader := func(context.Context) (int, error) {
		return 0, expectErr
	}

	got, leader, err := loaderImpl.load(context.Background(), "key", loader)
	if err != expectErr {
		t.Fatalf("expected error %v, got %v", expectErr, err)
	}
	if !leader {
		t.Fatalf("expected leader=true, got false")
	}
	if got != 0 {
		t.Fatalf("expected zero value, got %d", got)
	}
}

func TestSingleflightLoader_LoadTimesOut(t *testing.T) {
	t.Parallel()

	timeout := 10 * time.Millisecond
	loaderImpl := newSingleflightLoader[int](NoopMetricsProvider{}, timeout)
	start := time.Now()
	loader := func(ctx context.Context) (int, error) {
		<-ctx.Done()

		return 0, ctx.Err()
	}

	_, leader, err := loaderImpl.load(context.Background(), "key", loader)
	if err != context.DeadlineExceeded {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	if !leader {
		t.Fatalf("expected leader=true, got false")
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("expected timeout to fire promptly, took %v", time.Since(start))
	}
}

func TestSingleflightLoader_NewInflightNoTimeout(t *testing.T) {
	t.Parallel()

	loaderImpl := newSingleflightLoader[int](NoopMetricsProvider{}, 0)
	loader := func(ctx context.Context) (int, error) {
		if _, ok := ctx.Deadline(); ok {
			return 0, errors.New("unexpected deadline")
		}
		time.Sleep(20 * time.Millisecond)

		return 1, nil
	}

	got, leader, err := loaderImpl.load(context.Background(), "key", loader)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !leader {
		t.Fatalf("expected leader=true, got false")
	}
	if got != 1 {
		t.Fatalf("expected value 1, got %d", got)
	}
}

func TestDirectLoader_LoadSuccess(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	loader := func(context.Context) (int, error) {
		return 7, nil
	}

	impl := directLoader[int]{}
	got, leader, err := impl.load(ctx, "key", loader)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !leader {
		t.Fatalf("expected leader=true, got false")
	}
	if got != 7 {
		t.Fatalf("expected value 7, got %d", got)
	}
}

func TestDirectLoader_LoadError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	expectErr := context.Canceled
	loader := func(context.Context) (int, error) {
		return 0, expectErr
	}

	impl := directLoader[int]{}
	got, leader, err := impl.load(ctx, "key", loader)
	if err != expectErr {
		t.Fatalf("expected error %v, got %v", expectErr, err)
	}
	if !leader {
		t.Fatalf("expected leader=true, got false")
	}
	if got != 0 {
		t.Fatalf("expected zero value, got %d", got)
	}
}

func TestDirectLoader_LoadUsesContext(t *testing.T) {
	t.Parallel()

	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "ok")
	loader := func(ctx context.Context) (string, error) {
		value, _ := ctx.Value(ctxKey{}).(string)

		return value, nil
	}

	impl := directLoader[string]{}
	got, leader, err := impl.load(ctx, "key", loader)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !leader {
		t.Fatalf("expected leader=true, got false")
	}
	if got != "ok" {
		t.Fatalf("expected value \"ok\", got %q", got)
	}
}
