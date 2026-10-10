package executionstore

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

type RecordModelCallErrorAndCompleteContextInput struct {
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

func (s *Store) RecordModelCallErrorAndCompleteContext(
	ctx context.Context,
	input RecordModelCallErrorAndCompleteContextInput,
) (events.Event, error) {
	unit, h, err := beginExecution(ctx, s, input.ProjectID, input.AgentID)
	if err != nil {
		return events.Event{}, err
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
		return events.Event{}, err
	}
	q := dbsqlc.New(unit.DB())
	output, err := q.GetModelOutputByModelContext(
		ctx,
		dbsqlc.GetModelOutputByModelContextParams{ProjectID: input.ProjectID,
			AgentID: input.AgentID, ModelCallContextID: input.ModelCallContextID},
	)
	if err != nil {
		return events.Event{}, err
	}
	row, err := q.GetTypedAgentEventByModelOutput(ctx, dbsqlc.GetTypedAgentEventByModelOutputParams{
		ProjectID: input.ProjectID, AgentID: input.AgentID, ModelOutputID: &output.ID})
	if err != nil {
		return events.Event{}, err
	}
	event, err := typedAgentEventFromModelOutputSQLC(row)
	if err != nil {
		return events.Event{}, err
	}
	if err = unit.Commit(ctx, "record terminal model failure"); err != nil {
		return events.Event{}, err
	}
	return event.Event, nil
}
