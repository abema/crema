package crema

import "context"

// MetricsProvider receives cache and loader events for instrumentation.
// Implementations must be safe for concurrent use and should avoid blocking.
type MetricsProvider interface {
	// RecordCacheHit is called when a cached value is successfully returned.
	RecordCacheHit(ctx context.Context)
	// RecordCacheGet is called when a cache lookup is attempted.
	RecordCacheGet(ctx context.Context)
	// RecordCacheSet is called when a cache write is attempted.
	RecordCacheSet(ctx context.Context)
	// RecordCacheDelete is called when a cache delete is attempted.
	RecordCacheDelete(ctx context.Context)
	// RecordLoad is called when a load is started by the leader.
	RecordLoad(ctx context.Context)
	// RecordLoadConcurrency is called when a load finishes with the inflight count.
	RecordLoadConcurrency(ctx context.Context, concurrency int)
	// RecordLoadError is called when a load fails, including when the failure is
	// absorbed by the stale fallback.
	RecordLoadError(ctx context.Context)
}

type BaseMetricsProvider struct{}

func (BaseMetricsProvider) RecordCacheHit(context.Context)             {}
func (BaseMetricsProvider) RecordCacheGet(context.Context)             {}
func (BaseMetricsProvider) RecordCacheSet(context.Context)             {}
func (BaseMetricsProvider) RecordCacheDelete(context.Context)          {}
func (BaseMetricsProvider) RecordLoad(context.Context)                 {}
func (BaseMetricsProvider) RecordLoadConcurrency(context.Context, int) {}
func (BaseMetricsProvider) RecordLoadError(context.Context)            {}

type NoopMetricsProvider struct {
	BaseMetricsProvider
}
