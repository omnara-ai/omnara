package orglifecycle

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
)

type ReconcileDefaultsInput struct {
	Apply                bool
	DefaultMachinePools  []executionstore.DefaultMachinePoolTemplate
	DefaultModelProvider *modelstore.DefaultModelProviderTemplate
}

type ReconcileDefaultsResult struct {
	Changes  []string
	Warnings []string
}

func (s *Service) ReconcileDefaults(
	ctx context.Context,
	input ReconcileDefaultsInput,
) (ReconcileDefaultsResult, error) {
	txOptions := pgx.TxOptions{}
	if !input.Apply {
		txOptions.AccessMode = pgx.ReadOnly
	}
	tx, err := s.pool.BeginTx(ctx, txOptions)
	if err != nil {
		return ReconcileDefaultsResult{}, fmt.Errorf("begin default reconciliation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	changes, err := s.execution.ReconcileDefaultMachinePoolsTx(
		ctx,
		tx,
		input.DefaultMachinePools,
		input.Apply,
	)
	if err != nil {
		return ReconcileDefaultsResult{}, err
	}
	modelChanges, warnings, err := s.models.ReconcileDefaultModelProviderTx(
		ctx,
		tx,
		input.DefaultModelProvider,
		input.Apply,
	)
	if err != nil {
		return ReconcileDefaultsResult{}, err
	}
	changes = append(changes, modelChanges...)
	if err := tx.Commit(ctx); err != nil {
		return ReconcileDefaultsResult{}, fmt.Errorf("commit default reconciliation: %w", err)
	}
	return ReconcileDefaultsResult{Changes: changes, Warnings: warnings}, nil
}
