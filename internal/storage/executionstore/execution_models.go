package executionstore

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func prepareExecutionModel(
	ctx context.Context,
	unit *agentexecution.Unit,
	input PrepareNormalModelCallInput,
) (PreparedNormalModelCall, error) {
	h, err := unit.Handle(input.ProjectID, input.AgentID)
	if err != nil {
		return PreparedNormalModelCall{}, err
	}
	prepared, err := h.PrepareNormal(ctx, agentexecution.PrepareNormalInput{
		RuntimeLockID:   input.RuntimeLockID,
		OpeningInputIDs: input.OpeningInputIDs,
		SourceContextID: input.SourceModelCallContextID,
		SourceOutputID:  input.SourceModelOutputID,
	})
	if err != nil {
		return PreparedNormalModelCall{}, err
	}
	return shapePreparedModel(ctx, unit, input.ProjectID, input.AgentID, prepared)
}

func executionFailure(input RecordRecoverableModelCallFailureInput) agentexecution.ModelFailure {
	failure := executionFailureDetails(input.ModelCallContextID, input.RuntimeLockID,
		input.ErrorKind, input.ErrorCode, input.ErrorMessage, input.ErrorDetails,
		executionEvidence(input.APIFormat, input.APIVariant, input.ProviderRequestID, input.ProviderResponseID,
			input.Usage, input.ProviderReportedCostUSD, input.ProviderMetadata))
	failure.Recovery = agentexecution.RecoveryKind(input.RecoveryKind)
	failure.RetryDelay = input.RetryDelay
	return failure
}

func executionClaim(
	ctx context.Context,
	unit *agentexecution.Unit,
	projectID, agentID uuid.UUID,
	prepared agentexecution.PreparedModel,
) (ModelCallClaim, error) {
	if prepared.Context.ID == uuid.Nil {
		return ModelCallClaim{}, nil
	}
	record, err := loadModelCallContextByID(
		ctx,
		dbsqlc.New(unit.DB()),
		projectID,
		agentID,
		prepared.Context.ID,
	)
	return ModelCallClaim{Context: record, Claimed: prepared.Claimed, Created: prepared.Created}, err
}

func executionTerminalFailure(input RecordModelCallErrorAndCompleteContextInput) agentexecution.ModelFailure {
	failure := executionFailureDetails(input.ModelCallContextID, input.RuntimeLockID,
		input.ErrorKind, input.ErrorCode, input.ErrorMessage, input.ErrorDetails,
		executionEvidence(input.APIFormat, input.APIVariant, input.ProviderRequestID, input.ProviderResponseID,
			input.Usage, input.ProviderReportedCostUSD, input.ProviderMetadata))
	failure.ServedModel = input.ServedProviderModelSlug
	return failure
}

func executionFailureDetails(contextID, runtimeID uuid.UUID, kind modelprotocol.ErrorKind,
	code, message string, details json.RawMessage, evidence agentexecution.ModelEvidence) agentexecution.ModelFailure {
	return agentexecution.ModelFailure{
		ContextID: contextID, RuntimeLockID: runtimeID, ErrorKind: kind,
		ErrorCode: code, ErrorMessage: message, ErrorDetails: details, Evidence: evidence,
	}
}

func executionEvidence(format modelprotocol.APIFormat, variant modelprotocol.APIVariant,
	requestID, responseID string, usage modelenvelope.Usage, cost modelenvelope.ProviderReportedCostUSD,
	metadata modelenvelope.ProviderMetadata) agentexecution.ModelEvidence {
	return agentexecution.ModelEvidence{APIFormat: format, APIVariant: variant, RequestID: requestID,
		ResponseID: responseID, Usage: usage, Cost: cost, Metadata: metadata}
}
