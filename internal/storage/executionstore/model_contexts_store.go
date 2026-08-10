package executionstore

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"time"

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
	ID                        ID                                    `json:"id"`
	OrgID                     ID                                    `json:"org_id"`
	ProjectID                 ID                                    `json:"project_id"`
	AgentID                   ID                                    `json:"agent_id"`
	OperationKind             ModelCallOperation                    `json:"operation_kind"`
	AttemptNumber             int                                   `json:"attempt_number"`
	AgentConfigID             ID                                    `json:"agent_config_id"`
	ConfiguredModelRevisionID ID                                    `json:"configured_model_revision_id"`
	InputEventSequence        int64                                 `json:"input_event_sequence"`
	SourceEventSequenceEnd    *int64                                `json:"source_event_sequence_end,omitempty"`
	RuntimeLockID             ID                                    `json:"runtime_lock_id"`
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
	ProjectID                ID
	AgentID                  ID
	RuntimeLockID            ID
	OpeningInputIDs          []ID
	AgentConfigID            ID
	InputEventSequence       int64
	SourceModelCallContextID ID
	SourceModelOutputID      ID
}

type ClaimCompactionModelCallInput struct {
	ProjectID              ID
	AgentID                ID
	RuntimeLockID          ID
	InputEventSequence     int64
	SourceEventSequenceEnd int64
	ParentContextID        ID
}

type ClaimNextModelCallContextInput struct {
	ProjectID                     ID
	AgentID                       ID
	PredecessorModelCallContextID ID
	RuntimeLockID                 ID
}

type ReplaceCompactionSourceInput struct {
	ProjectID                  ID
	AgentID                    ID
	RuntimeLockID              ID
	ModelCallContextID         ID
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
	NextSourceEventSequenceEnd int64
}

type RecordRecoverableModelCallFailureInput struct {
	ProjectID               ID
	AgentID                 ID
	ModelCallContextID      ID
	RuntimeLockID           ID
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
}

type RecordModelCallFailureAndClaimCompactionInput struct {
	ParentContextID        ID
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
