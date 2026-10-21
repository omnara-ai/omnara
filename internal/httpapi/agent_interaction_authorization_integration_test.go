//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/mcpauthorization"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const httpMCPAuthorizationSecretName = "calendar-oauth"

func TestAgentInteractionAuthorizationFirstConnectGrantsAndAttachesSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newMCPOAuthIntegrationServer(pool, "http://omnara.test")
	project := bootstrapPublicHTTPProject(t, handler, "mcp-authz-first-connect")
	store := integrationStoreForHandler(t, handler)
	fake := newMCPOAuthFakeServer(t, mcpOAuthFakeConfig{RequireAuth: true, PKCE: true, DCR: true})
	mcpURL := fake.URL + "/mcp"

	launch := createHTTPMCPAuthorizationAgent(t, ctx, store, project, "mcp-authz-first-connect", mcpURL)
	agent := launch.Agent
	toolCallID, interactionID := createHTTPOpenAuthorizationInteraction(
		t, ctx, store, project, agent, mcpURL, []string{"files:read"},
	)
	agentPath := project.ProjectPath + "/agents/" + testPublicID(t, publicid.KindAgent, agent.ID)
	interactionPublicID := testPublicID(t, publicid.KindAgentInteraction, interactionID)

	listed := requestJSONWithHeaders(t, handler, http.MethodGet, agentPath+"/interactions?state=open",
		"", "", http.StatusOK, authHeaders(project.AdminToken))
	data := testutil.RequireType[[]any](t, listed["data"])
	if len(data) != 1 {
		t.Fatalf("open interactions = %+v, want one", listed)
	}
	item := testutil.RequireType[map[string]any](t, data[0])
	wantToolName := toolcatalog.MCPRuntimeToolName("calendar", "create_event")
	if item["id"] != interactionPublicID || item["interaction_kind"] != "authorization" ||
		item["state"] != "open" || item["tool_name"] != wantToolName {
		t.Fatalf("listed interaction = %+v, want open authorization for %s", item, wantToolName)
	}
	authorization := testutil.RequireType[map[string]any](t, item["authorization"])
	if authorization["mcp_server_key"] != "calendar" || authorization["mcp_tool_name"] != "create_event" ||
		authorization["mcp_url"] != mcpURL || authorization["secret_name"] != httpMCPAuthorizationSecretName {
		t.Fatalf("authorization block = %+v", authorization)
	}
	if _, ok := authorization["secret_id"]; ok {
		t.Fatalf("first-connect authorization has secret_id: %+v", authorization)
	}
	if got := jsonStringSlice(t, authorization["required_scopes"]); !slices.Equal(got, []string{"files:read"}) {
		t.Fatalf("required_scopes = %v, want [files:read]", got)
	}
	if got := jsonStringSlice(t, authorization["granted_scopes"]); len(got) != 0 {
		t.Fatalf("granted_scopes = %v, want empty", got)
	}

	start := requestJSONWithHeaders(t, handler, http.MethodPost,
		agentPath+"/interactions/"+interactionPublicID+"/authorize",
		`{"return_to":"/agents"}`, "", http.StatusCreated, authHeaders(project.AdminToken))
	if testutil.RequireType[string](t, start["flow_id"]) == "" || start["expires_at"] == nil {
		t.Fatalf("authorize response = %+v, want flow id and expiry", start)
	}
	authURL, err := url.Parse(testutil.RequireType[string](t, start["authorization_url"]))
	if err != nil {
		t.Fatalf("parse authorization_url: %v", err)
	}
	if scopes := strings.Fields(authURL.Query().Get("scope")); !slices.Contains(scopes, "files:read") {
		t.Fatalf("authorization scope = %q, want files:read", authURL.Query().Get("scope"))
	}
	if got := authURL.Query().Get("resource"); got != mcpURL {
		t.Fatalf("authorization resource = %q, want %q", got, mcpURL)
	}
	fake.setCodeChallenge(authURL.Query().Get("code_challenge"))

	location := completeMCPOAuthCallback(t, handler, authURL.Query().Get("state"), http.StatusFound)
	redirect, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse callback redirect: %v", err)
	}
	query := redirect.Query()
	if redirect.Path != "/agents" || query.Get("mcp_oauth") != "success" ||
		query.Get("interaction_id") != interactionPublicID || query.Get("mcp_oauth_error") != "" {
		t.Fatalf("callback redirect = %q, want success for %s", location, interactionPublicID)
	}
	secretPublicID := query.Get("secret_id")
	secretID, ok := parseOpenAPIPublicID(publicid.KindSecret, secretPublicID)
	if !ok {
		t.Fatalf("secret_id = %q, want public secret id", secretPublicID)
	}
	secret, err := store.Secrets().GetSecret(ctx, project.OrgUUID, secretID)
	if err != nil {
		t.Fatalf("get created secret: %v", err)
	}
	if secret.Name != httpMCPAuthorizationSecretName || secret.Kind != secretstore.SecretKindOAuthTokenSet ||
		secret.OwnerKind != secretstore.SecretOwnerProject || secret.OwnerProjectID != project.ProjectUUID {
		t.Fatalf("created secret = %+v, want project oauth secret %q", secret, httpMCPAuthorizationSecretName)
	}

	interaction, found, err := store.Execution().GetAgentInteraction(ctx, project.ProjectUUID, agent.ID, interactionID)
	if err != nil || !found {
		t.Fatalf("get interaction found=%v err=%v", found, err)
	}
	if interaction.State != executionstore.AgentInteractionStateResolved {
		t.Fatalf("interaction state = %s, want resolved", interaction.State)
	}
	resolvedItem := requestJSONWithHeaders(t, handler, http.MethodGet,
		agentPath+"/interactions?state=resolved", "", "", http.StatusOK, authHeaders(project.AdminToken))
	resolvedData := testutil.RequireType[[]any](t, resolvedItem["data"])
	if len(resolvedData) != 1 ||
		testutil.RequireType[map[string]any](t, resolvedData[0])["id"] != interactionPublicID {
		t.Fatalf("resolved interactions = %+v, want %s", resolvedItem, interactionPublicID)
	}
	toolCall, err := store.Execution().GetToolCall(ctx, project.ProjectUUID, agent.ID, toolCallID)
	if err != nil {
		t.Fatalf("get tool call: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateReady {
		t.Fatalf("tool call state = %s, want ready after grant", toolCall.State)
	}

	current, err := store.Execution().GetAgentInProject(ctx, project.ProjectUUID, agent.ID)
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if current.CurrentConfigID == agent.CurrentConfigID {
		t.Fatal("agent config was not changed after first-connect grant")
	}
	config, found, err := store.Execution().GetAgentConfig(ctx, project.ProjectUUID, current.CurrentConfigID)
	if err != nil || !found {
		t.Fatalf("get agent config found=%v err=%v", found, err)
	}
	source, err := agentconfig.ParseSource(agentconfig.SourceFormat(config.SourceFormat), []byte(config.Source))
	if err != nil {
		t.Fatalf("parse updated agent config source: %v\n%s", err, config.Source)
	}
	server, ok := source.MCP["calendar"]
	if !ok || server.URL != mcpURL || server.Auth == nil ||
		server.Auth.Type != agentconfig.MCPAuthTypeOAuth || server.Auth.SecretID != secretPublicID {
		t.Fatalf("updated mcp.calendar = %+v (auth %+v), want oauth auth with %s", server, server.Auth, secretPublicID)
	}
	if !strings.Contains(string(config.CompiledDefinition), secretID.String()) &&
		!strings.Contains(string(config.CompiledDefinition), secretPublicID) {
		t.Fatalf("compiled definition does not reference secret %s: %s", secretPublicID, config.CompiledDefinition)
	}
}

// Each agent's first connect creates its own secret, and a first connect never
// writes into a secret it did not create, even one that appears mid-flow.
func TestAgentInteractionAuthorizationFirstConnectNeverOverwritesAnotherSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newMCPOAuthIntegrationServer(pool, "http://omnara.test")
	project := bootstrapPublicHTTPProject(t, handler, "mcp-authz-isolation")
	store := integrationStoreForHandler(t, handler)
	fake := newMCPOAuthFakeServer(t, mcpOAuthFakeConfig{PKCE: true, DCR: true})
	mcpURL := fake.URL + "/mcp"

	type started struct {
		agentPath, interactionPublicID, state, secretName string
	}
	start := func(name string) started {
		t.Helper()
		agent := createHTTPMCPAuthorizationAgent(t, ctx, store, project, name, mcpURL).Agent
		secretName := "calendar-oauth-" + testPublicID(t, publicid.KindAgent, agent.ID)
		_, interactionID := createHTTPOpenAuthorizationInteractionWithChallenge(
			t, ctx, store, project, agent, mcpURL, []string{"files:read"},
			httpMCPResourceMetadataURL(mcpURL), secretName,
		)
		agentPath := project.ProjectPath + "/agents/" + testPublicID(t, publicid.KindAgent, agent.ID)
		interactionPublicID := testPublicID(t, publicid.KindAgentInteraction, interactionID)
		response := requestJSONWithHeaders(t, handler, http.MethodPost,
			agentPath+"/interactions/"+interactionPublicID+"/authorize",
			`{"return_to":"/agents"}`, "", http.StatusCreated, authHeaders(project.AdminToken))
		authURL, err := url.Parse(testutil.RequireType[string](t, response["authorization_url"]))
		if err != nil {
			t.Fatalf("parse authorization_url: %v", err)
		}
		fake.setCodeChallenge(authURL.Query().Get("code_challenge"))
		return started{agentPath, interactionPublicID, authURL.Query().Get("state"), secretName}
	}
	complete := func(flow started) url.Values {
		t.Helper()
		location := completeMCPOAuthCallback(t, handler, flow.state, http.StatusFound)
		redirect, err := url.Parse(location)
		if err != nil {
			t.Fatalf("parse callback redirect: %v", err)
		}
		return redirect.Query()
	}

	first := complete(start("mcp-authz-isolation-a"))
	second := complete(start("mcp-authz-isolation-b"))
	if first.Get("mcp_oauth") != "success" || second.Get("mcp_oauth") != "success" {
		t.Fatalf("callbacks = %v / %v, want both to succeed", first, second)
	}
	if first.Get("secret_id") == second.Get("secret_id") {
		t.Fatalf("two agents share secret %s, want one secret each", first.Get("secret_id"))
	}
	firstSecretID, ok := parseOpenAPIPublicID(publicid.KindSecret, first.Get("secret_id"))
	if !ok {
		t.Fatalf("secret_id = %q", first.Get("secret_id"))
	}
	firstSecret, err := store.Secrets().GetSecret(ctx, project.OrgUUID, firstSecretID)
	if err != nil {
		t.Fatalf("get first agent's secret: %v", err)
	}
	if firstSecret.CurrentVersionNumber != 1 {
		t.Fatalf("first agent's secret version = %d, want 1 after another agent connects", firstSecret.CurrentVersionNumber)
	}

	// A secret with the flow's name appears between consent start and callback.
	racing := start("mcp-authz-isolation-c")
	occupant, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          project.OrgUUID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: project.ProjectUUID,
		Name:           racing.secretName,
		Material:       secrets.OAuthTokenSetMaterial{AccessToken: "someone-else", MCPURL: mcpURL},
		Actor:          identitystore.NewUserPrincipal(project.AdminUserUUID),
	})
	if err != nil {
		t.Fatalf("create occupying secret: %v", err)
	}
	if got := complete(racing); got.Get("mcp_oauth_error") != "secret_exists" {
		t.Fatalf("callback = %v, want mcp_oauth_error=secret_exists", got)
	}
	occupant, err = store.Secrets().GetSecret(ctx, project.OrgUUID, occupant.ID)
	if err != nil {
		t.Fatalf("get occupying secret: %v", err)
	}
	if occupant.CurrentVersionNumber != 1 {
		t.Fatalf("occupying secret version = %d, want untouched", occupant.CurrentVersionNumber)
	}
	interactions := requestJSONWithHeaders(t, handler, http.MethodGet, racing.agentPath+"/interactions?state=open",
		"", "", http.StatusOK, authHeaders(project.AdminToken))
	if data := testutil.RequireType[[]any](t, interactions["data"]); len(data) != 1 {
		t.Fatalf("open interactions = %+v, want the rejected request still open", interactions)
	}
}

// Servers such as Google Calendar answer discovery anonymously and challenge
// only tools/call, so authorization must start from the recorded challenge
// rather than re-probing the server.
func TestAgentInteractionAuthorizationConnectsServerWithPublicDiscovery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newMCPOAuthIntegrationServer(pool, "http://omnara.test")
	project := bootstrapPublicHTTPProject(t, handler, "mcp-authz-public-discovery")
	store := integrationStoreForHandler(t, handler)
	fake := newMCPOAuthFakeServer(t, mcpOAuthFakeConfig{PKCE: true, DCR: true})
	mcpURL := fake.URL + "/mcp"

	launch := createHTTPMCPAuthorizationAgent(t, ctx, store, project, "mcp-authz-public-discovery", mcpURL)
	agent := launch.Agent
	toolCallID, interactionID := createHTTPOpenAuthorizationInteraction(
		t, ctx, store, project, agent, mcpURL, []string{"files:read"},
	)
	agentPath := project.ProjectPath + "/agents/" + testPublicID(t, publicid.KindAgent, agent.ID)
	interactionPublicID := testPublicID(t, publicid.KindAgentInteraction, interactionID)

	start := requestJSONWithHeaders(t, handler, http.MethodPost,
		agentPath+"/interactions/"+interactionPublicID+"/authorize",
		`{"return_to":"/agents"}`, "", http.StatusCreated, authHeaders(project.AdminToken))
	authURL, err := url.Parse(testutil.RequireType[string](t, start["authorization_url"]))
	if err != nil {
		t.Fatalf("parse authorization_url: %v", err)
	}
	if got := authURL.Query().Get("resource"); got != mcpURL {
		t.Fatalf("authorization resource = %q, want %q", got, mcpURL)
	}
	fake.setCodeChallenge(authURL.Query().Get("code_challenge"))

	location := completeMCPOAuthCallback(t, handler, authURL.Query().Get("state"), http.StatusFound)
	redirect, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse callback redirect: %v", err)
	}
	if redirect.Query().Get("mcp_oauth") != "success" || redirect.Query().Get("interaction_id") != interactionPublicID {
		t.Fatalf("callback redirect = %q, want success for %s", location, interactionPublicID)
	}
	toolCall, err := store.Execution().GetToolCall(ctx, project.ProjectUUID, agent.ID, toolCallID)
	if err != nil {
		t.Fatalf("get tool call: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateReady {
		t.Fatalf("tool call state = %s, want ready after grant", toolCall.State)
	}
}

func TestAgentInteractionAuthorizationResolveOnlyAcceptsDecline(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newMCPOAuthIntegrationServer(pool, "http://omnara.test")
	project := bootstrapPublicHTTPProject(t, handler, "mcp-authz-resolve")
	store := integrationStoreForHandler(t, handler)
	mcpURL := "http://127.0.0.1:1/mcp"

	launch := createHTTPMCPAuthorizationAgent(t, ctx, store, project, "mcp-authz-resolve", mcpURL)
	agent := launch.Agent
	toolCallID, interactionID := createHTTPOpenAuthorizationInteraction(
		t, ctx, store, project, agent, mcpURL, []string{"files:read"},
	)
	resolvePath := project.ProjectPath + "/agents/" + testPublicID(t, publicid.KindAgent, agent.ID) +
		"/interactions/" + testPublicID(t, publicid.KindAgentInteraction, interactionID) + "/resolve"

	granted := requestJSONWithHeaders(t, handler, http.MethodPost, resolvePath,
		httpQuestionResolutionRequest(mcpauthorization.GrantOptionIndex), "", http.StatusConflict,
		authHeaders(project.AdminToken))
	if granted["code"] != "conflict" {
		t.Fatalf("grant-by-answer response = %+v, want conflict", granted)
	}
	toolCall, err := store.Execution().GetToolCall(ctx, project.ProjectUUID, agent.ID, toolCallID)
	if err != nil {
		t.Fatalf("get tool call: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateWaiting {
		t.Fatalf("tool call state after rejected grant = %s, want waiting", toolCall.State)
	}

	declined := requestJSONWithHeaders(t, handler, http.MethodPost, resolvePath,
		httpQuestionResolutionRequest(mcpauthorization.DeclineOptionIndex), "", http.StatusOK,
		authHeaders(project.AdminToken))
	if declined["state"] != "resolved" || declined["interaction_kind"] != "authorization" {
		t.Fatalf("decline response = %+v, want resolved authorization", declined)
	}
	toolCall, err = store.Execution().GetToolCall(ctx, project.ProjectUUID, agent.ID, toolCallID)
	if err != nil {
		t.Fatalf("get declined tool call: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateCompleted || string(toolCall.Outcome) != "denied" {
		t.Fatalf("declined tool call state=%s outcome=%s, want completed/denied", toolCall.State, toolCall.Outcome)
	}
}

func TestAgentInteractionAuthorizationAuthorizeRejectsInvalidTargets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newMCPOAuthIntegrationServer(pool, "http://omnara.test")
	project := bootstrapPublicHTTPProject(t, handler, "mcp-authz-invalid-targets")
	store := integrationStoreForHandler(t, handler)

	questionAgentID, questionID := createHTTPStructuredQuestionInteraction(
		t, ctx, pool, store, project.OrgUUID, project.ProjectUUID, time.Now().UTC(),
	)
	questionPath := project.ProjectPath + "/agents/" + testPublicID(t, publicid.KindAgent, questionAgentID) +
		"/interactions/" + testPublicID(t, publicid.KindAgentInteraction, questionID) + "/authorize"
	notAuthorization := requestJSONWithHeaders(t, handler, http.MethodPost, questionPath, `{}`, "",
		http.StatusConflict, authHeaders(project.AdminToken))
	if !strings.Contains(testutil.RequireType[string](t, notAuthorization["error"]), "not an authorization") {
		t.Fatalf("question authorize response = %+v, want not-an-authorization conflict", notAuthorization)
	}

	// A first connect must not write a token into an unrelated secret that
	// happens to share the default name.
	fake := newMCPOAuthFakeServer(t, mcpOAuthFakeConfig{RequireAuth: true, PKCE: true, DCR: true})
	mcpURL := fake.URL + "/mcp"
	launch := createHTTPMCPAuthorizationAgent(t, ctx, store, project, "mcp-authz-name-collision", mcpURL)
	_, interactionID := createHTTPOpenAuthorizationInteraction(
		t, ctx, store, project, launch.Agent, mcpURL, []string{"files:read"},
	)
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/v1/orgs/"+project.OrgID+"/secrets",
		`{"owner":{"kind":"project","project_id":"`+project.ProjectID+`"},"name":"`+
			httpMCPAuthorizationSecretName+`","material":{"kind":"generic","value":"unrelated"}}`,
		"", http.StatusCreated, authHeaders(project.AdminToken))
	collisionPath := project.ProjectPath + "/agents/" + testPublicID(t, publicid.KindAgent, launch.Agent.ID) +
		"/interactions/" + testPublicID(t, publicid.KindAgentInteraction, interactionID) + "/authorize"
	collision := requestJSONWithHeaders(t, handler, http.MethodPost, collisionPath, `{}`, "",
		http.StatusConflict, authHeaders(project.AdminToken))
	if !strings.Contains(testutil.RequireType[string](t, collision["error"]), httpMCPAuthorizationSecretName) {
		t.Fatalf("name collision response = %+v, want conflict naming the secret", collision)
	}
}

func TestAgentInteractionAuthorizationCallbackRejectsInsufficientScope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	handler := newMCPOAuthIntegrationServer(pool, "http://omnara.test")
	project := bootstrapPublicHTTPProject(t, handler, "mcp-authz-insufficient-scope")
	store := integrationStoreForHandler(t, handler)
	// The fake token endpoint always grants only files:read.
	fake := newMCPOAuthFakeServer(t, mcpOAuthFakeConfig{RequireAuth: true, PKCE: true, DCR: true})
	mcpURL := fake.URL + "/mcp"

	launch := createHTTPMCPAuthorizationAgent(t, ctx, store, project, "mcp-authz-insufficient-scope", mcpURL)
	agent := launch.Agent
	toolCallID, interactionID := createHTTPOpenAuthorizationInteraction(
		t, ctx, store, project, agent, mcpURL, []string{"files:read", "files:write"},
	)
	interactionPublicID := testPublicID(t, publicid.KindAgentInteraction, interactionID)
	start := requestJSONWithHeaders(t, handler, http.MethodPost,
		project.ProjectPath+"/agents/"+testPublicID(t, publicid.KindAgent, agent.ID)+
			"/interactions/"+interactionPublicID+"/authorize",
		`{}`, "", http.StatusCreated, authHeaders(project.AdminToken))
	authURL, err := url.Parse(testutil.RequireType[string](t, start["authorization_url"]))
	if err != nil {
		t.Fatalf("parse authorization_url: %v", err)
	}
	scopes := strings.Fields(authURL.Query().Get("scope"))
	if !slices.Contains(scopes, "files:read") || !slices.Contains(scopes, "files:write") {
		t.Fatalf("authorization scope = %q, want files:read and files:write", authURL.Query().Get("scope"))
	}
	fake.setCodeChallenge(authURL.Query().Get("code_challenge"))

	location := completeMCPOAuthCallback(t, handler, authURL.Query().Get("state"), http.StatusFound)
	redirect, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse callback redirect: %v", err)
	}
	if redirect.Query().Get("mcp_oauth_error") != "insufficient_scope_granted" ||
		redirect.Query().Get("mcp_oauth") != "" || redirect.Query().Get("secret_id") != "" {
		t.Fatalf("callback redirect = %q, want insufficient_scope_granted", location)
	}
	if _, err := store.Secrets().GetSecretByOwnerName(ctx, project.OrgUUID, secretstore.SecretOwnerProject,
		project.ProjectUUID, uuid.Nil, httpMCPAuthorizationSecretName); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("secret lookup after rejected grant err = %v, want not found", err)
	}
	interaction, found, err := store.Execution().GetAgentInteraction(ctx, project.ProjectUUID, agent.ID, interactionID)
	if err != nil || !found {
		t.Fatalf("get interaction found=%v err=%v", found, err)
	}
	if interaction.State != executionstore.AgentInteractionStateOpen {
		t.Fatalf("interaction state = %s, want open after rejected grant", interaction.State)
	}
	toolCall, err := store.Execution().GetToolCall(ctx, project.ProjectUUID, agent.ID, toolCallID)
	if err != nil {
		t.Fatalf("get tool call: %v", err)
	}
	if toolCall.State != executionstore.ToolCallStateWaiting {
		t.Fatalf("tool call state = %s, want waiting after rejected grant", toolCall.State)
	}
	current, err := store.Execution().GetAgentInProject(ctx, project.ProjectUUID, agent.ID)
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if current.CurrentConfigID != agent.CurrentConfigID {
		t.Fatal("agent config changed after rejected grant")
	}
}

// createHTTPMCPAuthorizationAgent launches an agent whose config has an MCP
// server "calendar" at mcpURL with no auth configured.
func createHTTPMCPAuthorizationAgent(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	project publicHTTPProject,
	name, mcpURL string,
) executionstore.LaunchAgentResult {
	t.Helper()
	sourceYAML := `instruction: HTTP MCP authorization test agent.
model:
  provider_config: openai-prod
  name: http-test
mcp:
  calendar:
    url: ` + mcpURL + `
    permission:
      mode: always_allow
`
	source, err := agentconfig.ParseSource(agentconfig.SourceFormatYAML, []byte(sourceYAML))
	if err != nil {
		t.Fatalf("parse agent config source: %v", err)
	}
	provider := storagefixture.EnsureModelProvider(t, ctx, store.Models(), store.Secrets(),
		storagefixture.ModelProviderInput{
			OrgID: project.OrgUUID, UserID: project.AdminUserUUID, Name: source.Model.ProviderConfig,
		})
	configuredModel := storagefixture.EnsureModelAccess(t, ctx, store.Models(), project.ProjectUUID,
		storagefixture.DefaultModelInput(project.OrgUUID, provider.ID, source.Model.Name))
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(sourceYAML), agentconfig.CompileOptions{
		AllowInsecureLocalMCPHTTP: true,
		ResolveModelSelection: func(string, string) (agentconfig.ResolvedModelSelection, error) {
			return resolvedHTTPAgentConfigModel(configuredModel), nil
		},
	})
	if err != nil {
		t.Fatalf("compile mcp agent config: %v", err)
	}
	config, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               project.ProjectUUID,
		Source:                  sourceYAML,
		SourceFormat:            "yaml",
		ConfiguredModelID:       parseConfiguredModelID(t, compiled),
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		EffectiveDefinitionHash: compiled.Hash,
	})
	if err != nil {
		t.Fatalf("create %s config: %v", name, err)
	}
	profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID:       project.ProjectUUID,
		Name:            name,
		CurrentConfigID: config.ID,
		IdempotencyKey:  "http-profile-" + name,
	})
	if err != nil {
		t.Fatalf("create %s profile: %v", name, err)
	}
	launch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      project.ProjectUUID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     httpUserPrincipal(project.AdminUserUUID),
		IdempotencyKey: "http-launch-" + name,
	})
	if err != nil {
		t.Fatalf("launch %s agent: %v", name, err)
	}
	return launch
}

// createHTTPOpenAuthorizationInteraction records an MCP tool call, runs it,
// and parks it on an open first-connect authorization interaction the way the
// runtime does after a 401 challenge.
func createHTTPOpenAuthorizationInteraction(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	project publicHTTPProject,
	agent executionstore.AgentRecord,
	mcpURL string,
	requiredScopes []string,
) (uuid.UUID, uuid.UUID) {
	return createHTTPOpenAuthorizationInteractionWithChallenge(
		t, ctx, store, project, agent, mcpURL, requiredScopes, httpMCPResourceMetadataURL(mcpURL),
		httpMCPAuthorizationSecretName,
	)
}

// httpMCPResourceMetadataURL is the resource_metadata a fake MCP server's
// Bearer challenge names.
func httpMCPResourceMetadataURL(mcpURL string) string {
	return strings.TrimSuffix(mcpURL, "/mcp") + "/prm"
}

func createHTTPOpenAuthorizationInteractionWithChallenge(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	project publicHTTPProject,
	agent executionstore.AgentRecord,
	mcpURL string,
	requiredScopes []string,
	resourceMetadataURL string,
	secretName string,
) (uuid.UUID, uuid.UUID) {
	t.Helper()
	toolCall, runtime := recordHTTPToolCallForAgent(
		t, ctx, store, project.OrgUUID, project.ProjectUUID, project.AdminUserUUID, agent,
		toolcatalog.MCPRuntimeToolName("calendar", "create_event"), toolcatalog.ToolTypeMCP,
		json.RawMessage(`{"title":"Standup"}`),
	)
	if _, err := store.Execution().MarkToolCallReady(ctx, executionstore.MarkToolCallReadyInput{
		ProjectID:     project.ProjectUUID,
		AgentID:       agent.ID,
		ID:            toolCall.ID,
		RuntimeLockID: runtime.ID,
	}); err != nil {
		t.Fatalf("mark mcp tool call ready: %v", err)
	}
	if _, err := store.Execution().ExecuteToolCall(ctx, executionstore.ExecuteToolCallInput{
		ProjectID:     project.ProjectUUID,
		AgentID:       agent.ID,
		ToolCallID:    toolCall.ID,
		RuntimeLockID: runtime.ID,
	}, func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
		return executionstore.StartToolCallAsync(), nil
	}); err != nil {
		t.Fatalf("start mcp tool call: %v", err)
	}
	request, err := mcpauthorization.NewRequest(mcpauthorization.NewRequestInput{
		ServerKey:      "calendar",
		ToolName:       "create_event",
		MCPURL:         mcpURL,
		SecretName:     secretName,
		AgentConfigID:  agent.CurrentConfigID,
		RequiredScopes: requiredScopes,

		ResourceMetadataURL: resourceMetadataURL,
	})
	if err != nil {
		t.Fatalf("build authorization request: %v", err)
	}
	interaction, err := store.Execution().CreateAuthorizationInteraction(ctx,
		executionstore.CreateAuthorizationInteractionInput{
			ProjectID:     project.ProjectUUID,
			AgentID:       agent.ID,
			ToolCallID:    toolCall.ID,
			RuntimeLockID: runtime.ID,
			Request:       request,
		})
	if err != nil {
		t.Fatalf("create authorization interaction: %v", err)
	}
	parked, err := store.Execution().GetToolCall(ctx, project.ProjectUUID, agent.ID, toolCall.ID)
	if err != nil {
		t.Fatalf("get parked tool call: %v", err)
	}
	if parked.State != executionstore.ToolCallStateWaiting {
		t.Fatalf("parked tool call state = %s, want waiting", parked.State)
	}
	return toolCall.ID, interaction.ID
}
