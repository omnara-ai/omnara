package maintenance

import (
	"errors"
	"testing"

	"github.com/omnara-ai/omnara/observability/metrics"
)

func TestProviderRuntimeResultUsesCompletedOutcome(t *testing.T) {
	for _, test := range []struct {
		name    string
		outcome maintenanceOutcome
		want    metrics.ProviderRuntimeResult
	}{
		{name: "success", want: metrics.ProviderRuntimeResultSuccess},
		{
			name:    "failure",
			outcome: maintenanceOutcome{err: errors.New("provider failed")},
			want:    metrics.ProviderRuntimeResultError,
		},
		{
			name:    "shutdown cancellation",
			outcome: maintenanceOutcome{interrupted: true},
			want:    metrics.ProviderRuntimeResultCanceled,
		},
		{
			name: "failure during shutdown",
			outcome: maintenanceOutcome{
				interrupted: true,
				err:         errors.New("provider failed"),
			},
			want: metrics.ProviderRuntimeResultError,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := providerRuntimeResult(test.outcome); got != test.want {
				t.Fatalf("provider runtime result = %q, want %q", got, test.want)
			}
		})
	}
}
