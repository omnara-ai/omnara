package executionstore

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

const (
	MaxModelCallRetriesPerOperation = 8

	baseModelCallRetryBackoff = time.Second
	maxModelCallRetryBackoff  = 30 * time.Second

	ModelCallOperationNormal     ModelCallOperation = "normal"
	ModelCallOperationCompaction ModelCallOperation = "compaction"

	ModelCallContextStarted   ModelCallState = "started"
	ModelCallContextSucceeded ModelCallState = "succeeded"
	ModelCallContextFailed    ModelCallState = "failed"
	ModelCallContextCanceled  ModelCallState = "canceled"

	ModelCallRecoveryRetry                  ModelCallRecoveryKind = "retry"
	ModelCallRecoveryCompact                ModelCallRecoveryKind = "compact"
	ModelCallRecoveryReduceCompactionSource ModelCallRecoveryKind = "reduce_compaction_source"
)

type ModelCallOperation string

type ModelCallState string

type ModelCallRecoveryKind string

type ModelCallContextRecord struct {
	ID                        uuid.UUID                             `json:"id"`
	OrgID                     uuid.UUID                             `json:"org_id"`
	ProjectID                 uuid.UUID                             `json:"project_id"`
	AgentID                   uuid.UUID                             `json:"agent_id"`
	OperationKind             ModelCallOperation                    `json:"operation_kind"`
	AttemptNumber             int                                   `json:"attempt_number"`
	AgentConfigID             uuid.UUID                             `json:"agent_config_id"`
	ConfiguredModelRevisionID uuid.UUID                             `json:"configured_model_revision_id"`
	InputEventSequence        int64                                 `json:"input_event_sequence"`
	SourceEventSequenceEnd    *int64                                `json:"source_event_sequence_end,omitempty"`
	RuntimeLockID             uuid.UUID                             `json:"runtime_lock_id"`
	State                     ModelCallState                        `json:"state"`
	RecoveryKind              ModelCallRecoveryKind                 `json:"recovery_kind,omitempty"`
	APIFormat                 modelprotocol.APIFormat               `json:"api_format,omitempty"`
	APIVariant                modelprotocol.APIVariant              `json:"api_variant,omitempty"`
	ProviderRequestID         string                                `json:"provider_request_id,omitempty"`
	ProviderResponseID        string                                `json:"provider_response_id,omitempty"`
	ErrorKind                 modelprotocol.ErrorKind               `json:"error_kind,omitempty"`
	ErrorCode                 string                                `json:"error_code,omitempty"`
	ErrorMessage              string                                `json:"error_message,omitempty"`
	ErrorDetails              json.RawMessage                       `json:"error_details"`
	RetryAt                   *time.Time                            `json:"retry_at,omitempty"`
	Usage                     modelenvelope.Usage                   `json:"usage"`
	ProviderReportedCostUSD   modelenvelope.ProviderReportedCostUSD `json:"provider_reported_cost_usd,omitempty"`
	ProviderMetadata          modelenvelope.ProviderMetadata        `json:"provider_metadata,omitzero"`
	CreatedAt                 time.Time                             `json:"created_at"`
	CompletedAt               *time.Time                            `json:"completed_at,omitempty"`
}

func ModelCallRetryBackoff(attemptNumber int, contextID string) time.Duration {
	if attemptNumber < 1 {
		attemptNumber = 1
	}
	delay := baseModelCallRetryBackoff
	for i := 1; i < attemptNumber && delay < maxModelCallRetryBackoff; i++ {
		delay *= 2
	}

	percent := deterministicModelCallRetryJitterPercent(contextID, attemptNumber)
	delay = time.Duration(int64(delay) * int64(percent) / 100)
	if delay > maxModelCallRetryBackoff {
		return maxModelCallRetryBackoff
	}
	return delay
}

func deterministicModelCallRetryJitterPercent(contextID string, attemptNumber int) int {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(fmt.Sprintf("%s:%d", contextID, attemptNumber)))
	return 80 + int(hash.Sum32()%41)
}

type ModelCallClaim struct {
	Context ModelCallContextRecord
	Created bool
	Claimed bool
}

type ClaimNormalModelCallInput struct {
	ProjectID                uuid.UUID
	AgentID                  uuid.UUID
	RuntimeLockID            uuid.UUID
	OpeningInputIDs          []uuid.UUID
	AgentConfigID            uuid.UUID
	InputEventSequence       int64
	SourceModelCallContextID uuid.UUID
	SourceModelOutputID      uuid.UUID
}

type ClaimCompactionModelCallInput struct {
	ProjectID              uuid.UUID
	AgentID                uuid.UUID
	RuntimeLockID          uuid.UUID
	InputEventSequence     int64
	SourceEventSequenceEnd int64
	ParentContextID        uuid.UUID
}

type ClaimNextModelCallContextInput struct {
	ProjectID                     uuid.UUID
	AgentID                       uuid.UUID
	PredecessorModelCallContextID uuid.UUID
	RuntimeLockID                 uuid.UUID
}

type ReplaceCompactionSourceInput struct {
	ProjectID                  uuid.UUID
	AgentID                    uuid.UUID
	RuntimeLockID              uuid.UUID
	ModelCallContextID         uuid.UUID
	APIFormat                  modelprotocol.APIFormat
	APIVariant                 modelprotocol.APIVariant
	ProviderRequestID          string
	ProviderResponseID         string
	ErrorKind                  modelprotocol.ErrorKind
	ErrorCode                  string
	ErrorMessage               string
	ErrorDetails               json.RawMessage
	Usage                      modelenvelope.Usage
	ProviderReportedCostUSD    modelenvelope.ProviderReportedCostUSD
	ProviderMetadata           modelenvelope.ProviderMetadata
	NextSourceEventSequenceEnd int64
}

type RecordRecoverableModelCallFailureInput struct {
	ProjectID               uuid.UUID
	AgentID                 uuid.UUID
	ModelCallContextID      uuid.UUID
	RuntimeLockID           uuid.UUID
	RecoveryKind            ModelCallRecoveryKind
	APIFormat               modelprotocol.APIFormat
	APIVariant              modelprotocol.APIVariant
	ProviderRequestID       string
	ProviderResponseID      string
	ErrorKind               modelprotocol.ErrorKind
	ErrorCode               string
	ErrorMessage            string
	ErrorDetails            json.RawMessage
	RetryDelay              time.Duration
	Usage                   modelenvelope.Usage
	ProviderReportedCostUSD modelenvelope.ProviderReportedCostUSD
	ProviderMetadata        modelenvelope.ProviderMetadata
}

type RecordModelCallFailureAndClaimCompactionInput struct {
	ParentContextID        uuid.UUID
	Failure                RecordRecoverableModelCallFailureInput
	SourceEventSequenceEnd int64
}

type TriggeredCompactionHandoff struct {
	ParentContext     ModelCallContextRecord
	CompactionCall    ModelCallClaim
	BoundaryPreempted bool
}

type ReplaceCompactionSourceResult struct {
	CompactionCall    ModelCallClaim
	BoundaryPreempted bool
}
