package maintenance

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/observability/metrics"
)

const (
	executionHeadInterval   = 5 * time.Minute
	executionHeadSampleSize = 10
	executionHeadJob        = "execution_head_drift"
)

func sampleExecutionHeads(log *slog.Logger, recorder *metrics.JobRecorder,
	sample func(context.Context, uuid.UUID, int32) (executionstore.ExecutionHeadSampleReport, error),
) func(context.Context) maintenanceResult {
	recorder.InitializeItems(executionHeadJob, "checked", "mismatch", "error")
	var cursor uuid.UUID
	return func(ctx context.Context) maintenanceResult {
		var err error
		cursor, err = runExecutionHeadTick(ctx, log, recorder, cursor, sample)
		return maintenanceResult{err: err}
	}
}

func runExecutionHeadTick(ctx context.Context, log *slog.Logger, recorder *metrics.JobRecorder, cursor uuid.UUID,
	sample func(context.Context, uuid.UUID, int32) (executionstore.ExecutionHeadSampleReport, error),
) (uuid.UUID, error) {
	report, err := sample(ctx, cursor, executionHeadSampleSize)
	var checked, mismatch int64
	var failures []error
	for _, sample := range report.Samples {
		if sample.Err != nil {
			failures = append(failures, sample.Err)
			if ctx.Err() == nil {
				log.Error("sample execution head", "agent_id", sample.AgentID, "project_id", sample.ProjectID, "error", sample.Err)
			}
			continue
		}
		checked++
		if len(sample.Differences) > 0 {
			mismatch++
			fields := make([]string, 0, len(sample.Differences))
			for _, difference := range sample.Differences {
				fields = append(fields, difference.Field)
			}
			log.Error("execution head drift", "agent_id", sample.AgentID, "project_id", sample.ProjectID,
				"fields", fields, "differences", sample.Differences)
		}
	}
	if err != nil && ctx.Err() == nil {
		log.Error("sample execution heads", "error", err)
	}
	recorder.RecordItems(executionHeadJob, "checked", checked)
	recorder.RecordItems(executionHeadJob, "mismatch", mismatch)
	recorder.RecordItems(executionHeadJob, "error", int64(len(failures)))
	return report.Cursor, completedMaintenanceOutcome(ctx, errors.Join(append(failures, err)...)).err
}
