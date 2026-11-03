package agentexecution

import (
	"time"

	"github.com/google/uuid"
)

type AgentState string

const (
	AgentActive   AgentState = "active"
	AgentArchived AgentState = "archived"
)

type Operation string

const (
	OperationNormal     Operation = "normal"
	OperationCompaction Operation = "compaction"
)

type ContextState string

const (
	ContextStarted   ContextState = "started"
	ContextSucceeded ContextState = "succeeded"
	ContextFailed    ContextState = "failed"
	ContextCanceled  ContextState = "canceled"
)

type RecoveryKind string

const (
	RecoveryNone    RecoveryKind = ""
	RecoveryRetry   RecoveryKind = "retry"
	RecoveryCompact RecoveryKind = "compact"
	RecoveryReduce  RecoveryKind = "reduce_compaction_source"
)

type Opening struct {
	InputIDs      []uuid.UUID
	EventSequence int64
}

type EventBoundary struct {
	Sequence int64
	Time     time.Time
}

type Turn struct {
	ID                   uuid.UUID
	FirstOpeningSequence int64
	LastOpeningSequence  int64
	FirstContentSequence int64
	LatestSemantic       EventBoundary
	InitialOpening       Opening
	InitialReadyAt       time.Time
}

type ModelContext struct {
	ID                     uuid.UUID
	AgentID                uuid.UUID
	TurnID                 uuid.UUID
	Operation              Operation
	Attempt                int32
	InputEventSequence     int64
	SourceEventSequenceEnd int64
	State                  ContextState
	Recovery               RecoveryKind
	RetryAt                *time.Time
	Opening                Opening
}

type ModelOutput struct {
	ID      uuid.UUID
	EventID uuid.UUID
	Event   EventBoundary
	Context ModelContext
}

type BatchCompletion struct {
	LastResultSequence int64
	ReadyAt            time.Time
}

type ToolBatch struct {
	Output        ModelOutput
	HasIncomplete bool
	HasRunnable   bool
	Completion    *BatchCompletion
}

type BoundaryWork struct {
	ID      uuid.UUID
	AgentID uuid.UUID
	TurnID  uuid.UUID
	Event   EventBoundary
	Opening Opening
}

type InputAvailability struct {
	Steering bool
	Queued   bool
}

type ExecutionView struct {
	AgentID                 uuid.UUID
	State                   AgentState
	Turn                    *Turn
	StopSequence            int64
	MaxNormalInputSequence  int64
	MaxContextInputSequence int64
	NormalContext           *ModelContext
	CompactionContext       *ModelContext
	ToolBatch               *ToolBatch
	OutputLimit             *ModelOutput
	Config                  *BoundaryWork
	Checkpoint              *BoundaryWork
	Inputs                  InputAvailability
}

type WorkKind uint8

const (
	WorkNone WorkKind = iota
	WorkTool
	WorkModel
	WorkInput
)

type AdmissionKind uint8

const (
	AdmitNone AdmissionKind = iota
	AdmitAllSteering
	AdmitOneQueued
)

type WaitKind uint8

const (
	WaitNone WaitKind = iota
	WaitIdle
	WaitToolBatch
	WaitModelDeadline
	WaitArchived
)

type ModelKind string

const (
	ModelStart    ModelKind = "start"
	ModelResume   ModelKind = "resume"
	ModelContinue ModelKind = "continue"
)

type CandidateOrigin uint8

const (
	OriginInitial CandidateOrigin = iota + 1
	OriginRetry
	OriginSemantic
	OriginConfig
	OriginTools
	OriginOutputLimit
	OriginCheckpoint
)

type ModelDecision struct {
	Kind               ModelKind
	Origin             CandidateOrigin
	TurnID             uuid.UUID
	SourceContextID    uuid.UUID
	SourceOutputID     uuid.UUID
	SourceInputID      uuid.UUID
	SourceCheckpointID uuid.UUID
	Opening            Opening
	ReadyAt            time.Time
}

type ToolDecision struct {
	TurnID          uuid.UUID
	SourceContextID uuid.UUID
	OutputID        uuid.UUID
	SourceEventID   uuid.UUID
}

type Selection struct {
	Work            WorkKind
	Admission       AdmissionKind
	Wait            WaitKind
	Model           *ModelDecision
	Tool            *ToolDecision
	TurnContinuable bool
	IncompleteTools bool
	LogicalReadyAt  *time.Time
}
