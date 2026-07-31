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
		{reason: LoadReasonGetError, want: "get_error"},
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
