//go:build integration

package storagetest

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

const (
	ChannelConnector = channelconnector.BuiltInConnectorKey
	ChannelProvider  = "discord"
	ChannelHandler   = "test_channel_single_agent"
)

func CreateIntegrationProjectAdmin(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	pool *pgxpool.Pool,
	orgID, projectID uuid.UUID,
	email string,
) identitystore.UserRecord {
	t.Helper()
	user, err := CreateVerifiedUser(ctx, pool, CreateVerifiedUserInput{
		Email:       email,
		DisplayName: "Integration Admin",
	})
	if err != nil {
		t.Fatalf("create integration admin user: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{OrgID: orgID, UserID: user.ID, Role: "admin"},
	); err != nil {
		t.Fatalf("add integration admin org membership: %v", err)
	}
	if _, err := store.Identity().AddProjectMembership(
		ctx,
		identitystore.AddProjectMembershipInput{
			OrgID:     orgID,
			ProjectID: projectID,
			UserID:    user.ID,
			Role:      "admin",
		},
	); err != nil {
		t.Fatalf("add integration admin project membership: %v", err)
	}
	return user
}

func CreateIntegrationTestProfile(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	orgID, projectID uuid.UUID,
	key string,
) executionstore.AgentProfileRecord {
	t.Helper()
	config := storagefixture.SeedAgentConfig(
		t, ctx, store.Models(), store.Execution(), orgID, projectID, integrationAgentYAML,
	)
	profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID: projectID, Name: "Integration Test Agent " + key,
		CurrentConfigID: config.ID, IdempotencyKey: "profile-" + key,
	})
	require.NoError(t, err)
	return profile
}

func CreateIntegrationBoundAgent(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	projectID uuid.UUID,
	profile executionstore.AgentProfileRecord,
	userID uuid.UUID,
	key string,
) executionstore.AgentRecord {
	t.Helper()
	launch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      projectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     identitystore.NewUserPrincipal(userID),
		IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("launch integration-bound agent: %v", err)
	}
	return launch.Agent
}

func CreateIntegrationCredential(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	orgID, projectID, createdByUserID uuid.UUID,
	label string,
) uuid.UUID {
	t.Helper()
	secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          orgID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: projectID,
		Name:           "integration-" + label,
		Material: secrets.SlackAppCredentialsMaterial{
			AccessToken: "xoxb-" + label, ClientID: "client-id-" + label,
			ClientSecret: "client-" + label, SigningSecret: "signing-" + label,
		},
		Actor: identitystore.NewUserPrincipal(createdByUserID),
	})
	if err != nil {
		t.Fatalf("create integration credential: %v", err)
	}
	return secret.ID
}

func SlackIntegrationInstallInput(
	orgID, projectID, appID, routeProfileID, installedByUserID, credentialSecretID uuid.UUID,
	providerAccountRef, providerTenantID string,
) integrationstore.UpsertIntegrationInstallInput {
	input := integrationstore.UpsertIntegrationInstallInput{
		OrgID: orgID, ProjectID: projectID, IntegrationAppID: appID,
		InstalledBy: identitystore.NewUserPrincipal(installedByUserID),
		Provider:    integrationstore.IntegrationProviderSlack, IntegrationKind: integrationstore.IntegrationKindManaged,
		ConnectionMode: "webhook", State: integrationstore.IntegrationInstallStateActive,
		ProviderTenantID: providerTenantID, ProviderAccountRef: providerAccountRef,
		DisplayName: "Omnara", CredentialSecretID: credentialSecretID,
		ProviderIdentity: json.RawMessage(fmt.Sprintf(`{"bot_user_id":%q}`, "B_"+providerAccountRef)),
		Metadata:         json.RawMessage(`{"team_name":"Acme"}`),
	}
	if routeProfileID != uuid.Nil {
		input.InitialRoute = &integrationstore.CreateIntegrationRouteInput{
			AgentProfileID: routeProfileID, DeploymentKey: "slack", BehaviorKey: "slack_conversation",
			State: integrationstore.IntegrationRouteStateActive, Configuration: json.RawMessage(`{}`),
		}
	}
	return input
}

func CreateSlackIntegrationApp(
	t *testing.T, ctx context.Context, store *storage.Store, orgID, projectID uuid.UUID, appRef string,
) uuid.UUID {
	t.Helper()
	app, err := store.Integrations().GetOrCreateIntegrationApp(ctx, integrationstore.CreateIntegrationAppInput{
		OrgID: orgID, OwnerProjectID: projectID, Provider: integrationstore.IntegrationProviderSlack,
		ProviderAppRef: appRef, ConnectorKey: channelconnector.BuiltInConnectorKey,
		InstallationCredentialKind: "slack_app_credentials", State: integrationstore.IntegrationAppStateActive,
	})
	require.NoError(t, err)
	return app.ID
}

func ExternalConnectionInput(
	orgID, projectID, userID uuid.UUID,
) integrationstore.CreateExternalIntegrationInstallInput {
	return integrationstore.CreateExternalIntegrationInstallInput{
		OrgID: orgID, ProjectID: projectID, InstalledBy: identitystore.NewUserPrincipal(userID),
		DisplayName: "Customer connector", Metadata: json.RawMessage(`{"team":"support"}`),
	}
}

func ExternalDefinitionInput(projectID, installID uuid.UUID) integrationstore.PublishChannelDefinitionInput {
	return integrationstore.PublishChannelDefinitionInput{
		ProjectID: projectID, IntegrationInstallID: installID, ImplementationKey: "conversation",
		Kind:             integrationstore.ChannelKindExternal,
		SendParamsSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		Capabilities:     integrationstore.ChannelCapabilities{Read: true, Send: true, Text: true, CreatesReplyChannel: true},
	}
}

func CreateChannelInstallationFixture(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	pool *pgxpool.Pool,
	orgID, projectID uuid.UUID,
	suffix string,
) (
	identitystore.UserRecord,
	integrationstore.IntegrationAppRecord,
	integrationstore.IntegrationInstallRecord,
) {
	t.Helper()
	admin := CreateIntegrationProjectAdmin(t, ctx, store, pool, orgID, projectID, suffix+"@example.com")
	app, err := store.Integrations().CreateIntegrationApp(
		ctx,
		integrationstore.CreateIntegrationAppInput{
			OrgID: orgID, OwnerProjectID: projectID,
			Provider: ChannelProvider, ProviderAppRef: suffix + "-app",
			DisplayName: suffix, ConnectorKey: ChannelConnector,
			State: integrationstore.IntegrationAppStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create lifecycle integration app: %v", err)
	}
	install, err := store.Integrations().UpsertIntegrationInstall(
		ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: orgID, ProjectID: projectID, IntegrationAppID: app.ID,
			InstalledBy: identitystore.NewUserPrincipal(admin.ID),
			Provider:    ChannelProvider, IntegrationKind: integrationstore.IntegrationKindManaged,
			ConnectionMode: "gateway", State: integrationstore.IntegrationInstallStateActive,
			ProviderTenantID: suffix + "-tenant", ProviderAccountRef: suffix + "-account",
			DisplayName: suffix,
		},
	)
	if err != nil {
		t.Fatalf("create lifecycle integration install: %v", err)
	}
	return admin, app, install
}

func CreateChannelTestDefinition(
	t *testing.T, ctx context.Context, store *storage.Store, install integrationstore.IntegrationInstallRecord,
) uuid.UUID {
	t.Helper()
	definition, err := store.Integrations().PublishConnectorChannelDefinition(ctx,
		integrationstore.PublishChannelDefinitionInput{
			ProjectID: install.ProjectID, IntegrationInstallID: install.ID,
			ImplementationKey: "conversation", Kind: integrationstore.ChannelKindDiscordThread,
			SendParamsSchema:      json.RawMessage(`{"type":"object","additionalProperties":false}`),
			Capabilities:          integrationstore.ChannelCapabilities{Read: true, Send: true, Text: true},
			ConnectorCapabilities: ChannelCapabilities(install.Provider),
		})
	if err != nil {
		t.Fatalf("publish test channel definition: %v", err)
	}
	return definition.ID
}

func ChannelCapabilities(provider string) []channelconnector.Capability {
	return []channelconnector.Capability{ChannelCapability(provider)}
}

func ChannelCapability(provider string) channelconnector.Capability {
	return channelconnector.Capability{ConnectorKey: ChannelConnector, Provider: provider}
}

const integrationAgentYAML = `
instruction: Reply to users.
model:
  provider_config: openai-prod
  name: gpt-test
tools:
  run_command: {}
`

func CreateChannelLifecycleFixture(
	t *testing.T, ctx context.Context, store *storage.Store,
	pool *pgxpool.Pool, orgID, projectID uuid.UUID, suffix string,
) (
	identitystore.UserRecord, executionstore.AgentRecord,
	integrationstore.IntegrationAppRecord, integrationstore.IntegrationInstallRecord,
) {
	t.Helper()
	admin, app, install := CreateChannelInstallationFixture(t, ctx, store, pool, orgID, projectID, suffix)
	profile := CreateIntegrationTestProfile(t, ctx, store, orgID, projectID, suffix+"-profile")
	agent := CreateIntegrationBoundAgent(t, ctx, store, projectID, profile, admin.ID, suffix+"-agent")
	return admin, agent, app, install
}
