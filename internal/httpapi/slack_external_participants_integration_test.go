//go:build integration

package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	integrationruntime "github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

type slackParticipantTransport func(*http.Request) (*http.Response, error)

func (f slackParticipantTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func setSlackExternalParticipants(t *testing.T, project publicHTTPProject,
	install integrationstore.IntegrationRecord, allow bool,
) integrationstore.IntegrationRecord {
	t.Helper()
	var settings map[string]any
	require.NoError(t, json.Unmarshal(install.Settings, &settings))
	settings["allow_external_participants"] = allow
	raw, err := json.Marshal(settings)
	require.NoError(t, err)
	updated, err := project.Store.Integrations().UpdateIntegration(t.Context(), install.ID,
		integrationstore.SaveIntegrationInput{
			OrgID: install.OrgID, ProjectID: install.ProjectID, Name: install.Name,
			IntegrationKind: install.IntegrationKind, Settings: raw,
		})
	require.NoError(t, err)
	return updated
}

func TestSlackExternalMentionsAndRepliesRequireIntegrationOptIn(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	server := newSlackEventsTestServer(t)
	t.Cleanup(server.Close)
	f := newSlackEventsIntegrationFixture(t, ctx, pool, server, "slack-external-participants")
	disabledSettings := f.Install.Settings
	send := func(ts, eventType, thread string) map[string]any {
		body := fmt.Sprintf(`{"type":"event_callback","team_id":"T123","api_app_id":"A123",
			"event_id":"Ev-external-%s","is_ext_shared_channel":true,
			"authorizations":[{"team_id":"T123","user_id":"U_BOT","is_bot":true}],
			"event":{"type":%q,"user":%q,"user_team":"T_REMOTE","channel":"C123","channel_type":"channel",
			"text":"help with this","ts":%q,"thread_ts":%q}}`, ts, eventType, "U_REMOTE"+strings.ReplaceAll(ts, ".", ""), ts, thread)
		return requestJSONWithHeaders(t, f.Handler, http.MethodPost, slackEventsPath, body, "", http.StatusOK,
			unitSlackSignedHeaders(body, "signing-secret"))
	}
	require.Equal(t, "ignored", send("1.0", "app_mention", "")["ok"])
	f.Install = setSlackExternalParticipants(t, f.Project, f.Install, true)
	require.Equal(t, "received", send("2.0", "app_mention", "")["ok"])
	drainSlackJourney(t, ctx, f.Project, f.Slack)
	target, err := slackJourneyTarget(t, pool, f.Project.Store.Integrations(), ctx, f.Project.ProjectUUID,
		f.Install.ID, "C123:2.0")
	require.NoError(t, err)
	assertInput := func(ts string, want bool) {
		t.Helper()
		_, found, err := f.Project.Store.Execution().GetIntegrationTargetInputByIdempotency(ctx,
			executionstore.GetIntegrationTargetInputByIdempotencyInput{
				IntegrationID: f.Install.ID, IntegrationTargetID: target.ID,
				IdempotencyKey: "slack:message:T123:C123:" + ts,
			})
		require.NoError(t, err)
		require.Equal(t, want, found, ts)
	}
	assertInput("2.0", true)
	interaction := createInteractionForAgent(t, ctx, f.Project.Store, f.Project, target.AgentID,
		"slack-external-approval", "permission", model.ToolCall{
			ID: "call_slack_external_approval", Name: "storage_read", Input: json.RawMessage(`{}`),
		})
	require.Equal(t, "received", send("3.0", "message", "2.0")["ok"])
	drainSlackJourney(t, ctx, f.Project, f.Slack)
	assertInput("3.0", true)
	open, found, err := f.Project.Store.Execution().GetAgentInteraction(
		ctx, f.Project.ProjectUUID, target.AgentID, interaction.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.AgentInteractionStateOpen, open.State, "external replies cannot cancel approvals")
	require.Equal(t, "received", send("4.0", "message", "2.0")["ok"])
	var optOut sync.Once
	var optOutErr error
	transport := f.Slack.HTTPClient.Transport
	f.Slack.HTTPClient.Transport = slackParticipantTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/users.info" {
			optOut.Do(func() {
				_, optOutErr = f.Project.Store.Integrations().UpdateIntegration(ctx, f.Install.ID,
					integrationstore.SaveIntegrationInput{
						OrgID: f.Install.OrgID, ProjectID: f.Install.ProjectID, Settings: disabledSettings,
					})
			})
		}
		return transport.RoundTrip(r)
	})
	_, err = slackJourneyWorker(f.Project, f.Slack).RunOnce(ctx)
	require.NoError(t, optOutErr)
	assertInput("4.0", false)
	require.ErrorIs(t, err, integrationruntime.ErrIntegrationRoutingChanged)
	require.Equal(t, "ignored", send("5.0", "message", "2.0")["ok"])
}

func TestSlackExternalRequestCannotBeSelectedAfterOptOut(t *testing.T) {
	t.Parallel()
	for _, senderTeam := range []string{"T123", "T_REMOTE"} {
		t.Run(senderTeam, func(t *testing.T) {
			t.Parallel()
			f := newProfileChoiceHTTPFixture(t, integrationdefinition.ProviderSlack)
			f.integration = setSlackExternalParticipants(t, f.project, f.integration, true)
			menu := f.menuWithSenderTeam(t, false, senderTeam)
			blocker := integrationdb.BeginTx(t, t.Context(), f.pool)
			require.NoError(t, integrationstore.LockConversationTx(t.Context(), blocker,
				f.project.ProjectUUID, f.integration.ID, menu.Address))
			callback := integrationdb.RunAsync(func() (*httptest.ResponseRecorder, error) {
				return f.callback(t, menu, menu.ID, "reviewer", "U_SELECTOR", false, func(payload map[string]any) {
					if senderTeam == "T123" {
						payload["user"] = map[string]string{"id": "U_SELECTOR", "team_id": "T_REMOTE"}
					}
				}), nil
			})
			integrationdb.WaitForNamedLockWaiters(t, t.Context(), f.pool, "LockIntegrationConversation", 1)
			setSlackExternalParticipants(t, f.project, f.integration, false)
			require.NoError(t, blocker.Commit(t.Context()))
			response := integrationdb.AwaitSuccess(t, callback, "selection after opt-out")
			require.Equal(t, http.StatusOK, response.Code)
			require.JSONEq(t, `{"ok":"ignored"}`, response.Body.String())
			f.assertReceiptCount(t, menu.ID, 0)
			if senderTeam == "T123" {
				require.Equal(t, menu, f.readChoice(t, menu.ID))
				response = f.callback(t, menu, menu.ID, "reviewer", "U_LOCAL", false, nil)
				f.assertAccepted(t, response, menu)
			} else {
				require.Empty(t, f.readChoice(t, menu.ID).SelectedKey)
				require.False(t, time.Now().Before(f.readChoice(t, menu.ID).ExpiresAt))
				select {
				case update := <-f.updates:
					require.Equal(t, unavailableProfileChoice, update["text"])
				case <-time.After(3 * time.Second):
					t.Fatal("unavailable external menu was not dismissed")
				}
			}
		})
	}
}

func TestSlackExternalProfileSelectionRequiresOptIn(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceHTTPFixture(t, integrationdefinition.ProviderSlack)
	menu := f.menu(t, false)
	external := func(payload map[string]any) {
		payload["user"] = map[string]string{"id": "U_REMOTE", "team_id": "T_REMOTE"}
	}
	response := f.callback(t, menu, menu.ID, "reviewer", "U_REMOTE", false, external)
	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"ok":"ignored"}`, response.Body.String())
	f.assertReceiptCount(t, menu.ID, 0)
	require.Equal(t, menu, f.readChoice(t, menu.ID))
	f.integration = setSlackExternalParticipants(t, f.project, f.integration, true)
	response = f.callback(t, menu, menu.ID, "reviewer", "U_REMOTE", false, external)
	f.assertAccepted(t, response, menu)
	require.Equal(t, "U_REMOTE", f.readChoice(t, menu.ID).SelectedBy)
	f.assertReceiptCount(t, menu.ID, 1)
}
