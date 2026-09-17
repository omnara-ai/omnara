//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func TestSlackEventsURLVerification(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	slackServer := newSlackEventsTestServer(t)
	defer slackServer.Close()
	fixture := newSlackEventsIntegrationFixture(
		t,
		ctx,
		pool,
		slackServer,
		"slack-events-url",
	)
	body := `{"type":"url_verification","challenge":"challenge-123"}`
	response := requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		body,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(body, "signing-secret"),
	)
	if response["challenge"] != "challenge-123" {
		t.Fatalf("challenge response=%v", response)
	}
}

func TestSlackEventsLifecycleDisablesInstall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	slackServer := newSlackEventsTestServer(t)
	defer slackServer.Close()
	fixture := newSlackEventsIntegrationFixture(
		t,
		ctx,
		pool,
		slackServer,
		"slack-events-lifecycle",
	)
	oauthOnly := `{
		"type":"event_callback",
		"team_id":"T123",
		"api_app_id":"A123",
		"event_id":"Ev-oauth-revoked",
		"authorizations":[{"team_id":"T123","user_id":"U_ADMIN","is_bot":false}],
		"event":{"type":"tokens_revoked","tokens":{"oauth":["U_ADMIN"],"bot":[]}}
	}`
	response := requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		oauthOnly,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(oauthOnly, "signing-secret"),
	)
	if response["ok"] != "ignored" {
		t.Fatalf("oauth token revoked response=%v want ignored", response)
	}
	updated, err := fixture.Project.Store.Integrations().GetIntegrationInstall(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
	)
	if err != nil {
		t.Fatalf("get install: %v", err)
	}
	if updated.State != integrationstore.IntegrationInstallStateActive {
		t.Fatalf("oauth-only revoke disabled install: %+v", updated)
	}

	botRevoked := `{
		"type":"event_callback",
		"team_id":"T123",
		"api_app_id":"A123",
		"event_id":"Ev-bot-revoked",
		"authorizations":[{"team_id":"T123","user_id":"U_ADMIN","is_bot":false}],
		"event":{"type":"tokens_revoked","tokens":{"oauth":[],"bot":["U_BOT"]}}
	}`
	response = requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		botRevoked,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(botRevoked, "signing-secret"),
	)
	if response["ok"] != "disabled" {
		t.Fatalf("bot token revoked response=%v want disabled", response)
	}
	updated, err = fixture.Project.Store.Integrations().GetIntegrationInstall(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
	)
	if err != nil {
		t.Fatalf("get disabled install: %v", err)
	}
	if updated.State != integrationstore.IntegrationInstallStateDisabled {
		t.Fatalf("install state=%q want disabled", updated.State)
	}

	mention := `{
		"type":"event_callback",
		"team_id":"T123",
		"api_app_id":"A123",
		"event_id":"Ev-disabled-mention",
		"authorizations":[{"team_id":"T123","user_id":"U_BOT","is_bot":true}],
		"event":{
			"type":"app_mention",
			"user":"U123",
			"text":"<@U_BOT> run",
			"channel":"C123",
			"channel_type":"channel",
			"ts":"333.444",
			"team":"T123"
		}
	}`
	response = requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		mention,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(mention, "signing-secret"),
	)
	if response["ok"] != "ignored" {
		t.Fatalf("disabled event response=%v want ignored", response)
	}
	_, err = fixture.Project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
		"C123:333.444",
	)
	if !storeerr.IsNotFound(err) {
		t.Fatalf(
			"disabled runtime event should not create integration target, err=%v",
			err,
		)
	}
}

func TestSlackEventsAppUninstalledDisablesInstall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	slackServer := newSlackEventsTestServer(t)
	defer slackServer.Close()
	fixture := newSlackEventsIntegrationFixture(
		t,
		ctx,
		pool,
		slackServer,
		"slack-events-app-uninstalled",
	)
	body := `{
		"type":"event_callback",
		"team_id":"T123",
		"api_app_id":"A123",
		"event_id":"Ev-app-uninstalled",
		"authorizations":[{"team_id":"T123","user_id":"U_ADMIN","is_bot":false}],
		"event":{"type":"app_uninstalled"}
	}`
	response := requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		body,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(body, "signing-secret"),
	)
	if response["ok"] != "disabled" {
		t.Fatalf("app_uninstalled response=%v want disabled", response)
	}
	updated, err := fixture.Project.Store.Integrations().GetIntegrationInstall(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
	)
	if err != nil {
		t.Fatalf("get integration install: %v", err)
	}
	if updated.State != integrationstore.IntegrationInstallStateDisabled {
		t.Fatalf("install state=%q want disabled", updated.State)
	}
}

func TestSlackEventsRejectWrongSignedIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	slackServer := newSlackEventsTestServer(t)
	t.Cleanup(slackServer.Close)
	fixture := newSlackEventsIntegrationFixture(
		t,
		ctx,
		pool,
		slackServer,
		"slack-events-wrong-identity",
	)
	tests := []struct {
		name   string
		body   string
		status int
	}{
		{
			name: "wrong team",
			body: `{"type":"event_callback","team_id":"T999","api_app_id":"A123",` +
				`"event_id":"Ev-wrong-team","authorizations":[{"team_id":"T123",` +
				`"user_id":"U_BOT","is_bot":true}],"event":{"type":"app_mention","user":"U123",` +
				`"text":"<@U_BOT> run","channel":"C123","channel_type":"channel",` +
				`"ts":"111.222","team":"T999"}}`,
			status: http.StatusUnauthorized,
		},
		{
			name: "wrong app",
			body: `{"type":"event_callback","team_id":"T123","api_app_id":"A999",` +
				`"event_id":"Ev-wrong-app","authorizations":[{"team_id":"T123",` +
				`"user_id":"U_BOT","is_bot":true}],"event":{"type":"app_mention","user":"U123",` +
				`"text":"<@U_BOT> run","channel":"C123","channel_type":"channel",` +
				`"ts":"222.333","team":"T123"}}`,
			status: http.StatusUnauthorized,
		},
		{
			name: "wrong authorization team",
			body: `{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
				`"event_id":"Ev-wrong-authz","authorizations":[{"team_id":"T999",` +
				`"user_id":"U_BOT","is_bot":true}],"event":{"type":"app_mention","user":"U123",` +
				`"text":"<@U_BOT> run","channel":"C123","channel_type":"channel",` +
				`"ts":"333.444","team":"T123"}}`,
			status: http.StatusForbidden,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			requestJSONWithHeaders(
				t,
				fixture.Handler,
				http.MethodPost,
				integrationEventsPath,
				tt.body,
				"",
				tt.status,
				unitSlackSignedHeaders(tt.body, "signing-secret"),
			)
		})
	}
}

func TestSlackEventsStableCallbackUsesInstallSigningSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	slackServer := newSlackEventsTestServer(t)
	defer slackServer.Close()
	handler := newIntegrationServer(
		pool,
		WithPublicURL("http://omnara.test"),
		testSlackChannelGateway(t),
		WithSlackOAuth(
			SlackOAuthConfig{
				APIURL:     slackServer.URL,
				HTTPClient: slackServer.Client(),
			},
		),
	)
	project := bootstrapPublicHTTPProject(
		t,
		handler,
		"slack-events-multi-install",
	)
	profile := createSlackReadyHTTPProfile(
		t,
		handler,
		project,
		"slack-events-multi-install",
		project.AdminToken,
	)
	profileID := mustPublicHTTPID(
		t,
		publicid.KindAgentProfile,
		testutil.RequireType[string](t, profile["id"]),
	)
	installA := createSlackHTTPInstall(
		t,
		ctx,
		project,
		profileID,
		"A111",
		"T111",
		"U_BOT_A",
		"signing-secret-a",
	)
	installB := createSlackHTTPInstall(
		t,
		ctx,
		project,
		profileID,
		"A222",
		"T222",
		"U_BOT_B",
		"signing-secret-b",
	)
	body := `{
		"type":"event_callback",
		"team_id":"T222",
		"api_app_id":"A222",
		"event_id":"Ev-multi-install",
		"authorizations":[{"team_id":"T222","user_id":"U_BOT_B","is_bot":true}],
		"event":{
			"type":"app_mention",
			"user":"U123",
			"text":"<@U_BOT_B> run",
			"channel":"C222",
			"channel_type":"channel",
			"ts":"111.222",
			"team":"T222"
		}
	}`
	requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		integrationEventsPath,
		body,
		"",
		http.StatusUnauthorized,
		unitSlackSignedHeaders(body, "signing-secret-a"),
	)
	response := requestJSONWithHeaders(
		t,
		handler,
		http.MethodPost,
		integrationEventsPath,
		body,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(body, "signing-secret-b"),
	)
	if response["ok"] != "accepted" {
		t.Fatalf("event response=%v want accepted", response)
	}
	var receivedInstallID uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT integration_install_id FROM integration_event_receipts WHERE event_id='Ev-multi-install'`,
	).Scan(&receivedInstallID); err != nil {
		t.Fatalf("get install B durable receipt: %v", err)
	}
	if receivedInstallID != installB.ID || receivedInstallID == installA.ID {
		t.Fatalf("wrong receipt installation: %s", receivedInstallID)
	}

}

func TestSlackEventsNameUpdatesRefreshDisplayNames(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	slackServer := newSlackEventsTestServer(t)
	defer slackServer.Close()
	fixture := newSlackEventsIntegrationFixture(
		t,
		ctx,
		pool,
		slackServer,
		"slack-events-name-updates",
	)
	createSlackConversationForHTTPTest(t, ctx, fixture, "C123:111.222")
	userChange := `{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
		`"event_id":"Ev-name-update-user","authorizations":[{"team_id":"T123",` +
		`"user_id":"U_BOT","is_bot":true}],"event":{"type":"user_profile_changed",` +
		`"user":{"id":"U123","name":"ada","profile":{"display_name":"Ada Lovelace"}}}}`
	response := requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		userChange,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(userChange, "signing-secret"),
	)
	if response["ok"] != "updated" {
		t.Fatalf("user_profile_changed response=%v want updated", response)
	}
	names, err := fixture.Project.Store.Execution().ListActorDisplayNames(
		ctx,
		fixture.Project.ProjectUUID,
		identitystore.ActorProviderSlack,
		fixture.Install.ProviderTenantID,
		[]string{"U123"},
	)
	if err != nil {
		t.Fatalf("list external user display names: %v", err)
	}
	if names["U123"] != "Ada Lovelace" {
		t.Fatalf("user display name = %q, want Ada Lovelace", names["U123"])
	}
	rename := `{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
		`"event_id":"Ev-name-update-channel","authorizations":[{"team_id":"T123",` +
		`"user_id":"U_BOT","is_bot":true}],"event":{"type":"channel_rename",` +
		`"channel":{"id":"C123","name":"general-renamed"}}}`
	response = requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		rename,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(rename, "signing-secret"),
	)
	if response["ok"] != "updated" {
		t.Fatalf("channel_rename response=%v want updated", response)
	}
	integrationTarget, err := fixture.Project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
		"C123:111.222",
	)
	if err != nil {
		t.Fatalf("get integration target: %v", err)
	}
	if integrationTarget.DisplayName != "general-renamed" {
		t.Fatalf(
			"conversation display name = %q, want general-renamed",
			integrationTarget.DisplayName,
		)
	}
}

func TestSlackActionsResolveQuestionAsSlackActor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	slackServer := newSlackEventsTestServer(t)
	defer slackServer.Close()
	fixture := newSlackEventsIntegrationFixture(
		t,
		ctx,
		pool,
		slackServer,
		"slack-actions-question",
	)
	agent, integrationTarget := createSlackConversationForHTTPTest(t, ctx, fixture, "C123:111.444")
	if _, err := fixture.Project.Store.Integrations().CreateIntegrationTargetBinding(
		ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: fixture.Project.ProjectUUID, AgentID: agent.ID,
			IntegrationInstallID: fixture.Install.ID, IntegrationTargetID: integrationTarget.ID,
			ReceiveAllowed: false, SendAllowed: true, Source: "action-send-only-regression",
		},
	); err != nil {
		t.Fatalf("create route-less send-only binding: %v", err)
	}
	receiveBinding, err := fixture.Project.Store.Integrations().GetActiveReceiveBindingForTarget(
		ctx,
		fixture.Project.ProjectUUID,
		agent.ID,
		integrationTarget.ID,
	)
	if err != nil {
		t.Fatalf("load conversation receive binding alongside send-only binding: %v", err)
	}
	if receiveBinding.Source != "channel" || !receiveBinding.ReceiveAllowed {
		t.Fatalf("conversation receive binding = %+v", receiveBinding)
	}
	interaction := createQuestionInteractionForAgent(
		t,
		ctx,
		fixture.Project.Store,
		fixture.Project,
		agent.ID,
		"slack-actions-question",
	)
	if _, err := pool.Exec(ctx, `
INSERT INTO actors(project_id, provider, provider_tenant_id, provider_user_id, display_name, created_at, updated_at)
VALUES ($1, $2, $3, 'U999', 'Grace Hopper', $4, $4)
ON CONFLICT (project_id, provider, provider_tenant_id, provider_user_id)
DO UPDATE SET display_name = excluded.display_name, updated_at = excluded.updated_at
`,
		fixture.Project.ProjectUUID,
		identitystore.ActorProviderSlack,
		fixture.Install.ProviderTenantID,
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("seed action slack actor: %v", err)
	}
	actionValue := slack.PromptActionValue{
		Type: slack.PromptType,
		InteractionID: testPublicID(
			t,
			publicid.KindAgentInteraction,
			interaction.ID,
		),
		AgentID: testPublicID(
			t,
			publicid.KindAgent,
			agent.ID,
		),
		IntegrationTargetID: testPublicID(
			t,
			publicid.KindIntegrationTarget,
			integrationTarget.ID,
		),
	}
	valueBody, err := json.Marshal(actionValue)
	if err != nil {
		t.Fatalf("marshal action value: %v", err)
	}
	payloadBody, err := json.Marshal(map[string]any{
		"type":       "block_actions",
		"api_app_id": "A123",
		"team":       map[string]string{"id": "T123"},
		"user": map[string]string{
			"id":      "U999",
			"team_id": "T123",
			"name":    "ada",
		},
		"actions": []map[string]string{
			{"action_id": slack.PromptAction, "value": string(valueBody)},
		},
		"state": map[string]any{"values": map[string]any{
			"omnara_question_0": map[string]any{
				slack.PromptAnswerAction: map[string]any{
					"type":            "radio_buttons",
					"selected_option": map[string]string{"value": "0"},
				},
			},
		}},
	})
	if err != nil {
		t.Fatalf("marshal slack action payload: %v", err)
	}
	form := url.Values{"payload": {string(payloadBody)}}.Encode()
	headers := unitSlackSignedHeaders(form, "signing-secret")
	headers["Content-Type"] = "application/x-www-form-urlencoded"
	response := requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationActionsPath,
		form,
		"",
		http.StatusOK,
		headers,
	)
	if response["ok"] != "resolved" {
		t.Fatalf("action response=%v want resolved", response)
	}
	resolved, found, err := fixture.Project.Store.Execution().GetAgentInteraction(
		ctx,
		fixture.Project.ProjectUUID,
		agent.ID,
		interaction.ID,
	)
	if err != nil {
		t.Fatalf("get resolved interaction: %v", err)
	}
	if !found || resolved.State != executionstore.AgentInteractionStateResolved ||
		resolved.ResolvedByInputID == uuid.Nil {
		t.Fatalf("resolved interaction found=%v record=%+v", found, resolved)
	}
	resolvingActorID, resolvingInputKind := interactionResolvingInput(
		t,
		ctx,
		pool,
		fixture.Project.ProjectUUID,
		agent.ID,
		interaction.ID,
	)
	if resolvingInputKind != "interaction_response" {
		t.Fatalf("resolving input kind = %q, want interaction_response", resolvingInputKind)
	}
	resolvingActor, err := fixture.Project.Store.Execution().GetActor(
		ctx,
		fixture.Project.ProjectUUID,
		resolvingActorID,
	)
	if err != nil {
		t.Fatalf("get resolving actor: %v", err)
	}
	if resolvingActor.Provider != identitystore.ActorProviderSlack ||
		resolvingActor.ProviderUserID != "U999" {
		t.Fatalf("resolving actor = %+v, want slack U999", resolvingActor)
	}
	clickerNames, err := fixture.Project.Store.Execution().ListActorDisplayNames(
		ctx,
		fixture.Project.ProjectUUID,
		identitystore.ActorProviderSlack,
		fixture.Install.ProviderTenantID,
		[]string{"U999"},
	)
	if err != nil {
		t.Fatalf("list action external user display names: %v", err)
	}
	if clickerNames["U999"] != "Grace Hopper" {
		t.Fatalf(
			"action user display name = %q, want stored Grace Hopper over payload name",
			clickerNames["U999"],
		)
	}
}

func TestSlackActionsQuestionSubmissionRequiresAnswer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	slackServer := newSlackEventsTestServer(t)
	defer slackServer.Close()
	fixture := newSlackEventsIntegrationFixture(
		t,
		ctx,
		pool,
		slackServer,
		"slack-actions-empty-answer",
	)
	agent, integrationTarget := createSlackConversationForHTTPTest(t, ctx, fixture, "CEMPTY:111.444")
	interaction := createQuestionInteractionForAgent(
		t,
		ctx,
		fixture.Project.Store,
		fixture.Project,
		agent.ID,
		"slack-actions-empty-answer",
	)
	body := slackActionFormBody(t, slackActionPayloadInput{
		Install:             fixture.Install,
		AgentID:             agent.ID,
		IntegrationTargetID: integrationTarget.ID,
		InteractionID:       interaction.ID,
		UserID:              "U_ACTION",
	})
	response := requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationActionsPath,
		body,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(body, "signing-secret"),
	)
	if response["ok"] != "invalid" ||
		response["text"] != "question 0 requires an answer" {
		t.Fatalf("action response=%v want invalid answer response", response)
	}
	open, found, err := fixture.Project.Store.Execution().GetAgentInteraction(
		ctx,
		fixture.Project.ProjectUUID,
		agent.ID,
		interaction.ID,
	)
	if err != nil {
		t.Fatalf("get interaction: %v", err)
	}
	if !found || open.State != executionstore.AgentInteractionStateOpen {
		t.Fatalf("interaction=%+v found=%v", open, found)
	}
}

func TestSlackActionsResolvePermissionAsSlackActor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	slackServer := newSlackEventsTestServer(t)
	defer slackServer.Close()
	type slackActionMessageUpdate struct {
		Method string
		Body   map[string]any
	}
	var releaseOnce sync.Once
	responseReleased := make(chan struct{})
	releaseResponse := func() {
		releaseOnce.Do(func() { close(responseReleased) })
	}
	defer releaseResponse()
	messageUpdates := make(chan slackActionMessageUpdate, 1)
	responseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		messageUpdates <- slackActionMessageUpdate{Method: r.Method, Body: body}
		select {
		case <-responseReleased:
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer responseServer.Close()
	responseURL := "https://hooks.slack.com/actions/T123/123/secret"
	fixture := newSlackEventsIntegrationFixture(
		t,
		ctx,
		pool,
		slackServer,
		"slack-actions-permission",
		slackActionResponseTestClient(t, responseServer),
	)
	agent, integrationTarget := createSlackConversationForHTTPTest(t, ctx, fixture, "CPERMISSION:111.444")
	interaction := createPermissionInteractionForAgent(
		t,
		ctx,
		fixture.Project.Store,
		fixture.Project,
		agent.ID,
		"slack-actions-permission",
	)
	body := slackActionFormBody(t, slackActionPayloadInput{
		Install:             fixture.Install,
		AgentID:             agent.ID,
		IntegrationTargetID: integrationTarget.ID,
		InteractionID:       interaction.ID,
		UserID:              "U_ACTION",
		OptionValue:         strconv.Itoa(toolpermission.AllowOptionIndex),
		ResponseURL:         responseURL,
	})
	timeoutRelease := time.AfterFunc(1500*time.Millisecond, releaseResponse)
	defer timeoutRelease.Stop()
	startedAt := time.Now()
	response := requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationActionsPath,
		body,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(body, "signing-secret"),
	)
	elapsed := time.Since(startedAt)
	releaseResponse()
	if response["ok"] != "resolved" {
		t.Fatalf("action response=%v want resolved", response)
	}
	if elapsed > time.Second {
		t.Fatalf("action callback took %s waiting for slack response_url update", elapsed)
	}
	resolved, found, err := fixture.Project.Store.Execution().GetAgentInteraction(
		ctx,
		fixture.Project.ProjectUUID,
		agent.ID,
		interaction.ID,
	)
	if err != nil {
		t.Fatalf("get interaction: %v", err)
	}
	if !found || resolved.State != executionstore.AgentInteractionStateResolved ||
		resolved.ResolvedByInputID == uuid.Nil {
		t.Fatalf("interaction=%+v found=%v", resolved, found)
	}
	resolvingActorID, _ := interactionResolvingInput(
		t,
		ctx,
		pool,
		fixture.Project.ProjectUUID,
		agent.ID,
		interaction.ID,
	)
	if actor, err := fixture.Project.Store.Execution().GetActor(
		ctx,
		fixture.Project.ProjectUUID,
		resolvingActorID,
	); err != nil || actor.Provider != identitystore.ActorProviderSlack {
		t.Fatalf("resolving actor=%+v err=%v, want slack actor", actor, err)
	}
	var resolution interactionform.Resolution
	if err := json.Unmarshal(resolved.Resolution, &resolution); err != nil {
		t.Fatalf("unmarshal resolution: %v", err)
	}
	if len(resolution.Answers) != 1 ||
		len(resolution.Answers[0].OptionIndices) != 1 ||
		resolution.Answers[0].OptionIndices[0] != toolpermission.AllowOptionIndex {
		t.Fatalf("resolution=%+v want allow", resolution)
	}
	select {
	case update := <-messageUpdates:
		if update.Method != http.MethodPost {
			t.Fatalf("slack response method=%q want POST", update.Method)
		}
		if update.Body["replace_original"] != true ||
			update.Body["text"] != "Permission allowed for run_command." {
			t.Fatalf("slack response body=%v", update.Body)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for slack response_url update")
	}
	actionNames, err := fixture.Project.Store.Execution().ListActorDisplayNames(
		ctx,
		fixture.Project.ProjectUUID,
		identitystore.ActorProviderSlack,
		fixture.Install.ProviderTenantID,
		[]string{"U_ACTION"},
	)
	if err != nil {
		t.Fatalf("list action external user display names: %v", err)
	}
	if actionNames["U_ACTION"] != "Action User" {
		t.Fatalf("action user display name = %q, want Action User", actionNames["U_ACTION"])
	}
}

func TestSlackActionsRejectWrongSignedIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	slackServer := newSlackEventsTestServer(t)
	defer slackServer.Close()
	fixture := newSlackEventsIntegrationFixture(
		t,
		ctx,
		pool,
		slackServer,
		"slack-actions-wrong-identity",
	)

	body := `payload={"type":"block_actions","api_app_id":"A999","team":{"id":"T123"},` +
		`"user":{"id":"U_ACTION","team_id":"T123"},` +
		`"actions":[{"action_id":"omnara_interaction","value":"{}"}]}`
	requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationActionsPath,
		body,
		"",
		http.StatusUnauthorized,
		unitSlackSignedHeaders(body, "signing-secret"),
	)
}

func TestSlackActionsIgnoreSignedMalformedActionValue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	slackServer := newSlackEventsTestServer(t)
	defer slackServer.Close()
	fixture := newSlackEventsIntegrationFixture(
		t,
		ctx,
		pool,
		slackServer,
		"slack-actions-malformed-value",
	)

	body := `payload={"type":"block_actions","api_app_id":"A123","team":{"id":"T123"},` +
		`"user":{"id":"U_ACTION","team_id":"T123"},` +
		`"actions":[{"action_id":"omnara_interaction","value":"{}"}]}`
	response := requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationActionsPath,
		body,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(body, "signing-secret"),
	)
	if response["ok"] != "ignored" {
		t.Fatalf("action response=%v want ignored", response)
	}
}

type slackEventsIntegrationFixture struct {
	Handler http.Handler
	Project publicHTTPProject
	Install integrationstore.IntegrationInstallRecord
}

func newSlackEventsIntegrationFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	slackServer *httptest.Server,
	seed string,
	clients ...*http.Client,
) slackEventsIntegrationFixture {
	t.Helper()
	client := slackServer.Client()
	if len(clients) > 0 && clients[0] != nil {
		client = clients[0]
	}
	return newSlackEventsFixtureWithOptions(t, ctx, pool, slackServer, seed, client)
}

func newSlackEventsFixtureWithOptions(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, slackServer *httptest.Server,
	seed string, client *http.Client, opts ...Option,
) slackEventsIntegrationFixture {
	t.Helper()
	options := []Option{
		WithPublicURL("https://omnara.test"),
		testSlackChannelGateway(t),
		WithSlackOAuth(
			SlackOAuthConfig{
				AccessURL:  slackServer.URL + "/oauth.v2.access",
				APIURL:     slackServer.URL,
				HTTPClient: client,
			},
		),
	}
	options = append(options, opts...)
	handler := newIntegrationServerWithStoreOptions(pool,
		[]storage.Option{storage.WithBlobStore(integrationblob.MustOpen(t, ctx))}, options...)
	project := bootstrapPublicHTTPProject(t, handler, seed)
	profile := createSlackReadyHTTPProfile(
		t,
		handler,
		project,
		seed,
		project.AdminToken,
	)
	createBrowserSessionForHTTPTest(
		t,
		ctx,
		project.Store,
		project.AdminUserUUID,
		seed+"-browser",
		"slack-csrf",
	)
	install := completeSlackOAuthInstall(
		t,
		handler,
		project,
		testutil.RequireType[string](t, profile["id"]),
		seed+"-browser",
		seed+"-code",
	)
	return slackEventsIntegrationFixture{
		Handler: handler,
		Project: project,
		Install: install,
	}
}

func slackActionResponseTestClient(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse response server URL: %v", err)
	}
	base := server.Client()
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &http.Client{
		Transport: slackActionResponseTestTransport{target: target, base: transport},
	}
}

type slackActionResponseTestTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (t slackActionResponseTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if clone.URL.Scheme == "https" && clone.URL.Hostname() == "hooks.slack.com" {
		clone.URL.Scheme = t.target.Scheme
		clone.URL.Host = t.target.Host
	}
	return t.base.RoundTrip(clone)
}

func newSlackEventsTestServer(t *testing.T) *httptest.Server {
	return newSlackEventsTestServerWithReactionAttempts(t, nil)
}

func newSlackEventsTestServerWithReactionAttempts(
	t *testing.T,
	reactionAttempts chan<- struct{},
) *httptest.Server {
	t.Helper()
	return httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/oauth.v2.access":
				writeJSON(
					w,
					http.StatusOK,
					slackOAuthTestResponse("xoxb-events-token"),
				)
			case "/conversations.history", "/conversations.replies":
				writeJSON(
					w,
					http.StatusOK,
					map[string]any{"ok": true, "messages": []any{}},
				)
			case "/users.info", "/conversations.info":
				writeSlackLookupTestResponse(t, w, r)
				return
			case "/reactions.add":
				if reactionAttempts != nil {
					reactionAttempts <- struct{}{}
				}
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			default:
				t.Errorf("unexpected slack test path %s", r.URL.Path)
				http.Error(w, "test handler failed", http.StatusInternalServerError)
				return
			}
		}),
	)
}

func writeSlackLookupTestResponse(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	if err := r.ParseForm(); err != nil {
		t.Errorf("parse slack lookup form: %v", err)
		http.Error(w, "invalid lookup form", http.StatusBadRequest)
		return
	}
	switch r.URL.Path {
	case "/users.info":
		userID := r.Form.Get("user")
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true,
			"user": map[string]any{
				"id":   userID,
				"name": slackTestUserName(userID),
				"profile": map[string]string{
					"display_name": slackTestUserName(userID),
				},
			},
		})
	case "/conversations.info":
		channelID := r.Form.Get("channel")
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true,
			"channel": map[string]string{
				"id":   channelID,
				"name": slackTestChannelName(channelID),
			},
		})
	default:
		t.Errorf("unexpected slack lookup path %s", r.URL.Path)
		http.Error(w, "unexpected lookup path", http.StatusNotFound)
	}
}

func slackTestUserName(userID string) string {
	switch userID {
	case "U123":
		return "Ada"
	case "U456":
		return "Ben"
	case "U999":
		return "Grace"
	case "U_BOT", "U_BOT_B", "U_MANIFEST_BOT":
		return "Omnara"
	default:
		return userID
	}
}

func slackTestChannelName(channelID string) string {
	switch channelID {
	case "C123":
		return "general"
	case "COPEN":
		return "open"
	case "CEMPTY":
		return "empty"
	case "CPERMISSION":
		return "permission"
	case "CDELAY":
		return "delayed"
	case "CFILEFIRST":
		return "file-first"
	case "CTHREADFILE":
		return "thread-file"
	default:
		return channelID
	}
}

func createSlackHTTPInstall(
	t *testing.T,
	ctx context.Context,
	project publicHTTPProject,
	profileID uuid.UUID,
	appID, workspaceID, botUserID, signingSecret string,
) integrationstore.IntegrationInstallRecord {
	t.Helper()
	credentialPayload, err := slack.CredentialPayload(slack.AppCredentials{
		BotToken:      "xoxb-" + appID,
		ClientID:      "client-id-" + appID,
		ClientSecret:  "client-secret-" + appID,
		SigningSecret: signingSecret,
	})
	if err != nil {
		t.Fatalf("build Slack credential payload: %v", err)
	}
	credentialSecret := createSlackHTTPInstallSecret(
		t,
		ctx,
		project,
		appID+"-credentials",
		credentialPayload,
	)
	app, err := project.Store.Integrations().GetOrCreateIntegrationApp(ctx, integrationstore.CreateIntegrationAppInput{
		OrgID: project.OrgUUID, OwnerProjectID: project.ProjectUUID, Provider: "slack",
		ProviderAppRef: appID, DisplayName: "Slack", ConnectorKey: channelconnector.BuiltInConnectorKey,
		State:                      integrationstore.IntegrationAppStateActive,
		InstallationCredentialKind: string(secretstore.SecretKindSlackAppCredentials),
	})
	if err != nil {
		t.Fatalf("register Slack application: %v", err)
	}
	install, err := project.Store.Integrations().UpsertIntegrationInstall(
		ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID:            project.OrgUUID,
			ProjectID:        project.ProjectUUID,
			IntegrationAppID: app.ID,
			InitialRoute: &integrationstore.CreateIntegrationRouteInput{
				AgentProfileID: profileID, DeploymentKey: "slack", BehaviorKey: "slack_conversation",
			},
			InstalledBy:        identitystore.NewUserPrincipal(project.AdminUserUUID),
			Provider:           integrationstore.IntegrationProviderSlack,
			IntegrationKind:    integrationstore.IntegrationKindManaged,
			ConnectionMode:     slack.ConnectionModeWebhook,
			State:              integrationstore.IntegrationInstallStateActive,
			ProviderTenantID:   workspaceID,
			ProviderAccountRef: appID,
			CredentialSecretID: credentialSecret,
			ProviderIdentity:   json.RawMessage(fmt.Sprintf(`{"bot_user_id":%q}`, botUserID)),
			Metadata:           json.RawMessage(`{}`),
		},
	)
	if err != nil {
		t.Fatalf("create Slack install %s/%s: %v", appID, workspaceID, err)
	}
	return install
}

func createSlackHTTPInstallSecret(
	t *testing.T,
	ctx context.Context,
	project publicHTTPProject,
	name string,
	payload secrets.Payload,
) uuid.UUID {
	t.Helper()
	secret, _, err := project.Store.Secrets().CreateSecret(
		ctx,
		secretstore.CreateSecretInput{
			OrgID:          project.OrgUUID,
			OwnerKind:      secretstore.SecretOwnerProject,
			OwnerProjectID: project.ProjectUUID,
			Name:           name,
			Material:       secrets.SlackAppCredentialsMaterialFromPayload(payload),
			Actor:          httpUserPrincipal(project.AdminUserUUID),
		},
	)
	if err != nil {
		t.Fatalf("create Slack install secret %q: %v", name, err)
	}
	return secret.ID
}

type slackActionPayloadInput struct {
	Install             integrationstore.IntegrationInstallRecord
	AgentID             uuid.UUID
	IntegrationTargetID uuid.UUID
	InteractionID       uuid.UUID
	UserID              string
	OptionValue         string
	ResponseURL         string
}

func slackActionFormBody(t *testing.T, input slackActionPayloadInput) string {
	t.Helper()
	value := slack.PromptActionValue{
		Type: slack.PromptType,
		InteractionID: testPublicID(
			t,
			publicid.KindAgentInteraction,
			input.InteractionID,
		),
		AgentID: testPublicID(t, publicid.KindAgent, input.AgentID),
		IntegrationTargetID: testPublicID(
			t,
			publicid.KindIntegrationTarget,
			input.IntegrationTargetID,
		),
	}
	valueJSON := string(mustHTTPJSON(value))
	actionID := slack.PromptAction
	payload := map[string]any{
		"type":       "block_actions",
		"api_app_id": input.Install.ProviderAccountRef,
		"team": map[string]string{
			"id": input.Install.ProviderTenantID,
		},
		"user": map[string]string{
			"id":      input.UserID,
			"team_id": input.Install.ProviderTenantID,
			"name":    "Action User",
		},
		"actions": []map[string]string{{
			"action_id": actionID,
			"value":     valueJSON,
		}},
	}
	if input.ResponseURL != "" {
		payload["response_url"] = input.ResponseURL
	}
	if input.OptionValue != "" {
		payload["state"] = map[string]any{"values": map[string]any{
			"omnara_question_0": map[string]any{
				slack.PromptAnswerAction: map[string]any{
					"type":            "radio_buttons",
					"selected_option": map[string]string{"value": input.OptionValue},
				},
			},
		}}
	}
	return "payload=" + url.QueryEscape(string(mustHTTPJSON(payload)))
}

func createQuestionInteractionForAgent(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	project publicHTTPProject,
	agentID uuid.UUID,
	seed string,
) executionstore.AgentInteractionRecord {
	t.Helper()
	return createInteractionForAgent(
		t,
		ctx,
		store,
		project,
		agentID,
		seed,
		"question",
	)
}

func createPermissionInteractionForAgent(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	project publicHTTPProject,
	agentID uuid.UUID,
	seed string,
) executionstore.AgentInteractionRecord {
	t.Helper()
	return createInteractionForAgent(
		t,
		ctx,
		store,
		project,
		agentID,
		seed,
		"permission",
	)
}

func createInteractionForAgent(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	project publicHTTPProject,
	agentID uuid.UUID,
	seed string,
	kind string,
	additionalToolCalls ...model.ToolCall,
) executionstore.AgentInteractionRecord {
	t.Helper()
	claim, found, err := store.Execution().ClaimNextAgentWork(
		ctx,
		httpTestClaimInput(),
	)
	if err != nil {
		t.Fatalf("claim prompt input: %v", err)
	}
	if !found || claim.Kind != executionstore.AgentWorkModel {
		t.Fatalf("claim prompt input found=%v executable=%v", found, claim.Kind == executionstore.AgentWorkModel)
	}
	admitted := claim.Model.AdmittedInputTurn
	runtime := claim.RuntimeLock
	snapshot, err := store.Execution().CaptureAgentConfigForEventWatermark(
		ctx,
		project.ProjectUUID,
		agentID,
		admitted.Events[0].Sequence,
	)
	if err != nil {
		t.Fatalf("capture prompt config: %v", err)
	}
	modelCall := claimNormalModelCallForHTTPTest(
		t,
		ctx,
		store,
		project.ProjectUUID,
		agentID,
		runtime,
		[]uuid.UUID{admitted.Inputs[0].ID},
		snapshot.AgentConfig.ID,
		admitted.Events[0].Sequence,
	)
	modelContext := modelCall.Context
	providerResponseID := "resp_" + seed
	toolName := "ask_question"
	toolInput := json.RawMessage(
		`{"questions":[{"prompt":"Ship?","options":[` +
			`{"label":"Yes"},{"label":"No"}]}]}`,
	)
	interactionFormValue, err := interactionform.New(
		"Question",
		nil,
		[]interactionform.Question{{
			Prompt:  "Ship?",
			Options: []interactionform.Option{{Label: "Yes"}, {Label: "No"}},
		}},
	)
	if err != nil {
		t.Fatalf("build question interaction form: %v", err)
	}
	var permissionRequest toolpermission.Request
	if kind == "permission" {
		toolName = "run_command"
		toolInput = json.RawMessage(`{"command":"echo hi"}`)
		authorization, err := toolpermission.NewAuthorization(
			toolName,
			toolInput,
		)
		if err != nil {
			t.Fatalf("build permission authorization: %v", err)
		}
		mode, ok := toolpermission.FindMode(
			toolpermission.CommonModeDescriptors(),
			toolpermission.ModeAlwaysAsk,
		)
		if !ok {
			t.Fatal("always_ask permission mode missing")
		}
		permissionForm, err := toolpermission.NewAllowDenyForm(
			"Permission requested for run_command",
			nil,
		)
		if err != nil {
			t.Fatalf("build permission interaction form: %v", err)
		}
		permissionRequest, err = toolpermission.NewRequest(
			mode,
			toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk),
			authorization,
			permissionForm,
		)
		if err != nil {
			t.Fatalf("build permission request: %v", err)
		}
	}
	primaryToolCall := model.ToolCall{ID: "call_" + seed, Name: toolName, Input: toolInput}
	responseToolCalls := append([]model.ToolCall{primaryToolCall}, additionalToolCalls...)
	providerIdentity := loadModelCallProviderIdentityForHTTPTest(
		t, ctx, store, project.ProjectUUID, modelCall.Context,
	)
	providerResponse, err := model.NewResponseEnvelopeForStorage(
		providerIdentity.Slug,
		providerIdentity.APIFormat,
		providerIdentity.APIVariant,
		model.Response{
			ID:         providerResponseID,
			StopReason: model.StopReasonToolUse,
			Content:    modeltest.ResponsePartsForToolCalls(responseToolCalls),
		},
	)
	if err != nil {
		t.Fatalf("build prompt provider response envelope: %v", err)
	}
	providerToolCalls := model.ToolCallsFromEnvelope(providerResponse)
	bindings := make([]executionstore.ToolCallBindingInput, 0, len(providerToolCalls))
	for _, call := range providerToolCalls {
		bindings = append(bindings, executionstore.ToolCallBindingInput{
			ProviderCallID: call.ID,
			Type:           toolcatalog.ToolTypeBuiltIn,
		})
	}
	_, calls, err := store.Execution().RecordToolCallSourceAndCompleteContext(
		ctx,
		executionstore.RecordToolCallSourceAndCompleteContextInput{
			ProjectID:          project.ProjectUUID,
			AgentID:            agentID,
			RuntimeLockID:      runtime.ID,
			ModelCallContextID: modelContext.ID,
			ProviderResponse:   providerResponse,
			ToolCallBindings:   bindings,
		},
	)
	if err != nil || len(calls) != len(responseToolCalls) {
		t.Fatalf("record prompt tool calls=%d want=%d err=%v", len(calls), len(responseToolCalls), err)
	}
	callsByProviderID := make(map[string]executionstore.ToolCallRecord, len(calls))
	for _, call := range calls {
		callsByProviderID[call.ProviderCallID] = call
	}
	primaryRecord, ok := callsByProviderID[primaryToolCall.ID]
	if !ok {
		t.Fatalf("primary tool call %s missing from accepted proposal batch", primaryToolCall.ID)
	}
	if kind == "question" {
		if _, err := store.Execution().MarkToolCallReady(ctx, executionstore.MarkToolCallReadyInput{
			ProjectID:     project.ProjectUUID,
			AgentID:       agentID,
			ID:            primaryRecord.ID,
			RuntimeLockID: runtime.ID,
		}); err != nil {
			t.Fatalf("allow prompt tool call: %v", err)
		}
	}
	for _, call := range additionalToolCalls {
		record, ok := callsByProviderID[call.ID]
		if !ok {
			t.Fatalf("additional tool call %s missing from accepted proposal batch", call.ID)
		}
		if _, err := store.Execution().MarkToolCallReady(ctx, executionstore.MarkToolCallReadyInput{
			ProjectID:     project.ProjectUUID,
			AgentID:       agentID,
			ID:            record.ID,
			RuntimeLockID: runtime.ID,
		}); err != nil {
			t.Fatalf("allow additional tool call %s: %v", call.ID, err)
		}
	}
	if kind == "permission" {
		interaction, err := store.Execution().CreatePermissionInteraction(
			ctx,
			executionstore.CreatePermissionInteractionInput{
				ProjectID:     project.ProjectUUID,
				AgentID:       agentID,
				ToolCallID:    primaryRecord.ID,
				RuntimeLockID: runtime.ID,
				Request:       permissionRequest,
			},
		)
		if err != nil {
			t.Fatalf("create permission interaction: %v", err)
		}
		return interaction
	}
	execution, err := store.Execution().ExecuteToolCall(
		ctx,
		executionstore.ExecuteToolCallInput{
			ProjectID:     project.ProjectUUID,
			AgentID:       agentID,
			ToolCallID:    primaryRecord.ID,
			RuntimeLockID: runtime.ID,
		},
		func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			return executionstore.CreateQuestionForToolCall(
				executionstore.CreateQuestionInteractionInput{
					Form: interactionFormValue,
				},
			), nil
		},
	)
	if err != nil {
		t.Fatalf("create question interaction: %v", err)
	}
	interaction, ok := execution.CommandResult.(executionstore.AgentInteractionRecord)
	if !ok {
		t.Fatalf("question command returned %T", execution.CommandResult)
	}
	if err := store.Execution().ReleaseToolCallRuntimeOwnership(
		ctx,
		executionstore.ReleaseToolCallRuntimeOwnershipInput{
			ProjectID:     project.ProjectUUID,
			AgentID:       agentID,
			ToolCallID:    primaryRecord.ID,
			RuntimeLockID: runtime.ID,
		},
	); err != nil {
		t.Fatalf("release question tool call: %v", err)
	}
	return interaction
}
