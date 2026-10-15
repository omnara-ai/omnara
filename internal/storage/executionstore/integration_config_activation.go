package executionstore

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func launchIntegrationIDsTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	input LaunchAgentInput,
) ([]uuid.UUID, error) {
	var config AgentConfigRecord
	if input.DerivedConfig != nil {
		derived := withDefaultAgentConfigCompilation(*input.DerivedConfig)
		config = AgentConfigRecord{
			CompiledDefinition:      derived.CompiledDefinition,
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
	refs := contract.ReferencedIntegrationIDs()
	for _, subscription := range input.Subscriptions {
		if subscription.IntegrationID == uuid.Nil {
			return nil, storeerr.InvalidRequest(errors.New("subscription integration id is required"))
		}
		refs = append(refs, subscription.IntegrationID)
	}
	return refs, nil
}

func lockConfigChangeIntegrationsTx(
	ctx context.Context,
	tx dbsqlc.DBTX,
	q *dbsqlc.Queries,
	input ChangeAgentConfigInput,
) error {
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
	return integrationstore.LockIntegrationsTx(ctx, tx, input.ProjectID, next.ReferencedIntegrationIDs())
}
