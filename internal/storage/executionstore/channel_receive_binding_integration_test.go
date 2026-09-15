//go:build integration

package executionstore_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestTransactionalReceiveBindingLookupUsesCallerStateAndLiveExternalScope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newChannelAuthorityFixture(t, ctx, "receive-discovery")
	user := createIntegrationProjectAdmin(t, ctx, f.Store, "receive-discovery-external-owner@example.com")
	install, err := f.Store.Integrations().CreateExternalIntegrationInstall(ctx, externalConnectionInput(user.ID))
	require.NoError(t, err)
	definition, err := f.Store.Integrations().PublishExternalChannelDefinition(ctx, externalDefinitionInput(install.ID))
	require.NoError(t, err)
	target, err := f.Store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, IntegrationInstallID: install.ID, ChannelDefinitionID: definition.ID,
		ProviderRef: "external-address", ProviderRefKind: "conversation",
	})
	require.NoError(t, err)
	input := integrationstore.CreateIntegrationTargetBindingInput{
		ProjectID: testProjectID, AgentID: f.AgentID, IntegrationInstallID: install.ID,
		IntegrationTargetID: target.ID, Source: "api", ReceiveAllowed: true, ReadAllowed: true,
	}
	original, err := f.Store.Integrations().CreateIntegrationTargetBinding(ctx, input)
	require.NoError(t, err)
	require.Equal(t, uuid.Nil, original.IntegrationRouteID, "public setup has no fabricated route")
	tx, err := f.Store.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	input.Source, input.SendAllowed, input.ReadAllowed = "explicit-other-setup", true, false
	preferred, err := f.Store.Integrations().CreateIntegrationTargetBindingTx(ctx, tx, input)
	require.NoError(t, err)
	outside, err := f.Store.Integrations().GetActiveReceiveBindingForTarget(ctx, testProjectID, f.AgentID, target.ID)
	require.NoError(t, err)
	require.Equal(t, original.ID, outside.ID)
	inside, err := f.Store.Integrations().GetActiveReceiveBindingForTargetTx(ctx, tx, testProjectID, f.AgentID, target.ID)
	require.NoError(t, err)
	require.Equal(t, preferred.ID, inside.ID, "the transaction sees its writes and uses the existing send preference")
	require.False(t, inside.ReadAllowed, "selection returns one binding without combining grants")
	for _, scope := range []struct{ project, agent, target uuid.UUID }{
		{uuid.New(), f.AgentID, target.ID},
		{testProjectID, uuid.New(), target.ID},
		{testProjectID, f.AgentID, f.Target.ID},
	} {
		_, err := f.Store.Integrations().GetActiveReceiveBindingForTargetTx(
			ctx, tx, scope.project, scope.agent, scope.target)
		require.ErrorIs(t, err, storeerr.ErrNotFound)
	}
	require.NoError(t, tx.Rollback(ctx))
	outside, err = f.Store.Integrations().GetActiveReceiveBindingForTarget(ctx, testProjectID, f.AgentID, target.ID)
	require.NoError(t, err)
	require.Equal(t, original.ID, outside.ID, "lookup does not commit its caller's setup")
	changed, err := f.Store.Integrations().DisableIntegrationInstall(ctx, integrationstore.DisableIntegrationInstallInput{
		ProjectID: testProjectID, ID: install.ID, ExpectedOAuthFlowID: &install.LastOAuthFlowID,
	})
	require.NoError(t, err)
	require.True(t, changed)
	tx, err = f.Store.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = f.Store.Integrations().GetActiveReceiveBindingForTargetTx(ctx, tx, testProjectID, f.AgentID, target.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "external absence of an app never bypasses installation lifecycle")
}
