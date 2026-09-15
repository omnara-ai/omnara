//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

type channelAuthorityFixture struct {
	Store                     *Store
	AgentID, AppID, InstallID uuid.UUID
	Definition                integrationstore.ChannelDefinition
	Target                    integrationstore.IntegrationTargetRecord
}

func newChannelAuthorityFixture(t *testing.T, ctx context.Context, name string) channelAuthorityFixture {
	t.Helper()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, agent, app, install := createChannelLifecycleFixture(t, ctx, store, name)
	definition, err := store.Integrations().PublishConnectorChannelDefinition(ctx,
		integrationstore.PublishChannelDefinitionInput{
			ProjectID: testProjectID, IntegrationInstallID: install.ID,
			ImplementationKey: "conversation", Kind: integrationstore.ChannelKindExternal,
			SendParamsSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
			Capabilities: integrationstore.ChannelCapabilities{
				Read: true, Send: true, Text: true, CreatesReplyChannel: true,
			},
			ConnectorCapabilities: testChannelCapabilities(testChannelProvider),
		})
	require.NoError(t, err)
	target, err := store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, IntegrationInstallID: install.ID, ChannelDefinitionID: definition.ID,
		ProviderRef: "root", ProviderRefKind: "conversation",
	})
	require.NoError(t, err)
	return channelAuthorityFixture{Store: store, AgentID: agent.ID, AppID: app.ID, InstallID: install.ID,
		Definition: definition, Target: target}
}

func (f channelAuthorityFixture) bindingInput(source string) integrationstore.CreateIntegrationTargetBindingInput {
	return integrationstore.CreateIntegrationTargetBindingInput{
		ProjectID: testProjectID, AgentID: f.AgentID, IntegrationInstallID: f.InstallID,
		IntegrationTargetID: f.Target.ID, Source: source,
	}
}

func (f channelAuthorityFixture) operationInput() integrationstore.PrepareChannelBindingInput {
	return integrationstore.PrepareChannelBindingInput{
		ProjectID: testProjectID, AgentID: f.AgentID, IntegrationInstallID: f.InstallID,
		IntegrationTargetID: f.Target.ID, Operation: integrationstore.ChannelBindingOperationSend,
	}
}

func (f channelAuthorityFixture) prepareOrRecheck(
	t *testing.T,
	ctx context.Context,
	input integrationstore.PrepareChannelBindingInput,
	pinned *integrationstore.IntegrationTargetBindingRecord,
) (integrationstore.IntegrationTargetBindingRecord, error) {
	t.Helper()
	tx, err := f.Store.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	var result integrationstore.IntegrationTargetBindingRecord
	if pinned == nil {
		result, err = f.Store.Integrations().PrepareChannelBindingTx(ctx, tx, input)
	} else {
		result, err = f.Store.Integrations().RecheckChannelBindingTx(ctx, tx, input, *pinned)
	}
	if err != nil {
		return result, err
	}
	require.NoError(t, tx.Commit(ctx))
	return result, nil
}

func TestStandaloneChannelRegistrationComposesAtomicBindings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newChannelAuthorityFixture(t, ctx, "standalone-registration")
	var agentsBefore int
	require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT count(*) FROM agents`).Scan(&agentsBefore))
	input := integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, IntegrationInstallID: f.InstallID, ChannelDefinitionID: f.Definition.ID,
		ProviderRef: "child", ProviderRefKind: "thread", ParentChannelID: f.Target.ID,
	}
	results := make([]integrationstore.IntegrationTargetRecord, 2)
	errs := make([]error, 2)
	var workers sync.WaitGroup
	for i := range results {
		workers.Go(func() { results[i], errs[i] = f.Store.Integrations().CreateIntegrationTarget(ctx, input) })
	}
	workers.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, results[0].ID, results[1].ID, "concurrent address registration has one canonical identity")
	require.NotEqual(t, results[0].Created, results[1].Created)
	var agentsAfter, bindings int
	require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT count(*) FROM agents`).Scan(&agentsAfter))
	require.Equal(t, agentsBefore, agentsAfter)
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM integration_target_bindings WHERE integration_target_id = $1`, results[0].ID).Scan(&bindings))
	require.Zero(t, bindings, "registration and parentage confer no agent permissions")

	tx, err := f.Store.pool.Begin(ctx)
	require.NoError(t, err)
	input.ProviderRef = "rolled-back-child"
	uncommitted, err := f.Store.Integrations().CreateIntegrationTargetTx(ctx, tx, input)
	require.NoError(t, err)
	grant := f.bindingInput("setup")
	grant.IntegrationTargetID, grant.ReadAllowed = uncommitted.ID, true
	_, err = f.Store.Integrations().CreateIntegrationTargetBindingTx(ctx, tx, grant)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback(ctx))
	_, err = f.Store.Integrations().GetIntegrationTarget(ctx, testProjectID, uncommitted.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM integration_target_bindings WHERE integration_target_id = $1`, uncommitted.ID).Scan(&bindings))
	require.Zero(t, bindings)

	tx, err = f.Store.pool.Begin(ctx)
	require.NoError(t, err)
	input.ProviderRef = "committed-child"
	child, err := f.Store.Integrations().CreateIntegrationTargetTx(ctx, tx, input)
	require.NoError(t, err)
	grant.IntegrationTargetID = child.ID
	binding, err := f.Store.Integrations().CreateIntegrationTargetBindingTx(ctx, tx, grant)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	require.Nil(t, binding.ReplyChannelGrants, "ordinary child grants never delegate onward implicitly")
	loaded, err := f.Store.Integrations().GetIntegrationTargetBinding(ctx, testProjectID, binding.ID)
	require.NoError(t, err)
	require.Equal(t, child.ID, loaded.IntegrationTargetID)
	grant.SendAllowed = true
	grant.ReplyChannelGrants = &integrationstore.ChannelGrants{ReadAllowed: true}
	delegating, err := f.Store.Integrations().CreateIntegrationTargetBinding(ctx, grant)
	require.NoError(t, err, "later explicit setup may authorize child delegation")
	require.NotEqual(t, binding.ID, delegating.ID)
}

func TestReplyChannelGrantsAreExplicitImmutableAndReplaceable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newChannelAuthorityFixture(t, ctx, "reply-grants")
	input := f.bindingInput("setup")
	input.SendAllowed = true
	ordinary, err := f.Store.Integrations().CreateIntegrationTargetBinding(ctx, input)
	require.NoError(t, err)
	require.Nil(t, ordinary.ReplyChannelGrants)
	access, err := f.Store.Integrations().GetAgentChannelAccess(ctx, testProjectID, f.AgentID, f.Target.ID)
	require.NoError(t, err)
	require.False(t, access.Capabilities.CreatesReplyChannel, "definition support alone grants no delegation")
	input.ReplyChannelGrants = &integrationstore.ChannelGrants{ReceiveAllowed: true, SendAllowed: true}
	delegating, err := f.Store.Integrations().CreateIntegrationTargetBinding(ctx, input)
	require.NoError(t, err)
	require.NotEqual(t, ordinary.ID, delegating.ID)
	require.Equal(t, input.ReplyChannelGrants, delegating.ReplyChannelGrants)
	_, err = f.Store.Integrations().GetIntegrationTargetBinding(ctx, testProjectID, ordinary.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	replayed, err := f.Store.Integrations().CreateIntegrationTargetBinding(ctx, input)
	require.NoError(t, err)
	require.Equal(t, delegating.ID, replayed.ID)
	access, err = f.Store.Integrations().GetAgentChannelAccess(ctx, testProjectID, f.AgentID, f.Target.ID)
	require.NoError(t, err)
	require.True(t, access.Capabilities.CreatesReplyChannel)
	require.False(t, access.Capabilities.Read, "child receive/send grants do not grant parent history")
	_, err = f.Store.pool.Exec(ctx,
		`UPDATE integration_target_bindings SET reply_read_allowed = true WHERE id = $1`, delegating.ID)
	require.True(t, isPgCode(err, "25006"), "reply tuples are immutable: %v", err)
	input.ReplyChannelGrants.ReadAllowed = true
	widened, err := f.Store.Integrations().CreateIntegrationTargetBinding(ctx, input)
	require.NoError(t, err)
	require.NotEqual(t, delegating.ID, widened.ID)
	input.ReplyChannelGrants = nil
	removed, err := f.Store.Integrations().CreateIntegrationTargetBinding(ctx, input)
	require.NoError(t, err)
	require.NotEqual(t, widened.ID, removed.ID)
	require.Nil(t, removed.ReplyChannelGrants)

	for _, tc := range []struct {
		name    string
		send    bool
		r, h, s any
	}{
		{name: "partial", send: true, r: true},
		{name: "empty", send: true, r: false, h: false, s: false},
		{name: "parent_without_send", r: true, h: false, s: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := f.Store.pool.Exec(ctx, `INSERT INTO integration_target_bindings (
				project_id, agent_id, integration_install_id, integration_target_id, target_created_at,
				receive_allowed, read_allowed, send_allowed, reply_receive_allowed, reply_read_allowed,
				reply_send_allowed, source, created_at, updated_at)
				SELECT project_id, agent_id, integration_install_id, integration_target_id, target_created_at,
				false, true, $2, $3, $4, $5, $6, statement_timestamp(), statement_timestamp()
				FROM integration_target_bindings WHERE id = $1`,
				removed.ID, tc.send, tc.r, tc.h, tc.s, tc.name)
			require.True(t, isPgCode(err, "23514"), "database rejects invalid grant shape: %v", err)
		})
	}
}

func TestChannelOperationBindingPinsOneEligibleLiveTuple(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newChannelAuthorityFixture(t, ctx, "operation-binding")
	input := f.bindingInput("reader")
	input.ReadAllowed = true
	reader, err := f.Store.Integrations().CreateIntegrationTargetBinding(ctx, input)
	require.NoError(t, err)
	input.Source, input.ReadAllowed, input.SendAllowed = "sender", false, true
	sender, err := f.Store.Integrations().CreateIntegrationTargetBinding(ctx, input)
	require.NoError(t, err)
	input.Source = "first-delegating-sender"
	input.ReplyChannelGrants = &integrationstore.ChannelGrants{ReceiveAllowed: true}
	first, err := f.Store.Integrations().CreateIntegrationTargetBinding(ctx, input)
	require.NoError(t, err)
	secondInput := input
	secondInput.Source = "second-delegating-sender"
	secondInput.ReplyChannelGrants = &integrationstore.ChannelGrants{ReadAllowed: true, SendAllowed: true}
	second, err := f.Store.Integrations().CreateIntegrationTargetBinding(ctx, secondInput)
	require.NoError(t, err)
	op := f.operationInput()
	prepared, err := f.prepareOrRecheck(t, ctx, op, nil)
	require.NoError(t, err)
	require.Equal(t, sender.ID, prepared.ID, "ordinary send uses one sender, not the reader")
	op.Operation = integrationstore.ChannelBindingOperationRead
	prepared, err = f.prepareOrRecheck(t, ctx, op, nil)
	require.NoError(t, err)
	require.Equal(t, reader.ID, prepared.ID)
	op.Operation, op.CreatesReplyChannel = integrationstore.ChannelBindingOperationSend, true
	for range 3 {
		prepared, err = f.prepareOrRecheck(t, ctx, op, nil)
		require.NoError(t, err)
		require.Equal(t, first.ID, prepared.ID)
		require.Equal(t, first.ReplyChannelGrants, prepared.ReplyChannelGrants,
			"the second binding's read/send grants must not be ORed into the selected tuple")
	}
	_, err = f.prepareOrRecheck(t, ctx, op, &prepared)
	require.NoError(t, err)
	tampered := prepared
	tampered.ReplyChannelGrants = second.ReplyChannelGrants
	_, err = f.prepareOrRecheck(t, ctx, op, &tampered)
	require.ErrorIs(t, err, storeerr.ErrUnauthorized)
	input.ReplyChannelGrants = &integrationstore.ChannelGrants{SendAllowed: true}
	replacement, err := f.Store.Integrations().CreateIntegrationTargetBinding(ctx, input)
	require.NoError(t, err)
	require.NotEqual(t, first.ID, replacement.ID)
	_, err = f.prepareOrRecheck(t, ctx, op, &prepared)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "completion cannot substitute a replacement or another eligible sender")
	selected, err := f.prepareOrRecheck(t, ctx, op, nil)
	require.NoError(t, err)
	require.Equal(t, second.ID, selected.ID)
	require.NoError(t, f.Store.Integrations().RevokeIntegrationTargetBinding(ctx, testProjectID, second.ID))
	_, err = f.prepareOrRecheck(t, ctx, op, &selected)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "replay never revives a revoked parent binding")
}

func TestChannelRegistrationAndOperationRejectWrongScopeAndRetirement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newChannelAuthorityFixture(t, ctx, "operation-scope")
	_, otherAgent, _, otherInstall := createChannelLifecycleFixture(t, ctx, f.Store, "operation-other-scope")
	otherDefinitionID := createChannelTestDefinition(t, ctx, f.Store, otherInstall)
	input := f.bindingInput("setup")
	input.SendAllowed = true
	_, err := f.Store.Integrations().CreateIntegrationTargetBinding(ctx, input)
	require.NoError(t, err)
	op := f.operationInput()
	for _, altered := range []integrationstore.PrepareChannelBindingInput{
		{ProjectID: uuid.New(), AgentID: op.AgentID, IntegrationInstallID: op.IntegrationInstallID,
			IntegrationTargetID: op.IntegrationTargetID, Operation: op.Operation},
		{ProjectID: op.ProjectID, AgentID: otherAgent.ID, IntegrationInstallID: op.IntegrationInstallID,
			IntegrationTargetID: op.IntegrationTargetID, Operation: op.Operation},
		{ProjectID: op.ProjectID, AgentID: op.AgentID, IntegrationInstallID: otherInstall.ID,
			IntegrationTargetID: op.IntegrationTargetID, Operation: op.Operation},
		{ProjectID: op.ProjectID, AgentID: op.AgentID, IntegrationInstallID: op.IntegrationInstallID,
			IntegrationTargetID: uuid.New(), Operation: op.Operation},
	} {
		_, err = f.prepareOrRecheck(t, ctx, altered, nil)
		require.ErrorIs(t, err, storeerr.ErrNotFound)
	}
	targetInput := integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, IntegrationInstallID: otherInstall.ID, ProviderRef: "cross-definition",
		ProviderRefKind: "thread", ChannelDefinitionID: f.Definition.ID,
	}
	_, err = f.Store.Integrations().CreateIntegrationTarget(ctx, targetInput)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	targetInput.ChannelDefinitionID, targetInput.ParentChannelID = otherDefinitionID, f.Target.ID
	_, err = f.Store.Integrations().CreateIntegrationTarget(ctx, targetInput)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "parent belongs to a different connection")
	targetInput.IntegrationInstallID = f.InstallID
	targetInput.ChannelDefinitionID = f.Definition.ID
	targetInput.ProjectID = uuid.New()
	_, err = f.Store.Integrations().CreateIntegrationTarget(ctx, targetInput)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	prepared, err := f.prepareOrRecheck(t, ctx, op, nil)
	require.NoError(t, err)
	_, err = f.Store.pool.Exec(ctx, `UPDATE integration_apps SET state = 'disabled' WHERE id = $1`, f.AppID)
	require.NoError(t, err)
	_, err = f.prepareOrRecheck(t, ctx, op, &prepared)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	targetInput.ProjectID = testProjectID
	targetInput.ChannelDefinitionID = f.Definition.ID
	_, err = f.Store.Integrations().CreateIntegrationTarget(ctx, targetInput)
	require.ErrorIs(t, err, storeerr.ErrUnauthorized)
}

func TestReplyChannelRegistrationUsesPinnedGrantsWithoutReplayRevival(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newChannelAuthorityFixture(t, ctx, "reply-registration")
	grant := f.bindingInput("setup")
	grant.SendAllowed = true
	grant.ReplyChannelGrants = &integrationstore.ChannelGrants{ReadAllowed: true}
	_, err := f.Store.Integrations().CreateIntegrationTargetBinding(ctx, grant)
	require.NoError(t, err)
	op := f.operationInput()
	op.CreatesReplyChannel = true
	prepared, err := f.prepareOrRecheck(t, ctx, op, nil)
	require.NoError(t, err)
	child := integrationstore.CreateIntegrationTargetInput{
		ProviderRef: "provider-reply", ProviderRefKind: "thread",
		ParentChannelID: f.Target.ID, ChannelDefinitionID: f.Definition.ID,
	}
	register := func(
		target integrationstore.CreateIntegrationTargetInput,
	) (integrationstore.ReplyChannelRegistration, error) {
		tx, err := f.Store.pool.Begin(ctx)
		if err != nil {
			return integrationstore.ReplyChannelRegistration{}, err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		result, err := f.Store.Integrations().RegisterReplyChannelTx(ctx, tx, prepared, target)
		if err != nil {
			return result, err
		}
		return result, tx.Commit(ctx)
	}
	results := make([]integrationstore.ReplyChannelRegistration, 2)
	errs := make([]error, 2)
	var workers sync.WaitGroup
	for i := range results {
		workers.Go(func() { results[i], errs[i] = register(child) })
	}
	workers.Wait()
	for _, err := range errs {
		require.NoError(t, err, "concurrent completions must not deadlock while upgrading agent locks")
	}
	require.Equal(t, results[0].Channel.ID, results[1].Channel.ID)
	require.Equal(t, results[0].Binding.ID, results[1].Binding.ID)
	require.Equal(t, f.Target.ID, results[0].Channel.ParentChannelID)
	bound := results[0].Binding
	require.True(t, bound.ReadAllowed, "a reply may grant read without receive or send")
	require.False(t, bound.ReceiveAllowed)
	require.False(t, bound.SendAllowed)
	require.Nil(t, bound.ReplyChannelGrants, "creation never delegates onward")
	require.NoError(t, f.Store.Integrations().RevokeIntegrationTargetBinding(ctx, testProjectID, bound.ID))
	_, err = register(child)
	require.ErrorIs(t, err, storeerr.ErrUnauthorized, "automatic replay must not restore a revoked child")
	var bindings int
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM integration_target_bindings WHERE integration_target_id = $1`, results[0].Channel.ID).Scan(&bindings))
	require.Equal(t, 1, bindings)
	wrong := child
	wrong.ParentChannelID = uuid.New()
	_, err = register(wrong)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	grant.ReplyChannelGrants = &integrationstore.ChannelGrants{SendAllowed: true}
	_, err = f.Store.Integrations().CreateIntegrationTargetBinding(ctx, grant)
	require.NoError(t, err)
	child.ProviderRef = "replacement-must-not-create"
	_, err = register(child)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = f.Store.Integrations().GetIntegrationTargetByProviderRef(ctx, testProjectID, f.InstallID, child.ProviderRef)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
}
