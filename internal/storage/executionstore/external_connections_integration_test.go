//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestExternalChannelRegistrationPreservesTrimAndUTF8ByteBoundaries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newPublicChannelFixture(t, ctx, "registration-text-boundaries")
	ref, kind, name := strings.Repeat("é", 1024), strings.Repeat("é", 64), strings.Repeat("é", 256)
	input := integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, IntegrationInstallID: f.install.ID, ChannelDefinitionID: f.target.ChannelDefinitionID,
		ProviderRef: " \t" + ref + "\n ", ProviderRefKind: " " + kind + " ", DisplayName: " " + name + " ",
	}
	created, err := f.store.Integrations().RegisterExternalChannel(ctx, input)
	require.NoError(t, err, "limits count normalized UTF-8 bytes, after the existing whitespace trim")
	require.True(t, created.Created)
	require.Equal(t, ref, created.ProviderRef)
	require.Equal(t, kind, created.ProviderRefKind)
	require.Equal(t, name, created.DisplayName)
	input.ProviderRef, input.ProviderRefKind, input.DisplayName = ref, kind, name
	replayed, err := f.store.Integrations().RegisterExternalChannel(ctx, input)
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, created.ID, replayed.ID, "removing surrounding whitespace preserves the registered identity")
}

func TestChannelProviderReferenceLookupUsesCallerTransactionAndScope(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newPublicChannelFixture(t, ctx, "provider-reference-lookup")
	integrations := f.store.Integrations()
	tx, err := f.store.pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	created, err := integrations.CreateIntegrationTargetTx(ctx, tx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, IntegrationInstallID: f.install.ID, ChannelDefinitionID: f.target.ChannelDefinitionID,
		ProviderRef: "uncommitted-thread", ProviderRefKind: "thread",
	})
	require.NoError(t, err)
	resolved, err := integrations.GetIntegrationTargetByProviderRefTx(
		ctx, tx, testProjectID, f.install.ID, created.ProviderRef)
	require.NoError(t, err)
	require.Equal(t, created.ID, resolved.ID)
	require.Equal(t, created.ChannelDefinitionID, resolved.ChannelDefinitionID)
	_, err = integrations.GetIntegrationTargetByProviderRef(ctx, testProjectID, f.install.ID, created.ProviderRef)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "uncommitted registration is visible only inside the owning transaction")
	for _, scope := range []struct{ project, install ID }{
		{uuid.New(), f.install.ID}, {testProjectID, uuid.New()},
	} {
		_, err = integrations.GetIntegrationTargetByProviderRefTx(ctx, tx, scope.project, scope.install, created.ProviderRef)
		require.ErrorIs(t, err, storeerr.ErrNotFound, "an address alone confers no project or connection scope")
	}
	_, err = integrations.GetIntegrationTargetByProviderRefTx(
		ctx, tx, testProjectID, f.install.ID, " "+created.ProviderRef)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "lookup preserves opaque provider-reference identity")
	require.NoError(t, tx.Rollback(ctx))
	_, err = integrations.GetIntegrationTargetByProviderRef(ctx, testProjectID, f.install.ID, created.ProviderRef)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "lookup does not register a reference after its creation rolls back")
}

func externalConnectionInput(userID ID) integrationstore.CreateExternalIntegrationInstallInput {
	return integrationstore.CreateExternalIntegrationInstallInput{
		OrgID: testOrgID, ProjectID: testProjectID, InstalledBy: identitystore.NewUserPrincipal(userID),
		DisplayName: "Customer connector", Metadata: json.RawMessage(`{"team":"support"}`),
	}
}

func externalDefinitionInput(installID ID) integrationstore.PublishChannelDefinitionInput {
	return integrationstore.PublishChannelDefinitionInput{
		ProjectID: testProjectID, IntegrationInstallID: installID, ImplementationKey: "conversation",
		Kind:             integrationstore.ChannelKindExternal,
		SendParamsSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		Capabilities:     integrationstore.ChannelCapabilities{Read: true, Send: true, Text: true, CreatesReplyChannel: true},
	}
}

func TestExternalConnectionRegistrationRetainsRealPrincipalWithoutPhysicalIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newChannelAuthorityFixture(t, ctx, "external-registration")
	user := createIntegrationProjectAdmin(t, ctx, f.Store, "external-owner@example.com")
	var appsBefore, appsAfter int
	require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT count(*) FROM integration_apps`).Scan(&appsBefore))
	input := externalConnectionInput(user.ID)
	created, err := f.Store.Integrations().CreateExternalIntegrationInstall(ctx, input)
	require.NoError(t, err)
	require.True(t, created.Created)
	require.Equal(t, input.InstalledBy, created.InstalledBy)
	require.Equal(t, integrationstore.IntegrationKindExternal, created.IntegrationKind)
	require.Equal(t, "api", created.ConnectionMode)
	require.Equal(t, input.DisplayName, created.DisplayName)
	require.JSONEq(t, string(input.Metadata), string(created.Metadata))
	require.Empty(t, created.Provider)
	require.Empty(t, created.ProviderAccountRef)
	require.Equal(t, NilID, created.IntegrationAppID)
	var honestShape bool
	require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT integration_app_id IS NULL AND provider IS NULL
		AND provider_tenant_id IS NULL AND provider_account_ref IS NULL AND credential_secret_id IS NULL
		AND last_oauth_flow_id IS NULL
		AND provider_config = '{}'::jsonb AND provider_identity = '{}'::jsonb
		FROM integration_installs WHERE id = $1`, created.ID).Scan(&honestShape))
	require.True(t, honestShape, "absence is SQL NULL, not invented provider/app identity")
	require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT count(*) FROM integration_apps`).Scan(&appsAfter))
	require.Equal(t, appsBefore, appsAfter)
	second, err := f.Store.Integrations().CreateExternalIntegrationInstall(ctx, input)
	require.NoError(t, err)
	require.NotEqual(t, created.ID, second.ID, "external registration has no provider-account upsert key")

	_, err = f.Store.Integrations().GetConnectorIntegrationInstallByID(
		ctx, f.AppID, created.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = f.Store.Integrations().ReceiveIntegrationEvent(ctx, integrationstore.ReceiveIntegrationEventInput{
		ProjectID: testProjectID, IntegrationInstallID: created.ID, EventID: "not-managed",
		Payload: json.RawMessage(`{}`), Capabilities: testChannelCapabilities(testChannelProvider),
	})
	require.ErrorIs(t, err, storeerr.ErrNotFound, "managed connector capability does not own external connections")

	for _, statement := range []string{
		`UPDATE integration_installs SET connection_mode = 'webhook' WHERE id = $1`,
		`UPDATE integration_installs SET provider_config = '{"token":"fake"}' WHERE id = $1`,
		`UPDATE integration_installs SET provider_identity = '{"tenant":"fake"}' WHERE id = $1`,
	} {
		_, err := f.Store.pool.Exec(ctx, statement, created.ID)
		require.True(t, isPgCode(err, "23514"), "external shape rejects managed fields: %v", err)
	}
	_, err = f.Store.pool.Exec(ctx,
		`UPDATE integration_installs SET integration_kind = 'managed' WHERE id = $1`, created.ID)
	require.True(t, isPgCode(err, "25006"), "kind is immutable: %v", err)

	key, err := f.Store.Identity().CreateOrgAPIKeyWithPlaintext(ctx, identitystore.CreateOrgAPIKeyInput{
		OrgID: testOrgID, CreatedByUserID: user.ID, Name: "External installer", OrgRole: "member",
	})
	require.NoError(t, err)
	principal, err := f.Store.Identity().AuthenticateOrgAPIKey(ctx, key.Token)
	require.NoError(t, err)
	input.InstalledBy = principal
	_, err = f.Store.Integrations().CreateExternalIntegrationInstall(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrUnauthorized, "org membership alone grants no project setup")
	_, err = f.Store.Identity().SetOrgAPIKeyProjectRole(ctx, identitystore.OrgAPIKeyProjectRoleInput{
		OrgID: testOrgID, KeyID: key.Record.ID, ProjectID: testProjectID, Role: "developer",
	})
	require.NoError(t, err)
	keyInstall, err := f.Store.Integrations().CreateExternalIntegrationInstall(ctx, input)
	require.NoError(t, err)
	require.Equal(t, principal, keyInstall.InstalledBy)
	_, err = f.Store.Identity().RevokeOrgAPIKey(ctx, testOrgID, key.Record.ID, identitystore.PrincipalRecord{})
	require.NoError(t, err)
	_, err = f.Store.Integrations().CreateExternalIntegrationInstall(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrUnauthorized)
	input = externalConnectionInput(user.ID)
	input.ProjectID = uuid.New()
	_, err = f.Store.Integrations().CreateExternalIntegrationInstall(ctx, input)
	require.Error(t, err, "request project identity does not grant authority")
}

func TestExternalChannelsUseLiveGrantsAndActualInputAttribution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newChannelAuthorityFixture(t, ctx, "external-authority")
	user := createIntegrationProjectAdmin(t, ctx, f.Store, "external-input-owner@example.com")
	install, err := f.Store.Integrations().CreateExternalIntegrationInstall(ctx, externalConnectionInput(user.ID))
	require.NoError(t, err)
	definitionInput := externalDefinitionInput(install.ID)
	definition, err := f.Store.Integrations().PublishExternalChannelDefinition(ctx, definitionInput)
	require.NoError(t, err)
	target, err := f.Store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, IntegrationInstallID: install.ID, ChannelDefinitionID: definition.ID,
		ProviderRef: "ticket-123", ProviderRefKind: "ticket",
	})
	require.NoError(t, err)
	f.InstallID, f.Target = install.ID, target
	grant := f.bindingInput("customer-setup")
	grant.ReceiveAllowed, grant.ReadAllowed = true, true
	binding, err := f.Store.Integrations().CreateIntegrationTargetBinding(ctx, grant)
	require.NoError(t, err)
	access, err := f.Store.Integrations().GetAgentChannelAccess(ctx, testProjectID, f.AgentID, target.ID)
	require.NoError(t, err)
	require.True(t, access.Active)
	require.Equal(t, integrationstore.IntegrationKindExternal, access.IntegrationKind)
	require.Equal(t, NilID, access.IntegrationAppID)
	require.True(t, access.Capabilities.Read)
	require.False(t, access.Capabilities.Send)
	page, err := f.Store.Integrations().ListAgentChannelTargets(ctx, testProjectID, f.AgentID,
		integrationstore.ListAgentChannelTargetsInput{Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Targets, 1)
	require.Equal(t, integrationstore.IntegrationKindExternal, page.Targets[0].IntegrationKind)
	eligibility, err := f.Store.Integrations().GetAgentChannelToolEligibility(ctx, testProjectID, f.AgentID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.AgentChannelToolEligibility{List: true, Read: true}, eligibility)
	op := f.operationInput()
	op.Operation = integrationstore.ChannelBindingOperationRead
	prepared, err := f.prepareOrRecheck(t, ctx, op, nil)
	require.NoError(t, err)
	require.Equal(t, binding.ID, prepared.ID)
	_, err = executionstore.IntegrationSetAgentIntegrationTarget(ctx, f.Store.q, testProjectID, f.AgentID, target.ID)
	require.NoError(t, err, "explicit current-channel selection works without a managed app")

	self, err := executionstore.OmnaraActorParams(testOrgID, identitystore.NewUserPrincipal(user.ID))
	require.NoError(t, err)
	key, err := f.Store.Identity().CreateOrgAPIKeyWithPlaintext(ctx, identitystore.CreateOrgAPIKeyInput{
		OrgID: testOrgID, CreatedByUserID: user.ID, Name: "Input author", OrgRole: "member",
	})
	require.NoError(t, err)
	_, err = f.Store.Identity().SetOrgAPIKeyProjectRole(ctx, identitystore.OrgAPIKeyProjectRoleInput{
		OrgID: testOrgID, KeyID: key.Record.ID, ProjectID: testProjectID, Role: "developer",
	})
	require.NoError(t, err)
	principal, err := f.Store.Identity().AuthenticateOrgAPIKey(ctx, key.Token)
	require.NoError(t, err)
	keySelf, err := executionstore.OmnaraActorParams(testOrgID, principal)
	require.NoError(t, err)
	actors := []*executionstore.ActorParams{
		self, keySelf,
		{Provider: identitystore.ActorProviderExternal, ProviderUserID: "customer-author"},
	}
	for _, actor := range actors {
		created, _, _, err := f.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
			ProjectID: testProjectID, AgentID: f.AgentID, Actor: actor,
			IntegrationTargetID: target.ID, IntegrationTargetBindingID: binding.ID,
			ContentBlocks: json.RawMessage(`[{"type":"text","text":"customer input"}]`),
			DeliveryMode:  executionstore.DeliveryModeSteering, IdempotencyKey: actor.Provider + ":" + actor.ProviderUserID,
		})
		require.NoError(t, err, "authorized Omnara self-attribution and external authors are both valid")
		require.Equal(t, target.ID, created.IntegrationTargetID)
		var provider, author string
		require.NoError(t, f.Store.pool.QueryRow(ctx,
			`SELECT actor.provider, actor.provider_user_id FROM actors actor
            JOIN agent_inputs input ON input.actor_id = actor.id WHERE input.id = $1`,
			created.ID).Scan(&provider, &author))
		require.Equal(t, actor.Provider, provider)
		require.Equal(t, actor.ProviderUserID, author)
	}
	_, _, _, err = f.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID: testProjectID, AgentID: f.AgentID,
		Actor: &executionstore.ActorParams{
			Provider: "slack", ProviderUserID: "forged-provider", ProviderTenantID: "T_OTHER",
		},
		IntegrationTargetID: target.ID, IntegrationTargetBindingID: binding.ID,
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"forged"}]`),
		DeliveryMode:  executionstore.DeliveryModeSteering, IdempotencyKey: "forged",
	})
	require.ErrorIs(t, err, storeerr.ErrUnauthorized)
	var forgedActors int
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM actors WHERE provider_user_id = 'forged-provider'`).Scan(&forgedActors))
	require.Zero(t, forgedActors, "invalid input also rolls back its actor")

	definitionInput.Capabilities.Read = false
	_, err = f.Store.Integrations().PublishExternalChannelDefinition(ctx, definitionInput)
	require.NoError(t, err)
	eligibility, err = f.Store.Integrations().GetAgentChannelToolEligibility(ctx, testProjectID, f.AgentID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.AgentChannelToolEligibility{List: true}, eligibility,
		"live grant without current implementation support cannot expose a read tool")
	_, err = f.Store.Integrations().DisableIntegrationInstall(ctx, integrationstore.DisableIntegrationInstallInput{
		ProjectID: testProjectID, ID: install.ID, ExpectedOAuthFlowID: &install.LastOAuthFlowID,
	})
	require.NoError(t, err)
	_, err = f.prepareOrRecheck(t, ctx, op, &prepared)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = executionstore.IntegrationSetAgentIntegrationTarget(ctx, f.Store.q, testProjectID, f.AgentID, target.ID)
	require.Error(t, err)
	_, err = f.Store.Integrations().PublishExternalChannelDefinition(ctx, definitionInput)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
}

func TestManagedConnectionNullTenantIdentityAndDefinitionScope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newChannelAuthorityFixture(t, ctx, "managed-null-tenant")
	user := createIntegrationProjectAdmin(t, ctx, f.Store, "managed-null-owner@example.com")
	input := integrationstore.UpsertIntegrationInstallInput{
		OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: f.AppID,
		InstalledBy: identitystore.NewUserPrincipal(user.ID), IntegrationKind: integrationstore.IntegrationKindManaged,
		Provider: testChannelProvider, ProviderAccountRef: "tenantless-account", ConnectionMode: "gateway",
		State: integrationstore.IntegrationInstallStateActive,
	}
	results := make([]integrationstore.IntegrationInstallRecord, 2)
	errs := make([]error, 2)
	var workers sync.WaitGroup
	for i := range results {
		workers.Go(func() { results[i], errs[i] = f.Store.Integrations().UpsertIntegrationInstall(ctx, input) })
	}
	workers.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, results[0].ID, results[1].ID)
	var nullTenant bool
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT provider_tenant_id IS NULL FROM integration_installs WHERE id = $1`, results[0].ID).Scan(&nullTenant))
	require.True(t, nullTenant)
	loaded, err := f.Store.Integrations().GetConnectorIntegrationInstall(
		ctx, f.AppID, "", input.ProviderAccountRef)
	require.NoError(t, err)
	require.Equal(t, results[0].ID, loaded.ID)
	_, agent, otherApp, _ := createChannelLifecycleFixture(t, ctx, f.Store, "same-provider-other-app")
	input.IntegrationAppID = otherApp.ID
	other, err := f.Store.Integrations().UpsertIntegrationInstall(ctx, input)
	require.NoError(t, err, "physical account identity is scoped to its real app")
	require.NotEqual(t, loaded.ID, other.ID)
	_, err = f.Store.Integrations().CreateIntegrationRoute(ctx, integrationstore.CreateIntegrationRouteInput{
		ProjectID: testProjectID, IntegrationInstallID: other.ID, AgentProfileID: agent.AgentProfileID,
		DeploymentKey: "profile-setup", BehaviorKey: "conversation",
		State: integrationstore.IntegrationRouteStateActive,
	})
	require.NoError(t, err)
	listed, err := f.Store.Integrations().ListIntegrationInstallsForProject(ctx,
		integrationstore.ListIntegrationInstallsForProjectInput{
			ProjectID: testProjectID, Limit: 10,
			Filters: integrationstore.IntegrationInstallListFilters{AgentProfileID: agent.AgentProfileID},
		})
	require.NoError(t, err)
	require.Len(t, listed.Installs, 1)
	require.Equal(t, other.ID, listed.Installs[0].ID, "setup-status profile filter follows the route association")
	definitionInput := externalDefinitionInput(other.ID)
	_, err = f.Store.Integrations().PublishExternalChannelDefinition(ctx, definitionInput)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "public external setup cannot publish a managed connection")
	definitionInput.Kind = integrationstore.ChannelKindSlackThread
	definitionInput.ConnectorCapabilities = testChannelCapabilities(testChannelProvider)
	_, err = f.Store.Integrations().PublishConnectorChannelDefinition(ctx, definitionInput)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest, "storage rejects provider-kind substitution")
	_, err = f.Store.pool.Exec(ctx, `UPDATE integration_apps SET state = 'disabled' WHERE id = $1`, otherApp.ID)
	require.NoError(t, err)
	_, err = f.Store.Integrations().GetConnectorIntegrationInstall(
		ctx, otherApp.ID, "", input.ProviderAccountRef)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "missing live managed app is never treated as external authority")
}
