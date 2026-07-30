package crema

import "context"

// LoadReason describes what triggered a load.
type LoadReason int

const (
	// LoadReasonMiss indicates that no usable cache entry was found.
	LoadReasonMiss LoadReason = iota
	// LoadReasonExpired indicates that the cached entry was already expired.
	LoadReasonExpired
	// LoadReasonRevalidation indicates that the cached entry was still valid but
	// was picked up by probabilistic revalidation.
	LoadReasonRevalidation
	// LoadReasonGetError indicates that reading or decoding the cached entry failed.
	LoadReasonGetError
)

// String returns a stable, metric-friendly name for the reason.
func (r LoadReason) String() string {
	switch r {
	case LoadReasonMiss:
		return "miss"
	case LoadReasonExpired:
		return "expired"
	case LoadReasonRevalidation:
		return "revalidation"
	case LoadReasonGetError:
		return "get_error"
	default:
		return "unknown"
	}
}

// MetricsProvider receives cache and loader events for instrumentation.
// Implementations must be safe for concurrent use and should avoid blocking.
//
// Load latency is intentionally not reported here; wrap your CacheLoadFunc to
// measure it with your APM.
//
// Embed BaseMetricsProvider to implement only the callbacks you need.
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
	// RecordLoadReason is called with the reason that triggered the load.
	RecordLoadReason(ctx context.Context, reason LoadReason)
	// RecordLoadError is called once when a loader execution fails.
	RecordLoadError(ctx context.Context)
	// RecordLoadConcurrency is called when a load finishes with the number of
	// callers sharing that loader execution.
	RecordLoadConcurrency(ctx context.Context, concurrency int)
}

// BaseMetricsProvider is a no-op metrics implementation.
type BaseMetricsProvider struct{}

func (BaseMetricsProvider) RecordCacheHit(context.Context)               {}
func (BaseMetricsProvider) RecordCacheGet(context.Context)               {}
func (BaseMetricsProvider) RecordCacheSet(context.Context)               {}
func (BaseMetricsProvider) RecordCacheDelete(context.Context)            {}
func (BaseMetricsProvider) RecordLoad(context.Context)                   {}
func (BaseMetricsProvider) RecordLoadReason(context.Context, LoadReason) {}
func (BaseMetricsProvider) RecordLoadError(context.Context)              {}
func (BaseMetricsProvider) RecordLoadConcurrency(context.Context, int)   {}

type NoopMetricsProvider struct {
	BaseMetricsProvider
}
