//go:build integration

package httpapi

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturedHTTPFixture struct {
	handler        http.Handler
	project        publicHTTPProject
	pool           *pgxpool.Pool
	app            integrationstore.ProjectAppRecord
	callbackStatus int
	signingSecret  string
	record         executionstore.AgentInteractionRecord
	key            ed25519.PrivateKey
	client         *http.Client
	dismissed      chan struct{}
}

type capturedHTTPFixtureOptions struct {
	handlerConfig    map[string]any
	prepareOnly      bool
	providerOverride func(http.ResponseWriter, *http.Request) bool
}

type capturedTestTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func (t capturedTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	request := r.Clone(r.Context())
	request.URL.Scheme, request.URL.Host = t.target.Scheme, t.target.Host
	return t.base.RoundTrip(request)
}

func newCapturedHTTPFixture(t *testing.T, provider, kind string) capturedHTTPFixture {
	t.Helper()
	return newCapturedHTTPFixtureWithDismiss(t, provider, kind, nil)
}

func newCapturedHTTPFixtureWithDismiss(
	t *testing.T,
	provider, kind string,
	dismiss http.HandlerFunc,
	options ...capturedHTTPFixtureOptions,
) capturedHTTPFixture {
	t.Helper()
	ctx := t.Context()
	var opts capturedHTTPFixtureOptions
	if len(options) != 0 {
		opts = options[0]
	}
	dismissed := make(chan struct{}, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if opts.providerOverride != nil && opts.providerOverride(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/auth.test":
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": true, "team_id": "T123", "user_id": "U_BOT", "bot_id": "B123",
			})
		case "/api/chat.postMessage":
			writeJSON(w, 200, map[string]any{"ok": true, "channel": "C123", "ts": "222.333"})
		case "/api/chat.update":
			if dismiss != nil {
				dismiss(w, r)
				dismissed <- struct{}{}
				return
			}
			writeJSON(w, 200, map[string]any{"ok": true})
			dismissed <- struct{}{}
		case "/api/v10/users/@me":
			writeJSON(w, 200, map[string]any{"id": "200", "bot": true})
		case "/api/v10/applications/@me":
			writeJSON(w, 200, map[string]any{"id": "100"})
		case "/api/v10/channels/300":
			writeJSON(w, 200, map[string]any{"id": "300", "guild_id": "500", "type": 0})
		case "/api/v10/channels/300/messages", "/api/v10/channels/300/messages/400":
			var payload map[string]any
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload)) {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			writeJSON(
				w,
				200,
				map[string]any{
					"id":         "400",
					"channel_id": "300",
					"author":     map[string]any{"id": "200"},
					"nonce":      payload["nonce"],
				},
			)
			if r.Method == http.MethodPatch {
				dismissed <- struct{}{}
			}
		default:
			t.Errorf("unexpected local provider request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	require.NoError(t, err)
	client := &http.Client{Transport: capturedTestTransport{base: server.Client().Transport, target: target}}
	pool := openIntegrationDB(t, ctx)
	handler := newIntegrationServer(pool, WithSlackOAuth(SlackOAuthConfig{HTTPClient: client}))
	project := bootstrapPublicHTTPProject(t, handler, "captured-"+provider+"-"+kind)
	publicKey, key, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	material := secrets.Material(secrets.GenericMaterial{Value: "test-bot-token"})
	tenant, account, ref := "100", "200", "300"
	config := json.RawMessage(`{"public_key":"` + hex.EncodeToString(publicKey) + `"}`)
	identity := json.RawMessage(`{}`)
	if provider == "slack" {
		material = secrets.SlackAppCredentialsMaterial{
			AccessToken:   "xoxb-test",
			ClientID:      "client",
			ClientSecret:  "client-secret",
			SigningSecret: "signing-secret",
		}
		tenant, account, ref, config = "T123", "A123", "C123", json.RawMessage(`{}`)
		identity = json.RawMessage(`{"bot_user_id":"U_BOT"}`)
	}
	secret, _, err := project.Store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          project.OrgUUID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: project.ProjectUUID,
		Name:           "bot",
		Material:       material,
		Actor:          httpUserPrincipal(project.AdminUserUUID),
	})
	require.NoError(t, err)
	app, err := project.Store.Integrations().CreateProjectApp(ctx, integrationstore.SaveProjectAppInput{
		OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, Name: "support", DefinitionID: "omnara." + provider,
	})
	require.NoError(t, err)
	app, err = project.Store.Integrations().ConfigureProjectApp(ctx, integrationstore.ConfigureProjectAppInput{
		OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, AppID: app.ID, ExpectedSetupRevision: app.SetupRevision,
		InstalledByUserID: project.AdminUserUUID, Provider: provider, ProviderTenantID: tenant, ProviderAccountRef: account,
		CredentialSecretID: secret.ID, CredentialVersionID: secret.CurrentVersionID, OAuthFlowID: uuid.Must(uuid.NewV7()),
		ProviderConfig: config, ProviderIdentity: identity,
	})
	require.NoError(t, err)
	handlerConfig := map[string]any{"channel_id": ref}
	for key, value := range opts.handlerConfig {
		handlerConfig[key] = value
	}
	source := map[string]any{
		"instruction":          "Help with the request.",
		"model":                map[string]any{"provider_config": "openai-prod", "name": "gpt-test"},
		"interaction_handlers": map[string]any{"support": map[string]any{"config": handlerConfig}},
	}
	agentConfig := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"captured",
		"json",
		projectAppHTTPJSON(t, source),
		project.AdminToken,
		201,
	)
	configID := testutil.RequireType[string](t, agentConfig["id"])
	profile := createPublicHTTPAgentProfile(
		t,
		handler,
		project,
		"captured",
		"Captured",
		configID,
		project.AdminToken,
		201,
	)
	launched := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents",
		projectAppHTTPJSON(
			t,
			map[string]any{"profile": profile["id"], "config": configID},
		),
		"launch",
		201,
		authHeaders(project.AdminToken),
	)
	agentID := testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, launched["agent"])["id"])
	agentUUID := mustPublicHTTPID(t, publicid.KindAgent, agentID)
	choices, err := project.Store.Execution().ListInteractionHandlers(ctx, project.ProjectUUID, agentUUID, "", 100)
	require.NoError(t, err)
	require.Len(t, choices.Handlers, 1, "fixed handler must be discoverable without prior input")
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	address := integrationstore.ConversationAddress{Kind: "channel", Ref: ref}
	require.NoError(t, integrationstore.LockAppsTx(ctx, tx, project.ProjectUUID, nil, app.ID))
	require.NoError(t, integrationstore.LockConversationTx(ctx, tx, project.ProjectUUID, app.ID, address))
	_, err = tx.Exec(ctx, "SELECT id FROM agents WHERE id=$1 FOR UPDATE", agentUUID)
	require.NoError(t, err)
	origin, err := project.Store.Integrations().
		EnsureConversationTargetTx(ctx, tx, integrationstore.EnsureConversationTargetInput{
			ProjectID: project.ProjectUUID, AgentID: agentUUID, AppID: app.ID,
			Address: address, Role: integrationstore.TargetAttribution,
		})
	require.NoError(t, err)
	_, err = project.Store.Execution().SelectInteractionDestinationForOriginTx(
		ctx, tx, project.ProjectUUID, agentUUID, origin.ID,
	)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/inputs",
		projectAppHTTPJSON(t, map[string]any{
			"content_blocks": []any{map[string]any{"type": "text", "text": "Please respond"}},
		}),
		"input",
		201,
		authHeaders(project.AdminToken),
	)
	record := createInteractionForAgent(
		t,
		ctx,
		project.Store,
		project,
		mustPublicHTTPID(t, publicid.KindAgent, agentID),
		"captured",
		kind,
	)
	presenter := integration.InteractionPresenter{Store: project.Store, HTTPClient: client}
	if !opts.prepareOnly {
		require.NoError(t, presenter.Present(ctx, project.ProjectUUID, record.AgentID, record.ID))
	}
	record, found, err := project.Store.Execution().
		GetAgentInteraction(ctx, project.ProjectUUID, record.AgentID, record.ID)
	require.NoError(t, err)
	require.True(t, found)
	if !opts.prepareOnly {
		require.NotEmpty(t, record.PresentationReceipt)
	}
	return capturedHTTPFixture{
		handler: handler, project: project, pool: pool, app: app,
		record: record, key: key, client: client, dismissed: dismissed,
	}
}

func (f capturedHTTPFixture) discordRequest(
	t *testing.T, action string, modal bool, wrongSurface bool, changes ...func(map[string]any),
) map[string]any {
	t.Helper()
	id, err := discord.EncodeCustomID(
		discord.CustomID{InteractionID: testPublicID(t, publicid.KindAgentInteraction, f.record.ID), Action: action},
	)
	require.NoError(t, err)
	channel := "300"
	if wrongSurface {
		channel = "301"
	}
	kind := 3
	data := map[string]any{"custom_id": id}
	if modal {
		kind = 5
		value := "1"
		if strings.HasPrefix(action, "t") {
			value = "Please wait"
		}
		data["components"] = []any{
			map[string]any{"components": []any{map[string]any{"custom_id": "q0", "value": value}}},
		}
	}
	payload := map[string]any{
		"id":             "600",
		"application_id": "100",
		"type":           kind,
		"channel_id":     channel,
		"guild_id":       "500",
		"member":         map[string]any{"user": map[string]any{"id": "700", "username": "participant"}},
		"message": map[string]any{
			"id":         "400",
			"channel_id": channel,
			"author":     map[string]any{"id": "200"},
		},
		"data": data,
	}
	for _, change := range changes {
		change(payload)
	}
	body := projectAppHTTPJSON(t, payload)
	timestamp := fmt.Sprint(time.Now().Unix())
	headers := map[string]string{"Content-Type": "application/json", "X-Signature-Timestamp": timestamp,
		"X-Signature-Ed25519": hex.EncodeToString(ed25519.Sign(f.key, []byte(timestamp+body)))}
	path := "/api/integrations/discord/" + f.app.ProviderTenantID + "/interactions"
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := performRequest(f.handler, request)
	status := f.callbackStatus
	if status == 0 {
		status = http.StatusOK
	}
	require.Equal(t, status, response.Code, response.Body.String())
	if status != http.StatusOK {
		return nil
	}
	var result map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	return result
}

func (f capturedHTTPFixture) slackRequest(
	t *testing.T,
	wrongSurface bool,
	changes ...func(map[string]any),
) map[string]any {
	t.Helper()
	destination, err := f.record.CapturedDestination()
	require.NoError(t, err)
	body := slackActionFormBody(t, slackActionPayloadInput{
		Install:             f.app,
		AgentID:             f.record.AgentID,
		IntegrationTargetID: destination.IntegrationTargetID,
		InteractionID:       f.record.ID,
		UserID:              "U_OTHER",
		OptionValue:         "0",
	})
	values, err := url.ParseQuery(body)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(values.Get("payload")), &payload))
	channel := "C123"
	if wrongSurface {
		channel = "C_OTHER"
	}
	payload["channel"] = map[string]any{"id": channel}
	payload["message"] = map[string]any{"ts": "222.333"}
	for _, change := range changes {
		change(payload)
	}
	values.Set("payload", projectAppHTTPJSON(t, payload))
	body = values.Encode()
	secret := f.signingSecret
	if secret == "" {
		secret = "signing-secret"
	}
	headers := unitSlackSignedHeaders(body, secret)
	headers["Content-Type"] = "application/x-www-form-urlencoded"
	status := f.callbackStatus
	if status == 0 {
		status = http.StatusOK
	}
	return requestJSONWithHeaders(t, f.handler, http.MethodPost, integrationActionsPath, body, "", status, headers)
}

func TestCapturedInteractionCallbacksResolveVerifiedSurface(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"slack", "discord"} {
		for _, kind := range []string{"question", "permission"} {
			t.Run(provider+"/"+kind, func(t *testing.T) {
				t.Parallel()
				f := newCapturedHTTPFixture(t, provider, kind)
				if provider == "slack" {
					require.Equal(t, "ignored", f.slackRequest(t, true)["ok"])
				} else {
					require.Equal(t, float64(4), f.discordRequest(t, "c0", false, true)["type"])
				}
				current, found, err := f.project.Store.Execution().
					GetAgentInteraction(t.Context(), f.project.ProjectUUID, f.record.AgentID, f.record.ID)
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, executionstore.AgentInteractionStateOpen, current.State)
				// Moving the current selection does not reroute an existing prompt.
				_, err = f.pool.Exec(
					t.Context(),
					"UPDATE agents SET integration_target_id=NULL, interaction_handler_key=NULL, interaction_handler_args=NULL WHERE id=$1",
					f.record.AgentID,
				)
				require.NoError(t, err)
				if provider == "slack" {
					require.Equal(t, "resolved", f.slackRequest(t, false)["ok"])
				} else {
					require.Equal(t, float64(6), f.discordRequest(t, "c0", false, false)["type"])
				}
				current, found, err = f.project.Store.Execution().
					GetAgentInteraction(t.Context(), f.project.ProjectUUID, f.record.AgentID, f.record.ID)
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, executionstore.AgentInteractionStateResolved, current.State)
				require.NotEqual(t, uuid.Nil, current.ResolvedByInputID)
				if provider == "slack" {
					require.Equal(t, "already_resolved", f.slackRequest(t, false)["ok"])
				} else {
					require.Equal(t, float64(4), f.discordRequest(t, "c0", false, false)["type"])
				}
				select {
				case <-f.dismissed:
				case <-time.After(3 * time.Second):
					t.Fatal("confirmed prompt was not dismissed")
				}
			})
		}
	}
}

func TestCapturedDiscordModalTextAndReplay(t *testing.T) {
	f := newCapturedHTTPFixture(t, "discord", "permission")
	first := f.discordRequest(t, "t1", false, false)
	require.Equal(t, float64(9), first["type"])
	require.Equal(t, first, f.discordRequest(t, "t1", false, false))
	require.Equal(t, float64(4), f.discordRequest(t, "t1", true, false)["type"])
	current, _, err := f.project.Store.Execution().
		GetAgentInteraction(t.Context(), f.project.ProjectUUID, f.record.AgentID, f.record.ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.AgentInteractionStateResolved, current.State)
	require.Contains(t, string(current.Resolution), "Please wait")
}

func TestCapturedCallbackRevocationLeavesDashboardAvailable(t *testing.T) {
	for _, provider := range []string{"slack", "discord"} {
		t.Run(provider, func(t *testing.T) {
			f := newCapturedHTTPFixture(t, provider, "question")
			_, err := f.project.Store.Integrations().DisconnectProjectApp(
				t.Context(), integrationstore.DisconnectProjectAppInput{
					ProjectID: f.project.ProjectUUID, AppID: f.app.ID,
					ExpectedSetupRevision: &f.app.SetupRevision,
				},
			)
			require.NoError(t, err)
			if provider == "slack" {
				f.callbackStatus = http.StatusForbidden
				f.slackRequest(t, false)
			} else {
				f.callbackStatus = http.StatusForbidden
				f.discordRequest(t, "c0", false, false)
			}
			path := f.project.ProjectPath + "/agents/" + testPublicID(
				t,
				publicid.KindAgent,
				f.record.AgentID,
			) + "/interactions/" + testPublicID(
				t,
				publicid.KindAgentInteraction,
				f.record.ID,
			) + "/resolve"
			response := requestJSONWithHeaders(
				t,
				f.handler,
				http.MethodPost,
				path,
				`{"answers":[{"option_indices":[0]}]}`,
				"",
				200,
				f.project.adminBrowserAuthHeaders(),
			)
			require.Equal(t, "resolved", response["state"])
		})
	}
}

func TestSlackActionsResolveQuestionAsSlackActor(t *testing.T) {
	t.Parallel()
	f := newCapturedHTTPFixture(t, "slack", "question")
	_, err := f.pool.Exec(t.Context(), `
INSERT INTO actors(project_id, provider, provider_tenant_id, provider_user_id, display_name, created_at, updated_at)
VALUES ($1, 'slack', $2, 'U_OTHER', 'Grace Hopper', now(), now())
ON CONFLICT (project_id, provider, provider_tenant_id, provider_user_id)
DO UPDATE SET display_name = excluded.display_name, updated_at = excluded.updated_at`,
		f.project.ProjectUUID, f.app.ProviderTenantID)
	require.NoError(t, err)
	require.Equal(t, "resolved", f.slackRequest(t, false)["ok"])
	actorID, inputKind := interactionResolvingInput(
		t,
		t.Context(),
		f.pool,
		f.project.ProjectUUID,
		f.record.AgentID,
		f.record.ID,
	)
	require.Equal(t, "interaction_response", inputKind)
	actor, err := f.project.Store.Execution().GetActor(t.Context(), f.project.ProjectUUID, actorID)
	require.NoError(t, err)
	require.Equal(t, identitystore.ActorProviderSlack, actor.Provider)
	require.Equal(t, "U_OTHER", actor.ProviderUserID)
	names, err := f.project.Store.Execution().ListActorDisplayNames(t.Context(), f.project.ProjectUUID,
		identitystore.ActorProviderSlack, f.app.ProviderTenantID, []string{"U_OTHER"})
	require.NoError(t, err)
	require.Equal(t, "Grace Hopper", names["U_OTHER"], "retain the stored name over the signed payload name")
}

func TestSlackActionsQuestionSubmissionRequiresAnswer(t *testing.T) {
	t.Parallel()
	f := newCapturedHTTPFixture(t, "slack", "question")
	response := f.slackRequest(t, false, func(payload map[string]any) { delete(payload, "state") })
	require.Equal(t, "invalid", response["ok"])
	require.Equal(t, "question 0 requires an answer", response["text"])
	current, found, err := f.project.Store.Execution().
		GetAgentInteraction(t.Context(), f.project.ProjectUUID, f.record.AgentID, f.record.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.AgentInteractionStateOpen, current.State)
}

func TestCapturedSlackRootPromptRemainsValidAfterThreadReply(t *testing.T) {
	t.Parallel()
	f := newCapturedHTTPFixture(t, "slack", "question")
	wrong := f.slackRequest(t, false, func(payload map[string]any) {
		payload["message"] = map[string]any{"ts": "222.333", "thread_ts": "111.222"}
	})
	require.Equal(t, "ignored", wrong["ok"])
	root := f.slackRequest(t, false, func(payload map[string]any) {
		payload["message"] = map[string]any{"ts": "222.333", "thread_ts": "222.333"}
	})
	require.Equal(t, "resolved", root["ok"])
}

func TestSlackActionsResolvePermissionAsSlackActor(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	var once sync.Once
	releaseDismiss := func() { once.Do(func() { close(release) }) }
	updates := make(chan map[string]any, 1)
	f := newCapturedHTTPFixtureWithDismiss(t, "slack", "permission", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		updates <- body
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	defer releaseDismiss()
	timer := time.AfterFunc(1500*time.Millisecond, releaseDismiss)
	defer timer.Stop()
	started := time.Now()
	response := f.slackRequest(t, false)
	require.Less(t, time.Since(started), time.Second, "callback must not wait for provider dismissal")
	require.Equal(t, "resolved", response["ok"])
	current, found, err := f.project.Store.Execution().
		GetAgentInteraction(t.Context(), f.project.ProjectUUID, f.record.AgentID, f.record.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.AgentInteractionStateResolved, current.State)
	var resolution interactionform.Resolution
	require.NoError(t, json.Unmarshal(current.Resolution, &resolution))
	require.Equal(t, []int{toolpermission.AllowOptionIndex}, resolution.Answers[0].OptionIndices)
	actorID, _ := interactionResolvingInput(
		t,
		t.Context(),
		f.pool,
		f.project.ProjectUUID,
		f.record.AgentID,
		f.record.ID,
	)
	actor, err := f.project.Store.Execution().GetActor(t.Context(), f.project.ProjectUUID, actorID)
	require.NoError(t, err)
	require.Equal(t, identitystore.ActorProviderSlack, actor.Provider)
	require.Equal(t, "U_OTHER", actor.ProviderUserID)
	select {
	case update := <-updates:
		require.Equal(t, "C123", update["channel"])
		require.Equal(t, "222.333", update["ts"])
		require.Empty(t, update["blocks"])
		require.Equal(t, "Response recorded.", update["text"])
	case <-time.After(3 * time.Second):
		t.Fatal("confirmed prompt was not dismissed")
	}
}

func TestCapturedDiscordEndpointVerifiesPingSignatureAndApplication(t *testing.T) {
	f := newCapturedHTTPFixture(t, "discord", "question")
	path := "/api/integrations/discord/" + f.app.ProviderTenantID + "/interactions"
	for _, test := range []struct {
		name, application string
		badSignature      bool
		wantStatus        int
	}{
		{"valid", "100", false, http.StatusOK},
		{"invalid signature", "100", true, http.StatusUnauthorized},
		{"wrong application", "101", false, http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := `{"id":"600","type":1,"application_id":"` + test.application + `"}`
			timestamp := fmt.Sprint(time.Now().Unix())
			signature := ed25519.Sign(f.key, []byte(timestamp+body))
			if test.badSignature {
				signature[0] ^= 1
			}
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			req.Header.Set("X-Signature-Timestamp", timestamp)
			req.Header.Set("X-Signature-Ed25519", hex.EncodeToString(signature))
			response := httptest.NewRecorder()
			f.handler.ServeHTTP(response, req)
			require.Equal(t, test.wantStatus, response.Code)
			if response.Code == http.StatusOK {
				require.JSONEq(t, `{"type":1}`, response.Body.String())
			}
		})
	}
}

func TestCapturedInteractionPublicVisibilityAndResolution(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"slack", "discord"} {
		for _, surface := range []string{"api", "dashboard"} {
			t.Run(provider+"/"+surface, func(t *testing.T) {
				t.Parallel()
				f := newCapturedHTTPFixture(t, provider, "permission")
				token := customIntegrationHTTPKey(t, f.handler, f.project, "presenter", "operator")
				apiHeaders := authHeaders(token)
				browserHeaders := f.project.adminBrowserAuthHeaders()
				path := f.project.ProjectPath + "/agents/" + testPublicID(t, publicid.KindAgent, f.record.AgentID) + "/interactions"
				read := func(headers map[string]string) map[string]any {
					t.Helper()
					listed := requestJSONWithHeaders(t, f.handler, http.MethodGet, path, "", "", http.StatusOK, headers)
					data := testutil.RequireType[[]any](t, listed["data"])
					require.Len(t, data, 1)
					return testutil.RequireType[map[string]any](t, data[0])
				}
				listed := read(apiHeaders)
				require.Equal(t, listed, read(browserHeaders))
				destination, err := f.record.CapturedDestination()
				require.NoError(t, err)
				require.NotNil(t, destination)
				captured := testutil.RequireType[map[string]any](t, listed["destination"])
				require.Equal(t, map[string]any{
					"handler_definition":    "omnara." + provider,
					"config":                map[string]any{"channel_id": destination.Address.Ref},
					"args":                  map[string]any{},
					"handler_key":           "support",
					"app_id":                testPublicID(t, publicid.KindProjectApp, f.app.ID),
					"integration_target_id": testPublicID(t, publicid.KindIntegrationTarget, destination.IntegrationTargetID),
					"address":               map[string]any{"kind": destination.Address.Kind, "ref": destination.Address.Ref},
				}, captured)
				var receipt map[string]any
				require.NoError(t, json.Unmarshal(f.record.PresentationReceipt, &receipt))
				require.Equal(t, receipt, listed["presentation_receipt"])
				require.NotContains(t, projectAppHTTPJSON(t, listed), f.app.ID.String())
				require.NotContains(t, projectAppHTTPJSON(t, listed), destination.IntegrationTargetID.String())
				headers := apiHeaders
				if surface == "dashboard" {
					headers = browserHeaders
				}
				resolved := requestJSONWithHeaders(t, f.handler, http.MethodPost, path+"/"+
					testPublicID(t, publicid.KindAgentInteraction, f.record.ID)+"/resolve",
					`{"answers":[{"option_indices":[0]}]}`, "", http.StatusOK, headers)
				require.Equal(t, "resolved", resolved["state"])
				require.Equal(t, captured, resolved["destination"])
				require.Equal(t, receipt, resolved["presentation_receipt"])
				require.Equal(t, resolved, read(apiHeaders))
				require.Equal(t, resolved, read(browserHeaders))
				assertInteractionResponseAgentInput(t, t.Context(), f.pool, f.project.ProjectUUID, f.record.AgentID, f.record.ID)
				assertInteractionResponseLedgerEvent(t, t.Context(), f.pool, f.project.ProjectUUID, f.record.AgentID, f.record.ID)
			})
		}
	}
}

func capturedSiblingApp(t *testing.T, f capturedHTTPFixture) (integrationstore.ProjectAppRecord, ed25519.PrivateKey) {
	t.Helper()
	publicKey, key, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	config := json.RawMessage(`{"public_key":"` + hex.EncodeToString(publicKey) + `"}`)
	material := secrets.Material(secrets.GenericMaterial{Value: "sibling-token"})
	if f.app.Provider == "slack" {
		material = secrets.SlackAppCredentialsMaterial{AccessToken: "xoxb-sibling", ClientID: "client",
			ClientSecret: "client-secret", SigningSecret: "sibling-signing-secret"}
		config = json.RawMessage(`{}`)
	}
	secret, _, err := f.project.Store.Secrets().CreateSecret(t.Context(), secretstore.CreateSecretInput{
		OrgID: f.app.OrgID, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: f.app.ProjectID,
		Name: "sibling", Material: material, Actor: httpUserPrincipal(f.project.AdminUserUUID),
	})
	require.NoError(t, err)
	app, err := f.project.Store.Integrations().CreateProjectApp(t.Context(), integrationstore.SaveProjectAppInput{
		OrgID: f.app.OrgID, ProjectID: f.app.ProjectID, Name: "sibling", DefinitionID: f.app.DefinitionID,
	})
	require.NoError(t, err)
	app, err = f.project.Store.Integrations().ConfigureProjectApp(t.Context(), integrationstore.ConfigureProjectAppInput{
		OrgID: f.app.OrgID, ProjectID: f.app.ProjectID, AppID: app.ID, ExpectedSetupRevision: app.SetupRevision,
		InstalledByUserID: f.project.AdminUserUUID, Provider: f.app.Provider,
		ProviderTenantID: f.app.ProviderTenantID, ProviderAccountRef: f.app.ProviderAccountRef,
		CredentialSecretID: secret.ID, CredentialVersionID: secret.CurrentVersionID, OAuthFlowID: uuid.Must(uuid.NewV7()),
		ProviderConfig: config, ProviderIdentity: f.app.ProviderIdentity,
	})
	require.NoError(t, err)
	return app, key
}

func TestCapturedCallbackUsesOwnerCredentialsWithSharedBot(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"slack", "discord"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			f := newCapturedHTTPFixture(t, provider, "question")
			sibling, key := capturedSiblingApp(t, f)
			forged := f
			forged.key, forged.signingSecret, forged.callbackStatus = key, "sibling-signing-secret", http.StatusUnauthorized
			if provider == "slack" {
				forged.slackRequest(t, false)
			} else {
				forged.discordRequest(t, "c0", false, false)
			}
			current, found, err := f.project.Store.Execution().
				GetAgentInteraction(t.Context(), f.app.ProjectID, f.record.AgentID, f.record.ID)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, executionstore.AgentInteractionStateOpen, current.State)
			if provider == "slack" {
				require.Equal(t, "resolved", f.slackRequest(t, false)["ok"])
			} else {
				require.Equal(t, float64(6), f.discordRequest(t, "c0", false, false)["type"])
			}
			var targets int
			require.NoError(t, f.pool.QueryRow(t.Context(),
				`SELECT count(*) FROM integration_targets WHERE app_id=$1`, sibling.ID).Scan(&targets))
			require.Zero(t, targets)
		})
	}
}

func TestCapturedDiscordPingAcceptsAnyActiveSharedApp(t *testing.T) {
	t.Parallel()
	f := newCapturedHTTPFixture(t, "discord", "question")
	_, key := capturedSiblingApp(t, f)
	_, err := f.project.Store.Integrations().DisconnectProjectApp(t.Context(), integrationstore.DisconnectProjectAppInput{
		ProjectID: f.app.ProjectID, AppID: f.app.ID,
	})
	require.NoError(t, err)
	body := `{"id":"600","type":1,"application_id":"100"}`
	for _, test := range []struct {
		key    ed25519.PrivateKey
		status int
	}{{f.key, http.StatusUnauthorized}, {key, http.StatusOK}} {
		timestamp := fmt.Sprint(time.Now().Unix())
		r := httptest.NewRequest(http.MethodPost, "/api/integrations/discord/100/interactions", strings.NewReader(body))
		r.Header.Set("X-Signature-Timestamp", timestamp)
		r.Header.Set("X-Signature-Ed25519", hex.EncodeToString(ed25519.Sign(test.key, []byte(timestamp+body))))
		response := performRequest(f.handler, r)
		require.Equal(t, test.status, response.Code, response.Body.String())
		if test.status == http.StatusOK {
			require.JSONEq(t, `{"type":1}`, response.Body.String())
		}
	}
}

func (f capturedHTTPFixture) runtimeLockID(t *testing.T) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, f.pool.QueryRow(t.Context(),
		`SELECT id FROM agent_runtime_locks WHERE agent_id=$1 AND lease_expires_at > now()`, f.record.AgentID).Scan(&id))
	return id
}

func TestCapturedDiscordGuildGuardsPromptAndRuntimeMessage(t *testing.T) {
	for _, operation := range []string{"prompt", "runtime"} {
		t.Run(operation, func(t *testing.T) {
			var sends atomic.Int32
			f := newCapturedHTTPFixtureWithDismiss(t, "discord", "question", nil, capturedHTTPFixtureOptions{
				handlerConfig: map[string]any{"guild_id": "500"}, prepareOnly: true,
				providerOverride: func(w http.ResponseWriter, r *http.Request) bool {
					if r.URL.Path == "/api/v10/channels/300" {
						writeJSON(w, http.StatusOK, map[string]any{"id": "300", "guild_id": "999", "type": 0})
						return true
					}
					if r.Method == http.MethodPost {
						sends.Add(1)
					}
					return false
				},
			})
			p := integration.InteractionPresenter{Store: f.project.Store, HTTPClient: f.client}
			var err error
			if operation == "prompt" {
				err = p.Present(t.Context(), f.app.ProjectID, f.record.AgentID, f.record.ID)
			} else {
				err = p.PostRuntimeMessage(t.Context(), f.app.ProjectID, f.record.AgentID, f.runtimeLockID(t), "status")
			}
			var providerErr *discord.APIError
			require.ErrorAs(t, err, &providerErr)
			require.Equal(t, discord.ScopeMismatch, providerErr.Code)
			require.Zero(t, sends.Load(), "fixed guild must be checked before any provider send")
			current, found, err := f.project.Store.Execution().
				GetAgentInteraction(t.Context(), f.app.ProjectID, f.record.AgentID, f.record.ID)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, executionstore.AgentInteractionStateOpen, current.State)
			require.Empty(t, current.PresentationReceipt)
		})
	}
}

func TestCapturedDiscordCallbackRequiresCapturedGuild(t *testing.T) {
	f := newCapturedHTTPFixtureWithDismiss(t, "discord", "permission", nil,
		capturedHTTPFixtureOptions{handlerConfig: map[string]any{"guild_id": "500"}})
	for _, guild := range []string{"999", ""} {
		for _, action := range []string{"c0", "t1"} {
			response := f.discordRequest(t, action, false, false, func(body map[string]any) { body["guild_id"] = guild })
			require.Equal(t, float64(4), response["type"], "wrong guild cannot resolve or open a modal")
			current, _, err := f.project.Store.Execution().
				GetAgentInteraction(t.Context(), f.app.ProjectID, f.record.AgentID, f.record.ID)
			require.NoError(t, err)
			require.Equal(t, executionstore.AgentInteractionStateOpen, current.State)
		}
	}
	require.Equal(t, float64(6), f.discordRequest(t, "c0", false, false)["type"])
}

func TestCapturedSlackRotatedTokenIdentityBeforeProviderIO(t *testing.T) {
	for _, operation := range []string{"prompt", "dismiss", "runtime"} {
		for _, identity := range []string{"same", "other_workspace", "other_bot", "non_bot"} {
			t.Run(operation+"/"+identity, func(t *testing.T) {
				var rotated atomic.Bool
				var sends atomic.Int32
				f := newCapturedHTTPFixtureWithDismiss(t, "slack", "question", nil, capturedHTTPFixtureOptions{
					prepareOnly: operation != "dismiss",
					providerOverride: func(w http.ResponseWriter, r *http.Request) bool {
						if r.URL.Path == "/api/auth.test" && rotated.Load() {
							assert.Equal(t, "Bearer xoxb-rotated", r.Header.Get("Authorization"))
							team, user, bot := "T123", "U_BOT", "B123"
							switch identity {
							case "other_workspace":
								team = "TOTHER"
							case "other_bot":
								user = "U_OTHER"
							case "non_bot":
								bot = ""
							}
							writeJSON(w, http.StatusOK, map[string]any{"ok": true, "team_id": team, "user_id": user, "bot_id": bot})
							return true
						}
						if rotated.Load() && (r.URL.Path == "/api/chat.postMessage" || r.URL.Path == "/api/chat.update") {
							sends.Add(1)
						}
						return false
					},
				})
				_, _, err := f.project.Store.Secrets().CreateSecretVersion(t.Context(), secretstore.CreateSecretVersionInput{
					OrgID: f.app.OrgID, SecretID: f.app.CredentialSecretID,
					Actor: httpUserPrincipal(f.project.AdminUserUUID),
					Material: secrets.SlackAppCredentialsMaterial{AccessToken: "xoxb-rotated", ClientID: "client",
						ClientSecret: "client-secret", SigningSecret: "signing-secret"},
				})
				require.NoError(t, err)
				rotated.Store(true)
				p := integration.InteractionPresenter{Store: f.project.Store, HTTPClient: f.client}
				switch operation {
				case "prompt":
					err = p.Present(t.Context(), f.app.ProjectID, f.record.AgentID, f.record.ID)
				case "dismiss":
					_, err = f.project.Store.Execution().CancelAgent(t.Context(), executionstore.CancelAgentInput{
						ProjectID: f.app.ProjectID, AgentID: f.record.AgentID,
					})
					require.NoError(t, err)
					closed, found, readErr := f.project.Store.Execution().
						GetAgentInteraction(t.Context(), f.app.ProjectID, f.record.AgentID, f.record.ID)
					require.NoError(t, readErr)
					require.True(t, found)
					err = p.Dismiss(t.Context(), closed)
				case "runtime":
					err = p.PostRuntimeMessage(t.Context(), f.app.ProjectID, f.record.AgentID, f.runtimeLockID(t), "status")
				}
				if identity == "same" {
					require.NoError(t, err)
					require.EqualValues(t, 1, sends.Load())
				} else {
					require.Error(t, err)
					require.Zero(t, sends.Load())
				}
			})
		}
	}
}
