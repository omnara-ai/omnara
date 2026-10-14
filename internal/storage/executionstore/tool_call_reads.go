package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (r *ToolCallReader) Agent(ctx context.Context) (AgentRecord, error) {
	t := r.transaction
	return loadAgentInProjectTx(ctx, t.tx, t.input.ProjectID, t.input.AgentID)
}

func (r *ToolCallReader) AgentDepth(ctx context.Context) (int, error) {
	t := r.transaction
	depth, err := t.q.CountAgentAncestors(ctx, dbsqlc.CountAgentAncestorsParams{
		ProjectID: t.input.ProjectID,
		AgentID:   t.input.AgentID,
	})
	if err != nil {
		return 0, fmt.Errorf("count agent ancestors: %w", err)
	}
	return int(depth), nil
}

func (r *ToolCallReader) GetAgentConfig(ctx context.Context, configID uuid.UUID) (AgentConfigRecord, error) {
	t := r.transaction
	return loadAgentConfigTx(ctx, t.q, t.input.ProjectID, configID)
}

func (r *ToolCallReader) GetAgentProfile(ctx context.Context, profileID uuid.UUID) (AgentProfileRecord, error) {
	t := r.transaction
	profile, err := loadAgentProfileTx(ctx, t.q, t.input.ProjectID, profileID)
	if err != nil {
		return AgentProfileRecord{}, err
	}
	config, err := loadAgentConfigTx(ctx, t.q, t.input.ProjectID, profile.CurrentConfigID)
	if err != nil {
		return AgentProfileRecord{}, err
	}
	profile.CurrentConfig = config
	return profile, nil
}

// RuntimeContract loads the config a model call context ran against and
// compiles its runtime contract without leaving the tool call transaction.
func (r *ToolCallReader) RuntimeContract(
	ctx context.Context,
	modelCallContextID uuid.UUID,
) (agentconfig.RuntimeContract, AgentConfigRecord, error) {
	contextRow, found, err := r.GetModelCallContext(ctx, modelCallContextID)
	if err != nil {
		return agentconfig.RuntimeContract{}, AgentConfigRecord{}, err
	}
	if !found {
		return agentconfig.RuntimeContract{}, AgentConfigRecord{}, fmt.Errorf(
			"model call context %s not found", modelCallContextID,
		)
	}
	config, err := r.GetAgentConfig(ctx, contextRow.AgentConfigID)
	if err != nil {
		return agentconfig.RuntimeContract{}, AgentConfigRecord{}, err
	}
	contract, err := agentconfig.RuntimeContractFromCompiled(
		config.CompiledDefinition, config.EffectiveDefinitionHash,
	)
	if err != nil {
		return agentconfig.RuntimeContract{}, AgentConfigRecord{}, err
	}
	return contract, config, nil
}

func (r *ToolCallReader) ResolveMemoryTransferStore(ctx context.Context, name string) (uuid.UUID, error) {
	t := r.transaction
	existing, found, err := getProcessByToolCallTx(ctx, t.tx, t.input.ProjectID, t.input.AgentID, t.input.ToolCallID)
	if err != nil {
		return uuid.Nil, err
	}
	if found {
		transfer := existing.ExecutionSpec.FileTransfer
		if transfer == nil || transfer.Target.Memory == nil {
			return uuid.Nil, storeerr.ErrIdempotencyConflict
		}
		id := transfer.Target.Memory.StoreID
		savedName, err := t.q.GetMemoryStoreName(ctx, dbsqlc.GetMemoryStoreNameParams{ProjectID: t.input.ProjectID, ID: id})
		if err != nil {
			return uuid.Nil, fmt.Errorf("load saved memory transfer: %w", err)
		}
		if savedName != name {
			return uuid.Nil, storeerr.ErrIdempotencyConflict
		}
		return id, nil
	}
	id, err := t.q.ResolveAgentMemoryStore(ctx, dbsqlc.ResolveAgentMemoryStoreParams{
		ProjectID: t.input.ProjectID, AgentID: t.input.AgentID, Name: name,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("memory store is unavailable: %w", storeerr.ErrNotFound)
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve memory transfer store: %w", err)
	}
	return id, nil
}
