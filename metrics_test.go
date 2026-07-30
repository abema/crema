package crema

import "testing"

func TestLoadReason_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		reason LoadReason
		want   string
	}{
		{reason: LoadReasonMiss, want: "miss"},
		{reason: LoadReasonExpired, want: "expired"},
		{reason: LoadReasonRevalidation, want: "revalidation"},
		{reason: LoadReason(99), want: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()

			if got := tt.reason.String(); got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestBaseMetricsProvider_ImplementsMetricsProvider(t *testing.T) {
	t.Parallel()

	var provider MetricsProvider = NoopMetricsProvider{}

	ctx := t.Context()
	provider.RecordCacheHit(ctx)
	provider.RecordCacheGet(ctx)
	provider.RecordCacheSet(ctx)
	provider.RecordCacheDelete(ctx)
	provider.RecordLoad(ctx)
	provider.RecordLoadConcurrency(ctx, 1)

	var reasonProvider LoadReasonMetricsProvider = NoopMetricsProvider{}
	reasonProvider.RecordLoadReason(ctx, LoadReasonRevalidation)

	var errorProvider LoadErrorMetricsProvider = NoopMetricsProvider{}
	errorProvider.RecordLoadError(ctx)
}
