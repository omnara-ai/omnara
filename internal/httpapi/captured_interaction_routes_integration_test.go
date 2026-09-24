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
	integrationruntime "github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type capturedHTTPFixture struct {
	handler        http.Handler
	project        publicHTTPProject
	pool           *pgxpool.Pool
	integration    integrationstore.ProjectIntegrationRecord
	callbackStatus int
	signingSecret  string
	record         executionstore.AgentInteractionRecord
	key            ed25519.PrivateKey
	client         *http.Client
	dismissed      chan struct{}
	prompts        <-chan map[string]any
}

type capturedHTTPFixtureOptions struct {
	slackDM          bool
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
	slackChannel, slackThread := "C123", "111.222"
	if opts.slackDM {
		slackChannel, slackThread = "D123", ""
	}
	dismissed := make(chan struct{}, 8)
	prompts := make(chan map[string]any, 8)
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
			var payload map[string]any
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload)) {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			assert.Equal(t, slackChannel, payload["channel"])
			if slackThread == "" {
				assert.Empty(t, payload["thread_ts"])
			} else {
				assert.Equal(t, slackThread, payload["thread_ts"])
			}
			prompts <- payload
			writeJSON(w, 200, map[string]any{"ok": true, "channel": slackChannel, "ts": "222.333"})
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
		case "/api/v10/channels/301":
			writeJSON(w, 200, map[string]any{"id": "301", "parent_id": "300", "guild_id": "500", "type": 11})
		case "/api/v10/channels/301/messages", "/api/v10/channels/301/messages/400":
			var payload map[string]any
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload)) {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			if r.Method == http.MethodPost {
				prompts <- payload
			}
			writeJSON(
				w,
				200,
				map[string]any{
					"id":         "400",
					"channel_id": "301",
					"guild_id":   "500",
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
	handler := newIntegrationServer(pool,
		WithSlackOAuth(SlackOAuthConfig{HTTPClient: client}), WithIntegrationHTTPClient(client))
	project := bootstrapPublicHTTPProject(t, handler, "captured-"+provider+"-"+kind)
	publicKey, key, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	material := secrets.Material(secrets.GenericMaterial{Value: "test-bot-token"})
	tenant, account, ref := "100", "200", "300:301"
	config := json.RawMessage(`{"public_key":"` + hex.EncodeToString(publicKey) + `"}`)
	identity := json.RawMessage(`{}`)
	if provider == "slack" {
		material = secrets.SlackAppCredentialsMaterial{
			AccessToken:   "xoxb-test",
			ClientID:      "client",
			ClientSecret:  "client-secret",
			SigningSecret: "signing-secret",
		}
		tenant, account, ref, config = "T123", "A123", slackChannel, json.RawMessage(`{}`)
		if slackThread != "" {
			ref += ":" + slackThread
		}
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
	integrationTypes := integrationdefinition.IntegrationTypesForProvider(provider)
	require.Len(t, integrationTypes, 1)
	integrationType := integrationdefinition.Type(integrationTypes[0])
	integration, err := project.Store.Integrations().CreateProjectIntegration(
		ctx,
		integrationstore.SaveProjectIntegrationInput{
			OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, Name: "support", IntegrationType: integrationType,
		},
	)
	require.NoError(t, err)
	integration, err = project.Store.Integrations().ConfigureProjectIntegration(
		ctx,
		integrationstore.ConfigureProjectIntegrationInput{
			OrgID:                 project.OrgUUID,
			ProjectID:             project.ProjectUUID,
			IntegrationID:         integration.ID,
			ExpectedSetupRevision: integration.SetupRevision,
			InstalledByUserID:     project.AdminUserUUID,
			Provider:              provider,
			ProviderTenantID:      tenant,
			ProviderAccountRef:    account,
			CredentialSecretID:    secret.ID,
			CredentialVersionID:   secret.CurrentVersionID,
			OAuthFlowID:           uuid.Must(uuid.NewV7()),
			ProviderConfig:        config,
			ProviderIdentity:      identity,
		},
	)
	require.NoError(t, err)
	source := map[string]any{
		"instruction":          "Help with the request.",
		"model":                map[string]any{"provider_config": "openai-prod", "name": "gpt-test"},
		"interaction_handlers": map[string]any{"support": map[string]any{}},
		"tools": map[string]any{
			toolcatalog.ToolNameListInteractionHandlers: map[string]any{},
			toolcatalog.ToolNameSetInteractionHandler:   map[string]any{},
		},
	}
	agentConfig := createPublicHTTPAgentConfig(
		t,
		handler,
		project,
		"captured",
		"json",
		projectIntegrationHTTPJSON(t, source),
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
		projectIntegrationHTTPJSON(
			t,
			map[string]any{"profile": profile["id"], "config": configID},
		),
		"launch",
		201,
		authHeaders(project.AdminToken),
	)
	agentID := testutil.RequireType[string](t, testutil.RequireType[map[string]any](t, launched["agent"])["id"])
	agentUUID := mustPublicHTTPID(t, publicid.KindAgent, agentID)
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	address := integrationstore.ConversationAddress{Kind: "thread", Ref: ref}
	if provider == "slack" && opts.slackDM {
		address.Kind = "dm"
	}
	require.NoError(t, integrationstore.LockIntegrationsTx(ctx, tx, project.ProjectUUID, nil, integration.ID))
	require.NoError(t, integrationstore.LockConversationTx(ctx, tx, project.ProjectUUID, integration.ID, address))
	_, err = tx.Exec(ctx, "SELECT id FROM agents WHERE id=$1 FOR UPDATE", agentUUID)
	require.NoError(t, err)
	require.NoError(t, project.Store.Integrations().AssignAgentIntegrationConversationTx(
		ctx, tx, project.ProjectUUID, agentUUID, integration.ID, address,
	))
	origin, err := project.Store.Integrations().
		EnsureConversationTargetTx(ctx, tx, integrationstore.EnsureConversationTargetInput{
			ProjectID: project.ProjectUUID, AgentID: agentUUID, IntegrationID: integration.ID,
			Address: address,
		})
	require.NoError(t, err)
	selection, err := project.Store.Execution().SelectInteractionDestinationForOriginTx(
		ctx, tx, project.ProjectUUID, agentUUID, origin.ID,
	)
	require.NoError(t, err)
	require.Equal(t, executionstore.InteractionSelection{
		HandlerKey: "support", IntegrationTargetID: origin.ID,
	}, selection)
	require.NoError(t, tx.Commit(ctx))
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		project.ProjectPath+"/agents/"+agentID+"/inputs",
		projectIntegrationHTTPJSON(t, map[string]any{
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
		model.ToolCall{
			ID: "call_select_handler", Name: toolcatalog.ToolNameSetInteractionHandler,
			Input: json.RawMessage(`{"handler":"support","args":{}}`),
		},
	)
	destination, err := record.CapturedDestination()
	require.NoError(t, err)
	require.Equal(t, &executionstore.InteractionDestination{
		IntegrationType: integrationType, HandlerKey: "support", IntegrationID: integration.ID,
		IntegrationTargetID: origin.ID, Address: address,
	}, destination)
	presenter := integrationruntime.InteractionPresenter{Store: project.Store, HTTPClient: client}
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
		handler: handler, project: project, pool: pool, integration: integration,
		record: record, key: key, client: client, dismissed: dismissed, prompts: prompts,
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
	channel := "301"
	if wrongSurface {
		channel = "302"
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
	body := projectIntegrationHTTPJSON(t, payload)
	timestamp := fmt.Sprint(time.Now().Unix())
	headers := map[string]string{"Content-Type": "application/json", "X-Signature-Timestamp": timestamp,
		"X-Signature-Ed25519": hex.EncodeToString(ed25519.Sign(f.key, []byte(timestamp+body)))}
	path := "/api/integrations/discord/" + f.integration.ProviderTenantID + "/interactions"
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
		Install:             f.integration,
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
	channel, thread, _ := strings.Cut(destination.Address.Ref, ":")
	if wrongSurface {
		channel = "C_OTHER"
	}
	payload["channel"] = map[string]any{"id": channel}
	payload["message"] = map[string]any{"ts": "222.333", "thread_ts": thread}
	for _, change := range changes {
		change(payload)
	}
	values.Set("payload", projectIntegrationHTTPJSON(t, payload))
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

func TestCapturedSlackSelectionWaitsForSubmit(t *testing.T) {
	t.Parallel()
	f := newCapturedHTTPFixture(t, "slack", "permission")
	prompt := <-f.prompts
	selection := func(payload map[string]any) {
		testutil.RequireType[map[string]any](t, payload["message"])["blocks"] = prompt["blocks"]
		payload["actions"] = []any{map[string]any{
			"type": "radio_buttons", "action_id": "omnara_answer",
			"selected_option": map[string]string{"value": "1"},
		}}
		payload["state"] = map[string]any{"values": map[string]any{
			"omnara_question_0": map[string]any{"omnara_answer": map[string]any{
				"type": "radio_buttons", "selected_option": map[string]string{"value": "1"},
			}},
		}}
	}
	for range 2 {
		require.Equal(t, "ignored", f.slackRequest(t, false, selection)["ok"])
	}
	capturedSiblingIntegration(t, f)
	forged := f
	forged.signingSecret, forged.callbackStatus = "sibling-signing-secret", http.StatusUnauthorized
	forged.slackRequest(t, false, selection)
	current, found, err := f.project.Store.Execution().
		GetAgentInteraction(t.Context(), f.integration.ProjectID, f.record.AgentID, f.record.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.AgentInteractionStateOpen, current.State)
	require.Equal(t, uuid.Nil, current.ResolvedByInputID)
	require.JSONEq(t, string(f.record.PresentationReceipt), string(current.PresentationReceipt))
	select {
	case <-f.dismissed:
		t.Fatal("selection dismissed an unanswered prompt")
	default:
	}
	response := f.slackRequest(t, false, func(payload map[string]any) {
		submit := payload["actions"]
		selection(payload)
		payload["actions"] = submit
	})
	require.Equal(t, "resolved", response["ok"])
	require.Equal(t, "Denied: run_command", response["text"])
	current, found, err = f.project.Store.Execution().
		GetAgentInteraction(t.Context(), f.integration.ProjectID, f.record.AgentID, f.record.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.AgentInteractionStateResolved, current.State)
	form, err := current.Form()
	require.NoError(t, err)
	resolution, err := interactionform.ParseResolution(form, current.Resolution)
	require.NoError(t, err)
	require.Equal(t, []interactionform.Answer{{OptionIndices: []int{toolpermission.DenyOptionIndex}}}, resolution.Answers)
	select {
	case <-f.dismissed:
	case <-time.After(3 * time.Second):
		t.Fatal("submitted prompt was not dismissed")
	}
}

func TestCapturedInteractionCallbacksResolveVerifiedSurface(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"slack", "discord"} {
		for _, choice := range []struct {
			kind, name, confirmation string
			index                    int
		}{
			{"question", "yes", "Answers recorded.", 0},
			{"permission", "allow", "Approved: run_command", toolpermission.AllowOptionIndex},
			{"permission", "deny", "Denied: run_command", toolpermission.DenyOptionIndex},
		} {
			t.Run(provider+"/"+choice.kind+"/"+choice.name, func(t *testing.T) {
				t.Parallel()
				f := newCapturedHTTPFixture(t, provider, choice.kind)
				if choice.kind == "permission" {
					assertCapturedPermissionPrompt(t, f)
				}
				action := fmt.Sprintf("c%d", choice.index)
				slackChoice := func(payload map[string]any) {
					payload["state"] = map[string]any{"values": map[string]any{
						"omnara_question_0": map[string]any{"omnara_answer": map[string]any{
							"type": "radio_buttons", "selected_option": map[string]string{"value": fmt.Sprint(choice.index)},
						}},
					}}
				}
				if provider == "slack" {
					require.Equal(t, "ignored", f.slackRequest(t, true, slackChoice)["ok"])
				} else {
					require.Equal(t, float64(4), f.discordRequest(t, action, false, true)["type"])
				}
				current, found, err := f.project.Store.Execution().
					GetAgentInteraction(t.Context(), f.project.ProjectUUID, f.record.AgentID, f.record.ID)
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, executionstore.AgentInteractionStateOpen, current.State)
				_, err = f.pool.Exec(
					t.Context(),
					"UPDATE agents SET integration_target_id=NULL, interaction_handler_key=NULL WHERE id=$1",
					f.record.AgentID,
				)
				require.NoError(t, err)
				if provider == "slack" {
					response := f.slackRequest(t, false, slackChoice)
					require.Equal(t, "resolved", response["ok"])
					require.Equal(t, choice.confirmation, response["text"])
				} else {
					require.Equal(t, float64(6), f.discordRequest(t, action, false, false)["type"])
				}
				current, found, err = f.project.Store.Execution().
					GetAgentInteraction(t.Context(), f.project.ProjectUUID, f.record.AgentID, f.record.ID)
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, executionstore.AgentInteractionStateResolved, current.State)
				require.NotEqual(t, uuid.Nil, current.ResolvedByInputID)
				require.Equal(t, f.record.Request, current.Request)
				require.JSONEq(t, string(f.record.Destination), string(current.Destination))
				require.JSONEq(t, string(f.record.PresentationReceipt), string(current.PresentationReceipt))
				form, err := current.Form()
				require.NoError(t, err)
				resolution, err := interactionform.ParseResolution(form, current.Resolution)
				require.NoError(t, err)
				require.Equal(t, []interactionform.Answer{{OptionIndices: []int{choice.index}}}, resolution.Answers)
				if provider == "slack" {
					require.Equal(t, "already_resolved", f.slackRequest(t, false, slackChoice)["ok"])
				} else {
					require.Equal(t, float64(4), f.discordRequest(t, action, false, false)["type"])
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

func assertCapturedPermissionPrompt(t *testing.T, f capturedHTTPFixture) {
	t.Helper()
	form, err := f.record.Form()
	require.NoError(t, err)
	require.True(t, form.Questions[0].Options[toolpermission.DenyOptionIndex].AllowsText)
	require.Len(t, f.prompts, 1)
	prompt := <-f.prompts
	if f.integration.Provider == "slack" {
		blocks := testutil.RequireType[[]any](t, prompt["blocks"])
		require.Len(t, blocks, 3, "heading, choices and Submit; no permission textbox")
		choices := testutil.RequireType[map[string]any](t, blocks[1])
		element := testutil.RequireType[map[string]any](t, choices["element"])
		require.Equal(t, "radio_buttons", element["type"])
		require.JSONEq(t, `[
			{"text":{"type":"plain_text","text":"Allow"},"value":"0"},
			{"text":{"type":"plain_text","text":"Deny"},"value":"1"}
		]`, projectIntegrationHTTPJSON(t, element["options"]))
		actions := testutil.RequireType[map[string]any](t, blocks[2])
		buttons := testutil.RequireType[[]any](t, actions["elements"])
		require.Len(t, buttons, 1)
		submit := testutil.RequireType[map[string]any](t, buttons[0])
		require.Equal(t, "button", submit["type"])
		require.Equal(t, "Submit", testutil.RequireType[map[string]any](t, submit["text"])["text"])
		return
	}
	var rows []discord.ActionRow
	require.NoError(t, json.Unmarshal([]byte(projectIntegrationHTTPJSON(t, prompt["components"])), &rows))
	require.Len(t, rows, 1)
	require.Len(t, rows[0].Components, 2)
	require.NotContains(t, prompt["content"], "optional text")
	for i, label := range []string{"Allow", "Deny"} {
		button := rows[0].Components[i]
		require.Equal(t, label, button.Label)
		id, err := discord.DecodeCustomID(button.CustomID)
		require.NoError(t, err)
		require.Equal(t, fmt.Sprintf("c%d", i), id.Action)
		require.Equal(t, testPublicID(t, publicid.KindAgentInteraction, f.record.ID), id.InteractionID)
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
		for _, revoked := range []string{"integration", "assignment", "target"} {
			t.Run(provider+"/"+revoked, func(t *testing.T) {
				f := newCapturedHTTPFixture(t, provider, "question")
				switch revoked {
				case "integration":
					_, err := f.project.Store.Integrations().DisconnectProjectIntegration(
						t.Context(), integrationstore.DisconnectProjectIntegrationInput{
							ProjectID: f.project.ProjectUUID, IntegrationID: f.integration.ID,
							ExpectedSetupRevision: &f.integration.SetupRevision,
						},
					)
					require.NoError(t, err)
					f.callbackStatus = http.StatusForbidden
				case "assignment":
					changed, err := f.pool.Exec(t.Context(), `DELETE FROM integration_states
					WHERE project_id=$1 AND integration_id=$2 AND kind='agent_conversation' AND key=$3`,
						f.project.ProjectUUID, f.integration.ID, f.record.AgentID.String())
					require.NoError(t, err)
					require.EqualValues(t, 1, changed.RowsAffected())
					var targets int
					require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM integration_targets
					WHERE project_id=$1 AND agent_id=$2 AND integration_id=$3 AND deleted_at IS NULL`,
						f.project.ProjectUUID, f.record.AgentID, f.integration.ID).Scan(&targets))
					require.Equal(t, 1, targets, "target history survives removal of the agent assignment")
				case "target":
					destination, err := f.record.CapturedDestination()
					require.NoError(t, err)
					changed, err := f.pool.Exec(t.Context(),
						`UPDATE integration_targets SET deleted_at=now() WHERE id=$1`,
						destination.IntegrationTargetID)
					require.NoError(t, err)
					require.EqualValues(t, 1, changed.RowsAffected())
				}
				selected, err := f.project.Store.Execution().GetSelectedInteractionDestination(
					t.Context(), f.project.ProjectUUID, f.record.AgentID)
				require.NoError(t, err)
				require.Nil(t, selected)
				if provider == "slack" {
					response := f.slackRequest(t, false)
					if revoked != "integration" {
						require.Equal(t, "ignored", response["ok"])
					}
				} else {
					response := f.discordRequest(t, "c0", false, false)
					if revoked != "integration" {
						require.Equal(t, float64(4), response["type"])
					}
				}
				current, found, err := f.project.Store.Execution().GetAgentInteraction(
					t.Context(), f.project.ProjectUUID, f.record.AgentID, f.record.ID)
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, executionstore.AgentInteractionStateOpen, current.State)
				require.Equal(t, uuid.Nil, current.ResolvedByInputID)
				require.JSONEq(t, string(f.record.Destination), string(current.Destination))
				require.JSONEq(t, string(f.record.PresentationReceipt), string(current.PresentationReceipt))
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
}

func TestSlackActionsResolveQuestionAsIntegrationActor(t *testing.T) {
	t.Parallel()
	f := newCapturedHTTPFixture(t, "slack", "question")
	_, err := f.pool.Exec(t.Context(), `
INSERT INTO actors(project_id, provider, provider_tenant_id, provider_user_id, display_name, created_at, updated_at)
VALUES ($1, 'integration', $2, 'U_OTHER', 'Grace Hopper', now(), now())
ON CONFLICT (project_id, provider, provider_tenant_id, provider_user_id)
DO UPDATE SET display_name = excluded.display_name, updated_at = excluded.updated_at`,
		f.project.ProjectUUID, testPublicID(t, publicid.KindProjectIntegration, f.integration.ID))
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
	require.Equal(t, executionstore.ActorProviderIntegration, actor.Provider)
	require.Equal(t, "U_OTHER", actor.ProviderUserID)
	names, err := f.project.Store.Execution().ListActorDisplayNames(
		t.Context(),
		f.project.ProjectUUID,
		executionstore.ActorProviderIntegration,
		testPublicID(t, publicid.KindProjectIntegration, f.integration.ID),
		[]string{"U_OTHER"},
	)
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

func TestCapturedSlackCallbackRequiresAssignedThread(t *testing.T) {
	t.Parallel()
	f := newCapturedHTTPFixture(t, "slack", "question")
	for _, thread := range []string{"", "222.333", "999.888"} {
		wrong := f.slackRequest(t, false, func(payload map[string]any) {
			payload["message"] = map[string]any{"ts": "222.333", "thread_ts": thread}
		})
		require.Equal(t, "ignored", wrong["ok"])
		current, found, err := f.project.Store.Execution().
			GetAgentInteraction(t.Context(), f.integration.ProjectID, f.record.AgentID, f.record.ID)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, executionstore.AgentInteractionStateOpen, current.State)
	}
	require.Equal(t, "resolved", f.slackRequest(t, false)["ok"])
}

func TestCapturedSlackDMRootPromptRemainsValidAfterThreadReply(t *testing.T) {
	t.Parallel()
	f := newCapturedHTTPFixtureWithDismiss(t, "slack", "question", nil, capturedHTTPFixtureOptions{slackDM: true})
	wrong := f.slackRequest(t, false, func(payload map[string]any) {
		payload["message"] = map[string]any{"ts": "222.333", "thread_ts": "111.222"}
	})
	require.Equal(t, "ignored", wrong["ok"])
	root := f.slackRequest(t, false, func(payload map[string]any) {
		payload["message"] = map[string]any{"ts": "222.333", "thread_ts": "222.333"}
	})
	require.Equal(t, "resolved", root["ok"])
}

func TestSlackActionsResolvePermissionAsIntegrationActor(t *testing.T) {
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
	require.Equal(t, executionstore.ActorProviderIntegration, actor.Provider)
	require.Equal(t, "U_OTHER", actor.ProviderUserID)
	select {
	case update := <-updates:
		require.Equal(t, "C123", update["channel"])
		require.Equal(t, "222.333", update["ts"])
		require.Empty(t, update["blocks"])
		require.Equal(t, "Approved: run_command", update["text"])
	case <-time.After(3 * time.Second):
		t.Fatal("confirmed prompt was not dismissed")
	}
}

func TestCapturedDiscordEndpointVerifiesPingSignatureAndApplication(t *testing.T) {
	f := newCapturedHTTPFixture(t, "discord", "question")
	path := "/api/integrations/discord/" + f.integration.ProviderTenantID + "/interactions"
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
					"integration_type":      string(f.integration.IntegrationType),
					"handler_key":           "support",
					"integration_id":        testPublicID(t, publicid.KindProjectIntegration, f.integration.ID),
					"integration_target_id": testPublicID(t, publicid.KindIntegrationTarget, destination.IntegrationTargetID),
					"address":               map[string]any{"kind": destination.Address.Kind, "ref": destination.Address.Ref},
				}, captured)
				var receipt map[string]any
				require.NoError(t, json.Unmarshal(f.record.PresentationReceipt, &receipt))
				require.Equal(t, receipt, listed["presentation_receipt"])
				require.NotContains(t, projectIntegrationHTTPJSON(t, listed), f.integration.ID.String())
				require.NotContains(t, projectIntegrationHTTPJSON(t, listed), destination.IntegrationTargetID.String())
				headers := apiHeaders
				if surface == "dashboard" {
					headers = browserHeaders
				}
				resolved := requestJSONWithHeaders(t, f.handler, http.MethodPost, path+"/"+
					testPublicID(t, publicid.KindAgentInteraction, f.record.ID)+"/resolve",
					`{"answers":[{"option_indices":[1],"text":"Please wait"}]}`, "", http.StatusOK, headers)
				require.Equal(t, "resolved", resolved["state"])
				require.JSONEq(t, `{"answers":[{"option_indices":[1],"text":"Please wait"}]}`,
					projectIntegrationHTTPJSON(t, resolved["resolution"]))
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

func capturedSiblingIntegration(
	t *testing.T,
	f capturedHTTPFixture,
) (integrationstore.ProjectIntegrationRecord, ed25519.PrivateKey) {
	t.Helper()
	publicKey, key, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	config := json.RawMessage(`{"public_key":"` + hex.EncodeToString(publicKey) + `"}`)
	material := secrets.Material(secrets.GenericMaterial{Value: "sibling-token"})
	if f.integration.Provider == "slack" {
		material = secrets.SlackAppCredentialsMaterial{AccessToken: "xoxb-sibling", ClientID: "client",
			ClientSecret: "client-secret", SigningSecret: "sibling-signing-secret"}
		config = json.RawMessage(`{}`)
	}
	secret, _, err := f.project.Store.Secrets().CreateSecret(t.Context(), secretstore.CreateSecretInput{
		OrgID: f.integration.OrgID, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: f.integration.ProjectID,
		Name: "sibling", Material: material, Actor: httpUserPrincipal(f.project.AdminUserUUID),
	})
	require.NoError(t, err)
	integration, err := f.project.Store.Integrations().CreateProjectIntegration(
		t.Context(),
		integrationstore.SaveProjectIntegrationInput{
			OrgID:           f.integration.OrgID,
			ProjectID:       f.integration.ProjectID,
			Name:            "sibling",
			IntegrationType: f.integration.IntegrationType,
		},
	)
	require.NoError(t, err)
	integration, err = f.project.Store.Integrations().ConfigureProjectIntegration(
		t.Context(),
		integrationstore.ConfigureProjectIntegrationInput{
			OrgID:                 f.integration.OrgID,
			ProjectID:             f.integration.ProjectID,
			IntegrationID:         integration.ID,
			ExpectedSetupRevision: integration.SetupRevision,
			InstalledByUserID:     f.project.AdminUserUUID,
			Provider:              f.integration.Provider,
			ProviderTenantID:      f.integration.ProviderTenantID,
			ProviderAccountRef:    f.integration.ProviderAccountRef,
			CredentialSecretID:    secret.ID,
			CredentialVersionID:   secret.CurrentVersionID,
			OAuthFlowID:           uuid.Must(uuid.NewV7()),
			ProviderConfig:        config,
			ProviderIdentity:      f.integration.ProviderIdentity,
		},
	)
	require.NoError(t, err)
	return integration, key
}

func TestCapturedCallbackUsesOwnerCredentialsWithSharedBot(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"slack", "discord"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			f := newCapturedHTTPFixture(t, provider, "question")
			sibling, key := capturedSiblingIntegration(t, f)
			forged := f
			forged.key, forged.signingSecret, forged.callbackStatus = key, "sibling-signing-secret", http.StatusUnauthorized
			if provider == "slack" {
				forged.slackRequest(t, false)
			} else {
				forged.discordRequest(t, "c0", false, false)
			}
			current, found, err := f.project.Store.Execution().
				GetAgentInteraction(t.Context(), f.integration.ProjectID, f.record.AgentID, f.record.ID)
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
				`SELECT count(*) FROM integration_targets WHERE integration_id=$1`, sibling.ID).Scan(&targets))
			require.Zero(t, targets)
		})
	}
}

func TestCapturedDiscordPingAcceptsAnyActiveSharedIntegration(t *testing.T) {
	t.Parallel()
	f := newCapturedHTTPFixture(t, "discord", "question")
	_, key := capturedSiblingIntegration(t, f)
	_, err := f.project.Store.Integrations().DisconnectProjectIntegration(
		t.Context(),
		integrationstore.DisconnectProjectIntegrationInput{
			ProjectID: f.integration.ProjectID, IntegrationID: f.integration.ID,
		},
	)
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

func TestCapturedDiscordThreadGuardsPromptAndRuntimeMessage(t *testing.T) {
	for _, operation := range []string{"prompt", "runtime"} {
		for _, resource := range []struct {
			name, parent, guild string
			kind                int
		}{
			{"wrong_parent", "999", "500", 11},
			{"not_a_thread", "300", "500", 0},
			{"missing_guild", "300", "", 11},
		} {
			t.Run(operation+"/"+resource.name, func(t *testing.T) {
				var sends atomic.Int32
				f := newCapturedHTTPFixtureWithDismiss(t, "discord", "question", nil, capturedHTTPFixtureOptions{
					prepareOnly: true,
					providerOverride: func(w http.ResponseWriter, r *http.Request) bool {
						if r.URL.Path == "/api/v10/channels/301" {
							writeJSON(w, http.StatusOK, map[string]any{
								"id": "301", "parent_id": resource.parent,
								"guild_id": resource.guild, "type": resource.kind,
							})
							return true
						}
						if r.Method == http.MethodPost {
							sends.Add(1)
						}
						return false
					},
				})
				p := integrationruntime.InteractionPresenter{Store: f.project.Store, HTTPClient: f.client}
				var err error
				if operation == "prompt" {
					err = p.Present(t.Context(), f.integration.ProjectID, f.record.AgentID, f.record.ID)
				} else {
					err = p.PostRuntimeMessage(t.Context(), f.integration.ProjectID,
						f.record.AgentID, f.runtimeLockID(t), "status")
				}
				var providerErr *discord.APIError
				require.ErrorAs(t, err, &providerErr)
				require.Equal(t, discord.ScopeMismatch, providerErr.Code)
				require.Zero(t, sends.Load(), "the assigned thread resource must be verified before any provider send")
				current, found, err := f.project.Store.Execution().
					GetAgentInteraction(t.Context(), f.integration.ProjectID, f.record.AgentID, f.record.ID)
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, executionstore.AgentInteractionStateOpen, current.State)
				require.Empty(t, current.PresentationReceipt)
			})
		}
	}
}

func TestCapturedDiscordPresentationRejectsInvalidReceipt(t *testing.T) {
	for _, field := range []string{"guild_id", "channel_id", "author", "nonce"} {
		t.Run(field, func(t *testing.T) {
			var sends atomic.Int32
			f := newCapturedHTTPFixtureWithDismiss(t, "discord", "question", nil, capturedHTTPFixtureOptions{
				prepareOnly: true,
				providerOverride: func(w http.ResponseWriter, r *http.Request) bool {
					if r.Method != http.MethodPost || r.URL.Path != "/api/v10/channels/301/messages" {
						return false
					}
					var payload map[string]any
					if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload)) {
						w.WriteHeader(http.StatusBadRequest)
						return true
					}
					sends.Add(1)
					message := map[string]any{
						"id": "400", "channel_id": "301", "guild_id": "500",
						"author": map[string]any{"id": "200"}, "nonce": payload["nonce"],
					}
					message[field] = "999"
					if field == "author" {
						message[field] = map[string]any{"id": "201"}
					}
					writeJSON(w, http.StatusOK, message)
					return true
				},
			})
			p := integrationruntime.InteractionPresenter{Store: f.project.Store, HTTPClient: f.client}
			err := p.Present(t.Context(), f.integration.ProjectID, f.record.AgentID, f.record.ID)
			var providerErr *discord.APIError
			require.ErrorAs(t, err, &providerErr)
			require.Equal(t, discord.DeliveryUnknown, providerErr.Code)
			require.EqualValues(t, 1, sends.Load(), "an unverified send must not be repeated")
			current, found, err := f.project.Store.Execution().
				GetAgentInteraction(t.Context(), f.integration.ProjectID, f.record.AgentID, f.record.ID)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, executionstore.AgentInteractionStateOpen, current.State)
			require.Empty(t, current.PresentationReceipt)
			require.Equal(t, float64(4), f.discordRequest(t, "c0", false, false)["type"])
		})
	}
}

func TestCapturedInteractionFailedSendLeavesDashboardAvailable(t *testing.T) {
	for _, provider := range []string{"slack", "discord"} {
		t.Run(provider, func(t *testing.T) {
			var sends atomic.Int32
			f := newCapturedHTTPFixtureWithDismiss(t, provider, "question", nil, capturedHTTPFixtureOptions{
				prepareOnly: true,
				providerOverride: func(w http.ResponseWriter, r *http.Request) bool {
					switch r.URL.Path {
					case "/api/chat.postMessage":
						sends.Add(1)
						writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "channel_not_found"})
						return true
					case "/api/v10/channels/301/messages":
						sends.Add(1)
						writeJSON(w, http.StatusForbidden, map[string]any{
							"code": 50013, "message": "Missing Permissions",
						})
						return true
					default:
						return false
					}
				},
			})
			p := integrationruntime.InteractionPresenter{Store: f.project.Store, HTTPClient: f.client}
			require.Error(t, p.Present(t.Context(), f.project.ProjectUUID, f.record.AgentID, f.record.ID))
			require.EqualValues(t, 1, sends.Load())
			if provider == "slack" {
				require.Equal(t, "ignored", f.slackRequest(t, false)["ok"])
			} else {
				require.Equal(t, float64(4), f.discordRequest(t, "c0", false, false)["type"])
			}
			current, found, err := f.project.Store.Execution().GetAgentInteraction(
				t.Context(), f.project.ProjectUUID, f.record.AgentID, f.record.ID)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, executionstore.AgentInteractionStateOpen, current.State)
			require.Empty(t, current.PresentationReceipt)
			require.Equal(t, uuid.Nil, current.ResolvedByInputID)
			require.JSONEq(t, string(f.record.Destination), string(current.Destination))
			path := f.project.ProjectPath + "/agents/" + testPublicID(t, publicid.KindAgent, f.record.AgentID) +
				"/interactions/" + testPublicID(t, publicid.KindAgentInteraction, f.record.ID) + "/resolve"
			response := requestJSONWithHeaders(t, f.handler, http.MethodPost, path,
				`{"answers":[{"option_indices":[0]}]}`, "", http.StatusOK, f.project.adminBrowserAuthHeaders())
			require.Equal(t, "resolved", response["state"])
			require.EqualValues(t, 1, sends.Load(), "dashboard resolution must not retry a failed presentation")
		})
	}
}

func TestCapturedDiscordCallbackRequiresReceiptAndBot(t *testing.T) {
	f := newCapturedHTTPFixture(t, "discord", "permission")
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"missing_message", func(body map[string]any) { delete(body, "message") }},
		{"wrong_message", func(body map[string]any) {
			testutil.RequireType[map[string]any](t, body["message"])["id"] = "401"
		}},
		{"wrong_message_channel", func(body map[string]any) {
			testutil.RequireType[map[string]any](t, body["message"])["channel_id"] = "300"
		}},
		{"wrong_callback_channel", func(body map[string]any) { body["channel_id"] = "300" }},
		{"wrong_bot", func(body map[string]any) {
			testutil.RequireType[map[string]any](t, body["message"])["author"] = map[string]any{"id": "201"}
		}},
	} {
		for _, action := range []struct {
			name, value string
			modal       bool
		}{{"choice", "c0", false}, {"open_modal", "t1", false}, {"submit_modal", "t1", true}} {
			t.Run(test.name+"/"+action.name, func(t *testing.T) {
				response := f.discordRequest(t, action.value, action.modal, false, test.change)
				require.Equal(t, float64(4), response["type"],
					"an invalid receipt or bot cannot resolve or open a modal")
				current, found, err := f.project.Store.Execution().
					GetAgentInteraction(t.Context(), f.integration.ProjectID, f.record.AgentID, f.record.ID)
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, executionstore.AgentInteractionStateOpen, current.State)
				require.Equal(t, uuid.Nil, current.ResolvedByInputID)
			})
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
					OrgID: f.integration.OrgID, SecretID: f.integration.CredentialSecretID,
					Actor: httpUserPrincipal(f.project.AdminUserUUID),
					Material: secrets.SlackAppCredentialsMaterial{AccessToken: "xoxb-rotated", ClientID: "client",
						ClientSecret: "client-secret", SigningSecret: "signing-secret"},
				})
				require.NoError(t, err)
				rotated.Store(true)
				p := integrationruntime.InteractionPresenter{Store: f.project.Store, HTTPClient: f.client}
				switch operation {
				case "prompt":
					err = p.Present(t.Context(), f.integration.ProjectID, f.record.AgentID, f.record.ID)
				case "dismiss":
					_, err = f.project.Store.Execution().CancelAgent(t.Context(), executionstore.CancelAgentInput{
						ProjectID: f.integration.ProjectID, AgentID: f.record.AgentID,
					})
					require.NoError(t, err)
					closed, found, readErr := f.project.Store.Execution().
						GetAgentInteraction(t.Context(), f.integration.ProjectID, f.record.AgentID, f.record.ID)
					require.NoError(t, readErr)
					require.True(t, found)
					err = p.Dismiss(t.Context(), closed)
				case "runtime":
					err = p.PostRuntimeMessage(t.Context(), f.integration.ProjectID, f.record.AgentID, f.runtimeLockID(t), "status")
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

func TestCapturedDiscordRuntimeMessageWithoutInteractionKey(t *testing.T) {
	var requests, sends atomic.Int32
	f := newCapturedHTTPFixtureWithDismiss(t, "discord", "question", nil, capturedHTTPFixtureOptions{
		prepareOnly: true,
		providerOverride: func(_ http.ResponseWriter, r *http.Request) bool {
			requests.Add(1)
			if r.Method == http.MethodPost {
				sends.Add(1)
			}
			return false
		},
	})
	_, err := f.pool.Exec(
		t.Context(),
		`UPDATE project_integrations SET provider_config='{}' WHERE id=$1`,
		f.integration.ID,
	)
	require.NoError(t, err)
	p := integrationruntime.InteractionPresenter{Store: f.project.Store, HTTPClient: f.client}
	require.ErrorIs(
		t,
		p.Present(t.Context(), f.integration.ProjectID, f.record.AgentID, f.record.ID),
		storeerr.ErrUnauthorized,
	)
	require.Zero(t, requests.Load(), "a missing callback key must reject prompts before any provider request")
	require.NoError(t, p.PostRuntimeMessage(t.Context(), f.integration.ProjectID, f.record.AgentID,
		f.runtimeLockID(t), integrationruntime.AgentRequestFailureMessage))
	require.EqualValues(t, 1, sends.Load(), "plain text needs bot authority, not a callback verification key")
	payload := <-f.prompts
	require.Equal(t, integrationruntime.AgentRequestFailureMessage, payload["content"])
	require.Empty(t, payload["components"])
}

func TestCapturedDiscordDismissWithoutInteractionKey(t *testing.T) {
	var dismissals atomic.Int32
	f := newCapturedHTTPFixtureWithDismiss(t, "discord", "question", nil, capturedHTTPFixtureOptions{
		providerOverride: func(w http.ResponseWriter, r *http.Request) bool {
			if r.Method != http.MethodPatch {
				return false
			}
			assert.Equal(t, "/api/v10/channels/301/messages/400", r.URL.Path)
			var payload map[string]any
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload)) {
				w.WriteHeader(http.StatusBadRequest)
				return true
			}
			assert.Equal(t, "This interaction is closed.", payload["content"])
			assert.Equal(t, []any{}, payload["components"], "dismissal must clear the original buttons")
			dismissals.Add(1)
			writeJSON(w, http.StatusOK, map[string]any{
				"id": "400", "channel_id": "301", "author": map[string]any{"id": "200"},
			})
			return true
		},
	})
	_, err := f.pool.Exec(
		t.Context(),
		`UPDATE project_integrations SET provider_config='{}' WHERE id=$1`,
		f.integration.ID,
	)
	require.NoError(t, err)
	_, err = f.project.Store.Execution().CancelAgent(t.Context(), executionstore.CancelAgentInput{
		ProjectID: f.integration.ProjectID, AgentID: f.record.AgentID,
	})
	require.NoError(t, err)
	closed, found, err := f.project.Store.Execution().
		GetAgentInteraction(t.Context(), f.integration.ProjectID, f.record.AgentID, f.record.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.AgentInteractionStateCanceled, closed.State)
	p := integrationruntime.InteractionPresenter{Store: f.project.Store, HTTPClient: f.client}
	require.NoError(t, p.Dismiss(t.Context(), closed))
	require.EqualValues(t, 1, dismissals.Load())
}
