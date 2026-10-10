package executionstore

import (
	"context"

	"github.com/google/uuid"
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
	return agentexecution.ModelFailure{
		ContextID:     input.ModelCallContextID,
		RuntimeLockID: input.RuntimeLockID,
		Recovery:      agentexecution.RecoveryKind(input.RecoveryKind),
		RetryDelay:    input.RetryDelay,
		ErrorKind:     input.ErrorKind,
		ErrorCode:     input.ErrorCode,
		ErrorMessage:  input.ErrorMessage,
		ErrorDetails:  input.ErrorDetails,
		Evidence: agentexecution.ModelEvidence{APIFormat: input.APIFormat, APIVariant: input.APIVariant,
			RequestID: input.ProviderRequestID, ResponseID: input.ProviderResponseID, Usage: input.Usage,
			Cost: input.ProviderReportedCostUSD, Metadata: input.ProviderMetadata},
	}
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
