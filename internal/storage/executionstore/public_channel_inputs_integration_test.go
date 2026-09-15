//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

type publicChannelFixture struct {
	store   *Store
	user    identitystore.UserRecord
	agent   executionstore.AgentRecord
	install integrationstore.IntegrationInstallRecord
	target  integrationstore.IntegrationTargetRecord
}

func newPublicChannelFixture(t *testing.T, ctx context.Context, name string) publicChannelFixture {
	t.Helper()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	user := createIntegrationProjectAdmin(t, ctx, store, name+"@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, name+"-profile")
	agent := createIntegrationBoundAgent(t, ctx, store, profile, user.ID, name+"-agent")
	install, err := store.Integrations().CreateExternalIntegrationInstall(ctx, externalConnectionInput(user.ID))
	require.NoError(t, err)
	definition, err := store.Integrations().PublishExternalChannelDefinition(ctx, externalDefinitionInput(install.ID))
	require.NoError(t, err)
	target, err := store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, IntegrationInstallID: install.ID, ChannelDefinitionID: definition.ID,
		ProviderRef: "conversation", ProviderRefKind: "thread", DisplayName: "Customer conversation",
	})
	require.NoError(t, err)
	return publicChannelFixture{store, user, agent, install, target}
}

func (f publicChannelFixture) grants(receive bool) integrationstore.CreateIntegrationTargetBindingInput {
	return integrationstore.CreateIntegrationTargetBindingInput{
		ProjectID: testProjectID, AgentID: f.agent.ID, IntegrationInstallID: f.install.ID,
		IntegrationTargetID: f.target.ID, Source: "api", ReceiveAllowed: receive, SendAllowed: true,
	}
}

func (f publicChannelFixture) input() executionstore.CreateAgentContentInputInput {
	return executionstore.CreateAgentContentInputInput{
		ProjectID: testProjectID, AgentID: f.agent.ID, ChannelID: f.target.ID,
		IdempotencyKey: "customer-message", ContentBlocks: json.RawMessage(`[{"type":"text","text":"hello"}]`),
		Actor: &executionstore.ActorParams{Provider: "external", ProviderTenantID: "customer", ProviderUserID: "alice"},
	}
}

func TestPublicChannelInputRequiresReceiveAndPreservesReplayOrigin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newPublicChannelFixture(t, ctx, "public-origin")
	input := f.input()
	_, _, _, err := f.store.Execution().CreateAgentContentInput(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	var actors int
	require.NoError(t, f.store.pool.QueryRow(ctx,
		`SELECT count(*) FROM actors WHERE provider_user_id = 'alice'`).Scan(&actors))
	require.Zero(t, actors, "rejected origin must not create its external actor")
	binding, err := f.store.Integrations().CreateIntegrationTargetBinding(ctx, f.grants(true))
	require.NoError(t, err)
	results := make([]executionstore.AgentInputRecord, 2)
	created := make([]bool, 2)
	errs := make([]error, 2)
	var workers sync.WaitGroup
	for i := range results {
		workers.Go(func() { results[i], _, created[i], errs[i] = f.store.Execution().CreateAgentContentInput(ctx, input) })
	}
	workers.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, results[0].ID, results[1].ID)
	require.NotEqual(t, created[0], created[1])
	require.Equal(t, binding.ID, results[0].IntegrationTargetBindingID)
	require.Equal(t, f.target.ID, results[0].IntegrationTargetID)
	var current ID
	require.NoError(t, f.store.pool.QueryRow(ctx,
		`SELECT integration_target_id FROM agents WHERE id=$1`, f.agent.ID).Scan(&current))
	require.Equal(t, NilID, current, "enqueue has not changed the model turn origin")
	claim, found, err := f.store.Execution().ClaimNextAgentWork(ctx, testClaimNextAgentWorkInput())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.AgentWorkModel, claim.Kind)
	require.Len(t, claim.Model.AdmittedInputTurn.Inputs, 1)
	require.Equal(t, results[0].ID, claim.Model.AdmittedInputTurn.Inputs[0].ID)
	require.NoError(t, f.store.pool.QueryRow(ctx,
		`SELECT integration_target_id FROM agents WHERE id=$1`, f.agent.ID).Scan(&current))
	require.Equal(t, f.target.ID, current, "admitting the input selects its origin")

	replacement, err := f.store.Integrations().CreateIntegrationTargetBinding(ctx, f.grants(false))
	require.NoError(t, err)
	require.NotEqual(t, binding.ID, replacement.ID)
	replayed, _, didCreate, err := f.store.Execution().CreateAgentContentInput(ctx, input)
	require.NoError(t, err)
	require.False(t, didCreate)
	require.Equal(t, binding.ID, replayed.IntegrationTargetBindingID)
	fresh := input
	fresh.IdempotencyKey = "fresh-without-receive"
	_, _, _, err = f.store.Execution().CreateAgentContentInput(ctx, fresh)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	for _, changed := range []executionstore.CreateAgentContentInputInput{
		func() executionstore.CreateAgentContentInputInput {
			x := input
			x.ContentBlocks = json.RawMessage(`[{"type":"text","text":"different"}]`)
			return x
		}(),
		func() executionstore.CreateAgentContentInputInput { x := input; x.ChannelID = uuid.New(); return x }(),
		func() executionstore.CreateAgentContentInputInput {
			x := input
			x.Actor = &executionstore.ActorParams{Provider: "external", ProviderUserID: "mallory"}
			return x
		}(),
	} {
		_, _, _, err := f.store.Execution().CreateAgentContentInput(ctx, changed)
		require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
	}
	require.NoError(t, f.store.Integrations().DeleteIntegrationInstall(ctx, testProjectID, f.install.ID))
	replayed, _, didCreate, err = f.store.Execution().CreateAgentContentInput(ctx, input)
	require.NoError(t, err)
	require.False(t, didCreate)
	require.Equal(t, binding.ID, replayed.IntegrationTargetBindingID, "accepted retry survives connection retirement")
}

func TestPublicChannelInputRejectsForeignAgentAndManagedOrigin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newPublicChannelFixture(t, ctx, "public-scope")
	_, err := f.store.Integrations().CreateIntegrationTargetBinding(ctx, f.grants(true))
	require.NoError(t, err)
	profile := createIntegrationTestProfile(t, ctx, f.store, "other-agent-profile")
	other := createIntegrationBoundAgent(t, ctx, f.store, profile, f.user.ID, "other-agent")
	input := f.input()
	input.AgentID = other.ID
	_, _, _, err = f.store.Execution().CreateAgentContentInput(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	input = f.input()
	input.ProjectID = uuid.New()
	_, _, _, err = f.store.Execution().CreateAgentContentInput(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrNotFound)

	_, _, _, managed := createChannelLifecycleFixture(t, ctx, f.store, "verified-only")
	definitionID := createChannelTestDefinition(t, ctx, f.store, managed)
	target, err := f.store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, IntegrationInstallID: managed.ID, ChannelDefinitionID: definitionID,
		ProviderRef: "managed-thread", ProviderRefKind: "thread",
	})
	require.NoError(t, err)
	input = f.input()
	input.ChannelID = target.ID
	_, _, _, err = f.store.Execution().CreateAgentContentInput(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	require.Contains(t, err.Error(), "verified provider intake")
}
