package compaction

import (
	"context"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

type ExecutionStore interface {
	CaptureAgentConfigForEventWatermark(
		ctx context.Context,
		projectID, agentID storage.ID,
		watermark int64,
	) (executionstore.AgentConfigSnapshotRecord, error)
	ListCompactionSourceEvents(
		ctx context.Context,
		projectID, agentID storage.ID,
		afterSequence int64,
		limit int32,
	) ([]executionstore.CompactionSourceEventRecord, error)
	ListCompactionAtomicGroups(
		ctx context.Context,
		projectID, agentID storage.ID,
		lastCheckpointEnd int64,
		inputEventSequence int64,
	) ([]executionstore.CompactionAtomicGroupRecord, error)
	GetLatestApplicableContextCheckpoint(
		ctx context.Context,
		projectID, agentID storage.ID,
		maxEventSequence int64,
	) (executionstore.ContextCheckpointRecord, bool, error)
	GetContextCheckpointByProducerContext(
		ctx context.Context,
		projectID, agentID, modelCallContextID storage.ID,
	) (executionstore.ContextCheckpointRecord, bool, error)
	CountConsecutiveContextCheckpointLineage(
		ctx context.Context,
		projectID, agentID storage.ID,
		inputEventSequence int64,
	) (int, error)
	ModelCallOperationHasFailedWithErrorKind(
		ctx context.Context,
		projectID, agentID, modelCallContextID storage.ID,
		errorKind modelprotocol.ErrorKind,
	) (bool, error)
	ClaimCompactionModelCall(
		ctx context.Context,
		input executionstore.ClaimCompactionModelCallInput,
	) (executionstore.ModelCallClaim, error)
	ClaimNextModelCallContext(
		ctx context.Context,
		input executionstore.ClaimNextModelCallContextInput,
	) (executionstore.ModelCallClaim, error)
	RecordRetryableModelCallFailure(
		ctx context.Context,
		input executionstore.RecordRecoverableModelCallFailureInput,
	) (executionstore.ModelCallContextRecord, error)
	RecordTerminalCompactionFailure(
		ctx context.Context,
		input executionstore.RecordTerminalCompactionFailureInput,
	) error
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
	Store             Store
	Resolver          model.Resolver
	ContextBuilder    ContextBuilder
	ProgressivePolicy ProgressiveCheckpointPolicy
	Now               func() time.Time
	ModelRetryDelay   func(time.Duration) time.Duration
}

type RunInput struct {
	Plan                     Plan
	TurnID                   storage.ID
	OpeningInputIDs          []storage.ID
	OpeningEventSequence     int64
	RuntimeLockID            storage.ID
	ParentModelCallContextID storage.ID
}

type RunState string

const (
	RunCompleted      RunState = "completed"
	RunRetryScheduled RunState = "retry_scheduled"
	RunTerminal       RunState = "terminal"
)

type RunResult struct {
	State              RunState
	ModelCallContextID storage.ID
	Checkpoint         *executionstore.ContextCheckpointRecord
	RetryAt            *time.Time
}
