package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// launchAppIDsTx reads immutable config authority without taking profile,
// model, machine-source or agent locks. Derived configs are not inserted yet.
func launchAppIDsTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	input LaunchAgentInput,
) ([]string, error) {
	var config AgentConfigRecord
	if input.DerivedConfig != nil {
		derived := withDefaultAgentConfigCompilation(*input.DerivedConfig)
		config = AgentConfigRecord{
			CompiledDefinition:      derived.CompiledDefinition,
			CompilerVersion:         derived.CompilerVersion,
			EffectiveDefinitionHash: derived.EffectiveDefinitionHash,
		}
	} else {
		var err error
		config, err = loadAgentConfigTx(ctx, q, input.ProjectID, input.AgentConfigID)
		if err != nil {
			return nil, err
		}
	}
	contract, err := launchableRuntimeContract(config)
	if err != nil {
		return nil, err
	}
	refs := contract.ReferencedAppIDs()
	for _, subscription := range input.Subscriptions {
		ref, err := publicid.Encode(publicid.KindProjectApp, subscription.AppID)
		if err != nil {
			return nil, storeerr.InvalidRequest(err)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func configChangeReplayExistsTx(ctx context.Context, q *dbsqlc.Queries, input ChangeAgentConfigInput) (bool, error) {
	if input.IdempotencyKey == "" {
		return false, nil
	}
	_, err := q.GetAgentInputByIdempotency(ctx, dbsqlc.GetAgentInputByIdempotencyParams{
		ProjectID: input.ProjectID, AgentID: input.AgentID,
		IdempotencyScope: "agent_config_change", InputIdempotencyKey: input.IdempotencyKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load idempotent config change: %w", err)
	}
	return true, nil
}

// lockConfigChangeAppsTx locks only the next immutable config's references,
// before the agent-source lock. Config changes do not own subscriptions, and
// interaction selection is reconciled under the agent lock, so previous-only
// apps need no gate. A committed replay needs no live app authority.
func lockConfigChangeAppsTx(
	ctx context.Context,
	tx pgx.Tx,
	q *dbsqlc.Queries,
	input ChangeAgentConfigInput,
) error {
	if replay, err := configChangeReplayExistsTx(ctx, q, input); err != nil || replay {
		return err
	}
	_, next, err := validateLiveAgentConfigChangeTx(
		ctx,
		q,
		input.ProjectID,
		uuid.Nil,
		input.CreateAgentConfigInput,
	)
	if err != nil {
		return err
	}
	if err := integrationstore.LockAppsTx(ctx, tx, input.ProjectID, next.ReferencedAppIDs()); err != nil {
		// An activation may have committed while we waited. Let the normal
		// replay path compare the request before rejecting its app references.
		if replay, replayErr := configChangeReplayExistsTx(ctx, q, input); replayErr == nil && replay {
			return nil
		}
		return err
	}
	return nil
}
