//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/bearertoken"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests use normal OAuth, public parent registration, durable runtime
// ingress, real TS provider behavior, and real PG admission/completion. The
// socket SDK's capture/reconnect boundary is covered separately in gateway tests.
func TestDiscordGatewaySavedReceiptToAgentJourney(t *testing.T) {
	t.Parallel()
	f := newDiscordJourney(t)
	f.request(t, http.MethodPut, f.installPath(t)+"/launch-profile", map[string]any{
		"agent_profile_id": f.profile["id"],
	}, http.StatusOK)
	f.capture(t, 1, "34", "35", "<@33> help with **this**", true)
	f.drain(t, 1)
	var agentID, targetID uuid.UUID
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT workflow.agent_id, target.id
FROM integration_workflows workflow JOIN integration_targets target
ON target.project_id=workflow.project_id AND target.integration_install_id=workflow.integration_install_id
WHERE workflow.integration_install_id=$1 AND target.provider_ref='35'`, f.install.ID).Scan(&agentID, &targetID))
	require.NotEqual(t, uuid.Nil, agentID)
	// A new socket session/Dispatch sequence for the same message is still one input.
	f.capture(t, 2, "34", "35", "<@33> help with **this**", true)
	f.drain(t, 1)
	f.capture(t, 3, "35", "38", "continue without a mention", false)
	f.drain(t, 1)
	f.requireInputs(t, agentID, targetID, 2)
	var agents, workflows int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM agents WHERE project_id=$1`,
		f.project.ProjectUUID).Scan(&agents))
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM integration_workflows
WHERE integration_install_id=$1`, f.install.ID).Scan(&workflows))
	require.Equal(t, 1, agents)
	require.Equal(t, 1, workflows)
	require.Equal(t, int32(1), f.provider.threads.Load())
	require.Zero(t, f.provider.posts.Load(), "mention creates only its reply thread, not another root message")
	f.drain(t, 0)
}

func TestDiscordGatewayAgentStartedThreadJourney(t *testing.T) {
	t.Parallel()
	f := newDiscordJourney(t)
	// Resolve/register the parent through the same public API used at setup.
	registered := f.request(t, http.MethodPost, f.installPath(t)+"/channels", map[string]any{
		"source": "managed", "provider_ref": "34", "provider_ref_kind": "channel",
	}, http.StatusOK)
	channelID := channelReceiptString(t, registered, "channel_id")
	parentID := mustPublicHTTPID(t, publicid.KindIntegrationTarget, channelID)
	config := testutil.RequireType[map[string]any](t, f.profile["current_config"])
	created := f.request(t, http.MethodPost, f.project.ProjectPath+"/agents", map[string]any{
		"profile": f.profile["id"], "config": config["id"],
		"channel_bindings": []openapi.AttachAgentChannelRequest{{
			ChannelId: channelID, Grants: openapi.ChannelGrants{Send: true},
			ReplyChannelGrants: &openapi.ChannelGrants{Receive: true, Send: true},
		}},
	}, http.StatusCreated)
	agent := testutil.RequireType[map[string]any](t, created["agent"])
	agentID := mustPublicHTTPID(t, publicid.KindAgent, channelReceiptString(t, agent, "id"))
	configID := mustPublicHTTPID(t, publicid.KindAgentConfig, channelReceiptString(t, config, "id"))
	owner, prepared, payload := f.prepareSend(t, agentID, configID, parentID)
	result := f.run(t, &discordJourneyOperation{
		Kind: "send", RequestID: testPublicID(t, publicid.KindToolCall, owner.ToolCallID),
		Scope: map[string]string{
			"project_id": f.project.ProjectID, "integration_app_id": f.appID,
			"integration_install_id": testPublicID(t, publicid.KindIntegrationInstall, f.install.ID),
			"agent_id":               channelReceiptString(t, agent, "id"), "channel_id": channelID,
		}, Payload: payload,
	})
	require.NotNil(t, result.OperationResult)
	require.Equal(t, channelconnector.OperationCompleted, result.OperationResult.Outcome)
	record, err := f.project.Store.Execution().CompleteChannelOperation(t.Context(),
		executionstore.CompleteChannelOperationInput{Prepared: prepared, Payload: payload, Result: *result.OperationResult})
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateCompleted, record.State)
	child, err := f.project.Store.Integrations().GetIntegrationTargetByProviderRef(
		t.Context(), f.project.ProjectUUID, f.install.ID, "36")
	require.NoError(t, err)
	require.Equal(t, parentID, child.ParentChannelID)
	access, err := f.project.Store.Integrations().GetAgentChannelAccess(
		t.Context(), f.project.ProjectUUID, agentID, child.ID)
	require.NoError(t, err)
	require.True(t, access.ReceiveAllowed)
	require.True(t, access.Capabilities.Send)
	require.False(t, access.Capabilities.Read, "completion uses explicit reply grants, not provider capabilities")
	f.capture(t, 1, "36", "38", "tell me more", false)
	f.drain(t, 1)
	f.capture(t, 2, "36", "38", "tell me more", false)
	f.drain(t, 1)
	f.requireInputs(t, agentID, child.ID, 1)
	var workflows int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM integration_workflows
WHERE integration_install_id=$1`, f.install.ID).Scan(&workflows))
	require.Zero(t, workflows, "the cron-created agent receives replies without a launch route or workflow")
	binding, err := f.project.Store.Integrations().GetActiveReceiveBindingForTarget(
		t.Context(), f.project.ProjectUUID, agentID, child.ID)
	require.NoError(t, err)
	require.NoError(t, f.project.Store.Integrations().RevokeIntegrationTargetBinding(
		t.Context(), f.project.ProjectUUID, binding.ID))
	f.capture(t, 3, "36", "39", "<@33> after revocation", true)
	f.drain(t, 1)
	f.requireInputs(t, agentID, child.ID, 1)
	require.Equal(t, int32(1), f.provider.posts.Load())
	require.Equal(t, int32(1), f.provider.threads.Load())
}

type discordJourney struct {
	pool          *pgxpool.Pool
	handler       http.Handler
	project       publicHTTPProject
	install       integrationstore.IntegrationInstallRecord
	profile       map[string]any
	provider      *discordJourneyProvider
	configuration map[string]any
	appID, token  string
	lease         integrationstore.IntegrationRuntimeUnitRecord
}

func newDiscordJourney(t *testing.T) *discordJourney {
	t.Helper()
	if os.Getenv("OMNARA_TEST_DISCORD_GATEWAY_RUNNER") == "" {
		t.Skip("OMNARA_TEST_DISCORD_GATEWAY_RUNNER is required for the cross-service journey")
	}
	f := &discordJourney{pool: openIntegrationDB(t, t.Context()), provider: newDiscordJourneyProvider(t)}
	core := httptest.NewUnstartedServer(nil)
	origin := "http://" + core.Listener.Addr().String()
	var err error
	f.token, err = bearertoken.Generate(bearertoken.KindChannelConnector)
	require.NoError(t, err)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !assert.Equal(t, "Bearer "+f.token, r.Header.Get("Authorization")) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var operation managedRegistrationOperation
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&operation); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		configuration := map[string]any{
			"coreUrl": origin + "/api/v1", "discordUrl": f.provider.server.URL, "token": f.token,
			"operation": discordJourneyOperation{Kind: string(operation.Kind), RequestID: operation.RequestID,
				Scope: operation.Scope, Payload: operation.Payload},
		}
		result, err := executeDiscordJourney(r.Context(), configuration)
		if !assert.NoError(t, err) || !assert.NotNil(t, result.OperationResult) {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, result.OperationResult)
	}))
	t.Cleanup(gateway.Close)
	capability := channelconnector.Capability{ConnectorKey: channelconnector.BuiltInConnectorKey, Provider: "discord"}
	connectors := []channelconnector.Config{{ID: "discord-journey", Token: f.token,
		OperationsURL: gateway.URL, Capabilities: []channelconnector.Capability{capability}}}
	auth, err := channelconnector.NewAuthenticator(connectors)
	require.NoError(t, err)
	operations, err := channelconnector.NewOperationsClient(connectors, gateway.Client())
	require.NoError(t, err)
	f.handler = newIntegrationServer(f.pool, WithPublicURL("https://omnara.test"),
		WithInternalAPIOrigins([]string{origin}), WithPublicAPIURL(origin), WithChannelConnectorAuthenticator(auth),
		WithChannelOperations(operations), WithDiscordSetup(discord.SetupConfig{
			APIURL: f.provider.server.URL, TokenURL: f.provider.server.URL + "/token",
			AuthorizeURL: f.provider.server.URL + "/authorize", HTTPClient: f.provider.server.Client(),
		}))
	core.Config.Handler = f.handler
	core.Start()
	t.Cleanup(core.Close)
	f.configuration = map[string]any{
		"coreUrl": core.URL + "/api/v1", "discordUrl": f.provider.server.URL, "token": f.token,
	}
	f.project = bootstrapPublicHTTPProject(t, f.handler, "discord-journey")
	apps := publicAppFixture{handler: f.handler, project: f.project,
		apps: "/api/v1/orgs/" + f.project.OrgID + "/integration-apps"}
	secret := apps.request(t, http.MethodPost, "/api/v1/orgs/"+f.project.OrgID+"/secrets", map[string]any{
		"name": "Discord journey", "owner": map[string]any{"kind": "org"},
		"material": map[string]any{"kind": "integration_credentials", "values": map[string]any{
			"client_secret": "local-setup-secret", "bot_token": "local-discord-bot",
		}},
	}, http.StatusCreated)
	appBody := publicAppCreateBody(channelReceiptString(t, secret, "id"), "")
	appBody["provider"], appBody["provider_app_ref"], appBody["provider_config"] = "discord", "31", map[string]any{}
	app := apps.request(t, http.MethodPost, apps.apps, appBody, http.StatusCreated)
	f.appID = channelReceiptString(t, app, "id")
	setup := f.request(t, http.MethodPost, f.project.ProjectPath+"/integration-oauth/setup", map[string]any{
		"integration_app_id": f.appID, "return_to": "/settings/integrations",
	}, http.StatusCreated)
	link, err := url.Parse(channelReceiptString(t, setup, "oauth_url"))
	require.NoError(t, err)
	query := url.Values{"code": {"local-code"}, "state": {link.Query().Get("state")}}
	callback := httptest.NewRequest(http.MethodGet,
		"https://omnara.test"+integrationOAuthCallbackPath+"?"+query.Encode(), nil)
	callback.AddCookie(&http.Cookie{Name: httpauth.BrowserSessionHostCookieName, Value: f.project.AdminSession})
	response := performRequest(f.handler, callback)
	require.Equal(t, http.StatusFound, response.Code, response.Body.String())
	require.Contains(t, response.Header().Get("Location"), "integration_oauth=success")
	f.install, err = f.project.Store.Integrations().FindIntegrationInstall(t.Context(), f.project.ProjectUUID,
		mustPublicHTTPID(t, publicid.KindIntegrationApp, f.appID), "32", "33")
	require.NoError(t, err)
	f.profile = createPublicHTTPAgent(t, f.handler, f.project, "discord-profile", f.project.AdminToken)
	leases, err := f.project.Store.Integrations().ClaimIntegrationRuntimeUnits(t.Context(),
		integrationstore.ClaimIntegrationRuntimeUnitsInput{LeaseOwner: "journey", LeaseDuration: time.Minute,
			Capability: capability, Limit: 1})
	require.NoError(t, err)
	require.Len(t, leases, 1)
	f.lease = leases[0]
	return f
}

func (f *discordJourney) installPath(t *testing.T) string {
	t.Helper()
	return f.project.ProjectPath + "/integration-installs/" +
		testPublicID(t, publicid.KindIntegrationInstall, f.install.ID)
}

func (f *discordJourney) request(t *testing.T, method, path string, body any, status int) map[string]any {
	t.Helper()
	return requestJSONWithHeaders(t, f.handler, method, path, workflowHTTPJSON(t, body),
		"", status, authHeaders(f.project.AdminToken))
}

func (f *discordJourney) capture(t *testing.T, sequence int, channel, id, text string, mention bool) {
	t.Helper()
	message := discordJourneyMessage(channel, id, text, "37", false)
	mentions := []map[string]string{}
	if mention {
		mentions = append(mentions, map[string]string{"id": "33"})
	}
	message["mentions"] = mentions
	eventID := fmt.Sprintf("discord:journey:%d", sequence)
	body := map[string]any{"lease_token": f.lease.LeaseToken, "lease_generation": f.lease.LeaseGeneration,
		"event": map[string]any{
			"integration_install_id": testPublicID(t, publicid.KindIntegrationInstall, f.install.ID),
			"event_id":               eventID,
			"payload":                map[string]any{"op": 0, "t": "MESSAGE_CREATE", "s": sequence, "d": message},
		}}
	path := "/api/v1/channel-connector/apps/" + f.appID + "/runtime-units/" +
		testPublicID(t, publicid.KindIntegrationRuntimeUnit, f.lease.ID) + "/events"
	requestJSONWithHeaders(t, f.handler, http.MethodPost, path, workflowHTTPJSON(t, body),
		"", http.StatusAccepted, authHeaders(f.token))
	var state string
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT state FROM integration_event_receipts
WHERE integration_install_id=$1 AND event_id=$2`, f.install.ID, eventID).Scan(&state))
	require.Equal(t, "pending", state, "the event is durable before starting the TS consumer")
}

func (f *discordJourney) requireInputs(t *testing.T, agentID, targetID uuid.UUID, want int) {
	t.Helper()
	var count int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_inputs
WHERE project_id=$1 AND agent_id=$2 AND integration_target_id=$3`,
		f.project.ProjectUUID, agentID, targetID).Scan(&count))
	require.Equal(t, want, count)
}

type discordJourneyOperation struct {
	Kind      string            `json:"kind"`
	RequestID string            `json:"request_id"`
	Scope     map[string]string `json:"scope"`
	Payload   json.RawMessage   `json:"payload"`
}

type discordJourneyResult struct {
	Processed       int                               `json:"processed"`
	OperationResult *channelconnector.OperationResult `json:"operation_result"`
}

func (f *discordJourney) run(t *testing.T, operation *discordJourneyOperation) discordJourneyResult {
	t.Helper()
	configuration := mapsClone(f.configuration)
	if operation != nil {
		configuration["operation"] = operation
	}
	result, err := executeDiscordJourney(t.Context(), configuration)
	require.NoError(t, err)
	return result
}

func (f *discordJourney) drain(t *testing.T, want int) {
	t.Helper()
	require.Equal(t, want, f.run(t, nil).Processed)
}

func executeDiscordJourney(ctx context.Context, configuration map[string]any) (discordJourneyResult, error) {
	var result discordJourneyResult
	body, err := json.Marshal(configuration)
	if err != nil {
		return result, err
	}
	node := os.Getenv("OMNARA_TEST_NODE")
	if node == "" {
		node = "node"
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, node, os.Getenv("OMNARA_TEST_DISCORD_GATEWAY_RUNNER"))
	command.Stdin = bytes.NewReader(body)
	output, err := command.CombinedOutput()
	if err != nil {
		return result, fmt.Errorf("Discord journey: %w: %s", err, output)
	}
	err = json.Unmarshal(output, &result)
	return result, err
}

type discordJourneyProvider struct {
	server         *httptest.Server
	posts, threads atomic.Int32
	channels       sync.Map
}

func newDiscordJourneyProvider(t *testing.T) *discordJourneyProvider {
	t.Helper()
	f := &discordJourneyProvider{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			writeJSON(w, http.StatusOK, map[string]any{"token_type": "Bearer", "scope": "bot",
				"guild": map[string]any{"id": "32", "name": "Journey guild"}})
			return
		}
		if !assert.Equal(t, "Bot local-discord-bot", r.Header.Get("Authorization")) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/applications/@me":
			writeJSON(w, http.StatusOK, map[string]any{"id": "31", "flags": 524288, "bot_require_code_grant": true})
		case "/users/@me":
			writeJSON(w, http.StatusOK, map[string]any{"id": "33", "bot": true, "username": "Omnara"})
		case "/guilds/32":
			writeJSON(w, http.StatusOK, map[string]any{"id": "32", "name": "Journey guild"})
		case "/gateway/bot":
			writeJSON(w, http.StatusOK, map[string]any{"shards": 1})
		case "/channels/34":
			writeJSON(w, http.StatusOK, map[string]any{"id": "34", "guild_id": "32", "type": 0, "name": "engineering"})
		case "/channels/34/messages":
			if r.Method != http.MethodPost {
				t.Error("unexpected history read")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.posts.Add(1)
			writeJSON(w, http.StatusOK, discordJourneyMessage("34", "36", "Scheduled update", "33", true))
		default:
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			if r.Method == http.MethodPost && len(parts) == 5 && parts[0] == "channels" &&
				parts[1] == "34" && parts[2] == "messages" && parts[4] == "threads" {
				thread := map[string]any{"id": parts[3], "guild_id": "32", "parent_id": "34", "type": 11, "name": "Reply"}
				if _, loaded := f.channels.LoadOrStore(parts[3], thread); loaded {
					t.Error("a message's thread was created twice")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				f.threads.Add(1)
				writeJSON(w, http.StatusCreated, thread)
				return
			}
			if r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "channels" {
				if thread, found := f.channels.Load(parts[1]); found {
					writeJSON(w, http.StatusOK, thread)
					return
				}
				writeJSON(w, http.StatusNotFound, map[string]any{"code": 10003, "message": "Unknown Channel"})
				return
			}
			t.Errorf("unexpected native request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func discordJourneyMessage(channel, id, text, author string, bot bool) map[string]any {
	return map[string]any{"id": id, "channel_id": channel, "guild_id": "32", "type": 0,
		"content": text, "timestamp": "2026-09-15T12:00:00Z", "attachments": []any{}, "embeds": []any{},
		"author": map[string]any{"id": author, "username": "Journey user", "bot": bot}}
}

func (f *discordJourney) prepareSend(t *testing.T, agentID, configID, parentID uuid.UUID) (
	executionstore.PrepareChannelOperationInput, executionstore.PreparedChannelOperation, json.RawMessage,
) {
	t.Helper()
	ctx, store := t.Context(), f.project.Store.Execution()
	input, _, _, err := store.CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID: f.project.ProjectUUID, AgentID: agentID,
		Actor:         httpOmnaraActorParams(t, f.project.OrgUUID, f.project.AdminUserUUID),
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"Post the scheduled update"}]`), IdempotencyKey: "cron-input",
	})
	require.NoError(t, err)
	work, found, err := store.ClaimNextAgentWork(ctx, httpTestClaimInput())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, agentID, work.RuntimeLock.AgentID)
	claim := claimNormalModelCallForHTTPTest(t, ctx, f.project.Store, f.project.ProjectUUID, agentID, work.RuntimeLock,
		[]uuid.UUID{input.ID}, configID, work.Model.AdmittedInputTurn.Events[0].Sequence)
	channelID := testPublicID(t, publicid.KindIntegrationTarget, parentID)
	proposal := model.ToolCall{ID: "scheduled-send", Name: toolcatalog.ToolNameSendChannelMessage,
		Input: json.RawMessage(`{"channel_id":"` + channelID + `","message":{"text":"Scheduled update"}}`)}
	identity := loadModelCallProviderIdentityForHTTPTest(t, ctx, f.project.Store, f.project.ProjectUUID, claim.Context)
	response, err := model.NewResponseEnvelopeForStorage(identity.Slug, identity.APIFormat, identity.APIVariant,
		model.Response{ID: "scheduled-response", StopReason: model.StopReasonToolUse,
			Content: modeltest.ResponsePartsForToolCalls([]model.ToolCall{proposal})})
	require.NoError(t, err)
	_, calls, err := store.RecordToolCallSourceAndCompleteContext(ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID: f.project.ProjectUUID, AgentID: agentID, RuntimeLockID: work.RuntimeLock.ID,
			ModelCallContextID: claim.Context.ID, ProviderResponse: response,
			ToolCallBindings: []executionstore.ToolCallBindingInput{
				{ProviderCallID: proposal.ID, Type: toolcatalog.ToolTypeBuiltIn},
			},
		})
	require.NoError(t, err)
	require.Len(t, calls, 1)
	_, err = store.MarkToolCallReady(ctx, executionstore.MarkToolCallReadyInput{
		ProjectID: f.project.ProjectUUID, AgentID: agentID, ID: calls[0].ID, RuntimeLockID: work.RuntimeLock.ID,
	})
	require.NoError(t, err)
	owner := executionstore.PrepareChannelOperationInput{
		ExecuteToolCallInput: executionstore.ExecuteToolCallInput{
			ProjectID: f.project.ProjectUUID, AgentID: agentID, ToolCallID: calls[0].ID, RuntimeLockID: work.RuntimeLock.ID,
		}, TurnID: calls[0].TurnID, ChannelID: parentID, Operation: integrationstore.ChannelBindingOperationSend,
	}
	_, err = store.ExecuteToolCall(ctx, owner.ExecuteToolCallInput,
		func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			return executionstore.StartToolCallAsync(), nil
		})
	require.NoError(t, err)
	prepared, err := store.PrepareChannelOperation(ctx, owner)
	require.NoError(t, err)
	access := prepared.Access()
	payload, err := json.Marshal(channelconnector.SendPayload{
		Destination: channelconnector.OperationDestination{
			ImplementationKey: access.ImplementationKey, ProviderRef: access.ProviderRef,
			ProviderRefKind: access.ProviderRefKind, ProviderMetadata: access.ProviderMetadata,
		},
		Message: channelconnector.Message{Text: "Scheduled update"}, Params: json.RawMessage(`{}`),
		ReplyChannelGrants: &channelconnector.ChannelGrants{Receive: true, Send: true},
	})
	require.NoError(t, err)
	return owner, prepared, payload
}
