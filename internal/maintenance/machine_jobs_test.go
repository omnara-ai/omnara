package maintenance

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
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

func TestArchiveIdleAgentsReturnsMachineDeletionFailure(t *testing.T) {
	failure := errors.New("provider failed")
	var output bytes.Buffer
	run := func(ctx context.Context, err error) maintenanceResult {
		output.Reset()
		return archiveIdleAgents(slog.New(slog.NewJSONHandler(&output, nil)),
			func(context.Context, int) ([]executionstore.MachineRecord, int, bool, error) {
				return []executionstore.MachineRecord{{}}, 1, true, nil
			},
			func(context.Context, []executionstore.MachineRecord) (int, error) { return 0, err },
		)(ctx)
	}
	if result := run(t.Context(), nil); result != (maintenanceResult{worked: true, more: true}) {
		t.Fatalf("archived and deleted = %+v, want worked and more", result)
	}
	if result := run(t.Context(), failure); !errors.Is(result.err, failure) {
		t.Fatalf("failed machine deletion returned %v, want %v", result.err, failure)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if result := run(canceled, context.Canceled); result.err != nil || output.Len() > 0 {
		t.Fatalf("shutdown returned %v and logged %q, want neither", result.err, output.String())
	}
}
