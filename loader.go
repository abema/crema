package crema

import (
	"context"
	"hash/maphash"
	"sync"
)

const defaultShardCount = 16

var mapHashSeed = maphash.MakeSeed()

type internalLoader[V any] interface {
	load(ctx context.Context, key string, loader CacheLoadFunc[V]) (V, bool, error)
}

type inflight[V any] struct {
	ctx    context.Context
	cancel context.CancelFunc
	refs   int
	val    V
	err    error
	doneCh chan struct{}
}

var _ internalLoader[any] = (*singleflightLoader[any])(nil)

type singleflightLoader[V any] struct {
	_       noCopy
	shards  []singleflightShard[V]
	metrics MetricsProvider
}

type singleflightShard[V any] struct {
	_        noCopy
	mu       sync.Mutex
	inflight map[string]*inflight[V]
}

func (l *singleflightLoader[V]) shardFor(key string) *singleflightShard[V] {
	return &l.shards[hashKey(key)%uint64(len(l.shards))]
}

func newSingleflightLoader[V any](metrics MetricsProvider) *singleflightLoader[V] {
	shards := make([]singleflightShard[V], defaultShardCount)
	for i := range shards {
		shards[i].inflight = make(map[string]*inflight[V])
	}

	return &singleflightLoader[V]{
		shards:  shards,
		metrics: metrics,
	}
}

func newInflight[V any](ctx context.Context) *inflight[V] {
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))

	return &inflight[V]{
		ctx:    ctx,
		cancel: cancel,
		refs:   1,
		doneCh: make(chan struct{}),
	}
}

func hashKey(key string) uint64 {
	return maphash.String(mapHashSeed, key)
}

func (l *singleflightLoader[V]) acquireInflight(ctx context.Context, key string) (*inflight[V], bool, *singleflightShard[V]) {
	shard := l.shardFor(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()

	if inf, ok := shard.inflight[key]; ok {
		select {
		case <-inf.doneCh:
			newInf := newInflight[V](ctx)
			shard.inflight[key] = newInf

			return newInf, true, shard
		default:
		}
		inf.refs++

		return inf, false, shard
	} else {
		newInf := newInflight[V](ctx)
		shard.inflight[key] = newInf

		return newInf, true, shard
	}
}

func (l *singleflightLoader[V]) finishInflight(inf *inflight[V], shard *singleflightShard[V], v V, err error) {
	var refs int
	var ctx context.Context
	shard.mu.Lock()

	refs = inf.refs
	ctx = inf.ctx
	inf.val = v
	inf.err = err
	close(inf.doneCh)
	shard.mu.Unlock()

	l.metrics.RecordLoadConcurrency(ctx, refs)
}

func (l *singleflightLoader[V]) releaseInflight(key string, inf *inflight[V], shard *singleflightShard[V]) {
	shard.mu.Lock()
	inf.refs--
	if inf.refs <= 0 {
		if current, ok := shard.inflight[key]; ok && current == inf {
			delete(shard.inflight, key)
		}
		inf.cancel()
	}
	shard.mu.Unlock()
}

func (l *singleflightLoader[V]) load(ctx context.Context, key string, loader CacheLoadFunc[V]) (V, bool, error) {
	inf, leader, shard := l.acquireInflight(ctx, key)
	if leader {
		go func() {
			l.metrics.RecordLoad(ctx)

			v, err := loader(inf.ctx)
			l.finishInflight(inf, shard, v, err)
		}()
	}

	select {
	case <-ctx.Done():
		l.releaseInflight(key, inf, shard)
		var zero V

		return zero, leader, ctx.Err()
	case <-inf.doneCh:
	}
	v := inf.val
	err := inf.err
	l.releaseInflight(key, inf, shard)

	if err != nil {
		var zero V

		return zero, leader, err
	}

	return v, leader, nil
}

type directLoader[V any] struct{}

var _ internalLoader[any] = directLoader[any]{}

func (directLoader[V]) load(ctx context.Context, key string, loader CacheLoadFunc[V]) (V, bool, error) {
	v, err := loader(ctx)
	if err != nil {
		var zero V

		return zero, true, err
	}

	return v, true, nil
}
