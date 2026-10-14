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
	_, err := agentexecution.Retry(ctx,
		"record_terminal_compaction_failure",
		func(ctx context.Context) (struct{}, error) {
			unit, h, err := beginExecution(ctx, s, input.ProjectID, input.AgentID, true, input.RuntimeLockID)
			if err != nil {
				return struct{}{}, err
			}
			defer func() { _ = unit.Rollback(ctx) }()
			_, err = h.FailModel(
				ctx,
				executionTerminalFailure(RecordModelCallErrorAndCompleteContextInput(input)),
			)
			if err != nil {
				return struct{}{}, err
			}
			return struct{}{}, unit.Commit(ctx, "record terminal compaction failure")
		})
	return err
}
