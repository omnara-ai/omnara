package maintenance

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/omnara-ai/omnara/internal/machinepool"
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

func TestProviderRuntimeFailuresLeaveRetryToScopeCooldown(t *testing.T) {
	failure := errors.New("provider failed")
	run := func(stats machinepool.RuntimeReconciliationStats) maintenanceResult {
		return reconcileProviderRuntimes(slog.New(slog.DiscardHandler), nil, metrics.ProviderRuntimeOperationDiscovery,
			func(context.Context) (machinepool.RuntimeReconciliationStats, error) { return stats, failure },
		)(t.Context())
	}
	cooling := machinepool.RuntimeReconciliationStats{ProviderErrors: 1, Failures: 1}
	if result := run(cooling); !errors.Is(result.err, failure) || !result.retryScheduled {
		t.Fatalf("scope failure = %+v, want its error with the retry left to the scope cooldown", result)
	}
	mixed := machinepool.RuntimeReconciliationStats{ProviderErrors: 1, Failures: 2}
	if result := run(mixed); !errors.Is(result.err, failure) || result.retryScheduled {
		t.Fatalf("scope failure with a database failure = %+v, want an early retry", result)
	}
	if result := run(machinepool.RuntimeReconciliationStats{}); !errors.Is(result.err, failure) || result.retryScheduled {
		t.Fatalf("pass failure = %+v, want an early retry", result)
	}
}

func TestArchiveIdleAgentsReturnsMachineDeletionFailure(t *testing.T) {
	archiveFailure, deleteFailure := errors.New("store failed"), errors.New("provider failed")
	var output bytes.Buffer
	run := func(ctx context.Context, archiveErr, deleteErr error) maintenanceResult {
		output.Reset()
		return archiveIdleAgents(slog.New(slog.NewJSONHandler(&output, nil)),
			func(context.Context, int) ([]executionstore.MachineRecord, int, bool, error) {
				return []executionstore.MachineRecord{{}}, 1, true, archiveErr
			},
			func(context.Context, []executionstore.MachineRecord) (int, error) { return 0, deleteErr },
		)(ctx)
	}
	if result := run(t.Context(), nil, nil); result != (maintenanceResult{worked: true, more: true}) {
		t.Fatalf("archived and deleted = %+v, want worked and more", result)
	}
	if result := run(t.Context(), nil, deleteFailure); !errors.Is(result.err, deleteFailure) || !result.retryScheduled {
		t.Fatalf("failed machine deletion = %+v, want its error with the retry left to machine cleanup", result)
	}
	if result := run(t.Context(), archiveFailure, deleteFailure); !errors.Is(result.err, archiveFailure) ||
		result.retryScheduled {
		t.Fatalf("failed archive = %+v, want an early retry", result)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if result := run(canceled, nil, context.Canceled); result.err != nil || output.Len() > 0 {
		t.Fatalf("shutdown returned %v and logged %q, want neither", result.err, output.String())
	}
}
