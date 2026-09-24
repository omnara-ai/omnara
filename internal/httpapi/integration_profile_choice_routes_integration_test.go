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
	integrationruntime "github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
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
	handler       http.Handler
	project       publicHTTPProject
	pool          *pgxpool.Pool
	integration   integrationstore.ProjectIntegrationRecord
	options       []integrationstore.IntegrationProfileChoiceOption
	key           ed25519.PrivateKey
	updates       chan map[string]any
	signingSecret string
}

func newProfileChoiceHTTPFixture(t *testing.T, provider string) profileChoiceHTTPFixture {
	t.Helper()
	updates := make(chan map[string]any, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/auth.test" {
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": true, "team_id": "T123", "user_id": "U_BOT", "bot_id": "B123",
			})
			return
		}
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
	tenant, account, scopeKind, scopeRef := "100", "200", "", ""
	config := json.RawMessage(
		projectIntegrationHTTPJSON(t, map[string]string{"public_key": hex.EncodeToString(publicKey)}),
	)
	identity := json.RawMessage(`{}`)
	integrationType := integrationdefinition.DiscordThread
	if provider == integrationdefinition.ProviderSlack {
		integrationType = integrationdefinition.SlackThread
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
	integration, err := project.Store.Integrations().CreateProjectIntegration(
		t.Context(),
		integrationstore.SaveProjectIntegrationInput{
			OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, Name: "support", IntegrationType: integrationType,
		},
	)
	require.NoError(t, err)
	integration, err = project.Store.Integrations().ConfigureProjectIntegration(
		t.Context(),
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
	base := createPublicHTTPAgentConfig(t, handler, project, "profile-choice", "json",
		`{"instruction":"Help with the original request","model":{"provider_config":"openai-prod","name":"gpt-test"}}`,
		project.AdminToken, http.StatusCreated)
	configID := mustPublicHTTPID(t, publicid.KindAgentConfig, testutil.RequireType[string](t, base["id"]))
	f := profileChoiceHTTPFixture{
		handler: handler, project: project, pool: pool, integration: integration, key: privateKey, updates: updates,
	}
	var slots []integrationstore.IntegrationLaunchSlot
	for _, name := range []string{"Support", "Reviewer"} {
		profile, err := project.Store.Execution().CreateAgentProfile(t.Context(), executionstore.CreateAgentProfileInput{
			OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, Name: name, CurrentConfigID: configID,
		})
		require.NoError(t, err)
		key := strings.ToLower(name)
		slots = append(slots, integrationstore.IntegrationLaunchSlot{Key: key, AgentProfileID: &profile.ID})
		f.options = append(
			f.options,
			integrationstore.IntegrationProfileChoiceOption{Key: key, ProfileID: profile.ID, Name: name},
		)
	}
	f.integration, err = project.Store.Integrations().UpdateProjectIntegration(
		t.Context(),
		integration.ID,
		integrationstore.SaveProjectIntegrationInput{
			OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, Name: "support", IntegrationType: integrationType,
			Settings: integrationstore.ProjectIntegrationSettings{
				Launcher: &integrationstore.IntegrationLauncher{
					Trigger:   "mention",
					ScopeKind: scopeKind,
					ScopeRef:  scopeRef,
					Slots:     slots,
				},
			},
		},
	)
	require.NoError(t, err)
	return f
}

func (f profileChoiceHTTPFixture) menu(t *testing.T, other bool) integrationstore.IntegrationProfileChoiceRecord {
	t.Helper()
	channel, message, thread, originalActor := "300", "400", "300", "701"
	if other {
		message, thread = "401", "301"
	}
	scope := integrationdefinition.Scope{
		Discord: &integrationdefinition.DiscordScope{GuildID: "500", ChannelID: "299", ThreadID: thread},
	}
	if f.integration.Provider == integrationdefinition.ProviderSlack {
		channel, message, thread, originalActor = "C123", "222.333", "111.222", "U_ORIGINAL"
		if other {
			message, thread = "222.444", "111.444"
		}
		scope = integrationdefinition.Scope{Slack: &integrationdefinition.SlackScope{ChannelID: channel, ThreadTS: thread}}
	}
	source := integrationruntime.IntegrationEvent{
		Event:       integrationdefinition.Event{Kind: "message", Mentioned: true, Scope: scope},
		SemanticKey: "source:" + message, ContentBlocks: json.RawMessage(`[{"type":"text","text":"original request"}]`),
		Actor: executionstore.ActorParams{Provider: executionstore.ActorProviderIntegration,
			ProviderTenantID: testPublicID(t, publicid.KindProjectIntegration, f.integration.ID), ProviderUserID: originalActor},
		DeliveryMode: executionstore.DeliveryModeSteering, CancelOpenInteractions: true,
	}
	kind, ref, err := scope.Conversation()
	require.NoError(t, err)
	payload := []byte("  {\"text\":\"original request\"}\n")
	store := f.project.Store.Integrations()
	accepted, created, err := store.AcceptIntegrationReceipt(t.Context(), integrationstore.VerifiedIntegrationReceipt{
		ProjectID: f.project.ProjectUUID, IntegrationID: f.integration.ID, ReceiptKey: source.SemanticKey, Payload: payload,
	})
	require.NoError(t, err)
	require.True(t, created)
	require.Nil(t, accepted.Events)
	claimed, found, err := store.ClaimIntegrationInbox(t.Context(), integrationstore.ClaimIntegrationInboxInput{
		ProjectID: f.project.ProjectUUID, IntegrationID: f.integration.ID,
		LeaseDuration: integrationstore.IntegrationInboxMaxLease,
	})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, accepted.ID, claimed.ID)
	choice, created, err := store.EnsureIntegrationProfileChoice(t.Context(), claimed.Lease(),
		integrationstore.EnsureIntegrationProfileChoiceInput{
			IntegrationID: f.integration.ID, Address: integrationstore.ConversationAddress{Kind: kind, Ref: ref},
			SourceKey: source.SemanticKey, Event: json.RawMessage(projectIntegrationHTTPJSON(t, source)),
			Payload: payload, Options: f.options,
		})
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, store.RecordIntegrationProfileChoiceMessage(t.Context(), f.project.ProjectUUID, f.integration.ID,
		choice.ID, channel, message))
	return f.readChoice(t, choice.ID)
}

func (
	f profileChoiceHTTPFixture,
) readChoice(t *testing.T, id uuid.UUID) integrationstore.IntegrationProfileChoiceRecord {
	t.Helper()
	choice, err := f.project.Store.Integrations().GetIntegrationProfileChoice(
		t.Context(), f.project.ProjectUUID, f.integration.ID, id)
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
		WHERE project_id=$1 AND integration_id=$2 AND receipt_key=$3`,
		f.project.ProjectUUID, f.integration.ID, "choice:"+choiceID.String()).Scan(&count))
	require.Equal(t, want, count)
}

func (f profileChoiceHTTPFixture) callback(
	t *testing.T, menu integrationstore.IntegrationProfileChoiceRecord, choiceID uuid.UUID, key, actor string,
	badSignature bool, change func(map[string]any),
) *httptest.ResponseRecorder {
	t.Helper()
	id := testPublicID(t, publicid.KindIntegrationProfileChoice, choiceID)
	path := "/api/integrations/discord/" +
		f.integration.ProviderTenantID + "/interactions"
	payload := map[string]any{
		"id": "600", "type": 3, "application_id": "100", "guild_id": "500", "channel_id": menu.MessageChannelID,
		"member": map[string]any{"user": map[string]any{"id": actor, "username": "selector"}},
		"message": map[string]any{"id": menu.MessageID, "channel_id": menu.MessageChannelID,
			"author": map[string]any{"id": "200"}},
		"data": map[string]any{"custom_id": discord.ProfileChoiceCustomIDPrefix + id,
			"component_type": 3, "values": []string{key}},
	}
	if f.integration.Provider == integrationdefinition.ProviderSlack {
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
	body := projectIntegrationHTTPJSON(t, payload)
	timestamp := fmt.Sprint(time.Now().Unix())
	headers := map[string]string{"Content-Type": "application/json", "X-Signature-Timestamp": timestamp,
		"X-Signature-Ed25519": hex.EncodeToString(ed25519.Sign(f.key, []byte(timestamp+body)))}
	if f.integration.Provider == integrationdefinition.ProviderSlack {
		body = url.Values{"payload": []string{body}}.Encode()
		secret := f.signingSecret
		if secret == "" {
			secret = "signing-secret"
		}
		headers = unitSlackSignedHeaders(body, secret)
		headers["Content-Type"] = "application/x-www-form-urlencoded"
	}
	if badSignature {
		body += " "
	}
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	return performRequest(f.handler, request)
}

func (f profileChoiceHTTPFixture) assertAccepted(
	t *testing.T, response *httptest.ResponseRecorder, menu integrationstore.IntegrationProfileChoiceRecord,
) {
	t.Helper()
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	if f.integration.Provider == integrationdefinition.ProviderDiscord {
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

func TestIntegrationProfileChoiceSignedCallbacksOnlyQueueOriginalRequest(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{integrationdefinition.ProviderSlack, integrationdefinition.ProviderDiscord} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			f := newProfileChoiceHTTPFixture(t, provider)
			menu := f.menu(t, false)
			f.assertNoAgent(t)
			f.assertReceiptCount(t, menu.ID, 0)
			actor := "700"
			if provider == integrationdefinition.ProviderSlack {
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
				`SELECT id FROM integration_inbox WHERE project_id=$1 AND integration_id=$2 AND receipt_key=$3`,
				f.project.ProjectUUID, f.integration.ID, "choice:"+menu.ID.String()).Scan(&receiptID))
			receipt, err := f.project.Store.Integrations().GetIntegrationInbox(t.Context(), f.project.ProjectUUID, receiptID)
			require.NoError(t, err)
			require.Equal(t, integrationstore.IntegrationInboxPending, receipt.State)
			require.Nil(t, receipt.Plan, "the callback does not plan or admit a launch")
			require.Equal(t, menu.Payload, receipt.Payload)
			var expected integrationruntime.IntegrationEvent
			require.NoError(t, json.Unmarshal(menu.Event, &expected))
			expected.Directed = true
			expected.Launches = []integrationruntime.IntegrationLaunchIntent{{
				IntegrationID: f.integration.ID, Slot: "reviewer", ProfileID: f.options[1].ProfileID,
			}}
			require.JSONEq(
				t,
				projectIntegrationHTTPJSON(t, []integrationruntime.IntegrationEvent{expected}),
				string(receipt.Events),
			)
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

func TestIntegrationProfileChoiceCallbacksRejectForgedAndCrossMenuChoices(t *testing.T) {
	for _, provider := range []string{integrationdefinition.ProviderSlack, integrationdefinition.ProviderDiscord} {
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
					if provider == integrationdefinition.ProviderSlack {
						payload["actions"] = []map[string]any{{"type": "static_select",
							"action_id":       slack.ProfileChoiceActionPrefix + testPublicID(t, publicid.KindIntegrationProfileChoice, menu.ID),
							"selected_option": map[string]string{"value": "unknown-option"}}}
					} else {
						data := testutil.RequireType[map[string]any](t, payload["data"])
						data["values"] = []string{"unknown-option"}
					}
				}},
				{"wrong conversation", menu.ID, false, func(payload map[string]any) {
					if provider == integrationdefinition.ProviderSlack {
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
					if test.id != menu.ID && test.id != other.ID {
						require.Equal(t, http.StatusNotFound, response.Code, response.Body.String())
					} else if test.bad {
						require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
					} else if provider == integrationdefinition.ProviderDiscord {
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

func TestIntegrationProfileChoiceDiscordRetiresUnavailableMenu(t *testing.T) {
	t.Parallel()
	for _, staleProfile := range []bool{false, true} {
		t.Run(fmt.Sprintf("removed_profile=%t", staleProfile), func(t *testing.T) {
			t.Parallel()
			f := newProfileChoiceHTTPFixture(t, integrationdefinition.ProviderDiscord)
			menu := f.menu(t, false)
			if staleProfile {
				settings := f.integration.Settings
				settings.Launcher.Slots = settings.Launcher.Slots[:1]
				_, err := f.project.Store.Integrations().UpdateProjectIntegration(t.Context(), f.integration.ID,
					integrationstore.SaveProjectIntegrationInput{
						OrgID: f.project.OrgUUID, ProjectID: f.project.ProjectUUID,
						Name: f.integration.Name, IntegrationType: f.integration.IntegrationType, Settings: settings,
					})
				require.NoError(t, err)
			} else {
				require.NoError(t, f.project.Store.Integrations().ExpireIntegrationProfileChoice(
					t.Context(), f.project.ProjectUUID, f.integration.ID, menu.ID))
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

func TestIntegrationProfileChoiceSharedBotAuthenticatesCapturedOwnerOnly(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{integrationdefinition.ProviderSlack, integrationdefinition.ProviderDiscord} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			f := newProfileChoiceHTTPFixture(t, provider)
			menu := f.menu(t, false)
			other := projectIntegrationHTTPSecondProject(t, f.handler, f.project)
			publicKey, privateKey, err := ed25519.GenerateKey(nil)
			require.NoError(t, err)
			material := secrets.Material(secrets.GenericMaterial{Value: "other-token"})
			config := json.RawMessage(
				projectIntegrationHTTPJSON(t, map[string]string{"public_key": hex.EncodeToString(publicKey)}),
			)
			if provider == integrationdefinition.ProviderSlack {
				material = secrets.SlackAppCredentialsMaterial{AccessToken: "xoxb-other", ClientID: "client",
					ClientSecret: "client-secret", SigningSecret: "other-signing-secret"}
				config = json.RawMessage(`{}`)
			}
			secret, _, err := f.project.Store.Secrets().CreateSecret(t.Context(), secretstore.CreateSecretInput{
				OrgID: other.OrgUUID, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: other.ProjectUUID,
				Name: "other-bot", Material: material, Actor: httpUserPrincipal(other.AdminUserUUID),
			})
			require.NoError(t, err)
			integration, err := f.project.Store.Integrations().CreateProjectIntegration(
				t.Context(), integrationstore.SaveProjectIntegrationInput{
					OrgID: other.OrgUUID, ProjectID: other.ProjectUUID,
					Name: "sibling", IntegrationType: f.integration.IntegrationType,
				},
			)
			require.NoError(t, err)
			integration, err = f.project.Store.Integrations().ConfigureProjectIntegration(
				t.Context(),
				integrationstore.ConfigureProjectIntegrationInput{
					OrgID:                 other.OrgUUID,
					ProjectID:             other.ProjectUUID,
					IntegrationID:         integration.ID,
					ExpectedSetupRevision: integration.SetupRevision,
					InstalledByUserID:     other.AdminUserUUID,
					Provider:              provider,
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
			forged := f
			forged.key, forged.signingSecret = privateKey, "other-signing-secret"
			response := forged.callback(t, menu, menu.ID, "reviewer", "700", false, nil)
			require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
			f.assertReceiptCount(t, menu.ID, 0)
			f.assertAccepted(t, f.callback(t, menu, menu.ID, "reviewer", "700", false, nil), menu)
			f.assertReceiptCount(t, menu.ID, 1)
			var count int
			require.NoError(t, f.pool.QueryRow(t.Context(),
				`SELECT count(*) FROM integration_inbox WHERE integration_id=$1`, integration.ID).Scan(&count))
			require.Zero(t, count, "shared provider identity must not fan out a callback")
		})
	}
}

func TestIntegrationProfileChoiceMetadataEditPreservesCallbackAuthority(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{integrationdefinition.ProviderSlack, integrationdefinition.ProviderDiscord} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			f := newProfileChoiceHTTPFixture(t, provider)
			menu := f.menu(t, false)
			settings := f.integration.Settings
			launcher := *settings.Launcher
			launcher.Slots = []integrationstore.IntegrationLaunchSlot{launcher.Slots[1], launcher.Slots[0]}
			settings.Launcher = &launcher
			updated, err := f.project.Store.Integrations().
				UpdateProjectIntegration(t.Context(), f.integration.ID, integrationstore.SaveProjectIntegrationInput{
					OrgID: f.integration.OrgID, ProjectID: f.integration.ProjectID, Name: f.integration.Name,
					IntegrationType: f.integration.IntegrationType, Settings: settings,
				})
			require.NoError(t, err)
			require.Equal(t, f.integration.SetupRevision, updated.SetupRevision)
			f.assertAccepted(t, f.callback(t, menu, menu.ID, "reviewer", "700", false, nil), menu)
			f.assertReceiptCount(t, menu.ID, 1)
		})
	}
}

func TestIntegrationProfileChoiceRejectsSetupChangedAfterAuthentication(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceHTTPFixture(t, integrationdefinition.ProviderDiscord)
	menu := f.menu(t, false)
	credential, err := f.project.Store.Secrets().GetSecret(
		t.Context(),
		f.integration.OrgID,
		f.integration.CredentialSecretID,
	)
	require.NoError(t, err)
	updated, err := f.project.Store.Integrations().
		ConfigureProjectIntegration(t.Context(), integrationstore.ConfigureProjectIntegrationInput{
			OrgID:                 f.integration.OrgID,
			ProjectID:             f.integration.ProjectID,
			IntegrationID:         f.integration.ID,
			ExpectedSetupRevision: f.integration.SetupRevision,
			InstalledByUserID:     f.project.AdminUserUUID,
			Provider:              f.integration.Provider,
			ProviderTenantID:      f.integration.ProviderTenantID,
			ProviderAccountRef:    f.integration.ProviderAccountRef,
			CredentialSecretID:    credential.ID,
			CredentialVersionID:   credential.CurrentVersionID,
			ProviderConfig:        f.integration.ProviderConfig,
			ProviderIdentity:      f.integration.ProviderIdentity,
		})
	require.NoError(t, err)
	require.Greater(t, updated.SetupRevision, f.integration.SetupRevision)
	input := discord.Interaction{Type: 3, ChannelID: menu.MessageChannelID,
		User: &discord.User{ID: "700"},
		Message: &discord.Message{
			ID: menu.MessageID, ChannelID: menu.MessageChannelID, Author: discord.User{ID: f.integration.ProviderAccountRef},
		},
		Data: discord.InteractionData{
			CustomID:      discord.ProfileChoiceCustomIDPrefix + testPublicID(t, publicid.KindIntegrationProfileChoice, menu.ID),
			ComponentType: 3, Values: []string{"reviewer"}},
	}
	response, err := (&Server{store: f.project.Store}).discordProfileChoiceAction(t.Context(), f.integration, input)
	require.NoError(t, err)
	require.Equal(t, 4, response.Type)
	require.Equal(t, unavailableProfileChoice, response.Data.Content)
	f.assertReceiptCount(t, menu.ID, 0)
}
