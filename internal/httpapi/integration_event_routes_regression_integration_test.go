//go:build integration

package httpapi

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestProviderIngressBodyLimits(t *testing.T) {
	t.Parallel()
	for _, endpoint := range []string{"slack-events", "slack-actions", "discord-interactions"} {
		t.Run(endpoint, func(t *testing.T) {
			t.Parallel()
			provider := "slack"
			if endpoint == "discord-interactions" {
				provider = "discord"
			}
			f := newCapturedHTTPFixture(t, provider, "question")
			path, contentType, padding := integrationEventsPath, "application/json", " "
			limit := slack.EventBodyMaxBytes
			base := `{"type":"event_callback","team_id":"T123","api_app_id":"A123","event_id":"Ev-body-limit",` +
				`"authorizations":[{"team_id":"T123","user_id":"U_BOT","is_bot":true}],` +
				`"event":{"type":"message","user":"U123","text":"hello","channel":"C123","ts":"111.222"}}`
			switch endpoint {
			case "slack-actions":
				path, contentType, padding = integrationActionsPath, "application/x-www-form-urlencoded", "x"
				limit = slack.ActionBodyMaxBytes
				destination, err := f.record.CapturedDestination()
				require.NoError(t, err)
				base = slackActionFormBody(t, slackActionPayloadInput{
					Install: f.app, AgentID: f.record.AgentID, IntegrationTargetID: destination.IntegrationTargetID,
					InteractionID: f.record.ID, UserID: "U_OTHER", OptionValue: "0", ChannelID: "C123", MessageTS: "222.333",
				}) + "&padding="
			case "discord-interactions":
				path, limit = "/api/integrations/discord/100/interactions", discord.InteractionMaxBytes
				base = `{"id":"600","type":1,"application_id":"100"}`
			}
			// Unknown-length bodies exercise the streaming cap, not Content-Length.
			// Exactly the limit must still pass provider authentication and dispatch.
			for _, size := range []int{limit + 1, limit + 16*1024, limit} {
				body := base + strings.Repeat(padding, size-len(base))
				reader := &io.LimitedReader{R: strings.NewReader(body), N: int64(size)}
				request := httptest.NewRequest(http.MethodPost, path, reader)
				request.ContentLength = -1
				request.Header.Set("Content-Type", contentType)
				if provider == "discord" {
					timestamp := fmt.Sprint(time.Now().Unix())
					request.Header.Set("X-Signature-Timestamp", timestamp)
					request.Header.Set("X-Signature-Ed25519", hex.EncodeToString(ed25519.Sign(f.key, []byte(timestamp+body))))
				} else {
					for key, value := range unitSlackSignedHeaders(body, "signing-secret") {
						request.Header.Set(key, value)
					}
				}
				response := performRequest(f.handler, request)
				require.LessOrEqual(t, int64(size)-reader.N, int64(limit+1), "oversized streams must stop being read")
				if size > limit {
					require.Equal(t, http.StatusRequestEntityTooLarge, response.Code, response.Body.String())
				} else {
					require.Equal(t, http.StatusOK, response.Code, response.Body.String())
					if provider == "discord" {
						require.JSONEq(t, `{"type":1}`, response.Body.String())
					}
				}
				current, found, err := f.project.Store.Execution().GetAgentInteraction(
					t.Context(), f.app.ProjectID, f.record.AgentID, f.record.ID)
				require.NoError(t, err)
				require.True(t, found)
				wantState := executionstore.AgentInteractionStateOpen
				if endpoint == "slack-actions" && size == limit {
					wantState = executionstore.AgentInteractionStateResolved
				}
				require.Equal(t, wantState, current.State, "oversized callbacks must not resolve a prompt")
				var receipts int
				require.NoError(t, f.pool.QueryRow(t.Context(),
					`SELECT count(*) FROM integration_inbox WHERE app_id=$1`, f.app.ID).Scan(&receipts))
				if endpoint == "slack-events" && size == limit {
					require.Equal(t, 1, receipts)
					var captured []byte
					require.NoError(t, f.pool.QueryRow(t.Context(),
						`SELECT payload FROM integration_inbox WHERE app_id=$1`, f.app.ID).Scan(&captured))
					require.Equal(t, body, string(captured), "the maximum-sized signed body must survive unchanged")
				} else {
					require.Zero(t, receipts, "oversized events must not reach durable intake")
				}
			}
			if endpoint == "slack-actions" {
				select {
				case <-f.dismissed:
				case <-time.After(3 * time.Second):
					t.Fatal("accepted boundary callback did not dismiss its prompt")
				}
			}
		})
	}
}

func TestSlackSharedBotUninstallVerifiesEachAppAndFencesSetupRevision(t *testing.T) {
	t.Parallel()
	for _, concurrentSetup := range []bool{false, true} {
		t.Run(fmt.Sprintf("concurrent_setup=%t", concurrentSetup), func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			pool := openIntegrationDB(t, ctx)
			provider := newSlackEventsTestServer(t)
			t.Cleanup(provider.Close)
			f := newSlackEventsIntegrationFixture(t, ctx, pool, provider, "shared-uninstall")
			second := projectAppHTTPSecondProject(t, f.Handler, f.Project)
			profile := createSlackReadyHTTPProfile(t, f.Handler, second, "second-profile", second.AdminToken)
			profileID := mustPublicHTTPID(t, publicid.KindAgentProfile, testutil.RequireType[string](t, profile["id"]))
			verified := createSlackHTTPInstall(t, ctx, second, profileID, "A123", "T123", "U_BOT", "signing-secret")
			unverified := createSlackHTTPInstall(t, ctx, second, profileID, "A123", "T123", "U_BOT", "other-secret")
			body := `{"type":"event_callback","team_id":"T123","api_app_id":"A123","event_id":"Ev-uninstall",` +
				`"authorizations":[{"team_id":"T123","user_id":"U_ADMIN","is_bot":false}],` +
				`"event":{"type":"app_uninstalled"}}`
			request := httptest.NewRequest(http.MethodPost, integrationEventsPath, strings.NewReader(body))
			for key, value := range unitSlackSignedHeaders(body, "signing-secret") {
				request.Header.Set(key, value)
			}
			var response *httptest.ResponseRecorder
			if concurrentSetup {
				// Hold the app row until the authenticated callback reaches its
				// disconnect write, then publish a newer verified setup revision.
				tx := integrationdb.BeginTx(t, ctx, pool)
				_, err := tx.Exec(ctx, `SELECT id FROM project_apps WHERE id=$1 FOR UPDATE`, f.Install.ID)
				require.NoError(t, err)
				done := integrationdb.RunAsync(func() (*httptest.ResponseRecorder, error) {
					return performRequest(f.Handler, request), nil
				})
				integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "DisconnectProjectApp", 1)
				_, err = tx.Exec(ctx,
					`UPDATE project_apps SET setup_revision=setup_revision+1,updated_at=now() WHERE id=$1`, f.Install.ID)
				require.NoError(t, err)
				require.NoError(t, tx.Commit(ctx))
				response = integrationdb.AwaitSuccess(t, done, "uninstall after new setup")
			} else {
				response = performRequest(f.Handler, request)
			}
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			for _, before := range []integrationstore.ProjectAppRecord{f.Install, verified, unverified} {
				after, err := f.Project.Store.Integrations().GetProjectApp(ctx, before.ProjectID, before.ID)
				require.NoError(t, err)
				if before.ID == unverified.ID {
					require.Equal(t, before, after, "a sibling cannot borrow another app's callback signature")
					continue
				}
				wantState := integrationstore.ProjectAppStateDisconnected
				if concurrentSetup && before.ID == f.Install.ID {
					wantState = integrationstore.ProjectAppStateActive
				}
				require.Equal(t, wantState, after.State)
				require.Equal(t, before.SetupRevision+1, after.SetupRevision,
					"a stale callback must not advance the revision of a newer setup")
			}
		})
	}
}

func TestSlackChannelOnlySubscriptionReceivesRootMentionAndThreadReply(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	provider := newSlackEventsTestServer(t)
	t.Cleanup(provider.Close)
	f := newSlackEventsIntegrationFixture(t, ctx, pool, provider, "channel-subscription")
	app, err := f.Project.Store.Integrations().UpdateProjectApp(ctx, f.Install.ID, integrationstore.SaveProjectAppInput{
		OrgID: f.Install.OrgID, ProjectID: f.Install.ProjectID, Name: f.Install.Name, AppType: f.Install.AppType,
	})
	require.NoError(t, err)
	require.Nil(t, app.Settings.Launcher)
	source := projectAppHTTPSource(nil)
	config := createPublicHTTPAgentConfig(t, f.Handler, f.Project, "channel-subscription", "json",
		projectAppHTTPJSON(t, source), f.Project.AdminToken, http.StatusCreated)
	configID := mustPublicHTTPID(t, publicid.KindAgentConfig, testutil.RequireType[string](t, config["id"]))
	launched, err := f.Project.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: f.Project.ProjectUUID, AgentConfigID: configID, LaunchedBy: httpUserPrincipal(f.Project.AdminUserUUID),
	})
	require.NoError(t, err)
	_, err = f.Project.Store.Integrations().CreateAppSubscription(ctx, integrationstore.CreateAppSubscriptionInput{
		OrgID: f.Project.OrgUUID, ProjectID: f.Project.ProjectUUID, AppID: app.ID, AgentID: launched.Agent.ID,
		Type: "thread_messages", Conversation: json.RawMessage(`{"channel_id":"C123"}`),
	})
	require.NoError(t, err)
	for _, event := range []struct {
		key, kind, channel, ts, thread, text string
		inputs                               int
	}{
		{"mention", "app_mention", "C123", "111.222", "", "<@U_BOT> help", 1},
		{"message-duplicate", "message", "C123", "111.222", "", "<@U_BOT> help", 1},
		{"reply", "message", "C123", "111.333", "111.222", "here are the details", 2},
		{"reply-duplicate", "message", "C123", "111.333", "111.222", "here are the details", 2},
		{"other-channel", "app_mention", "COTHER", "222.111", "", "<@U_BOT> other request", 2},
	} {
		body := projectAppHTTPJSON(t, map[string]any{
			"type": "event_callback", "team_id": "T123", "api_app_id": "A123", "event_id": event.key,
			"authorizations": []any{map[string]any{"team_id": "T123", "user_id": "U_BOT", "is_bot": true}},
			"event": map[string]any{"type": event.kind, "user": "U123", "text": event.text,
				"channel": event.channel, "channel_type": "channel", "ts": event.ts, "thread_ts": event.thread, "team": "T123"},
		})
		requestJSONWithHeaders(t, f.Handler, http.MethodPost, integrationEventsPath, body, "", http.StatusOK,
			unitSlackSignedHeaders(body, "signing-secret"))
		drainSlackJourney(t, ctx, f.Project, f.Slack)
		var agents, inputs, subscriptions int
		require.NoError(t, pool.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM agents WHERE project_id=$1),
			(SELECT count(*) FROM agent_inputs WHERE agent_id=$2 AND input_kind='content'),
			(SELECT count(*) FROM app_subscriptions WHERE agent_id=$2)`,
			f.Project.ProjectUUID, launched.Agent.ID).Scan(&agents, &inputs, &subscriptions))
		require.Equal(t, 1, agents, event.key+": forwarding must not launch another agent")
		require.Equal(t, event.inputs, inputs, event.key)
		require.Equal(t, 1, subscriptions, "delivery must use the channel subscription without adding a follow")
	}
	target, err := slackJourneyTarget(t, pool, f.Project.Store.Integrations(), ctx,
		f.Project.ProjectUUID, app.ID, "C123:111.222")
	require.NoError(t, err)
	require.Equal(t, launched.Agent.ID, target.AgentID)
}
