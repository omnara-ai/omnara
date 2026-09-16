//go:build integration

package storagetest

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

type ChannelAuthorityFixture struct {
	store                     *storage.Store
	pool                      *pgxpool.Pool
	projectID                 uuid.UUID
	AgentID, AppID, InstallID uuid.UUID
	Definition                integrationstore.ChannelDefinition
	Target                    integrationstore.IntegrationTargetRecord
}

func NewChannelAuthorityFixture(
	t *testing.T, ctx context.Context, store *storage.Store,
	pool *pgxpool.Pool, orgID, projectID uuid.UUID, name string,
) ChannelAuthorityFixture {
	t.Helper()
	_, agent, app, install := CreateChannelLifecycleFixture(t, ctx, store, pool, orgID, projectID, name)
	definition, err := store.Integrations().PublishConnectorChannelDefinition(ctx,
		integrationstore.PublishChannelDefinitionInput{
			ProjectID: projectID, IntegrationInstallID: install.ID,
			ImplementationKey: "conversation", Kind: integrationstore.ChannelKindDiscordThread,
			SendParamsSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
			Capabilities: integrationstore.ChannelCapabilities{
				Read: true, Send: true, Text: true, CreatesReplyChannel: true,
			},
			ConnectorCapabilities: ChannelCapabilities(ChannelProvider),
		})
	require.NoError(t, err)
	target, err := store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: projectID, IntegrationInstallID: install.ID, ChannelDefinitionID: definition.ID,
		ProviderRef: "root", ProviderRefKind: "conversation",
	})
	require.NoError(t, err)
	return ChannelAuthorityFixture{store: store, pool: pool, projectID: projectID,
		AgentID: agent.ID, AppID: app.ID, InstallID: install.ID,
		Definition: definition, Target: target}
}

func (f ChannelAuthorityFixture) BindingInput(source string) integrationstore.CreateIntegrationTargetBindingInput {
	return integrationstore.CreateIntegrationTargetBindingInput{
		ProjectID: f.projectID, AgentID: f.AgentID, IntegrationInstallID: f.InstallID,
		IntegrationTargetID: f.Target.ID, Source: source,
	}
}

func (f ChannelAuthorityFixture) OperationInput() integrationstore.PrepareChannelBindingInput {
	return integrationstore.PrepareChannelBindingInput{
		ProjectID: f.projectID, AgentID: f.AgentID, IntegrationInstallID: f.InstallID,
		IntegrationTargetID: f.Target.ID, Operation: integrationstore.ChannelBindingOperationSend,
	}
}

func (f ChannelAuthorityFixture) PrepareOrRecheck(
	t *testing.T,
	ctx context.Context,
	input integrationstore.PrepareChannelBindingInput,
	pinned *integrationstore.IntegrationTargetBindingRecord,
) (integrationstore.IntegrationTargetBindingRecord, error) {
	t.Helper()
	tx, err := f.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	var result integrationstore.IntegrationTargetBindingRecord
	if pinned == nil {
		result, err = f.store.Integrations().PrepareChannelBindingTx(ctx, tx, input)
	} else {
		result, err = f.store.Integrations().RecheckChannelBindingTx(ctx, tx, input, *pinned)
	}
	if err != nil {
		return result, err
	}
	require.NoError(t, tx.Commit(ctx))
	return result, nil
}
