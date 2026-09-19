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
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type profileChoiceHTTPFixture struct {
	handler    http.Handler
	project    publicHTTPProject
	pool       *pgxpool.Pool
	connection integrationstore.IntegrationConnectionRecord
	app        integrationstore.ProjectAppRecord
	options    []integrationstore.AppProfileChoiceOption
	key        ed25519.PrivateKey
	updates    chan map[string]any
}

// Start with profiles and a verified source receipt, never an agent or a fake
// permission interaction. The callback must only decide a new inbox receipt.
func newProfileChoiceHTTPFixture(t *testing.T, provider string) profileChoiceHTTPFixture {
	t.Helper()
	updates := make(chan map[string]any, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !assert.Equal(t, "/api/chat.update", r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": body["channel"], "ts": body["ts"]})
		updates <- body
	}))
	t.Cleanup(server.Close)
	pool := openIntegrationDB(t, t.Context())
	handler := newIntegrationServer(pool, WithSlackOAuth(SlackOAuthConfig{
		HTTPClient: server.Client(), APIURL: server.URL + "/api",
	}))
	project := bootstrapPublicHTTPProject(t, handler, "profile-choice-"+provider)
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	material := secrets.Material(secrets.GenericMaterial{Value: "test-bot-token"})
	tenant, account, scopeKind, scopeRef := "100", "200", "guild", "500"
	config := json.RawMessage(projectAppHTTPJSON(t, map[string]string{"public_key": hex.EncodeToString(publicKey)}))
	identity := json.RawMessage(`{}`)
	if provider == appdefinition.ProviderSlack {
		material = secrets.SlackAppCredentialsMaterial{AccessToken: "xoxb-test", ClientID: "client",
			ClientSecret: "client-secret", SigningSecret: "signing-secret"}
		tenant, account, scopeKind, scopeRef = "T123", "A123", "workspace", "T123"
		config, identity = json.RawMessage(`{}`), json.RawMessage(`{"bot_user_id":"U_BOT"}`)
	}
	secret, _, err := project.Store.Secrets().CreateSecret(t.Context(), secretstore.CreateSecretInput{
		OrgID: project.OrgUUID, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: project.ProjectUUID,
		Name: "bot", Material: material, Actor: httpUserPrincipal(project.AdminUserUUID),
	})
	require.NoError(t, err)
	connection, err := project.Store.Integrations().CreateIntegrationConnection(t.Context(),
		integrationstore.SaveIntegrationConnectionInput{
			OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, InstalledByUserID: project.AdminUserUUID,
			Provider: provider, State: integrationstore.IntegrationConnectionStateActive,
			ProviderTenantID: tenant, ProviderAccountRef: account, CredentialSecretID: secret.ID,
			ProviderConfig: config, ProviderIdentity: identity,
		})
	require.NoError(t, err)
	base := createPublicHTTPAgentConfig(t, handler, project, "profile-choice", "json",
		`{"instruction":"Help with the original request","model":{"provider_config":"openai-prod","name":"gpt-test"}}`,
		project.AdminToken, http.StatusCreated)
	configID := mustPublicHTTPID(t, publicid.KindAgentConfig, testutil.RequireType[string](t, base["id"]))
	f := profileChoiceHTTPFixture{
		handler: handler, project: project, pool: pool, connection: connection, key: privateKey, updates: updates,
	}
	var slots []integrationstore.AppLaunchSlot
	for _, name := range []string{"Support", "Reviewer"} {
		profile, err := project.Store.Execution().CreateAgentProfile(t.Context(), executionstore.CreateAgentProfileInput{
			OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, Name: name, CurrentConfigID: configID,
		})
		require.NoError(t, err)
		key := strings.ToLower(name)
		slots = append(slots, integrationstore.AppLaunchSlot{Key: key, AgentProfileID: &profile.ID})
		f.options = append(f.options, integrationstore.AppProfileChoiceOption{Key: key, ProfileID: profile.ID, Name: name})
	}
	f.app, err = project.Store.Integrations().CreateProjectApp(t.Context(), integrationstore.SaveProjectAppInput{
		OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, Name: "Support", DefinitionID: "omnara." + provider,
		Enabled: true, Settings: integrationstore.ProjectAppSettings{
			Resource: agentconfig.AgentConfigAppResourceSource{
				Connection: testPublicID(t, publicid.KindIntegrationConnection, connection.ID),
			},
			Launcher: &integrationstore.AppLauncher{Trigger: "mention", ScopeKind: scopeKind, ScopeRef: scopeRef, Slots: slots},
		},
	})
	require.NoError(t, err)
	return f
}

func (f profileChoiceHTTPFixture) menu(t *testing.T, other bool) integrationstore.AppProfileChoiceRecord {
	t.Helper()
	channel, message, thread, originalActor := "300", "400", "300", "701"
	if other {
		message, thread = "401", "301"
	}
	scope := appdefinition.Scope{Discord: &appdefinition.DiscordScope{GuildID: "500", ChannelID: "299", ThreadID: thread}}
	if f.connection.Provider == appdefinition.ProviderSlack {
		channel, message, thread, originalActor = "C123", "222.333", "111.222", "U_ORIGINAL"
		if other {
			message, thread = "222.444", "111.444"
		}
		scope = appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: channel, ThreadTS: thread}}
	}
	source := integration.AppEvent{
		Event:       appdefinition.Event{Kind: "message", Mentioned: true, Scope: scope},
		SemanticKey: "source:" + message, ContentBlocks: json.RawMessage(`[{"type":"text","text":"original request"}]`),
		Actor: executionstore.ActorParams{Provider: f.connection.Provider,
			ProviderTenantID: f.connection.ProviderTenantID, ProviderUserID: originalActor},
		DeliveryMode: executionstore.DeliveryModeSteering, CancelOpenInteractions: true,
	}
	kind, ref, err := scope.Conversation()
	require.NoError(t, err)
	// Raw provider bytes remain distinct from the normalized and selected event.
	payload := []byte("  {\"text\":\"original request\"}\n")
	store := f.project.Store.Integrations()
	accepted, created, err := store.AcceptIntegrationReceipt(t.Context(), integrationstore.VerifiedIntegrationReceipt{
		ProjectID: f.project.ProjectUUID, ConnectionID: f.connection.ID, ReceiptKey: source.SemanticKey, Payload: payload,
	})
	require.NoError(t, err)
	require.True(t, created)
	require.Nil(t, accepted.Events)
	claimed, found, err := store.ClaimIntegrationInbox(t.Context(), integrationstore.ClaimIntegrationInboxInput{
		ProjectID: f.project.ProjectUUID, ConnectionID: f.connection.ID,
		LeaseDuration: integrationstore.IntegrationInboxMaxLease,
	})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, accepted.ID, claimed.ID)
	choice, created, err := store.EnsureAppProfileChoice(t.Context(), claimed.Lease(),
		integrationstore.EnsureAppProfileChoiceInput{
			AppID: f.app.ID, Address: integrationstore.ConversationAddress{Kind: kind, Ref: ref},
			SourceKey: source.SemanticKey, Event: json.RawMessage(projectAppHTTPJSON(t, source)),
			Payload: payload, Options: f.options,
		})
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, store.RecordAppProfileChoiceMessage(t.Context(), f.project.ProjectUUID, f.connection.ID,
		choice.ID, channel, message))
	return f.readChoice(t, choice.ID)
}

func (f profileChoiceHTTPFixture) readChoice(t *testing.T, id uuid.UUID) integrationstore.AppProfileChoiceRecord {
	t.Helper()
	choice, err := f.project.Store.Integrations().GetAppProfileChoice(
		t.Context(), f.project.ProjectUUID, f.connection.ID, id)
	require.NoError(t, err)
	return choice
}

func (f profileChoiceHTTPFixture) assertNoAgent(t *testing.T) {
	t.Helper()
	var agents, inputs, interactions int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM agents WHERE project_id=$1),
		(SELECT count(*) FROM agent_inputs WHERE project_id=$1),
		(SELECT count(*) FROM agent_interactions i JOIN agents a ON a.id=i.agent_id WHERE a.project_id=$1)`,
		f.project.ProjectUUID).Scan(&agents, &inputs, &interactions))
	require.Zero(t, agents, "selection must defer agent creation to inbox admission")
	require.Zero(t, inputs, "selection must not send the selector's click as agent input")
	require.Zero(t, interactions, "menus must not allocate fake permission interactions")
}

func (f profileChoiceHTTPFixture) assertReceiptCount(t *testing.T, choiceID uuid.UUID, want int) {
	t.Helper()
	var count int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM integration_inbox
		WHERE project_id=$1 AND connection_id=$2 AND receipt_key=$3`,
		f.project.ProjectUUID, f.connection.ID, "choice:"+choiceID.String()).Scan(&count))
	require.Equal(t, want, count)
}

func (f profileChoiceHTTPFixture) callback(
	t *testing.T, menu integrationstore.AppProfileChoiceRecord, choiceID uuid.UUID, key, actor string,
	badSignature bool, change func(map[string]any),
) *httptest.ResponseRecorder {
	t.Helper()
	id := testPublicID(t, publicid.KindAppProfileChoice, choiceID)
	path := "/api/integrations/discord/" +
		testPublicID(t, publicid.KindIntegrationConnection, f.connection.ID) + "/interactions"
	payload := map[string]any{
		"id": "600", "type": 3, "application_id": "100", "guild_id": "500", "channel_id": menu.MessageChannelID,
		"member": map[string]any{"user": map[string]any{"id": actor, "username": "selector"}},
		"message": map[string]any{"id": menu.MessageID, "channel_id": menu.MessageChannelID,
			"author": map[string]any{"id": "200"}},
		"data": map[string]any{"custom_id": discord.ProfileChoiceCustomIDPrefix + id,
			"component_type": 3, "values": []string{key}},
	}
	if f.connection.Provider == appdefinition.ProviderSlack {
		path = integrationActionsPath
		payload = map[string]any{
			"type": "block_actions", "api_app_id": "A123", "team": map[string]string{"id": "T123"},
			"user":    map[string]string{"id": actor, "team_id": "T123"},
			"channel": map[string]string{"id": menu.MessageChannelID},
			"message": map[string]string{"ts": menu.MessageID},
			"actions": []any{map[string]any{"type": "static_select", "action_id": slack.ProfileChoiceActionPrefix + id,
				"selected_option": map[string]string{"value": key}}},
		}
	}
	if change != nil {
		change(payload)
	}
	body := projectAppHTTPJSON(t, payload)
	timestamp := fmt.Sprint(time.Now().Unix())
	headers := map[string]string{"Content-Type": "application/json", "X-Signature-Timestamp": timestamp,
		"X-Signature-Ed25519": hex.EncodeToString(ed25519.Sign(f.key, []byte(timestamp+body)))}
	if f.connection.Provider == appdefinition.ProviderSlack {
		body = url.Values{"payload": []string{body}}.Encode()
		headers = unitSlackSignedHeaders(body, "signing-secret")
		headers["Content-Type"] = "application/x-www-form-urlencoded"
	}
	if badSignature {
		// Modify the signed body after computing an otherwise valid signature.
		body += " "
	}
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	return performRequest(f.handler, request)
}

func (f profileChoiceHTTPFixture) assertAccepted(
	t *testing.T, response *httptest.ResponseRecorder, menu integrationstore.AppProfileChoiceRecord,
) {
	t.Helper()
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	if f.connection.Provider == appdefinition.ProviderDiscord {
		require.JSONEq(t, `{"type":7,"data":{
"content":"Selected Reviewer. The original request has been queued for the agent.",
			"components":[],"allowed_mentions":{"parse":[]}}}`, response.Body.String())
		return
	}
	require.JSONEq(t, `{"ok":"recorded"}`, response.Body.String())
	select {
	case update := <-f.updates:
		require.Equal(t, menu.MessageID, update["ts"])
		require.Equal(t, menu.MessageChannelID, update["channel"])
		require.Equal(t, "Selected Reviewer. The original request has been queued for the agent.", update["text"])
		require.Equal(t, []any{}, update["blocks"])
	case <-time.After(3 * time.Second):
		t.Fatal("Slack profile menu was not dismissed")
	}
}

func TestAppProfileChoiceSignedCallbacksOnlyQueueOriginalRequest(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{appdefinition.ProviderSlack, appdefinition.ProviderDiscord} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			f := newProfileChoiceHTTPFixture(t, provider)
			menu := f.menu(t, false)
			f.assertNoAgent(t)
			f.assertReceiptCount(t, menu.ID, 0)
			actor := "700"
			if provider == appdefinition.ProviderSlack {
				actor = "U_SELECTOR"
			}
			response := f.callback(t, menu, menu.ID, "reviewer", actor, false, nil)
			f.assertAccepted(t, response, menu)
			chosen := f.readChoice(t, menu.ID)
			require.Equal(t, "reviewer", chosen.SelectedKey)
			require.Equal(t, actor, chosen.SelectedBy, "a participant other than the original requester can choose")
			f.assertReceiptCount(t, menu.ID, 1)
			var receiptID uuid.UUID
			require.NoError(t, f.pool.QueryRow(t.Context(),
				`SELECT id FROM integration_inbox WHERE project_id=$1 AND connection_id=$2 AND receipt_key=$3`,
				f.project.ProjectUUID, f.connection.ID, "choice:"+menu.ID.String()).Scan(&receiptID))
			receipt, err := f.project.Store.Integrations().GetIntegrationInbox(t.Context(), f.project.ProjectUUID, receiptID)
			require.NoError(t, err)
			require.Equal(t, integrationstore.IntegrationInboxPending, receipt.State)
			require.Nil(t, receipt.Plan, "the callback does not plan or admit a launch")
			require.Equal(t, menu.Payload, receipt.Payload)
			var expected integration.AppEvent
			require.NoError(t, json.Unmarshal(menu.Event, &expected))
			expected.Directed = true
			expected.Launches = []integration.AppLaunchIntent{{
				AppID: f.app.ID, Slot: "reviewer", ProfileID: f.options[1].ProfileID,
			}}
			require.JSONEq(t, projectAppHTTPJSON(t, []integration.AppEvent{expected}), string(receipt.Events))
			// Exact replay and a later participant choosing another offered profile
			// both preserve the first decision and its single frozen receipt.
			for _, replay := range []struct{ key, actor string }{{"reviewer", actor}, {"support", actor + "2"}} {
				f.assertAccepted(t, f.callback(t, menu, menu.ID, replay.key, replay.actor, false, nil), menu)
				replayed := f.readChoice(t, menu.ID)
				require.Equal(t, chosen, replayed)
				f.assertReceiptCount(t, menu.ID, 1)
			}
			replayed, err := f.project.Store.Integrations().GetIntegrationInbox(t.Context(), f.project.ProjectUUID, receiptID)
			require.NoError(t, err)
			require.Equal(t, receipt, replayed)
			f.assertNoAgent(t)
		})
	}
}

func TestAppProfileChoiceCallbacksRejectForgedAndCrossMenuChoices(t *testing.T) {
	// Cases share two unselected menus so every rejection must preserve both.
	for _, provider := range []string{appdefinition.ProviderSlack, appdefinition.ProviderDiscord} {
		t.Run(provider, func(t *testing.T) {
			f := newProfileChoiceHTTPFixture(t, provider)
			menu, other := f.menu(t, false), f.menu(t, true)
			for _, test := range []struct {
				name   string
				id     uuid.UUID
				bad    bool
				change func(map[string]any)
			}{
				{"forged signature", menu.ID, true, nil},
				{"cross menu", other.ID, false, nil},
				{"unknown choice", uuid.New(), false, nil},
				{"unoffered option", menu.ID, false, func(payload map[string]any) {
					if provider == appdefinition.ProviderSlack {
						payload["actions"] = []map[string]any{{"type": "static_select",
							"action_id":       slack.ProfileChoiceActionPrefix + testPublicID(t, publicid.KindAppProfileChoice, menu.ID),
							"selected_option": map[string]string{"value": "unknown-option"}}}
					} else {
						data := testutil.RequireType[map[string]any](t, payload["data"])
						data["values"] = []string{"unknown-option"}
					}
				}},
				{"wrong conversation", menu.ID, false, func(payload map[string]any) {
					if provider == appdefinition.ProviderSlack {
						payload["channel"] = map[string]string{"id": "C456"}
					} else {
						payload["channel_id"] = "301"
						payload["message"] = map[string]any{"id": menu.MessageID, "channel_id": "301",
							"author": map[string]string{"id": "200"}}
					}
				}},
			} {
				t.Run(test.name, func(t *testing.T) {
					response := f.callback(t, menu, test.id, "reviewer", "700", test.bad, test.change)
					if test.bad {
						require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
					} else if provider == appdefinition.ProviderDiscord {
						require.Equal(t, http.StatusOK, response.Code, response.Body.String())
						var body map[string]any
						require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
						require.Equal(t, float64(4), body["type"])
						data := testutil.RequireType[map[string]any](t, body["data"])
						require.Equal(t, float64(64), data["flags"])
						require.Equal(t, unavailableProfileChoice, data["content"])
					} else {
						require.Equal(t, http.StatusOK, response.Code, response.Body.String())
						require.JSONEq(t, `{"ok":"recorded"}`, response.Body.String())
					}
					require.Equal(t, menu, f.readChoice(t, menu.ID))
					require.Equal(t, other, f.readChoice(t, other.ID))
					f.assertReceiptCount(t, menu.ID, 0)
					f.assertReceiptCount(t, other.ID, 0)
					f.assertNoAgent(t)
				})
			}
			select {
			case update := <-f.updates:
				t.Fatalf("foreign callback changed a menu: %+v", update)
			default:
			}
		})
	}
}

func TestAppProfileChoiceDiscordRetiresUnavailableMenu(t *testing.T) {
	t.Parallel()
	for _, staleProfile := range []bool{false, true} {
		t.Run(fmt.Sprintf("removed_profile=%t", staleProfile), func(t *testing.T) {
			t.Parallel()
			f := newProfileChoiceHTTPFixture(t, appdefinition.ProviderDiscord)
			menu := f.menu(t, false)
			if staleProfile {
				settings := f.app.Settings
				settings.Launcher.Slots = settings.Launcher.Slots[:1]
				_, err := f.project.Store.Integrations().UpdateProjectApp(t.Context(), f.app.ID,
					integrationstore.SaveProjectAppInput{
						OrgID: f.project.OrgUUID, ProjectID: f.project.ProjectUUID,
						Name: f.app.Name, DefinitionID: f.app.DefinitionID, Enabled: true, Settings: settings,
					})
				require.NoError(t, err)
			} else {
				require.NoError(t, f.project.Store.Integrations().ExpireAppProfileChoice(
					t.Context(), f.project.ProjectUUID, f.connection.ID, menu.ID))
			}
			response := f.callback(t, menu, menu.ID, "reviewer", "700", false, nil)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.JSONEq(t, fmt.Sprintf(`{"type":7,"data":{"content":%q,
				"components":[],"allowed_mentions":{"parse":[]}}}`, unavailableProfileChoice), response.Body.String())
			f.assertReceiptCount(t, menu.ID, 0)
			f.assertNoAgent(t)
			choice := f.readChoice(t, menu.ID)
			require.Empty(t, choice.SelectedKey)
			require.False(t, time.Now().Before(choice.ExpiresAt))
		})
	}
}
