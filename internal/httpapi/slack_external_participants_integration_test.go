//go:build integration

package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

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
	require.Equal(t, install.SetupRevision, updated.SetupRevision, "access settings do not change installation identity")
	return updated
}

func TestSlackExternalMentionsAndRepliesRequireIntegrationOptIn(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	server := newSlackEventsTestServer(t)
	t.Cleanup(server.Close)
	f := newSlackEventsIntegrationFixture(t, ctx, pool, server, "slack-external-participants")
	send := func(ts, eventType, thread string) map[string]any {
		body := fmt.Sprintf(`{"type":"event_callback","team_id":"T123","api_app_id":"A123",
			"event_id":"Ev-external-%s","is_ext_shared_channel":true,
			"authorizations":[{"team_id":"T123","user_id":"U_BOT","is_bot":true}],
			"event":{"type":%q,"user":"U_REMOTE","user_team":"T_REMOTE","channel":"C123","channel_type":"channel",
			"text":"help with this","ts":%q,"thread_ts":%q}}`, ts, eventType, ts, thread)
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
	// An event queued before opt-out is rechecked before it is expanded.
	require.Equal(t, "received", send("4.0", "message", "2.0")["ok"])
	f.Install = setSlackExternalParticipants(t, f.Project, f.Install, false)
	drainSlackJourney(t, ctx, f.Project, f.Slack)
	assertInput("4.0", false)
	require.Equal(t, "ignored", send("5.0", "message", "2.0")["ok"])
}

func TestSlackExternalRequestCannotBeSelectedAfterOptOut(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceHTTPFixture(t, integrationdefinition.ProviderSlack)
	f.integration = setSlackExternalParticipants(t, f.project, f.integration, true)
	menu := f.menuWithSenderTeam(t, false, "T_REMOTE")
	f.integration = setSlackExternalParticipants(t, f.project, f.integration, false)
	response := f.callback(t, menu, menu.ID, "reviewer", "U_LOCAL", false, nil)
	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"ok":"ignored"}`, response.Body.String())
	f.assertReceiptCount(t, menu.ID, 0)
	require.Empty(t, f.readChoice(t, menu.ID).SelectedKey)
	require.False(t, time.Now().Before(f.readChoice(t, menu.ID).ExpiresAt))
	select {
	case update := <-f.updates:
		require.Equal(t, unavailableProfileChoice, update["text"])
	case <-time.After(3 * time.Second):
		t.Fatal("unavailable external menu was not dismissed")
	}
}

func TestSlackExternalProfileSelectionRequiresOptIn(t *testing.T) {
	t.Parallel()
	f := newProfileChoiceHTTPFixture(t, integrationdefinition.ProviderSlack)
	menu := f.menu(t, false)
	other := f.menu(t, true)
	external := func(payload map[string]any) {
		payload["user"] = map[string]string{"id": "U_REMOTE", "team_id": "T_REMOTE"}
	}
	response := f.callback(t, menu, menu.ID, "reviewer", "U_REMOTE", false, external)
	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"ok":"ignored"}`, response.Body.String())
	f.assertReceiptCount(t, menu.ID, 0)
	f.integration = setSlackExternalParticipants(t, f.project, f.integration, true)
	response = f.callback(t, menu, menu.ID, "reviewer", "U_REMOTE", true, external)
	require.Equal(t, http.StatusUnauthorized, response.Code, "opt-in cannot bypass signature verification")
	f.assertReceiptCount(t, menu.ID, 0)
	for _, channel := range []string{"C_OTHER", "D123", "G123"} {
		response = f.callback(t, menu, menu.ID, "reviewer", "U_REMOTE", false, func(payload map[string]any) {
			external(payload)
			payload["channel"] = map[string]string{"id": channel}
		})
		require.Equal(t, http.StatusOK, response.Code)
		require.JSONEq(t, `{"ok":"ignored"}`, response.Body.String())
		f.assertReceiptCount(t, menu.ID, 0)
		require.Empty(t, f.readChoice(t, menu.ID).SelectedKey)
	}
	response = f.callback(t, menu, menu.ID, "reviewer", "U_REMOTE", false, external)
	f.assertAccepted(t, response, menu)
	require.Equal(t, "U_REMOTE", f.readChoice(t, menu.ID).SelectedBy)
	f.assertReceiptCount(t, menu.ID, 1)
	f.assertNoAgent(t)
	f.integration = setSlackExternalParticipants(t, f.project, f.integration, false)
	response = f.callback(t, other, other.ID, "reviewer", "U_REMOTE", false, external)
	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"ok":"ignored"}`, response.Body.String())
	f.assertReceiptCount(t, other.ID, 0)
	require.True(t, time.Now().Before(f.readChoice(t, other.ID).ExpiresAt),
		"an internal request remains selectable by internal users")
}

func TestSlackExternalOptInDoesNotAuthorizeAgentPrompts(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"permission", "question"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f := newCapturedHTTPFixture(t, integrationdefinition.ProviderSlack, kind)
			f.integration = setSlackExternalParticipants(t, f.project, f.integration, true)
			f.callbackStatus = http.StatusForbidden
			f.slackRequest(t, false, func(payload map[string]any) {
				payload["user"] = map[string]string{"id": "U_REMOTE", "team_id": "T_REMOTE"}
			})
			record, found, err := f.project.Store.Execution().GetAgentInteraction(
				t.Context(), f.project.ProjectUUID, f.record.AgentID, f.record.ID)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, executionstore.AgentInteractionStateOpen, record.State)
		})
	}
}
