//go:build integration

package executionstore_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

func TestIntegrationInstallDestinationsAreNativeCompatibilityOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	admin := createIntegrationProjectAdmin(t, ctx, store, "install-ownership@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "install-ownership-profile")
	agent := createIntegrationBoundAgent(t, ctx, store, profile, admin.ID, "install-ownership-agent")

	connectorApp, err := store.Integrations().CreateIntegrationApp(
		ctx,
		integrationstore.CreateIntegrationAppInput{
			OrgID: testOrgID, OwnerProjectID: testProjectID,
			Provider: testChannelProvider, ProviderAppRef: "install-ownership-connector",
			DisplayName: "Connector app", ConnectorKey: testChannelConnector,
			State: integrationstore.IntegrationAppStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create connector app: %v", err)
	}
	connectorInput := integrationstore.UpsertIntegrationInstallInput{
		OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: connectorApp.ID,
		InstalledByUserID: admin.ID, Provider: testChannelProvider,
		IntegrationKind: "agentless_connector", ConnectionMode: "gateway",
		State:            integrationstore.IntegrationInstallStateActive,
		ProviderTenantID: "install-ownership-tenant", ProviderAccountRef: "connector-account",
	}
	withAgent := connectorInput
	withAgent.AgentID = agent.ID
	if _, err := store.Integrations().UpsertIntegrationInstall(ctx, withAgent); err == nil ||
		!strings.Contains(err.Error(), "cannot own an agent") {
		t.Fatalf("connector install with agent error = %v", err)
	}
	withProfile := connectorInput
	withProfile.AgentProfileID = profile.ID
	if _, err := store.Integrations().UpsertIntegrationInstall(ctx, withProfile); err == nil ||
		!strings.Contains(err.Error(), "cannot own an agent") {
		t.Fatalf("connector install with profile error = %v", err)
	}
	connectorInstall, err := store.Integrations().UpsertIntegrationInstall(ctx, connectorInput)
	if err != nil {
		t.Fatalf("create agentless connector install: %v", err)
	}
	if connectorInstall.AgentID != integrationstore.NilID ||
		connectorInstall.AgentProfileID != integrationstore.NilID {
		t.Fatalf("connector installation retained a destination: %+v", connectorInstall)
	}
	connectorTarget, err := store.Integrations().GetOrCreateIntegrationTargetForBinding(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: testProjectID, AgentID: agent.ID,
			IntegrationInstallID: connectorInstall.ID,
			ProviderRef:          "connector-binding-channel", ProviderRefKind: "channel",
		},
	)
	if err != nil {
		t.Fatalf("create connector target: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO integration_target_bindings(
  project_id, agent_id, integration_install_id, integration_target_id,
  target_created_at, integration_route_id, receive_allowed, send_allowed,
  source, created_at, updated_at
)
VALUES ($1, $2, $3, $4, $5, NULL, true, true,
        'legacy_target', statement_timestamp(), statement_timestamp())
`, testProjectID, agent.ID, connectorInstall.ID, connectorTarget.ID, connectorTarget.CreatedAt); !isPgCode(err, "23514") {
		t.Fatalf("raw connector legacy binding error = %v, want SQLSTATE 23514", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO integration_targets(
  project_id, agent_id, integration_install_id, target_ref, provider_ref,
  provider_ref_kind, created_at, updated_at
) VALUES ($1, $2, $3, 'raw-connector-target', 'raw-connector-channel',
          'channel', $4, $4)
`, testProjectID, agent.ID, connectorInstall.ID, time.Now()); !isPgCode(err, "23514") {
		t.Fatalf("raw connector target ownership insert error = %v, want SQLSTATE 23514", err)
	}

	if _, err := pool.Exec(ctx, `
INSERT INTO integration_installs(
  org_id, project_id, integration_app_id, agent_id, installed_by_user_id,
  provider, integration_kind, connection_mode, state,
  provider_tenant_id, provider_account_ref, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, 'raw_connector', 'gateway', 'active',
          'raw-connector-tenant', 'raw-connector-account', $7, $7)
`, testOrgID, testProjectID, connectorApp.ID, agent.ID, admin.ID, testChannelProvider, time.Now()); !isPgCode(err, "23514") {
		t.Fatalf("raw connector destination insert error = %v, want SQLSTATE 23514", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_installs SET agent_id = $2 WHERE id = $1`,
		connectorInstall.ID,
		agent.ID,
	); !isPgCode(err, "25006") {
		t.Fatalf("connector destination update error = %v, want SQLSTATE 25006", err)
	}

	nativeApp, err := store.Integrations().CreateIntegrationApp(
		ctx,
		integrationstore.CreateIntegrationAppInput{
			OrgID: testOrgID, OwnerProjectID: testProjectID,
			Provider: "slack", ProviderAppRef: "install-ownership-native",
			DisplayName: "Native Slack app", ConnectorKey: "native_slack_v1",
			State: integrationstore.IntegrationAppStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create native app: %v", err)
	}
	nativeInput := integrationstore.UpsertIntegrationInstallInput{
		OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: nativeApp.ID,
		InstalledByUserID: admin.ID, Provider: "slack",
		IntegrationKind: "mention_agent", ConnectionMode: "webhook",
		State:            integrationstore.IntegrationInstallStateActive,
		ProviderTenantID: "native-workspace", ProviderAccountRef: "native-app",
	}
	if _, err := store.Integrations().UpsertIntegrationInstall(ctx, nativeInput); err == nil ||
		!strings.Contains(err.Error(), "require exactly one") {
		t.Fatalf("native install without destination error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO integration_installs(
  org_id, project_id, integration_app_id, installed_by_user_id,
  provider, integration_kind, connection_mode, state,
  provider_tenant_id, provider_account_ref, created_at, updated_at
) VALUES ($1, $2, $3, $4, 'slack', 'raw_native', 'webhook', 'active',
          'raw-native-workspace', 'raw-native-app', $5, $5)
`, testOrgID, testProjectID, nativeApp.ID, admin.ID, time.Now()); !isPgCode(err, "23514") {
		t.Fatalf("raw native destinationless insert error = %v, want SQLSTATE 23514", err)
	}
	nativeInput.AgentID = agent.ID
	nativeInstall, err := store.Integrations().UpsertIntegrationInstall(ctx, nativeInput)
	if err != nil {
		t.Fatalf("create native install with destination: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO integration_targets(
  project_id, agent_id, integration_install_id, target_ref, provider_ref,
  provider_ref_kind, created_at, updated_at
) VALUES ($1, NULL, $2, 'raw-native-target', 'raw-native-channel',
          'channel', $3, $3)
`, testProjectID, nativeInstall.ID, time.Now()); !isPgCode(err, "23514") {
		t.Fatalf("raw native target ownership insert error = %v, want SQLSTATE 23514", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_installs SET agent_id = NULL WHERE id = $1`,
		nativeInstall.ID,
	); !isPgCode(err, "25006") {
		t.Fatalf("native destination update error = %v, want SQLSTATE 25006", err)
	}

	var nativeTargetID integrationstore.ID
	if err := pool.QueryRow(ctx, `
INSERT INTO integration_targets(
  project_id, agent_id, integration_install_id, target_ref, provider_ref,
  provider_ref_kind, created_at, updated_at
) VALUES ($1, $2, $3, 'raw-native-valid-target', 'raw-native-valid-channel',
          'channel', statement_timestamp(), statement_timestamp())
RETURNING id
`, testProjectID, agent.ID, nativeInstall.ID).Scan(&nativeTargetID); err != nil {
		t.Fatalf("raw native target insert: %v", err)
	}
	var bindingAgentID integrationstore.ID
	var source string
	var routeMissing, receiveAllowed, sendAllowed bool
	if err := pool.QueryRow(ctx, `
SELECT agent_id, source, integration_route_id IS NULL, receive_allowed, send_allowed
FROM integration_target_bindings
WHERE project_id = $1 AND integration_target_id = $2 AND revoked_at IS NULL
`, testProjectID, nativeTargetID).Scan(
		&bindingAgentID, &source, &routeMissing, &receiveAllowed, &sendAllowed,
	); err != nil {
		t.Fatalf("load raw native compatibility binding: %v", err)
	}
	if bindingAgentID != agent.ID || source != "legacy_target" ||
		!routeMissing || !receiveAllowed || !sendAllowed {
		t.Fatalf(
			"raw native compatibility binding agent=%s source=%q route_missing=%t receive=%t send=%t",
			bindingAgentID, source, routeMissing, receiveAllowed, sendAllowed,
		)
	}
}
