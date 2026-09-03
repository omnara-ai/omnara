//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/bearertoken"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func connectorTestCapabilities(provider string) []channelconnector.Capability {
	return []channelconnector.Capability{connectorTestCapability(provider)}
}

func connectorTestCapability(provider string) channelconnector.Capability {
	return channelconnector.Capability{ConnectorKey: "chat_sdk_v1", Provider: provider}
}

func TestChannelConnectorExactConfigurationJourney(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	token, err := bearertoken.Generate(bearertoken.KindChannelConnector)
	if err != nil {
		t.Fatalf("generate connector token: %v", err)
	}
	wrongScopeToken, err := bearertoken.Generate(bearertoken.KindChannelConnector)
	if err != nil {
		t.Fatalf("generate wrong-scope connector token: %v", err)
	}
	authenticator, err := channelconnector.NewAuthenticator([]channelconnector.Config{
		{
			ID: "gateway-test", Token: token, Capabilities: connectorTestCapabilities("discord"),
		},
		{
			ID: "other-gateway", Token: wrongScopeToken,
			Capabilities: []channelconnector.Capability{
				{ConnectorKey: "chat_sdk_v1", Provider: "telegram"},
				{ConnectorKey: "custom_v1", Provider: "discord"},
			},
		},
	})
	if err != nil {
		t.Fatalf("create connector authenticator: %v", err)
	}
	handler := newIntegrationServer(
		pool,
		WithChannelConnectorAuthenticator(authenticator),
		WithInternalAPIOrigins([]string{"http://api:8080"}),
	)
	project := bootstrapPublicHTTPProject(t, handler, "connector-configuration")

	appSecret, _, err := project.Store.Secrets().CreateSecret(
		ctx,
		secretstore.CreateSecretInput{
			OrgID: project.OrgUUID, OwnerKind: secretstore.SecretOwnerOrg,
			Name: "connector app credentials",
			Material: secrets.IntegrationCredentialsMaterial{Values: map[string]string{
				"client_id": "app-client", "signing_secret": "app-signing-secret",
			}},
			Actor: httpUserPrincipal(project.AdminUserUUID),
		},
	)
	if err != nil {
		t.Fatalf("create connector app credential: %v", err)
	}
	installSecret, _, err := project.Store.Secrets().CreateSecret(
		ctx,
		secretstore.CreateSecretInput{
			OrgID: project.OrgUUID, OwnerKind: secretstore.SecretOwnerProject,
			OwnerProjectID: project.ProjectUUID, Name: "connector install credentials",
			Material: secrets.IntegrationCredentialsMaterial{Values: map[string]string{
				"bot_token": "install-bot-token",
			}},
			Actor: httpUserPrincipal(project.AdminUserUUID),
		},
	)
	if err != nil {
		t.Fatalf("create connector installation credential: %v", err)
	}

	store := project.Store
	app, err := store.Integrations().CreateIntegrationApp(
		ctx,
		integrationstore.CreateIntegrationAppInput{
			OrgID: project.OrgUUID, Provider: "discord",
			ProviderAppRef: "connector-configuration-app", DisplayName: "Configuration app",
			ConnectorKey: "chat_sdk_v1", CredentialSecretID: appSecret.ID,
			InstallationCredentialKind: string(secrets.KindIntegrationCredentials),
			ProviderConfig:             json.RawMessage(`{"gateway_intents":["messages"]}`),
			ProviderMetadata:           json.RawMessage(`{"environment":"test"}`),
			State:                      integrationstore.IntegrationAppStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create connector app: %v", err)
	}
	install, err := store.Integrations().UpsertIntegrationInstall(
		ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, IntegrationAppID: app.ID,
			InstalledByUserID: project.AdminUserUUID,
			Provider:          "discord", IntegrationKind: "channel_single_agent", ConnectionMode: "gateway",
			State:            integrationstore.IntegrationInstallStateActive,
			ProviderTenantID: "guild-configuration", ProviderAccountRef: "bot-configuration",
			ProviderAgentDisplayName: "Configuration bot", CredentialSecretID: installSecret.ID,
			ProviderConfig:   json.RawMessage(`{"respond_to":"mentions"}`),
			ProviderIdentity: json.RawMessage(`{"bot_user_id":"bot-user-1"}`),
			ProviderMetadata: json.RawMessage(`{"tenant_name":"Test guild"}`),
		},
	)
	if err != nil {
		t.Fatalf("create connector installation: %v", err)
	}
	secondProject, err := store.Identity().CreateProjectForPrincipal(
		ctx,
		identitystore.CreateProjectForPrincipalInput{
			OrgID: project.OrgUUID, Creator: identitystore.NewUserPrincipal(project.AdminUserUUID),
			Name: "Second connector customer", IdempotencyKey: "connector-configuration-second",
		},
	)
	if err != nil {
		t.Fatalf("create second connector project: %v", err)
	}
	secondInstallSecret, _, err := store.Secrets().CreateSecret(
		ctx,
		secretstore.CreateSecretInput{
			OrgID: project.OrgUUID, OwnerKind: secretstore.SecretOwnerProject,
			OwnerProjectID: secondProject.ID, Name: "second connector install credentials",
			Material: secrets.IntegrationCredentialsMaterial{Values: map[string]string{
				"bot_token": "second-install-bot-token",
			}},
			Actor: httpUserPrincipal(project.AdminUserUUID),
		},
	)
	if err != nil {
		t.Fatalf("create second connector installation credential: %v", err)
	}
	secondInstall, err := store.Integrations().UpsertIntegrationInstall(
		ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: project.OrgUUID, ProjectID: secondProject.ID, IntegrationAppID: app.ID,
			InstalledByUserID: project.AdminUserUUID,
			Provider:          "discord", IntegrationKind: "channel_single_agent", ConnectionMode: "gateway",
			State:            integrationstore.IntegrationInstallStateActive,
			ProviderTenantID: "guild-second", ProviderAccountRef: "bot-second",
			ProviderAgentDisplayName: "Second bot", CredentialSecretID: secondInstallSecret.ID,
		},
	)
	if err != nil {
		t.Fatalf("create second connector installation: %v", err)
	}
	for _, resolved := range []struct {
		name      string
		load      func() (integrationstore.IntegrationInstallRecord, error)
		projectID integrationstore.ID
		installID integrationstore.ID
	}{
		{
			name: "first external identity",
			load: func() (integrationstore.IntegrationInstallRecord, error) {
				return store.Integrations().GetConnectorIntegrationInstall(
					ctx, app.ID, "guild-configuration", "bot-configuration",
				)
			},
			projectID: project.ProjectUUID, installID: install.ID,
		},
		{
			name: "second external identity",
			load: func() (integrationstore.IntegrationInstallRecord, error) {
				return store.Integrations().GetConnectorIntegrationInstall(
					ctx, app.ID, "guild-second", "bot-second",
				)
			},
			projectID: secondProject.ID, installID: secondInstall.ID,
		},
		{
			name: "second opaque id",
			load: func() (integrationstore.IntegrationInstallRecord, error) {
				return store.Integrations().GetConnectorIntegrationInstallByID(
					ctx, app.ID, secondInstall.ID,
				)
			},
			projectID: secondProject.ID, installID: secondInstall.ID,
		},
	} {
		record, err := resolved.load()
		if err != nil {
			t.Fatalf("resolve %s: %v", resolved.name, err)
		}
		if record.ProjectID != resolved.projectID || record.ID != resolved.installID {
			t.Fatalf("resolve %s = project %s install %s", resolved.name, record.ProjectID, record.ID)
		}
	}

	appID := testPublicID(t, publicid.KindIntegrationApp, app.ID)
	installID := testPublicID(t, publicid.KindIntegrationInstall, install.ID)
	appPath := "/api/v1/channel-connector/apps/" + appID + "/configuration"
	installPath := "/api/v1/channel-connector/apps/" + appID +
		"/installations/" + installID + "/configuration"
	resolvePath := "/api/v1/channel-connector/apps/" + appID +
		"/installations/resolve?external_tenant_id=guild-configuration&external_account_ref=bot-configuration"

	appConfiguration := requestJSONWithHeaders(
		t, handler, http.MethodGet, appPath, "", "", http.StatusOK, authHeaders(token),
	)
	internalRequest := httptest.NewRequest(http.MethodGet, appPath, nil)
	internalRequest.Host = "api:8080"
	internalRequest.Header.Set("Authorization", "Bearer "+token)
	internalResponse := httptest.NewRecorder()
	handler.ServeHTTP(internalResponse, internalRequest)
	if internalResponse.Code != http.StatusOK {
		t.Fatalf("Compose internal API journey status = %d, body = %s", internalResponse.Code, internalResponse.Body)
	}
	if got := internalResponse.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Compose internal API Cache-Control = %q, want no-store", got)
	}
	appBody := requiredChannelObject(t, appConfiguration, "app")
	if appBody["id"] != appID || appBody["provider"] != "discord" ||
		appBody["connector_key"] != "chat_sdk_v1" || appBody["configuration_revision"] != float64(1) {
		t.Fatalf("connector app configuration = %+v", appBody)
	}
	assertChannelConfigurationHasNoTenantAuthority(t, appBody)
	assertChannelCredential(
		t,
		appConfiguration,
		map[string]string{"client_id": "app-client", "signing_secret": "app-signing-secret"},
	)

	installConfiguration := requestJSONWithHeaders(
		t, handler, http.MethodGet, installPath, "", "", http.StatusOK, authHeaders(token),
	)
	assertChannelInstallationConfiguration(
		t,
		installConfiguration,
		appID,
		installID,
		1,
		1,
		"guild-configuration",
		"bot-configuration",
		"install-bot-token",
	)
	resolvedConfiguration := requestJSONWithHeaders(
		t, handler, http.MethodGet, resolvePath, "", "", http.StatusOK, authHeaders(token),
	)
	assertChannelInstallationConfiguration(
		t,
		resolvedConfiguration,
		appID,
		installID,
		1,
		1,
		"guild-configuration",
		"bot-configuration",
		"install-bot-token",
	)
	for _, invalidResolvePath := range []string{
		"/api/v1/channel-connector/apps/" + appID +
			"/installations/resolve?external_tenant_id=guild-configuration&external_account_ref=%20%20",
		"/api/v1/channel-connector/apps/" + appID +
			"/installations/resolve?external_tenant_id=guild%00configuration&external_account_ref=bot-configuration",
	} {
		requestJSONWithHeaders(
			t,
			handler,
			http.MethodGet,
			invalidResolvePath,
			"",
			"",
			http.StatusBadRequest,
			authHeaders(token),
		)
	}
	secondInstallID := testPublicID(t, publicid.KindIntegrationInstall, secondInstall.ID)
	secondResolvePath := "/api/v1/channel-connector/apps/" + appID +
		"/installations/resolve?external_tenant_id=guild-second&external_account_ref=bot-second"
	secondResolved := requestJSONWithHeaders(
		t, handler, http.MethodGet, secondResolvePath, "", "", http.StatusOK, authHeaders(token),
	)
	assertChannelInstallationConfiguration(
		t, secondResolved, appID, secondInstallID, 1, 1,
		"guild-second", "bot-second", "second-install-bot-token",
	)
	otherApp, err := store.Integrations().CreateIntegrationApp(
		ctx,
		integrationstore.CreateIntegrationAppInput{
			OrgID: project.OrgUUID, OwnerProjectID: secondProject.ID,
			Provider: "discord", ProviderAppRef: "connector-configuration-other-app",
			DisplayName: "Other configuration app", ConnectorKey: "chat_sdk_v1",
			State: integrationstore.IntegrationAppStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create other connector app: %v", err)
	}
	otherAppID := testPublicID(t, publicid.KindIntegrationApp, otherApp.ID)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/channel-connector/apps/"+otherAppID+"/installations/"+installID+"/configuration",
		"",
		"",
		http.StatusNotFound,
		authHeaders(token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		"/api/v1/channel-connector/apps/"+otherAppID+
			"/installations/resolve?external_tenant_id=guild-configuration&external_account_ref=bot-configuration",
		"",
		"",
		http.StatusNotFound,
		authHeaders(token),
	)

	requestJSONWithHeaders(
		t, handler, http.MethodGet, appPath, "", "", http.StatusUnauthorized, nil,
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodGet,
		appPath,
		"",
		"",
		http.StatusNotFound,
		authHeaders(wrongScopeToken),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/channel-connector/deliveries/claim",
		`{"owner":"wrong-gateway","lease_ms":30000,"limit":1,"capability":{"connector_key":"chat_sdk_v1","provider":"discord"}}`,
		"",
		http.StatusForbidden,
		authHeaders(wrongScopeToken),
	)

	if _, _, err := store.Secrets().CreateSecretVersion(
		ctx,
		secretstore.CreateSecretVersionInput{
			OrgID: project.OrgUUID, SecretID: appSecret.ID,
			Material: secrets.IntegrationCredentialsMaterial{Values: map[string]string{
				"client_id": "app-client", "signing_secret": "rotated-app-signing-secret",
			}},
			Actor: httpUserPrincipal(project.AdminUserUUID),
		},
	); err != nil {
		t.Fatalf("rotate connector app credential: %v", err)
	}
	rotatedAppConfiguration := requestJSONWithHeaders(
		t, handler, http.MethodGet, appPath, "", "", http.StatusOK, authHeaders(token),
	)
	rotatedApp := requiredChannelObject(t, rotatedAppConfiguration, "app")
	if rotatedApp["configuration_revision"] != float64(2) {
		t.Fatalf("rotated app configuration revision = %+v", rotatedApp)
	}
	assertChannelCredential(
		t,
		rotatedAppConfiguration,
		map[string]string{
			"client_id": "app-client", "signing_secret": "rotated-app-signing-secret",
		},
	)
	appRotatedInstall := requestJSONWithHeaders(
		t, handler, http.MethodGet, installPath, "", "", http.StatusOK, authHeaders(token),
	)
	assertChannelInstallationConfiguration(
		t,
		appRotatedInstall,
		appID,
		installID,
		2,
		1,
		"guild-configuration",
		"bot-configuration",
		"install-bot-token",
	)

	if _, _, err := store.Secrets().CreateSecretVersion(
		ctx,
		secretstore.CreateSecretVersionInput{
			OrgID: project.OrgUUID, SecretID: installSecret.ID,
			Material: secrets.IntegrationCredentialsMaterial{Values: map[string]string{
				"bot_token": "rotated-install-bot-token",
			}},
			Actor: httpUserPrincipal(project.AdminUserUUID),
		},
	); err != nil {
		t.Fatalf("rotate connector installation credential: %v", err)
	}
	installRotatedConfiguration := requestJSONWithHeaders(
		t, handler, http.MethodGet, installPath, "", "", http.StatusOK, authHeaders(token),
	)
	assertChannelInstallationConfiguration(
		t,
		installRotatedConfiguration,
		appID,
		installID,
		2,
		2,
		"guild-configuration",
		"bot-configuration",
		"rotated-install-bot-token",
	)

	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_installs SET state = 'disabled' WHERE id = $1`,
		install.ID,
	); err != nil {
		t.Fatalf("disable connector installation: %v", err)
	}
	requestJSONWithHeaders(
		t, handler, http.MethodGet, installPath, "", "", http.StatusNotFound, authHeaders(token),
	)
	requestJSONWithHeaders(
		t, handler, http.MethodGet, resolvePath, "", "", http.StatusNotFound, authHeaders(token),
	)
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_installs SET state = 'active' WHERE id = $1`,
		install.ID,
	); err != nil {
		t.Fatalf("re-enable connector installation: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_apps SET state = 'disabled' WHERE id = $1`,
		app.ID,
	); err != nil {
		t.Fatalf("disable connector app: %v", err)
	}
	requestJSONWithHeaders(
		t, handler, http.MethodGet, appPath, "", "", http.StatusNotFound, authHeaders(token),
	)
	requestJSONWithHeaders(
		t, handler, http.MethodGet, installPath, "", "", http.StatusNotFound, authHeaders(token),
	)
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_apps SET state = 'active' WHERE id = $1`,
		app.ID,
	); err != nil {
		t.Fatalf("re-enable connector app: %v", err)
	}
	if err := store.Integrations().DeleteIntegrationInstall(
		ctx,
		project.ProjectUUID,
		install.ID,
	); err != nil {
		t.Fatalf("delete connector installation: %v", err)
	}
	requestJSONWithHeaders(
		t, handler, http.MethodGet, installPath, "", "", http.StatusNotFound, authHeaders(token),
	)
	requestJSONWithHeaders(
		t, handler, http.MethodGet, resolvePath, "", "", http.StatusNotFound, authHeaders(token),
	)
}

func requiredChannelObject(
	t *testing.T,
	container map[string]any,
	key string,
) map[string]any {
	t.Helper()
	value, ok := container[key].(map[string]any)
	if !ok {
		t.Fatalf("channel response field %q = %#v, want object", key, container[key])
	}
	return value
}

func assertChannelConfigurationHasNoTenantAuthority(t *testing.T, value map[string]any) {
	t.Helper()
	for _, forbidden := range []string{
		"org_id",
		"owner_project_id",
		"project_id",
		"credential_secret_id",
	} {
		if _, ok := value[forbidden]; ok {
			t.Fatalf("connector configuration exposed internal authority field %q: %+v", forbidden, value)
		}
	}
}

func assertChannelCredential(
	t *testing.T,
	configuration map[string]any,
	want map[string]string,
) {
	t.Helper()
	credential := requiredChannelObject(t, configuration, "credential")
	if credential["kind"] != string(secrets.KindIntegrationCredentials) {
		t.Fatalf("channel credential kind = %#v", credential["kind"])
	}
	payload := requiredChannelObject(t, credential, "payload")
	if len(payload) != len(want) {
		t.Fatalf("channel credential payload = %+v, want only %+v", payload, want)
	}
	for key, expected := range want {
		if payload[key] != expected {
			t.Fatalf("channel credential %q = %#v, want %q", key, payload[key], expected)
		}
	}
}

func assertChannelInstallationConfiguration(
	t *testing.T,
	configuration map[string]any,
	appID, installID string,
	appRevision, installRevision int,
	providerTenantID, providerAccountRef string,
	credential string,
) {
	t.Helper()
	if configuration["integration_app_id"] != appID ||
		configuration["app_configuration_revision"] != float64(appRevision) {
		t.Fatalf("connector installation app fence = %+v", configuration)
	}
	install := requiredChannelObject(t, configuration, "install")
	if install["id"] != installID ||
		install["configuration_revision"] != float64(installRevision) ||
		install["provider_tenant_id"] != providerTenantID ||
		install["provider_account_ref"] != providerAccountRef {
		t.Fatalf("connector installation configuration = %+v", install)
	}
	assertChannelConfigurationHasNoTenantAuthority(t, install)
	assertChannelCredential(t, configuration, map[string]string{"bot_token": credential})
}

func TestChannelConnectorWebhookAndRuntimeIngressAreSeparated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	token, err := bearertoken.Generate(bearertoken.KindChannelConnector)
	if err != nil {
		t.Fatalf("generate connector token: %v", err)
	}
	authenticator, err := channelconnector.NewAuthenticator([]channelconnector.Config{
		{
			ID: "runtime-gateway", Token: token,
			Capabilities: connectorTestCapabilities("discord"),
		},
	})
	if err != nil {
		t.Fatalf("create connector authenticator: %v", err)
	}
	const handlerKey = "runtime_ingress_test"
	var routeAgentID integrationstore.ID
	deliveryPublisher := &recordingIntegrationDeliveryPublisher{}
	handler := newIntegrationServer(
		pool,
		WithChannelConnectorAuthenticator(authenticator),
		WithIntegrationDeliveryPublisher(deliveryPublisher),
		WithChannelRouteHandlers(integration.ChannelRouteHandlers{
			integration.ChannelRouteHandlerKey(handlerKey, 1): integration.ChannelRouteHandlerFunc(
				func(
					_ context.Context,
					_ integration.ChannelRouteContext,
					envelope integration.ChannelInboundEnvelope,
				) (integration.ChannelRouteDecision, error) {
					return integration.ChannelRouteDecision{
						Accept: true, ProviderRef: envelope.Conversation.Ref,
						ProviderRefKind: "thread", DisplayName: envelope.Conversation.DisplayName,
						DeliveryMode: executionstore.DeliveryModeQueued,
						Attachments: []integration.ChannelAttachmentAction{{
							AgentID: routeAgentID, SendAllowed: true,
						}},
					}, nil
				},
			),
		}),
	)
	project := bootstrapPublicHTTPProject(t, handler, "connector-runtime-ingress")
	store := project.Store
	launched := launchPublicHTTPAgent(
		t,
		handler,
		project,
		"connector-runtime-ingress-agent",
		project.AdminToken,
		http.StatusCreated,
	)
	agentID := mustPublicHTTPID(
		t,
		publicid.KindAgent,
		requiredChannelObject(t, launched, "agent")["id"].(string),
	)
	routeAgentID = agentID
	app, err := store.Integrations().CreateIntegrationApp(
		ctx,
		integrationstore.CreateIntegrationAppInput{
			OrgID: project.OrgUUID, OwnerProjectID: project.ProjectUUID,
			Provider: "discord", ProviderAppRef: "runtime-ingress-app",
			DisplayName: "Runtime ingress app", ConnectorKey: "chat_sdk_v1",
			State: integrationstore.IntegrationAppStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create runtime ingress app: %v", err)
	}
	install, err := store.Integrations().UpsertIntegrationInstall(
		ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, IntegrationAppID: app.ID,
			InstalledByUserID: project.AdminUserUUID,
			Provider:          "discord", IntegrationKind: "runtime_ingress", ConnectionMode: "gateway",
			State:            integrationstore.IntegrationInstallStateActive,
			ProviderTenantID: "runtime-ingress-guild", ProviderAccountRef: "runtime-ingress-bot",
			ProviderAgentDisplayName: "Runtime ingress bot",
		},
	)
	if err != nil {
		t.Fatalf("create runtime ingress installation: %v", err)
	}
	if _, err := store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID:            project.ProjectUUID,
			IntegrationInstallID: install.ID, DeploymentKey: "runtime-ingress",
			HandlerKey: handlerKey, HandlerVersion: 1, State: integrationstore.IntegrationRouteStateActive,
		},
	); err != nil {
		t.Fatalf("create runtime ingress route: %v", err)
	}
	unit, err := store.Integrations().UpsertIntegrationRuntimeUnit(
		ctx,
		integrationstore.UpsertIntegrationRuntimeUnitInput{
			OrgID: project.OrgUUID, IntegrationAppID: app.ID,
			ProjectID: project.ProjectUUID, IntegrationInstallID: install.ID,
			UnitKey: "runtime-ingress", RuntimeKind: "provider_socket",
			DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
			SpecRevision: 1,
		},
	)
	if err != nil {
		t.Fatalf("create runtime ingress unit: %v", err)
	}
	appID := testPublicID(t, publicid.KindIntegrationApp, app.ID)
	unitID := testPublicID(t, publicid.KindIntegrationRuntimeUnit, unit.ID)
	runtimeClaim := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/channel-connector/runtime-units/claim",
		`{"owner":"runtime-worker","lease_ms":60000,"limit":1,"capability":{"connector_key":"chat_sdk_v1","provider":"discord"}}`,
		"",
		http.StatusOK,
		authHeaders(token),
	)
	runtimeUnits, ok := runtimeClaim["runtime_units"].([]any)
	if !ok || len(runtimeUnits) != 1 {
		t.Fatalf("runtime claim response = %+v", runtimeClaim)
	}
	claimedUnit, ok := runtimeUnits[0].(map[string]any)
	if !ok || claimedUnit["id"] != unitID {
		t.Fatalf("claimed runtime unit = %+v, want %s", runtimeUnits[0], unitID)
	}
	leaseToken, ok := claimedUnit["lease_token"].(string)
	if !ok || leaseToken == "" {
		t.Fatalf("claimed runtime lease token = %#v", claimedUnit["lease_token"])
	}
	leaseGeneration, ok := claimedUnit["lease_generation"].(float64)
	if !ok || leaseGeneration <= 0 {
		t.Fatalf("claimed runtime lease generation = %#v", claimedUnit["lease_generation"])
	}
	heartbeatPath := "/api/v1/channel-connector/runtime-units/" + unitID + "/heartbeat"
	heartbeatBody := map[string]any{
		"lease_token": leaseToken, "lease_generation": leaseGeneration, "lease_ms": 60000,
		"checkpoint_version": 1, "checkpoint": map[string]any{"cursor": "before-event"},
	}
	heartbeat := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		heartbeatPath,
		mustMarshalChannelRequest(t, heartbeatBody),
		"",
		http.StatusOK,
		authHeaders(token),
	)
	if heartbeat["checkpoint_revision"] != float64(1) {
		t.Fatalf("runtime heartbeat response = %+v", heartbeat)
	}
	requestRawWithHeaders(
		t,
		handler,
		http.MethodPost,
		heartbeatPath,
		mustMarshalChannelRequest(t, map[string]any{
			"lease_token": leaseToken, "lease_generation": leaseGeneration, "lease_ms": 60000,
			"checkpoint_version": 1,
			"checkpoint": map[string]any{
				"cursor": "before-event", "postgres_exact_number": json.Number("1e1000"),
			},
		}),
		http.StatusOK,
		authHeaders(token),
	)
	webhookPath := "/api/v1/channel-connector/apps/" + appID + "/events"
	runtimePath := "/api/v1/channel-connector/apps/" + appID +
		"/runtime-units/" + unitID + "/events"
	event := func(providerEventID string) map[string]any {
		return map[string]any{
			"version": "v1", "provider_event_id": providerEventID,
			"external_tenant_id":   "runtime-ingress-guild",
			"external_account_ref": "runtime-ingress-bot", "event_type": "message.created",
			"conversation": map[string]any{
				"ref": "runtime-thread", "kind": "thread", "display_name": "Runtime thread",
				"mentioned": true, "direct": false, "metadata": map[string]any{},
			},
			"actor": map[string]any{
				"ref": "runtime-user", "display_name": "Runtime User", "metadata": map[string]any{},
			},
			"content_blocks": []any{map[string]any{"type": "text", "text": "hello"}},
			"occurred_at":    time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
			"metadata":       map[string]any{},
		}
	}

	forgedWebhook := event("forged-webhook-lease")
	forgedWebhook["runtime_lease"] = map[string]any{
		"lease_token": leaseToken, "lease_generation": leaseGeneration,
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		webhookPath,
		mustMarshalChannelRequest(t, forgedWebhook),
		"",
		http.StatusBadRequest,
		authHeaders(token),
	)

	staleRuntimeBody := map[string]any{
		"event": event("runtime-event"), "lease_token": leaseToken,
		"lease_generation": leaseGeneration + 1,
	}
	invalidRuntimeProofBody := map[string]any{
		"event":            event("runtime-event-invalid-proof"),
		"lease_token":      "00000000-0000-0000-0000-000000000000",
		"lease_generation": leaseGeneration,
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		runtimePath,
		mustMarshalChannelRequest(t, invalidRuntimeProofBody),
		"",
		http.StatusBadRequest,
		authHeaders(token),
	)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		runtimePath,
		mustMarshalChannelRequest(t, staleRuntimeBody),
		"",
		http.StatusConflict,
		authHeaders(token),
	)
	validRuntimeBody := map[string]any{
		"event": event("runtime-event"), "lease_token": leaseToken,
		"lease_generation": leaseGeneration,
	}
	accepted := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		runtimePath,
		mustMarshalChannelRequest(t, validRuntimeBody),
		"",
		http.StatusOK,
		authHeaders(token),
	)
	acceptances, ok := accepted["accepted"].([]any)
	if !ok || len(acceptances) != 1 || accepted["ignored_routes"] != float64(0) {
		t.Fatalf("runtime ingress response = %+v", accepted)
	}
	webhookAccepted := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		webhookPath,
		mustMarshalChannelRequest(t, event("webhook-event")),
		"",
		http.StatusOK,
		authHeaders(token),
	)
	webhookAcceptances, ok := webhookAccepted["accepted"].([]any)
	if !ok || len(webhookAcceptances) != 1 {
		t.Fatalf("webhook ingress response = %+v", webhookAccepted)
	}

	target, err := store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		project.ProjectUUID,
		install.ID,
		"runtime-thread",
	)
	if err != nil {
		t.Fatalf("load runtime ingress target: %v", err)
	}
	binding, err := store.Integrations().GetActiveSendBindingForTarget(
		ctx,
		project.ProjectUUID,
		agentID,
		target.ID,
	)
	if err != nil {
		t.Fatalf("load runtime ingress binding: %v", err)
	}
	deliveryPayload, err := json.Marshal(map[string]any{
		"context": map[string]any{
			"agent_id":         testPublicID(t, publicid.KindAgent, agentID),
			"provider_call_id": "lifecycle-call",
		},
		"destination": map[string]any{
			"channel_id":        testPublicID(t, publicid.KindIntegrationTarget, target.ID),
			"provider_metadata": map[string]any{},
			"provider_ref":      target.ProviderRef, "provider_ref_kind": target.ProviderRefKind,
		},
		"message": map[string]any{"text": "delivery lifecycle"},
	})
	if err != nil {
		t.Fatalf("encode lifecycle delivery: %v", err)
	}
	delivery, err := store.Integrations().CreateIntegrationDelivery(
		ctx,
		integrationstore.CreateIntegrationDeliveryInput{
			ProjectID: project.ProjectUUID, AgentID: agentID,
			IntegrationTargetBindingID: binding.ID,
			Transport:                  integrationstore.IntegrationDeliveryTransportConnector,
			DeliveryKind:               "message", PayloadVersion: "channel-message.v1",
			Payload: deliveryPayload, IdempotencyScope: "connector-lifecycle",
			IdempotencyKey: "delivery-1", NotifyRef: target.ID,
		},
	)
	if err != nil {
		t.Fatalf("create lifecycle delivery: %v", err)
	}
	deliveryClaim := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		"/api/v1/channel-connector/deliveries/claim",
		`{"owner":"delivery-worker","lease_ms":30000,"limit":1,"capability":{"connector_key":"chat_sdk_v1","provider":"discord"}}`,
		"",
		http.StatusOK,
		authHeaders(token),
	)
	deliveries, ok := deliveryClaim["deliveries"].([]any)
	if !ok || len(deliveries) != 1 {
		t.Fatalf("delivery claim response = %+v", deliveryClaim)
	}
	claimedDelivery, ok := deliveries[0].(map[string]any)
	deliveryID := testPublicID(t, publicid.KindIntegrationDelivery, delivery.ID)
	if !ok || claimedDelivery["id"] != deliveryID {
		t.Fatalf("claimed delivery = %+v, want %s", deliveries[0], deliveryID)
	}
	deliveryToken, ok := claimedDelivery["claim_token"].(string)
	if !ok || deliveryToken == "" {
		t.Fatalf("claimed delivery token = %#v", claimedDelivery["claim_token"])
	}
	deliveryGeneration, ok := claimedDelivery["claim_generation"].(float64)
	if !ok || deliveryGeneration <= 0 {
		t.Fatalf("claimed delivery generation = %#v", claimedDelivery["claim_generation"])
	}
	completePath := "/api/v1/channel-connector/deliveries/" + deliveryID + "/complete"
	invalidCompletion := map[string]any{
		"claim_token": deliveryToken, "claim_generation": deliveryGeneration,
		"outcome": "delivered", "provider_message_ref": "provider-message-1",
		"retry_after_ms": 100, "last_error": map[string]any{},
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		completePath,
		mustMarshalChannelRequest(t, invalidCompletion),
		"",
		http.StatusBadRequest,
		authHeaders(token),
	)
	delete(invalidCompletion, "retry_after_ms")
	invalidCompletion["provider_message_ref"] = strings.Repeat("界", 683)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		completePath,
		mustMarshalChannelRequest(t, invalidCompletion),
		"",
		http.StatusBadRequest,
		authHeaders(token),
	)
	invalidCompletion["provider_message_ref"] = "provider-message-1"
	invalidCompletion["last_error"] = map[string]any{"message": "bad\x00value"}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		completePath,
		mustMarshalChannelRequest(t, invalidCompletion),
		"",
		http.StatusBadRequest,
		authHeaders(token),
	)
	invalidCompletion["last_error"] = map[string]any{"value": json.Number("1e1000000")}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		completePath,
		mustMarshalChannelRequest(t, invalidCompletion),
		"",
		http.StatusBadRequest,
		authHeaders(token),
	)
	invalidCompletion["last_error"] = postgresTextExpansionObject(t)
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		completePath,
		mustMarshalChannelRequest(t, invalidCompletion),
		"",
		http.StatusBadRequest,
		authHeaders(token),
	)
	invalidCompletion["last_error"] = map[string]any{}
	completed := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		completePath,
		mustMarshalChannelRequest(t, invalidCompletion),
		"",
		http.StatusOK,
		authHeaders(token),
	)
	if completed["state"] != "delivered" ||
		completed["provider_message_ref"] != "provider-message-1" {
		t.Fatalf("completed delivery = %+v", completed)
	}
	if got := deliveryPublisher.notifyRefs(); len(got) != 1 || got[0] != target.ID {
		t.Fatalf("published delivery notify refs = %v, want [%s]", got, target.ID)
	}

	releasePath := "/api/v1/channel-connector/runtime-units/" + unitID + "/release"
	releaseRequest := map[string]any{
		"lease_token": leaseToken, "lease_generation": leaseGeneration,
		"checkpoint_version": 1, "checkpoint": map[string]any{"cursor": "after-event"},
		"last_error": map[string]any{},
	}
	releaseRequest["last_error"] = map[string]any{"message": "bad\x00value"}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		releasePath,
		mustMarshalChannelRequest(t, releaseRequest),
		"",
		http.StatusBadRequest,
		authHeaders(token),
	)
	releaseRequest["last_error"] = map[string]any{}
	releaseRequest["checkpoint"] = map[string]any{"cursor": "bad\x00value"}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		releasePath,
		mustMarshalChannelRequest(t, releaseRequest),
		"",
		http.StatusBadRequest,
		authHeaders(token),
	)
	releaseRequest["checkpoint"] = map[string]any{"cursor": "after-event"}
	released := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		releasePath,
		mustMarshalChannelRequest(t, releaseRequest),
		"",
		http.StatusOK,
		authHeaders(token),
	)
	if released["status"] != "idle" || released["checkpoint_revision"] != float64(3) {
		t.Fatalf("released runtime unit = %+v", released)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		heartbeatPath,
		mustMarshalChannelRequest(t, heartbeatBody),
		"",
		http.StatusConflict,
		authHeaders(token),
	)
}

type recordingIntegrationDeliveryPublisher struct {
	mu   sync.Mutex
	refs []integrationstore.ID
}

func (p *recordingIntegrationDeliveryPublisher) PublishIntegrationDeliveryUpdate(
	_ context.Context,
	notifyRef integrationstore.ID,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refs = append(p.refs, notifyRef)
	return nil
}

func (p *recordingIntegrationDeliveryPublisher) notifyRefs() []integrationstore.ID {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]integrationstore.ID(nil), p.refs...)
}

func mustMarshalChannelRequest(t *testing.T, body map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode channel request: %v", err)
	}
	return string(raw)
}

func postgresTextExpansionObject(t *testing.T) map[string]any {
	t.Helper()
	object := map[string]any{
		"a": json.Number("1e131071"),
		"b": json.Number("1e131071"),
	}
	compact, err := json.Marshal(object)
	if err != nil {
		t.Fatalf("marshal PostgreSQL text-expansion fixture: %v", err)
	}
	if len(compact) > channelconnector.MaxMetadataBytes {
		t.Fatalf("text-expansion fixture has %d compact bytes", len(compact))
	}
	if _, err := channelconnector.NormalizeOpaqueObject(compact); err == nil {
		t.Fatal("text-expansion fixture was not rejected before PostgreSQL")
	}
	return object
}

func TestChannelConnectorInteractionResolutionJourney(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	token, err := bearertoken.Generate(bearertoken.KindChannelConnector)
	if err != nil {
		t.Fatalf("generate connector token: %v", err)
	}
	authenticator, err := channelconnector.NewAuthenticator([]channelconnector.Config{
		{
			ID: "gateway-test", Token: token, Capabilities: connectorTestCapabilities("discord"),
		},
	})
	if err != nil {
		t.Fatalf("create connector authenticator: %v", err)
	}
	handler := newIntegrationServer(pool, WithChannelConnectorAuthenticator(authenticator))
	project := bootstrapPublicHTTPProject(t, handler, "connector-interaction")
	store := newIntegrationStore(pool)
	agentID, interactionID := createHTTPStructuredQuestionInteraction(
		t,
		ctx,
		pool,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
	)
	app, err := store.Integrations().CreateIntegrationApp(
		ctx,
		integrationstore.CreateIntegrationAppInput{
			OrgID: project.OrgUUID, OwnerProjectID: project.ProjectUUID,
			Provider: "discord", ProviderAppRef: "connector-interaction-app",
			DisplayName: "Connector interaction app", ConnectorKey: "chat_sdk_v1",
			State: integrationstore.IntegrationAppStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create connector app: %v", err)
	}
	install, err := store.Integrations().UpsertIntegrationInstall(
		ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, IntegrationAppID: app.ID,
			InstalledByUserID: project.AdminUserUUID,
			Provider:          "discord", IntegrationKind: "channel_single_agent", ConnectionMode: "gateway",
			State:                    integrationstore.IntegrationInstallStateActive,
			ProviderAccountRef:       "bot-actions",
			ProviderAgentDisplayName: "Omnara action bot",
		},
	)
	if err != nil {
		t.Fatalf("create connector install: %v", err)
	}
	route, err := store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID:            project.ProjectUUID,
			IntegrationInstallID: install.ID, DeploymentKey: "test-actions",
			HandlerKey: "test_actions", HandlerVersion: 1, State: integrationstore.IntegrationRouteStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create connector route: %v", err)
	}
	target, err := store.Integrations().GetOrCreateIntegrationTargetForBinding(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: project.ProjectUUID, AgentID: agentID,
			IntegrationInstallID: install.ID, ProviderRef: "thread-actions",
			ProviderRefKind: "thread", DisplayName: "Action thread",
		},
	)
	if err != nil {
		t.Fatalf("create connector target: %v", err)
	}
	binding, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: project.ProjectUUID, AgentID: agentID,
			IntegrationInstallID: install.ID, IntegrationTargetID: target.ID,
			IntegrationRouteID: route.ID, ReceiveAllowed: true, SendAllowed: false,
			Source: "test",
		},
	)
	if err != nil {
		t.Fatalf("create connector binding: %v", err)
	}
	alternateRoute, err := store.Integrations().CreateIntegrationRoute(
		ctx,
		integrationstore.CreateIntegrationRouteInput{
			ProjectID:            project.ProjectUUID,
			IntegrationInstallID: install.ID, DeploymentKey: "test-actions-alternate",
			HandlerKey: "test_actions_alternate", HandlerVersion: 1, State: integrationstore.IntegrationRouteStateActive,
		},
	)
	if err != nil {
		t.Fatalf("create alternate connector route: %v", err)
	}
	alternateBinding, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: project.ProjectUUID, AgentID: agentID,
			IntegrationInstallID: install.ID, IntegrationTargetID: target.ID,
			IntegrationRouteID: alternateRoute.ID, ReceiveAllowed: true, SendAllowed: false,
			Source: "test",
		},
	)
	if err != nil {
		t.Fatalf("create alternate connector binding: %v", err)
	}
	sendOnlyBinding, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: project.ProjectUUID, AgentID: agentID,
			IntegrationInstallID: install.ID, IntegrationTargetID: target.ID,
			ReceiveAllowed: false, SendAllowed: true, Source: "test-send-only",
		},
	)
	if err != nil {
		t.Fatalf("create send-only connector binding: %v", err)
	}
	otherTarget, err := store.Integrations().GetOrCreateIntegrationTargetForBinding(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: project.ProjectUUID, AgentID: agentID,
			IntegrationInstallID: install.ID, ProviderRef: "other-thread-actions",
			ProviderRefKind: "thread", DisplayName: "Other action thread",
		},
	)
	if err != nil {
		t.Fatalf("create alternate connector target: %v", err)
	}
	otherInstall, err := store.Integrations().UpsertIntegrationInstall(
		ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, IntegrationAppID: app.ID,
			InstalledByUserID: project.AdminUserUUID,
			Provider:          "discord", IntegrationKind: "channel_single_agent", ConnectionMode: "gateway",
			State:                    integrationstore.IntegrationInstallStateActive,
			ProviderAccountRef:       "other-bot-actions",
			ProviderAgentDisplayName: "Other action bot",
		},
	)
	if err != nil {
		t.Fatalf("create alternate connector install: %v", err)
	}

	appID := testPublicID(t, publicid.KindIntegrationApp, app.ID)
	interactionPublicID := testPublicID(t, publicid.KindAgentInteraction, interactionID)
	targetID := testPublicID(t, publicid.KindIntegrationTarget, target.ID)
	bindingID := testPublicID(t, publicid.KindIntegrationBinding, binding.ID)
	body := map[string]any{
		"version": "v1", "external_tenant_id": "",
		"external_account_ref": "bot-actions", "integration_target_id": targetID,
		"integration_target_binding_id": bindingID,
		"actor": map[string]any{
			"ref": "discord-user-1", "display_name": "Discord User",
			"metadata": map[string]any{"role": "member"},
		},
		"answers":  []any{map[string]any{"option_indices": []int{0}}},
		"metadata": map[string]any{"interaction_ref": "provider-action-1"},
	}
	rawBody, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode connector resolution: %v", err)
	}
	path := "/api/v1/channel-connector/apps/" + appID + "/interactions/" +
		interactionPublicID + "/resolve"
	var actorsBeforeInvalid int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM actors WHERE project_id = $1`,
		project.ProjectUUID,
	).Scan(&actorsBeforeInvalid); err != nil {
		t.Fatalf("count actors before invalid connector resolutions: %v", err)
	}
	largeMetadataValue := strings.Repeat("a", 140*1024)
	nulBody := cloneChannelInteractionRequestBody(body)
	nulBody["actor"].(map[string]any)["metadata"] = map[string]any{"value": "bad\x00value"}
	oversizedBody := cloneChannelInteractionRequestBody(body)
	oversizedBody["actor"].(map[string]any)["metadata"] = map[string]any{
		"value": largeMetadataValue,
	}
	oversizedBody["metadata"] = map[string]any{"value": largeMetadataValue}
	for _, invalidBody := range []map[string]any{nulBody, oversizedBody} {
		requestJSONWithHeaders(
			t,
			handler,
			http.MethodPost,
			path,
			mustMarshalChannelRequest(t, invalidBody),
			"",
			http.StatusBadRequest,
			authHeaders(token),
		)
	}
	wrongInstallBody := cloneChannelInteractionRequestBody(body)
	wrongInstallBody["external_account_ref"] = otherInstall.ProviderAccountRef
	sendOnlyBody := cloneChannelInteractionRequestBody(body)
	sendOnlyBody["integration_target_binding_id"] = testPublicID(
		t,
		publicid.KindIntegrationBinding,
		sendOnlyBinding.ID,
	)
	wrongTargetBody := cloneChannelInteractionRequestBody(body)
	wrongTargetBody["integration_target_id"] = testPublicID(
		t,
		publicid.KindIntegrationTarget,
		otherTarget.ID,
	)
	for _, forbiddenBody := range []map[string]any{
		wrongInstallBody,
		sendOnlyBody,
		wrongTargetBody,
	} {
		requestJSONWithHeaders(
			t,
			handler,
			http.MethodPost,
			path,
			mustMarshalChannelRequest(t, forbiddenBody),
			"",
			http.StatusForbidden,
			authHeaders(token),
		)
	}
	assertChannelInteractionUnchanged(
		t,
		ctx,
		pool,
		project.ProjectUUID,
		agentID,
		interactionID,
		actorsBeforeInvalid,
	)
	resolved := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		string(rawBody),
		"",
		http.StatusOK,
		authHeaders(token),
	)
	if resolved["status"] != "resolved" || resolved["text"] != "Answers recorded." {
		t.Fatalf("connector interaction response = %+v", resolved)
	}

	var storedTargetID, storedBindingID string
	var actorProvider, actorTenant, actorUserID string
	var metadata json.RawMessage
	if err := pool.QueryRow(
		ctx,
		`SELECT input.integration_target_id::text, input.integration_target_binding_id::text,
		        actor.provider, coalesce(actor.provider_tenant_id, ''),
		        actor.provider_user_id, input.metadata
		 FROM agent_inputs input
		 JOIN actors actor ON actor.project_id = input.project_id AND actor.id = input.actor_id
		 WHERE input.project_id = $1 AND input.agent_id = $2
		   AND input.input_kind = 'interaction_response' AND input.target_interaction_id = $3`,
		project.ProjectUUID,
		agentID,
		interactionID,
	).Scan(
		&storedTargetID,
		&storedBindingID,
		&actorProvider,
		&actorTenant,
		&actorUserID,
		&metadata,
	); err != nil {
		t.Fatalf("load connector interaction response input: %v", err)
	}
	if storedTargetID != target.ID.String() || storedBindingID != binding.ID.String() ||
		actorProvider != "discord" || actorTenant != "" ||
		actorUserID != "discord-user-1" {
		t.Fatalf(
			"connector response provenance target=%s binding=%s actor=%s/%s/%s",
			storedTargetID,
			storedBindingID,
			actorProvider,
			actorTenant,
			actorUserID,
		)
	}
	var storedMetadata map[string]any
	if err := json.Unmarshal(metadata, &storedMetadata); err != nil {
		t.Fatalf("decode connector response metadata: %v", err)
	}
	channelMetadata, ok := storedMetadata["channel"].(map[string]any)
	if !ok || channelMetadata["version"] != "v1" {
		t.Fatalf("connector response metadata = %+v", storedMetadata)
	}

	replayed := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		string(rawBody),
		"",
		http.StatusOK,
		authHeaders(token),
	)
	if replayed["status"] != "already_resolved" {
		t.Fatalf("connector interaction replay = %+v", replayed)
	}
	answerConflict := cloneChannelInteractionRequestBody(body)
	answerConflict["answers"] = []any{map[string]any{"option_indices": []int{1}}}
	actorConflict := cloneChannelInteractionRequestBody(body)
	actorConflict["actor"].(map[string]any)["ref"] = "discord-user-2"
	metadataConflict := cloneChannelInteractionRequestBody(body)
	metadataConflict["metadata"] = map[string]any{"interaction_ref": "provider-action-2"}
	for _, conflictingBody := range []map[string]any{
		answerConflict,
		actorConflict,
		metadataConflict,
	} {
		requestJSONWithHeaders(
			t,
			handler,
			http.MethodPost,
			path,
			mustMarshalChannelRequest(t, conflictingBody),
			"",
			http.StatusConflict,
			authHeaders(token),
		)
	}

	concurrentAgentID, concurrentInteractionID := createHTTPInteractionAuthority(
		t,
		ctx,
		pool,
		store,
		project.OrgUUID,
		project.ProjectUUID,
		time.Date(2026, 8, 30, 12, 1, 0, 0, time.UTC),
		"permission",
		"run_command",
		json.RawMessage(`{"command":"printf concurrent"}`),
	)
	concurrentBinding, err := store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: project.ProjectUUID, AgentID: concurrentAgentID,
			IntegrationInstallID: install.ID, IntegrationTargetID: target.ID,
			IntegrationRouteID: route.ID, ReceiveAllowed: true, SendAllowed: false,
			Source: "test",
		},
	)
	if err != nil {
		t.Fatalf("create concurrent connector binding: %v", err)
	}
	concurrentBody := cloneChannelInteractionRequestBody(body)
	concurrentBody["integration_target_binding_id"] = testPublicID(
		t,
		publicid.KindIntegrationBinding,
		concurrentBinding.ID,
	)
	concurrentPath := "/api/v1/channel-connector/apps/" + appID + "/interactions/" +
		testPublicID(t, publicid.KindAgentInteraction, concurrentInteractionID) + "/resolve"
	concurrentResults := resolveChannelInteractionConcurrently(
		handler,
		concurrentPath,
		mustMarshalChannelRequest(t, concurrentBody),
		token,
		2,
	)
	statusCounts := map[string]int{}
	for _, result := range concurrentResults {
		if result.err != nil || result.code != http.StatusOK {
			t.Fatalf("concurrent connector resolution = %+v", result)
		}
		statusCounts[result.status]++
	}
	if statusCounts["resolved"] != 1 || statusCounts["already_resolved"] != 1 {
		t.Fatalf("concurrent connector resolution statuses = %+v", statusCounts)
	}

	forged := mapsClone(body)
	forged["integration_target_binding_id"] = testPublicID(
		t,
		publicid.KindIntegrationBinding,
		project.ProjectUUID,
	)
	forgedRaw, err := json.Marshal(forged)
	if err != nil {
		t.Fatalf("encode forged connector resolution: %v", err)
	}
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		path,
		string(forgedRaw),
		"",
		http.StatusNotFound,
		authHeaders(token),
	)

	runtimeInteractionID := copyHTTPInteractionForTest(
		t,
		ctx,
		pool,
		project.ProjectUUID,
		agentID,
		interactionID,
		"permission",
		httpPermissionRequest(
			t,
			"ask_question",
			json.RawMessage(`{"questions":[{"prompt":"Ship?","options":[{"label":"Yes"},{"label":"No"}]}]}`),
		),
	)
	unit, err := store.Integrations().UpsertIntegrationRuntimeUnit(
		ctx,
		integrationstore.UpsertIntegrationRuntimeUnitInput{
			OrgID: project.OrgUUID, IntegrationAppID: app.ID,
			UnitKey: "interaction-runtime", RuntimeKind: "provider_gateway",
			DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning,
			SpecRevision: 1,
		},
	)
	if err != nil {
		t.Fatalf("create interaction runtime unit: %v", err)
	}
	leases, err := store.Integrations().ClaimIntegrationRuntimeUnits(
		ctx,
		integrationstore.ClaimIntegrationRuntimeUnitsInput{
			LeaseOwner: "gateway-test", LeaseDuration: time.Minute,
			Capability: connectorTestCapability("discord"), Limit: 1,
		},
	)
	if err != nil || len(leases) != 1 || leases[0].ID != unit.ID {
		t.Fatalf("claim interaction runtime unit = %+v, %v", leases, err)
	}
	lease := leases[0]
	runtimePath := "/api/v1/channel-connector/apps/" + appID + "/runtime-units/" +
		testPublicID(t, publicid.KindIntegrationRuntimeUnit, unit.ID) + "/interactions/" +
		testPublicID(t, publicid.KindAgentInteraction, runtimeInteractionID) + "/resolve"
	runtimeInteraction := mapsClone(body)
	runtimeInteraction["integration_target_binding_id"] = testPublicID(
		t,
		publicid.KindIntegrationBinding,
		alternateBinding.ID,
	)
	runtimeBody := map[string]any{
		"lease_token": lease.LeaseToken.String(), "lease_generation": lease.LeaseGeneration,
		"interaction": runtimeInteraction,
	}
	invalidRuntimeProofBody := mapsClone(runtimeBody)
	invalidRuntimeProofBody["lease_token"] = "00000000-0000-0000-0000-000000000000"
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		runtimePath,
		mustMarshalChannelRequest(t, invalidRuntimeProofBody),
		"",
		http.StatusBadRequest,
		authHeaders(token),
	)
	var actorsBeforeInvalidRuntime int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM actors WHERE project_id = $1`,
		project.ProjectUUID,
	).Scan(&actorsBeforeInvalidRuntime); err != nil {
		t.Fatalf("count actors before invalid runtime resolutions: %v", err)
	}
	runtimeNULInteraction := cloneChannelInteractionRequestBody(runtimeInteraction)
	runtimeNULInteraction["actor"].(map[string]any)["ref"] = "runtime-user\x00invalid"
	runtimeOversizedInteraction := cloneChannelInteractionRequestBody(runtimeInteraction)
	runtimeOversizedInteraction["actor"].(map[string]any)["metadata"] = map[string]any{
		"value": largeMetadataValue,
	}
	runtimeOversizedInteraction["metadata"] = map[string]any{"value": largeMetadataValue}
	for _, invalidInteraction := range []map[string]any{
		runtimeNULInteraction,
		runtimeOversizedInteraction,
	} {
		invalidRuntimeBody := mapsClone(runtimeBody)
		invalidRuntimeBody["interaction"] = invalidInteraction
		requestJSONWithHeaders(
			t,
			handler,
			http.MethodPost,
			runtimePath,
			mustMarshalChannelRequest(t, invalidRuntimeBody),
			"",
			http.StatusBadRequest,
			authHeaders(token),
		)
	}
	assertChannelInteractionUnchanged(
		t,
		ctx,
		pool,
		project.ProjectUUID,
		agentID,
		runtimeInteractionID,
		actorsBeforeInvalidRuntime,
	)
	staleRuntimeBody := mapsClone(runtimeBody)
	staleRuntimeBody["lease_generation"] = lease.LeaseGeneration + 1
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		runtimePath,
		mustMarshalChannelRequest(t, staleRuntimeBody),
		"",
		http.StatusConflict,
		authHeaders(token),
	)
	var runtimeResponses int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM agent_inputs
		 WHERE project_id = $1 AND agent_id = $2
		   AND input_kind = 'interaction_response' AND target_interaction_id = $3`,
		project.ProjectUUID,
		agentID,
		runtimeInteractionID,
	).Scan(&runtimeResponses); err != nil {
		t.Fatalf("count stale runtime interaction responses: %v", err)
	}
	if runtimeResponses != 0 {
		t.Fatalf("stale runtime created %d interaction responses", runtimeResponses)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE integration_target_bindings
		 SET revoked_at = statement_timestamp(), updated_at = statement_timestamp()
		 WHERE project_id = $1 AND id = $2`,
		project.ProjectUUID,
		binding.ID,
	); err != nil {
		t.Fatalf("revoke connector binding: %v", err)
	}
	displayName := "Discord User"
	_, err = store.Execution().ResolveAgentInteraction(
		ctx,
		executionstore.ResolveAgentInteractionInput{
			ProjectID: project.ProjectUUID, AgentID: agentID, ID: runtimeInteractionID,
			Resolution: httpQuestionResolution(0),
			Actor: &executionstore.ActorParams{
				Provider: "discord", ProviderUserID: "discord-user-1", DisplayName: &displayName,
			},
			IntegrationInstallID: install.ID, IntegrationTargetID: target.ID,
			IntegrationTargetBindingID: binding.ID,
		},
	)
	if !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("resolve through revoked connector binding error = %v, want not found", err)
	}
	var revokedState string
	if err := pool.QueryRow(
		ctx,
		`SELECT state FROM agent_interaction_read_projection
		 WHERE project_id = $1 AND agent_id = $2 AND id = $3`,
		project.ProjectUUID,
		agentID,
		runtimeInteractionID,
	).Scan(&revokedState); err != nil {
		t.Fatalf("load interaction rejected after binding revocation: %v", err)
	}
	if revokedState != "open" {
		t.Fatalf("revoked binding interaction state=%q, want open", revokedState)
	}
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM agent_inputs
		 WHERE project_id = $1 AND agent_id = $2
		   AND input_kind = 'interaction_response' AND target_interaction_id = $3`,
		project.ProjectUUID,
		agentID,
		runtimeInteractionID,
	).Scan(&runtimeResponses); err != nil {
		t.Fatalf("count revoked-binding interaction responses: %v", err)
	}
	if runtimeResponses != 0 {
		t.Fatalf("revoked binding created %d interaction responses", runtimeResponses)
	}
	runtimeResolved := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		runtimePath,
		mustMarshalChannelRequest(t, runtimeBody),
		"",
		http.StatusOK,
		authHeaders(token),
	)
	if runtimeResolved["status"] != "resolved" {
		t.Fatalf("runtime interaction response = %+v", runtimeResolved)
	}
}

func mapsClone(input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func cloneChannelInteractionRequestBody(input map[string]any) map[string]any {
	out := mapsClone(input)
	if actor, ok := input["actor"].(map[string]any); ok {
		out["actor"] = mapsClone(actor)
	}
	return out
}

func assertChannelInteractionUnchanged(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	projectID, agentID, interactionID integrationstore.ID,
	wantActors int,
) {
	t.Helper()
	var state string
	var responseInputs, actors int
	if err := pool.QueryRow(ctx, `
SELECT interaction.state,
       (SELECT count(*) FROM agent_inputs input
        WHERE input.project_id = $1 AND input.agent_id = $2
          AND input.input_kind = 'interaction_response'
          AND input.target_interaction_id = $3),
       (SELECT count(*) FROM actors actor WHERE actor.project_id = $1)
FROM agent_interaction_read_projection interaction
WHERE interaction.project_id = $1 AND interaction.agent_id = $2 AND interaction.id = $3
`, projectID, agentID, interactionID).Scan(&state, &responseInputs, &actors); err != nil {
		t.Fatalf("load interaction after rejected channel response: %v", err)
	}
	if state != "open" || responseInputs != 0 || actors != wantActors {
		t.Fatalf(
			"rejected channel response left state=%q inputs=%d actors=%d, want open/0/%d",
			state,
			responseInputs,
			actors,
			wantActors,
		)
	}
}

type channelInteractionHTTPResult struct {
	code   int
	status string
	err    error
}

func resolveChannelInteractionConcurrently(
	handler http.Handler,
	path, body, token string,
	count int,
) []channelInteractionHTTPResult {
	start := make(chan struct{})
	results := make(chan channelInteractionHTTPResult, count)
	var wait sync.WaitGroup
	for range count {
		wait.Add(1)
		go func() {
			defer wait.Done()
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			<-start
			handler.ServeHTTP(response, request)
			var decoded struct {
				Status string `json:"status"`
			}
			err := json.Unmarshal(response.Body.Bytes(), &decoded)
			results <- channelInteractionHTTPResult{
				code: response.Code, status: decoded.Status, err: err,
			}
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	out := make([]channelInteractionHTTPResult, 0, count)
	for result := range results {
		out = append(out, result)
	}
	return out
}
