package executionstore

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

// launchAppResourcesTx reads immutable config authority without taking profile,
// model, machine-source or agent locks. Derived configs are not inserted yet.
func launchAppResourcesTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	input LaunchAgentInput,
) (map[string]agentconfig.AppResourceCompiled, error) {
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
	return contract.AppResources, nil
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

// lockConfigChangeAppResourcesTx discovers the current config optimistically.
// The caller must recheck this ID after acquiring the agent-source lock and
// restart the entire transaction on change, never acquire new connection gates
// from below that lock. A committed replay needs no live connection authority.
func lockConfigChangeAppResourcesTx(
	ctx context.Context,
	tx pgx.Tx,
	q *dbsqlc.Queries,
	input ChangeAgentConfigInput,
) (uuid.UUID, error) {
	if replay, err := configChangeReplayExistsTx(ctx, q, input); err != nil || replay {
		return uuid.Nil, err
	}
	agent, err := q.GetAgentInProject(
		ctx,
		dbsqlc.GetAgentInProjectParams{ProjectID: input.ProjectID, ID: input.AgentID},
	)
	if err != nil {
		return uuid.Nil, fmt.Errorf("load agent before app connection gates: %w", err)
	}
	previous, next, err := validateLiveAgentConfigChangeTx(
		ctx,
		q,
		input.ProjectID,
		agent.CurrentConfigID,
		input.CreateAgentConfigInput,
	)
	if err != nil {
		return uuid.Nil, err
	}
	// Retiring authorities are locked but not validated as active. Removing a
	// revoked/deleted connection must remain possible. Lock the whole union in
	// UUID order before the helper re-enters only the held next-config gates.
	ids := map[uuid.UUID]struct{}{}
	for _, resources := range []map[string]agentconfig.AppResourceCompiled{previous.AppResources, next.AppResources} {
		for _, resource := range resources {
			if !resource.Enabled || resource.ConnectionID == "" {
				continue
			}
			id, err := publicid.Decode(publicid.KindIntegrationConnection, resource.ConnectionID)
			if err != nil {
				return uuid.Nil, err
			}
			ids[id] = struct{}{}
		}
	}
	ordered := slices.Collect(maps.Keys(ids))
	slices.SortFunc(ordered, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	for _, id := range ordered {
		if err := q.LockIntegrationConnectionLifecycleShared(
			ctx,
			dbsqlc.LockIntegrationConnectionLifecycleSharedParams{ConnectionID: id},
		); err != nil {
			return uuid.Nil, err
		}
	}
	if err := integrationstore.LockAppConnectionsTx(ctx, tx, input.ProjectID, next.AppResources); err != nil {
		// A matching activation may have committed while we waited, followed by
		// connection revocation. Let the normal replay path compare the request.
		if replay, replayErr := configChangeReplayExistsTx(ctx, q, input); replayErr == nil && replay {
			return uuid.Nil, nil
		}
		return uuid.Nil, err
	}
	if err := lockAppConversationsTx(ctx, tx, input.ProjectID, next.AppResources); err != nil {
		return uuid.Nil, err
	}
	return agent.CurrentConfigID, nil
}
