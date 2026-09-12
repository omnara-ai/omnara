package orglifecycle

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
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
	var orgIDs []ID
	seen := make(map[ID]bool)
	pools := make(map[ID][]dbsqlc.MachinePool)
	providers := make(map[ID][]dbsqlc.ModelProviderConfig)
	addOrg := func(orgID ID) {
		if !seen[orgID] {
			seen[orgID] = true
			orgIDs = append(orgIDs, orgID)
		}
	}
	for index, template := range input.DefaultMachinePools {
		if err := s.execution.ValidateDefaultMachinePoolTemplate(template); err != nil {
			return ReconcileDefaultsResult{}, fmt.Errorf("default machine pool: %w", err)
		}
		name, err := resourcename.CanonicalizeRequired("machine pool name", template.Name)
		if err != nil {
			return ReconcileDefaultsResult{}, fmt.Errorf("default machine pool: %w", err)
		}
		input.DefaultMachinePools[index].Name = name
		rows, err := s.q.ListClusterManagedMachinePoolsByName(
			ctx,
			dbsqlc.ListClusterManagedMachinePoolsByNameParams{Name: name},
		)
		if err != nil {
			return ReconcileDefaultsResult{}, fmt.Errorf("list default machine pools %q: %w", name, err)
		}
		if len(rows) == 0 {
			return ReconcileDefaultsResult{}, fmt.Errorf("no cluster-managed machine pools named %q", name)
		}
		for _, row := range rows {
			addOrg(row.OrgID)
			pools[row.OrgID] = append(pools[row.OrgID], row)
		}
	}
	if input.DefaultModelProvider != nil {
		prepared, err := modelstore.PrepareDefaultModelProviderTemplate(*input.DefaultModelProvider)
		if err != nil {
			return ReconcileDefaultsResult{}, fmt.Errorf("default model provider %q: %w", input.DefaultModelProvider.Name, err)
		}
		rows, err := s.q.ListClusterManagedModelProviderConfigsByName(
			ctx,
			dbsqlc.ListClusterManagedModelProviderConfigsByNameParams{Name: prepared.Name},
		)
		if err != nil {
			return ReconcileDefaultsResult{}, fmt.Errorf("list default model providers %q: %w", prepared.Name, err)
		}
		if len(rows) == 0 {
			return ReconcileDefaultsResult{}, fmt.Errorf("no cluster-managed model providers named %q", prepared.Name)
		}
		input.DefaultModelProvider = &prepared
		for _, row := range rows {
			addOrg(row.OrgID)
			providers[row.OrgID] = append(providers[row.OrgID], row)
		}
	}

	var result ReconcileDefaultsResult
	var reconcileErrs []error
	for _, orgID := range orgIDs {
		orgResult, err := s.reconcileOrgDefaults(ctx, orgID, input, pools[orgID], providers[orgID])
		if err != nil {
			reconcileErrs = append(reconcileErrs, fmt.Errorf("org %s: %w", orgID, err))
			if ctx.Err() != nil {
				break
			}
			continue
		}
		result.Changes = append(result.Changes, orgResult.Changes...)
		result.Warnings = append(result.Warnings, orgResult.Warnings...)
	}
	return result, errors.Join(reconcileErrs...)
}

func (s *Service) reconcileOrgDefaults(
	ctx context.Context,
	orgID ID,
	input ReconcileDefaultsInput,
	pools []dbsqlc.MachinePool,
	providers []dbsqlc.ModelProviderConfig,
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
	qtx := dbsqlc.New(tx)
	if input.Apply {
		if err := lifecyclelock.EnterActiveOrganization(ctx, tx, orgID); err != nil {
			return ReconcileDefaultsResult{}, err
		}
	}
	defaultProjectID := NilID
	if input.DefaultModelProvider != nil {
		project, projectErr := qtx.GetProjectByIdempotencyKey(
			ctx,
			dbsqlc.GetProjectByIdempotencyKeyParams{
				OrgID: orgID, IdempotencyKey: identitystore.DefaultProjectKey,
			},
		)
		switch {
		case projectErr == nil:
			defaultProjectID = project.ID
			if input.Apply {
				if err := lifecyclelock.EnterActiveProject(ctx, tx, orgID, defaultProjectID); err != nil {
					return ReconcileDefaultsResult{}, err
				}
			}
		case errors.Is(projectErr, pgx.ErrNoRows):
		default:
			return ReconcileDefaultsResult{}, fmt.Errorf("load default project: %w", projectErr)
		}
	}
	modelChanges, warnings, err := s.models.ReconcileDefaultModelProviderTx(
		ctx,
		tx,
		input.DefaultModelProvider,
		providers,
		defaultProjectID,
		input.Apply,
	)
	if err != nil {
		return ReconcileDefaultsResult{}, err
	}
	changes, err := s.execution.ReconcileDefaultMachinePoolsTx(
		ctx,
		tx,
		orgID,
		input.DefaultMachinePools,
		pools,
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
