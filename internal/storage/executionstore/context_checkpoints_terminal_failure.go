package executionstore

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
)

type RecordTerminalCompactionFailureInput struct {
	ProjectID               uuid.UUID
	AgentID                 uuid.UUID
	RuntimeLockID           uuid.UUID
	ModelCallContextID      uuid.UUID
	APIFormat               modelprotocol.APIFormat
	APIVariant              modelprotocol.APIVariant
	ServedProviderModelSlug string
	ProviderRequestID       string
	ProviderResponseID      string
	ErrorKind               modelprotocol.ErrorKind
	ErrorCode               string
	ErrorMessage            string
	ErrorDetails            json.RawMessage
	Usage                   modelenvelope.Usage
	ProviderReportedCostUSD modelenvelope.ProviderReportedCostUSD
	ProviderMetadata        modelenvelope.ProviderMetadata
}

func (s *Store) RecordTerminalCompactionFailure(
	ctx context.Context,
	input RecordTerminalCompactionFailureInput,
) error {
	unit, h, err := beginExecution(ctx, s, input.ProjectID, input.AgentID)
	if err != nil {
		return err
	}
	defer func() { _ = unit.Rollback(ctx) }()
	_, err = h.FailModel(
		ctx,
		agentexecution.ModelFailure{ContextID: input.ModelCallContextID, RuntimeLockID: input.RuntimeLockID,
			ErrorKind:    input.ErrorKind,
			ErrorCode:    input.ErrorCode,
			ErrorMessage: input.ErrorMessage,
			ErrorDetails: input.ErrorDetails,
			ServedModel:  input.ServedProviderModelSlug,
			Evidence: agentexecution.ModelEvidence{APIFormat: input.APIFormat,
				APIVariant: input.APIVariant, RequestID: input.ProviderRequestID, ResponseID: input.ProviderResponseID,
				Usage: input.Usage, Cost: input.ProviderReportedCostUSD, Metadata: input.ProviderMetadata}},
	)
	if err != nil {
		return err
	}
	return unit.Commit(ctx, "record terminal compaction failure")
}
