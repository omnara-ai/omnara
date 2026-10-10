package executionstore

import (
	"context"
	"errors"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func (s *Store) RecordRetryableModelCallFailure(
	ctx context.Context,
	input RecordRecoverableModelCallFailureInput,
) (ModelCallContextRecord, error) {
	if input.RecoveryKind == "" {
		input.RecoveryKind = ModelCallRecoveryRetry
	}
	if input.RecoveryKind != ModelCallRecoveryRetry {
		return ModelCallContextRecord{}, errors.New("retry recovery is required")
	}
	unit, h, err := beginExecution(ctx, s, input.ProjectID, input.AgentID)
	if err != nil {
		return ModelCallContextRecord{}, err
	}
	defer func() { _ = unit.Rollback(ctx) }()
	if _, err = h.FailModel(ctx, executionFailure(input)); err != nil {
		return ModelCallContextRecord{}, err
	}
	record, err := loadModelCallContextByID(
		ctx,
		dbsqlc.New(unit.DB()),
		input.ProjectID,
		input.AgentID,
		input.ModelCallContextID,
	)
	if err != nil {
		return ModelCallContextRecord{}, err
	}
	if err = unit.Commit(ctx, "record retryable failure"); err != nil {
		return ModelCallContextRecord{}, err
	}
	return record, nil
}
