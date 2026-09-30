package compaction

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

type ExecutionStore interface {
	CaptureAgentConfigForEventWatermark(
		ctx context.Context,
		projectID, agentID uuid.UUID,
		watermark int64,
	) (executionstore.AgentConfigSnapshotRecord, error)
	ListCompactionSourceEvents(
		ctx context.Context,
		projectID, agentID uuid.UUID,
		afterSequence int64,
		limit int32,
	) ([]executionstore.CompactionSourceEventRecord, error)
	ListCompactionAtomicGroups(
		ctx context.Context,
		projectID, agentID uuid.UUID,
		lastCheckpointEnd int64,
		inputEventSequence int64,
	) ([]executionstore.CompactionAtomicGroupRecord, error)
	GetLatestApplicableContextCheckpoint(
		ctx context.Context,
		projectID, agentID uuid.UUID,
		maxEventSequence int64,
	) (executionstore.ContextCheckpointRecord, bool, error)
	GetModelCallRecoveryState(
		ctx context.Context,
		projectID, agentID, modelCallContextID uuid.UUID,
	) (executionstore.ModelCallRecoveryState, error)
	GetProviderReplaySuppressionCutoff(
		ctx context.Context,
		projectID, agentID, modelCallContextID uuid.UUID,
	) (int64, error)
	RecordRetryableModelCallFailure(
		ctx context.Context,
		input executionstore.RecordRecoverableModelCallFailureInput,
	) (executionstore.ModelCallContextRecord, error)
	RecordTerminalCompactionFailure(
		ctx context.Context,
		input executionstore.RecordTerminalCompactionFailureInput,
	) error
	RecordCompactionFailureAndResumeNormal(
		ctx context.Context,
		input executionstore.RecordCompactionFailureAndResumeNormalInput,
	) (executionstore.ModelCallContextRecord, error)
	ReplaceCompactionSource(
		ctx context.Context,
		input executionstore.ReplaceCompactionSourceInput,
	) (executionstore.ReplaceCompactionSourceResult, error)
	PublishContextCheckpoint(
		ctx context.Context,
		input executionstore.PublishContextCheckpointInput,
	) (executionstore.ContextCheckpointRecord, error)
}

type Store interface {
	ExecutionStore
}

func NewStore(execution ExecutionStore) Store {
	return execution
}

type ContextBuilder interface {
	Build(context.Context, modelcontext.BuildInput) (modelcontext.Bundle, error)
}

type Runner struct {
	Store           Store
	Resolver        model.Resolver
	ContextBuilder  ContextBuilder
	Now             func() time.Time
	ModelRetryDelay func(time.Duration) time.Duration
}

type RunInput struct {
	Plan                     Plan
	TurnID                   uuid.UUID
	OpeningInputIDs          []uuid.UUID
	OpeningEventSequence     int64
	RuntimeLockID            uuid.UUID
	ParentModelCallContextID uuid.UUID
}

type RunState string

const (
	RunCompleted      RunState = "completed"
	RunRetryScheduled RunState = "retry_scheduled"
	RunResumeNormal   RunState = "resume_normal"
	RunTerminal       RunState = "terminal"
)

type RunResult struct {
	State              RunState
	ModelCallContextID uuid.UUID
	Checkpoint         *executionstore.ContextCheckpointRecord
	RetryAt            *time.Time
}
