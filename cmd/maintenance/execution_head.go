package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/observability/metrics"
)

const (
	executionHeadInterval   = 5 * time.Minute
	executionHeadSampleSize = 10
	executionHeadJob        = "execution_head_drift"
)

func runExecutionHeadLoop(ctx context.Context, log *slog.Logger, recorder *metrics.JobRecorder,
	sample func(context.Context, int32) (executionstore.ExecutionHeadSampleReport, error)) {
	for {
		runExecutionHeadTick(ctx, log, recorder, sample)
		timer := time.NewTimer(jitteredMaintenanceDelay(executionHeadInterval))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func runExecutionHeadTick(ctx context.Context, log *slog.Logger, recorder *metrics.JobRecorder,
	sample func(context.Context, int32) (executionstore.ExecutionHeadSampleReport, error)) {
	started := time.Now()
	report, err := sample(ctx, executionHeadSampleSize)
	result := metrics.JobResultSuccess
	var checked, mismatch, failed int64
	for _, sample := range report.Samples {
		if sample.Err != nil {
			failed++
			log.Error("sample execution head", "agent_id", sample.AgentID, "project_id", sample.ProjectID, "error", sample.Err)
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
	if err != nil || failed > 0 {
		result = metrics.JobResultError
	}
	if ctx.Err() != nil {
		result = metrics.JobResultCanceled
	}
	if err != nil && ctx.Err() == nil {
		log.Error("sample execution heads", "error", err)
	}
	recorder.RecordItems(executionHeadJob, "checked", checked)
	recorder.RecordItems(executionHeadJob, "mismatch", mismatch)
	recorder.RecordItems(executionHeadJob, "error", failed)
	recorder.RecordRun(executionHeadJob, result, time.Since(started))
}
