//go:build integration

package integrationstore_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestChannelDefinitionsAreSharedCurrentAndScoped(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, _, install := createChannelInstallationFixture(t, ctx, store, "channel-definitions")
	input := integrationstore.PublishChannelDefinitionInput{
		ProjectID: testProjectID, IntegrationInstallID: install.ID,
		ImplementationKey: "conversation", Kind: integrationstore.ChannelKindDiscordThread,
		SendParamsSchema:      json.RawMessage(`{"type":"object","additionalProperties":false}`),
		Capabilities:          integrationstore.ChannelCapabilities{Send: true, Text: true},
		ConnectorCapabilities: testChannelCapabilities(testChannelProvider),
	}
	definition, err := store.Integrations().PublishConnectorChannelDefinition(ctx, input)
	require.NoError(t, err)
	targetInput := integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, IntegrationInstallID: install.ID,
		ChannelDefinitionID: definition.ID, ProviderRefKind: "thread",
	}
	for _, ref := range []string{"first", "second"} {
		targetInput.ProviderRef = ref
		target, err := store.Integrations().CreateIntegrationTarget(ctx, targetInput)
		require.NoError(t, err)
		require.Equal(t, definition.ID, target.ChannelDefinitionID)
		loaded, err := store.Integrations().GetIntegrationTarget(ctx, testProjectID, target.ID)
		require.NoError(t, err)
		require.Equal(t, definition.ID, loaded.ChannelDefinitionID)
	}
	input.SendParamsSchema = json.RawMessage(`{"type":"object","properties":{"format":{"enum":["plain","markdown"]}},"additionalProperties":false}`)
	input.Capabilities.Read = true
	updated, err := store.Integrations().PublishConnectorChannelDefinition(ctx, input)
	require.NoError(t, err)
	require.Equal(t, definition.ID, updated.ID, "current schema changes don't fork every destination")
	loaded, err := store.Integrations().GetChannelDefinition(ctx, testProjectID, install.ID, definition.ID)
	require.NoError(t, err)
	require.JSONEq(t, string(input.SendParamsSchema), string(loaded.SendParamsSchema))
	require.True(t, loaded.Capabilities.Read)
	input.Kind = integrationstore.ChannelKindSlackThread
	_, err = store.Integrations().PublishConnectorChannelDefinition(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest, "a definition cannot substitute another provider's kind")
	_, err = pool.Exec(ctx,
		`UPDATE integration_channel_definitions SET kind = 'SLACK_THREAD' WHERE id = $1`, definition.ID)
	require.True(t, isPgCode(err, "25006"), "definition kind remains immutable: %v", err)
	input.Kind = definition.Kind
	input.ConnectorCapabilities = testChannelCapabilities("slack")
	_, err = store.Integrations().PublishConnectorChannelDefinition(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "another connector capability cannot publish this connection's contract")
	_, _, otherInstall := createChannelInstallationFixture(t, ctx, store, "other-channel-definitions")
	targetInput.IntegrationInstallID, targetInput.ProviderRef = otherInstall.ID, "cross-connection"
	_, err = store.Integrations().CreateIntegrationTarget(ctx, targetInput)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "definitions are scoped to their connection")
	_, err = store.Integrations().GetChannelDefinition(ctx, testProjectID, otherInstall.ID, definition.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
}
