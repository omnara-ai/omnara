//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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

func TestSlackEventsAppMentionCreatesIntegrationTargetInputAndDedupesMessageEvent(
	t *testing.T,
) {
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
		"slack-events-mention",
	)
	body := `{
		"type":"event_callback",
		"team_id":"T123",
		"api_app_id":"A123",
		"event_id":"Ev-app-mention-1",
		"authorizations":[{"team_id":"T123","user_id":"U_BOT","is_bot":true}],
		"event":{
			"type":"app_mention",
			"user":"U123",
			"user_profile":{"display_name":"Ada"},
			"text":"<@U_BOT> run",
			"channel":"C123",
			"channel_type":"channel",
			"ts":"111.222",
			"team":"T123"
		}
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
	if response["ok"] != "accepted" {
		t.Fatalf("event response=%v want accepted", response)
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
	if string(integrationTarget.ProviderMetadata) != "{}" {
		t.Fatalf("integration target metadata = %s want {}", integrationTarget.ProviderMetadata)
	}
	input, found, err := fixture.Project.Store.Execution().GetIntegrationTargetInputByIdempotency(
		ctx,
		executionstore.GetIntegrationTargetInputByIdempotencyInput{
			IntegrationInstallID: fixture.Install.ID,
			IntegrationTargetID:  integrationTarget.ID,
			IdempotencyKey:       "slack:message:T123:C123:111.222",
		},
	)
	if err != nil {
		t.Fatalf("get integration target input: %v", err)
	}
	if !found || input.ActorID == storage.NilID ||
		input.IntegrationTargetID != integrationTarget.ID {
		t.Fatalf(
			"unexpected integration input: found=%v input=%+v",
			found,
			input,
		)
	}
	actor, err := fixture.Project.Store.Execution().GetActor(
		ctx,
		fixture.Project.ProjectUUID,
		input.ActorID,
	)
	if err != nil {
		t.Fatalf("get producer actor: %v", err)
	}
	if actor.Provider != identitystore.ActorProviderSlack {
		t.Fatalf("actor provider = %q, want slack", actor.Provider)
	}
	if input.DeliveryMode != executionstore.DeliveryModeSteering {
		t.Fatalf(
			"integration input delivery mode = %q, want steering",
			input.DeliveryMode,
		)
	}
	agent, err := fixture.Project.Store.Execution().GetAgentInProject(
		ctx,
		fixture.Project.ProjectUUID,
		integrationTarget.AgentID,
	)
	if err != nil {
		t.Fatalf("get launched agent: %v", err)
	}
	if agent.IntegrationTargetID != integrationTarget.ID {
		t.Fatalf(
			"integration target target = %s want %s",
			agent.IntegrationTargetID,
			integrationTarget.ID,
		)
	}

	messageCopy := `{
		"type":"event_callback",
		"team_id":"T123",
		"api_app_id":"A123",
		"event_id":"Ev-app-mention-1-message-copy",
		"authorizations":[{"team_id":"T123","user_id":"U_BOT","is_bot":true}],
		"event":{
			"type":"message",
			"user":"U123",
			"text":"<@U_BOT> run",
			"channel":"C123",
			"channel_type":"channel",
			"ts":"111.222",
			"team":"T123"
		}
	}`
	response = requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		messageCopy,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(messageCopy, "signing-secret"),
	)
	if response["ok"] != "ignored" {
		t.Fatalf("duplicate message response=%v want ignored", response)
	}
	if _, found, err := fixture.Project.Store.Execution().GetIntegrationTargetInputByIdempotency(
		ctx,
		executionstore.GetIntegrationTargetInputByIdempotencyInput{
			IntegrationInstallID: fixture.Install.ID,
			IntegrationTargetID:  integrationTarget.ID,
			IdempotencyKey:       "Ev-app-mention-1-message-copy",
		},
	); err != nil {
		t.Fatalf("get duplicate input: %v", err)
	} else if found {
		t.Fatal("message duplicate of app_mention created an input")
	}

	response = requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		body,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(body, "signing-secret"),
	)
	if response["ok"] != "accepted" {
		t.Fatalf("redelivery response=%v want accepted", response)
	}

	threadBody := `{
		"type":"event_callback",
		"team_id":"T123",
		"api_app_id":"A123",
		"event_id":"Ev-thread-message-1",
		"authorizations":[{"team_id":"T123","user_id":"U_BOT","is_bot":true}],
		"event":{
			"type":"message",
			"user":"U123",
			"text":"more context without mention",
			"channel":"C123",
			"channel_type":"channel",
			"ts":"333.444",
			"thread_ts":"111.222",
			"team":"T123"
		}
	}`
	response = requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		threadBody,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(threadBody, "signing-secret"),
	)
	if response["ok"] != "accepted" {
		t.Fatalf("thread message response=%v want accepted", response)
	}
	threadInput, found, err := fixture.Project.Store.Execution().GetIntegrationTargetInputByIdempotency(
		ctx,
		executionstore.GetIntegrationTargetInputByIdempotencyInput{
			IntegrationInstallID: fixture.Install.ID,
			IntegrationTargetID:  integrationTarget.ID,
			IdempotencyKey:       "slack:message:T123:C123:333.444",
		},
	)
	if err != nil {
		t.Fatalf("get thread input: %v", err)
	} else if !found {
		t.Fatal("mapped thread reply did not create input")
	}
	if threadInput.DeliveryMode != executionstore.DeliveryModeSteering {
		t.Fatalf(
			"thread input delivery mode = %q, want steering",
			threadInput.DeliveryMode,
		)
	}

	unmappedThreadBody := `{
		"type":"event_callback",
		"team_id":"T123",
		"api_app_id":"A123",
		"event_id":"Ev-thread-message-unmapped",
		"authorizations":[{"team_id":"T123","user_id":"U_BOT","is_bot":true}],
		"event":{
			"type":"message",
			"user":"U123",
			"text":"unmapped thread",
			"channel":"C123",
			"channel_type":"channel",
			"ts":"999.111",
			"thread_ts":"999.000",
			"team":"T123"
		}
	}`
	response = requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		unmappedThreadBody,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(unmappedThreadBody, "signing-secret"),
	)
	if response["ok"] != "ignored" {
		t.Fatalf("unmapped thread response=%v want ignored", response)
	}
	_, err = fixture.Project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
		"C123:999.000",
	)
	if !storeerr.IsNotFound(err) {
		t.Fatalf(
			"unmapped thread reply should not create integration target, err=%v",
			err,
		)
	}
}

func TestSlackEventsThreadMentionAdmitsUnmentionedReplies(t *testing.T) {
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
		"slack-events-thread-mention",
	)
	mention := `{
		"type":"event_callback",
		"team_id":"T123",
		"api_app_id":"A123",
		"event_id":"Ev-thread-mention",
		"authorizations":[{"team_id":"T123","user_id":"U_BOT","is_bot":true}],
		"event":{
			"type":"app_mention",
			"user":"U123",
			"text":"<@U_BOT> join this existing thread",
			"channel":"C123",
			"channel_type":"channel",
			"ts":"222.333",
			"thread_ts":"111.222",
			"team":"T123"
		}
	}`
	response := requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		mention,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(mention, "signing-secret"),
	)
	if response["ok"] != "accepted" {
		t.Fatalf("thread mention response=%v want accepted", response)
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

	reply := `{
		"type":"event_callback",
		"team_id":"T123",
		"api_app_id":"A123",
		"event_id":"Ev-thread-unmentioned-reply",
		"authorizations":[{"team_id":"T123","user_id":"U_BOT","is_bot":true}],
		"event":{
			"type":"message",
			"user":"U123",
			"text":"follow up without mention",
			"channel":"C123",
			"channel_type":"channel",
			"ts":"333.444",
			"thread_ts":"111.222",
			"team":"T123"
		}
	}`
	response = requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		reply,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(reply, "signing-secret"),
	)
	if response["ok"] != "accepted" {
		t.Fatalf("unmentioned reply response=%v want accepted", response)
	}
	if _, found, err := fixture.Project.Store.Execution().GetIntegrationTargetInputByIdempotency(
		ctx,
		executionstore.GetIntegrationTargetInputByIdempotencyInput{
			IntegrationInstallID: fixture.Install.ID,
			IntegrationTargetID:  integrationTarget.ID,
			IdempotencyKey:       "slack:message:T123:C123:333.444",
		},
	); err != nil {
		t.Fatalf("get unmentioned reply input: %v", err)
	} else if !found {
		t.Fatal("unmentioned reply in mapped thread did not create input")
	}
}

func TestSlackEventsReusesStoredDisplayNames(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	var mu sync.Mutex
	userLookups := map[string]int{}
	channelLookups := map[string]int{}
	slackServer := httptest.NewServer(
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
			case "/users.info":
				if err := r.ParseForm(); err != nil {
					t.Fatalf("parse users.info form: %v", err)
				}
				mu.Lock()
				userLookups[r.Form.Get("user")]++
				mu.Unlock()
				writeSlackLookupTestResponse(t, w, r)
			case "/conversations.info":
				if err := r.ParseForm(); err != nil {
					t.Fatalf("parse conversations.info form: %v", err)
				}
				mu.Lock()
				channelLookups[r.Form.Get("channel")]++
				mu.Unlock()
				writeSlackLookupTestResponse(t, w, r)
			case "/reactions.add":
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			default:
				t.Fatalf("unexpected slack test path %s", r.URL.Path)
			}
		}),
	)
	defer slackServer.Close()
	fixture := newSlackEventsIntegrationFixture(
		t,
		ctx,
		pool,
		slackServer,
		"slack-events-stored-names",
	)
	mention := `{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
		`"event_id":"Ev-stored-names-mention","authorizations":[{"team_id":"T123",` +
		`"user_id":"U_BOT","is_bot":true}],"event":{"type":"app_mention","user":"U123",` +
		`"text":"<@U_BOT> start","channel":"C123","channel_type":"channel",` +
		`"ts":"111.222","team":"T123"}}`
	response := requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		mention,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(mention, "signing-secret"),
	)
	if response["ok"] != "accepted" {
		t.Fatalf("mention response=%v want accepted", response)
	}
	reply := `{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
		`"event_id":"Ev-stored-names-reply","authorizations":[{"team_id":"T123",` +
		`"user_id":"U_BOT","is_bot":true}],"event":{"type":"app_mention","user":"U123",` +
		`"text":"<@U_BOT> follow up","channel":"C123","channel_type":"channel",` +
		`"ts":"222.333","thread_ts":"111.222","team":"T123"}}`
	response = requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		reply,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(reply, "signing-secret"),
	)
	if response["ok"] != "accepted" {
		t.Fatalf("reply response=%v want accepted", response)
	}
	mu.Lock()
	got := userLookups["U123"]
	mu.Unlock()
	if got != 1 {
		t.Fatalf("users.info calls for U123 = %d, want 1", got)
	}
	mu.Lock()
	got = userLookups["U_BOT"]
	mu.Unlock()
	if got != 1 {
		t.Fatalf("users.info calls for U_BOT = %d, want 1", got)
	}
	mu.Lock()
	got = channelLookups["C123"]
	mu.Unlock()
	if got != 1 {
		t.Fatalf("conversations.info calls for C123 = %d, want 1", got)
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
	if integrationTarget.DisplayName != "general" {
		t.Fatalf(
			"stored conversation display name = %q, want general",
			integrationTarget.DisplayName,
		)
	}
	input, found, err := fixture.Project.Store.Execution().GetIntegrationTargetInputByIdempotency(
		ctx,
		executionstore.GetIntegrationTargetInputByIdempotencyInput{
			IntegrationInstallID: fixture.Install.ID,
			IntegrationTargetID:  integrationTarget.ID,
			IdempotencyKey:       "slack:message:T123:C123:222.333",
		},
	)
	if err != nil {
		t.Fatalf("get reply input: %v", err)
	} else if !found {
		t.Fatal("reply input missing")
	}
	assertAgentInputText(
		t,
		ctx,
		pool,
		input,
		"This message directly mentioned the agent inside a Slack thread that is already attached to this agent.\n\n"+
			"<@U123> (Ada) in <#C123> (#general), thread 111.222:\n"+
			"<@U_BOT> (Omnara) follow up",
		[]bool{true, false},
	)
}

func TestSlackEventsResolvesMentionedUserDisplayNameInMemory(t *testing.T) {
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
		"slack-events-mentioned-user-label",
	)
	body := `{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
		`"event_id":"Ev-mentioned-user-label","authorizations":[{"team_id":"T123",` +
		`"user_id":"U_BOT","is_bot":true}],"event":{"type":"app_mention","user":"U123",` +
		`"text":"<@U_BOT> ask <@U456> to review this","channel":"C123",` +
		`"channel_type":"channel","ts":"111.222","team":"T123"}}`
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
	if response["ok"] != "accepted" {
		t.Fatalf("mention response=%v want accepted", response)
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
	input, found, err := fixture.Project.Store.Execution().GetIntegrationTargetInputByIdempotency(
		ctx,
		executionstore.GetIntegrationTargetInputByIdempotencyInput{
			IntegrationInstallID: fixture.Install.ID,
			IntegrationTargetID:  integrationTarget.ID,
			IdempotencyKey:       "slack:message:T123:C123:111.222",
		},
	)
	if err != nil {
		t.Fatalf("get input: %v", err)
	} else if !found {
		t.Fatal("input missing")
	}
	assertAgentInputText(
		t,
		ctx,
		pool,
		input,
		"The agent was mentioned in a Slack channel, so this message starts a new Slack thread for communicating with the agent.\n\n"+
			"<@U123> (Ada) in <#C123> (#general), thread 111.222:\n"+
			"<@U_BOT> (Omnara) ask <@U456> (Ben) to review this",
		[]bool{true, false},
	)
	var displayText string
	if err := pool.QueryRow(ctx, `
		SELECT coalesce(metadata->>'omnara_display_text', '')
		FROM content_blocks
		WHERE agent_id = $1
		  AND owner_agent_input_id = $2
		  AND ordinal = 1
	`, input.AgentID, input.ID).Scan(&displayText); err != nil {
		t.Fatalf("load Slack input display text: %v", err)
	}
	if displayText != "@Omnara ask @Ben to review this" {
		t.Fatalf("Slack input display text = %q", displayText)
	}
	mentionedNames, err := fixture.Project.Store.Execution().ListActorDisplayNames(
		ctx,
		fixture.Project.ProjectUUID,
		identitystore.ActorProviderSlack,
		fixture.Install.ProviderTenantID,
		[]string{"U456"},
	)
	if err != nil {
		t.Fatalf("list mentioned user display names: %v", err)
	}
	if len(mentionedNames) != 0 {
		t.Fatalf("mentioned user display names = %v, want none persisted", mentionedNames)
	}
}

func TestSlackEventsMentionedMessageCopyDefersToAppMention(t *testing.T) {
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
		"slack-events-mentioned-message-copy",
	)
	start := `{
		"type":"event_callback",
		"team_id":"T123",
		"api_app_id":"A123",
		"event_id":"Ev-copy-start",
		"authorizations":[{"team_id":"T123","user_id":"U_BOT","is_bot":true}],
		"event":{
			"type":"app_mention",
			"user":"U123",
			"text":"<@U_BOT> start",
			"channel":"C123",
			"channel_type":"channel",
			"ts":"111.222",
			"team":"T123"
		}
	}`
	requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		start,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(start, "signing-secret"),
	)
	integrationTarget, err := fixture.Project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
		"C123:111.222",
	)
	if err != nil {
		t.Fatalf("get integration target: %v", err)
	}
	messageCopy := `{
		"type":"event_callback",
		"team_id":"T123",
		"api_app_id":"A123",
		"event_id":"Ev-mentioned-message-copy",
		"authorizations":[{"team_id":"T123","user_id":"U_BOT","is_bot":true}],
		"event":{
			"type":"message",
			"user":"U123",
			"text":"<@U_BOT> important follow up",
			"channel":"C123",
			"channel_type":"channel",
			"ts":"333.444",
			"thread_ts":"111.222",
			"team":"T123"
		}
	}`
	response := requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		messageCopy,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(messageCopy, "signing-secret"),
	)
	if response["ok"] != "ignored" {
		t.Fatalf("mentioned message copy response=%v want ignored", response)
	}
	if _, found, err := fixture.Project.Store.Execution().GetIntegrationTargetInputByIdempotency(
		ctx,
		executionstore.GetIntegrationTargetInputByIdempotencyInput{
			IntegrationInstallID: fixture.Install.ID,
			IntegrationTargetID:  integrationTarget.ID,
			IdempotencyKey:       "slack:message:T123:C123:333.444",
		},
	); err != nil {
		t.Fatalf("get message copy input: %v", err)
	} else if found {
		t.Fatal("mentioned message copy created an input before app_mention")
	}
	appMention := `{
		"type":"event_callback",
		"team_id":"T123",
		"api_app_id":"A123",
		"event_id":"Ev-mentioned-app-mention",
		"authorizations":[{"team_id":"T123","user_id":"U_BOT","is_bot":true}],
		"event":{
			"type":"app_mention",
			"user":"U123",
			"text":"<@U_BOT> important follow up",
			"channel":"C123",
			"channel_type":"channel",
			"ts":"333.444",
			"thread_ts":"111.222",
			"team":"T123"
		}
	}`
	response = requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		appMention,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(appMention, "signing-secret"),
	)
	if response["ok"] != "accepted" {
		t.Fatalf("app mention response=%v want accepted", response)
	}
	input, found, err := fixture.Project.Store.Execution().GetIntegrationTargetInputByIdempotency(
		ctx,
		executionstore.GetIntegrationTargetInputByIdempotencyInput{
			IntegrationInstallID: fixture.Install.ID,
			IntegrationTargetID:  integrationTarget.ID,
			IdempotencyKey:       "slack:message:T123:C123:333.444",
		},
	)
	if err != nil {
		t.Fatalf("get app mention input: %v", err)
	} else if !found {
		t.Fatal("app mention input missing")
	}
	assertAgentInputText(
		t,
		ctx,
		pool,
		input,
		"This message directly mentioned the agent inside a Slack thread that is already attached to this agent.\n\n"+
			"<@U123> (Ada) in <#C123> (#general), thread 111.222:\n"+
			"<@U_BOT> (Omnara) important follow up",
		[]bool{true, false},
	)
}

func TestSlackEventsOpenInteractionContinuesWithNewMessage(t *testing.T) {
	for _, kind := range []string{"question", "permission"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			testSlackEventsOpenInteractionContinuesWithNewMessage(t, kind)
		})
	}
}

func testSlackEventsOpenInteractionContinuesWithNewMessage(t *testing.T, kind string) {
	t.Helper()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	reactionAttempts := make(chan struct{}, 2)
	slackServer := newSlackEventsTestServerWithReactionAttempts(t, reactionAttempts)
	defer slackServer.Close()
	seed := "slack-events-open-" + kind
	fixture := newSlackEventsIntegrationFixture(
		t,
		ctx,
		pool,
		slackServer,
		seed,
	)
	body := `{
		"type":"event_callback",
		"team_id":"T123",
		"api_app_id":"A123",
		"event_id":"Ev-open-source",
		"authorizations":[{"team_id":"T123","user_id":"U_BOT","is_bot":true}],
		"event":{
			"type":"app_mention",
			"user":"U123",
			"text":"<@U_BOT> run",
			"channel":"COPEN",
			"channel_type":"channel",
			"ts":"111.222",
			"team":"T123"
		}
	}`
	requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		body,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(body, "signing-secret"),
	)
	select {
	case <-reactionAttempts:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial slack reaction")
	}
	integrationTarget, err := fixture.Project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
		"COPEN:111.222",
	)
	if err != nil {
		t.Fatalf("get integration target: %v", err)
	}
	siblingProviderCallID := "call_" + seed + "-sibling"
	interaction := createInteractionForAgent(
		t,
		ctx,
		fixture.Project.Store,
		fixture.Project,
		integrationTarget.AgentID,
		seed,
		kind,
		model.ToolCall{
			ID:    siblingProviderCallID,
			Name:  "read_file",
			Input: json.RawMessage(`{}`),
		},
	)
	siblingToolCall, found, err := fixture.Project.Store.Execution().GetToolCallByProviderCall(
		ctx,
		fixture.Project.ProjectUUID,
		integrationTarget.AgentID,
		interaction.ModelCallContextID,
		siblingProviderCallID,
	)
	if err != nil {
		t.Fatalf("load sibling tool call: %v", err)
	}
	if !found {
		t.Fatal("sibling tool call missing from accepted proposal batch")
	}
	siblingToolCallID := siblingToolCall.ID

	reply := `{
		"type":"event_callback",
		"team_id":"T123",
		"api_app_id":"A123",
		"event_id":"Ev-open-interaction",
		"authorizations":[{"team_id":"T123","user_id":"U_BOT","is_bot":true}],
		"event":{
			"type":"message",
			"user":"U123",
			"text":"answer while blocked",
			"channel":"COPEN",
			"channel_type":"channel",
			"ts":"222.333",
			"thread_ts":"111.222",
			"team":"T123"
		}
	}`
	response := requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		reply,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(reply, "signing-secret"),
	)
	if response["ok"] != "accepted" {
		t.Fatalf("open interaction response=%v want accepted", response)
	}
	input, found, err := fixture.Project.Store.Execution().GetIntegrationTargetInputByIdempotency(
		ctx,
		executionstore.GetIntegrationTargetInputByIdempotencyInput{
			IntegrationInstallID: fixture.Install.ID,
			IntegrationTargetID:  integrationTarget.ID,
			IdempotencyKey:       "slack:message:T123:COPEN:222.333",
		},
	)
	if err != nil {
		t.Fatalf("get steering input: %v", err)
	}
	if !found || input.State != "received" || input.DeliveryMode != executionstore.DeliveryModeSteering {
		t.Fatalf("steering input found=%v input=%+v", found, input)
	}
	canceled, found, err := fixture.Project.Store.Execution().GetAgentInteraction(
		ctx,
		fixture.Project.ProjectUUID,
		integrationTarget.AgentID,
		interaction.ID,
	)
	if err != nil {
		t.Fatalf("get canceled interaction: %v", err)
	}
	if !found || canceled.State != executionstore.AgentInteractionStateCanceled ||
		canceled.ResolvedByInputID != input.ID {
		t.Fatalf("canceled interaction found=%v interaction=%+v", found, canceled)
	}
	var resolution map[string]string
	if err := json.Unmarshal(canceled.Resolution, &resolution); err != nil {
		t.Fatalf("decode canceled interaction resolution: %v", err)
	}
	if resolution["reason"] != "superseded_by_input" {
		t.Fatalf("canceled interaction resolution=%v", resolution)
	}
	var resultTurnID storage.ID
	var resultSequence int64
	var resultReason string
	if err := pool.QueryRow(
		ctx,
		`SELECT event.turn_id, event.sequence, block.structured_data->>'reason'
	FROM tool_call_results result
	JOIN tool_call_read_projection tool_call
	  ON tool_call.agent_id = result.agent_id
	 AND tool_call.id = result.tool_call_id
	JOIN agent_events event ON event.agent_id = result.agent_id
	  AND event.tool_call_result_id = result.id
	JOIN content_blocks block ON block.agent_id = result.agent_id
	  AND block.owner_tool_call_result_id = result.id
	  AND block.block_kind = 'structured_data'
	WHERE tool_call.project_id = $1 AND result.agent_id = $2 AND result.tool_call_id = $3`,
		fixture.Project.ProjectUUID,
		integrationTarget.AgentID,
		interaction.ToolCallID,
	).Scan(&resultTurnID, &resultSequence, &resultReason); err != nil {
		t.Fatalf("load superseded interaction result: %v", err)
	}
	if resultTurnID != interaction.TurnID || resultSequence <= 0 {
		t.Fatalf("superseded result turn=%s sequence=%d", resultTurnID, resultSequence)
	}
	wantResultReason := kind + " interaction canceled"
	if resultReason != wantResultReason {
		t.Fatalf("superseded interaction result reason=%q", resultReason)
	}
	select {
	case <-reactionAttempts:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for steering input slack reaction")
	}
	var runtimeCanceled bool
	if err := pool.QueryRow(
		ctx,
		`SELECT runtime_lock.cancel_requested_at IS NOT NULL FROM agent_runtime_locks runtime_lock `+
			`JOIN agents agent ON agent.id = runtime_lock.agent_id `+
			`WHERE agent.project_id = $1 AND runtime_lock.agent_id = $2`,
		fixture.Project.ProjectUUID,
		integrationTarget.AgentID,
	).Scan(&runtimeCanceled); err != nil {
		t.Fatalf("query runtime cancellation: %v", err)
	}
	if runtimeCanceled {
		t.Fatal("slack message canceled the active runtime")
	}
	var stopEvents int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*)::int FROM agent_stop_events WHERE project_id = $1 AND agent_id = $2`,
		fixture.Project.ProjectUUID,
		integrationTarget.AgentID,
	).Scan(&stopEvents); err != nil {
		t.Fatalf("query stop events: %v", err)
	}
	if stopEvents != 0 {
		t.Fatalf("slack message created %d stop events", stopEvents)
	}
	var siblingState string
	var siblingResults int
	if err := pool.QueryRow(
		ctx,
		`SELECT tool_call.state, count(result.id)::int
	FROM tool_call_read_projection tool_call
	LEFT JOIN tool_call_results result ON result.agent_id = tool_call.agent_id
  AND result.tool_call_id = tool_call.id
WHERE tool_call.project_id = $1 AND tool_call.agent_id = $2 AND tool_call.id = $3
GROUP BY tool_call.state`,
		fixture.Project.ProjectUUID,
		integrationTarget.AgentID,
		siblingToolCallID,
	).Scan(&siblingState, &siblingResults); err != nil {
		t.Fatalf("query sibling tool call: %v", err)
	}
	if siblingState != "ready" || siblingResults != 0 {
		t.Fatalf(
			"sibling tool call state=%s results=%d, want ready/0",
			siblingState,
			siblingResults,
		)
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

func TestSlackEventsAndActionsRouteByProviderIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	defer pool.Close()
	slackServer := newSlackEventsTestServer(t)
	defer slackServer.Close()
	fixture := newSlackEventsIntegrationFixture(
		t,
		ctx,
		pool,
		slackServer,
		"slack-events-multiple-apps",
	)
	secondInstall := createSlackHTTPInstall(
		t,
		ctx,
		fixture.Project,
		fixture.Install.AgentProfileID,
		"A_SECOND",
		"T123",
		"U_SECOND_BOT",
		"second-signing-secret",
	)
	body := `{
		"type":"event_callback",
		"team_id":"T123",
		"api_app_id":"A_SECOND",
		"event_id":"Ev-second-app-mention",
		"authorizations":[{"team_id":"T123","user_id":"U_SECOND_BOT","is_bot":true}],
		"event":{
			"type":"app_mention",
			"user":"U123",
			"text":"<@U_SECOND_BOT> run",
			"channel":"CSECOND",
			"channel_type":"channel",
			"ts":"555.111",
			"team":"T123"
		}
	}`
	response := requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		body,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(body, "second-signing-secret"),
	)
	if response["ok"] != "accepted" {
		t.Fatalf("second app event response=%v want accepted", response)
	}
	integrationTarget, err := fixture.Project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		fixture.Project.ProjectUUID,
		secondInstall.ID,
		"CSECOND:555.111",
	)
	if err != nil {
		t.Fatalf("get second app integration target: %v", err)
	}
	_, err = fixture.Project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
		"CSECOND:555.111",
	)
	if !storeerr.IsNotFound(err) {
		t.Fatalf("second app integration was visible under first install, err=%v", err)
	}
	interaction := createPermissionInteractionForAgent(
		t,
		ctx,
		fixture.Project.Store,
		fixture.Project,
		integrationTarget.AgentID,
		"slack-events-multiple-apps",
	)
	actionBody := slackActionFormBody(t, slackActionPayloadInput{
		Install:             secondInstall,
		AgentID:             integrationTarget.AgentID,
		IntegrationTargetID: integrationTarget.ID,
		InteractionID:       interaction.ID,
		UserID:              "U_ACTION",
		OptionValue:         strconv.Itoa(toolpermission.AllowOptionIndex),
	})
	response = requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationActionsPath,
		actionBody,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(actionBody, "second-signing-secret"),
	)
	if response["ok"] != "resolved" {
		t.Fatalf("second app action response=%v want resolved", response)
	}
	resolved, found, err := fixture.Project.Store.Execution().GetAgentInteraction(
		ctx,
		fixture.Project.ProjectUUID,
		integrationTarget.AgentID,
		interaction.ID,
	)
	if err != nil {
		t.Fatalf("get second app resolved interaction: %v", err)
	}
	if !found || resolved.State != executionstore.AgentInteractionStateResolved {
		t.Fatalf("resolved interaction found=%v record=%+v", found, resolved)
	}
	uninstall := `{
		"type":"event_callback",
		"team_id":"T123",
		"api_app_id":"A_SECOND",
		"event_id":"Ev-second-app-uninstalled",
		"authorizations":[{"team_id":"T123","user_id":"U_ADMIN","is_bot":false}],
		"event":{"type":"app_uninstalled"}
	}`
	response = requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		uninstall,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(uninstall, "second-signing-secret"),
	)
	if response["ok"] != "disabled" {
		t.Fatalf("second app uninstall response=%v want disabled", response)
	}
	first, err := fixture.Project.Store.Integrations().GetIntegrationInstall(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
	)
	if err != nil {
		t.Fatalf("get first install: %v", err)
	}
	second, err := fixture.Project.Store.Integrations().GetIntegrationInstall(
		ctx,
		fixture.Project.ProjectUUID,
		secondInstall.ID,
	)
	if err != nil {
		t.Fatalf("get second install: %v", err)
	}
	if first.State != integrationstore.IntegrationInstallStateActive ||
		second.State != integrationstore.IntegrationInstallStateDisabled {
		t.Fatalf("install statees: first=%q second=%q", first.State, second.State)
	}
}

func TestSlackEventsRejectWrongSignedIdentity(t *testing.T) {
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
		profile["id"].(string),
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
	if _, err := project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		project.ProjectUUID,
		installB.ID,
		"C222:111.222",
	); err != nil {
		t.Fatalf("get install B integration target: %v", err)
	}
	_, err := project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		project.ProjectUUID,
		installA.ID,
		"C222:111.222",
	)
	if !storeerr.IsNotFound(err) {
		t.Fatalf("install A should not receive install B event, err=%v", err)
	}
}

func TestSlackEventsIgnoreRemoteAndBotEvents(t *testing.T) {
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
		"slack-events-ignore",
	)

	remote := `{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
		`"event_id":"Ev-remote-1","authorizations":[{"team_id":"T123",` +
		`"user_id":"U_BOT","is_bot":true}],"event":{"type":"app_mention",` +
		`"user":"U_REMOTE","text":"<@U_BOT> run","channel":"C123",` +
		`"channel_type":"channel","ts":"111.222","team":"T_REMOTE"}}`
	requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		remote,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(remote, "signing-secret"),
	)
	_, err := fixture.Project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
		"C123:111.222",
	)
	if !storeerr.IsNotFound(err) {
		t.Fatalf(
			"remote event should not create integration target, err=%v",
			err,
		)
	}

	bot := `{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
		`"event_id":"Ev-bot-1","authorizations":[{"team_id":"T123","user_id":"U_BOT",` +
		`"is_bot":true}],"event":{"type":"message","user":"U_BOT","text":"bot reply",` +
		`"channel":"D123","channel_type":"im","ts":"222.333","team":"T123"}}`
	requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		bot,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(bot, "signing-secret"),
	)
	_, err = fixture.Project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
		"D123",
	)
	if !storeerr.IsNotFound(err) {
		t.Fatalf("bot event should not create integration target, err=%v", err)
	}
}

func TestSlackEventsDMCreatesInputAndTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	reactionForms := make(chan map[string]string, 1)
	slackServer := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/oauth.v2.access":
				writeJSON(
					w,
					http.StatusOK,
					slackOAuthTestResponse("xoxb-events-token"),
				)
			case "/users.info", "/conversations.info":
				writeSlackLookupTestResponse(t, w, r)
			case "/reactions.add":
				if err := r.ParseForm(); err != nil {
					t.Fatalf("parse reaction form: %v", err)
				}
				reactionForms <- map[string]string{
					"authorization": r.Header.Get("Authorization"),
					"channel":       r.Form.Get("channel"),
					"timestamp":     r.Form.Get("timestamp"),
					"name":          r.Form.Get("name"),
				}
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			default:
				t.Fatalf("unexpected slack test path %s", r.URL.Path)
			}
		}),
	)
	defer slackServer.Close()
	fixture := newSlackEventsIntegrationFixture(
		t,
		ctx,
		pool,
		slackServer,
		"slack-events-dm",
	)
	body := `{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
		`"event_id":"Ev-dm-1","authorizations":[{"team_id":"T123","user_id":"U_BOT",` +
		`"is_bot":true}],"event":{"type":"message","user":"U123","text":"hello from ` +
		`dm","channel":"D123","channel_type":"im","ts":"111.222","team":"T123",` +
		`"user_profile":{"display_name":"Asher"}}}`
	requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		body,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(body, "signing-secret"),
	)
	integrationTarget, err := fixture.Project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
		"D123",
	)
	if err != nil {
		t.Fatalf("get dm integration target: %v", err)
	}
	if string(integrationTarget.ProviderMetadata) != "{}" {
		t.Fatalf("dm integration target metadata = %s want {}", integrationTarget.ProviderMetadata)
	}
	agent, err := fixture.Project.Store.Execution().GetAgentInProject(
		ctx,
		fixture.Project.ProjectUUID,
		integrationTarget.AgentID,
	)
	if err != nil {
		t.Fatalf("get dm agent: %v", err)
	}
	if agent.IntegrationTargetID != integrationTarget.ID {
		t.Fatalf(
			"integration target target = %s want %s",
			agent.IntegrationTargetID,
			integrationTarget.ID,
		)
	}
	var reactionForm map[string]string
	select {
	case reactionForm = <-reactionForms:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for slack reaction")
	}
	if reactionForm["authorization"] != "Bearer xoxb-events-token" ||
		reactionForm["channel"] != "D123" ||
		reactionForm["timestamp"] != "111.222" ||
		reactionForm["name"] != slack.InboundReaction {
		t.Fatalf("unexpected reaction form: %+v", reactionForm)
	}
}

func TestSlackEventsDMFileCreatesArtifactInput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	var slackServer *httptest.Server
	slackServer = httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/oauth.v2.access":
				writeJSON(
					w,
					http.StatusOK,
					slackOAuthTestResponse("xoxb-events-token"),
				)
			case "/users.info", "/conversations.info":
				writeSlackLookupTestResponse(t, w, r)
			case "/files.info":
				if err := r.ParseForm(); err != nil {
					t.Fatalf("parse files.info form: %v", err)
				}
				if r.Header.Get("Authorization") != "Bearer xoxb-events-token" ||
					r.Form.Get("file") != "F123" {
					t.Fatalf("unexpected files.info request auth=%q form=%v", r.Header.Get("Authorization"), r.Form)
				}
				writeJSON(w, http.StatusOK, map[string]any{
					"ok": true,
					"file": map[string]any{
						"id":                   "F123",
						"name":                 "pixel.png",
						"mimetype":             "image/png",
						"size":                 len(testPNGBytes),
						"url_private_download": slackServer.URL + "/files/pixel.png",
					},
				})
			case "/files/pixel.png":
				if r.Header.Get("Authorization") != "Bearer xoxb-events-token" {
					t.Fatalf("file download authorization = %q", r.Header.Get("Authorization"))
				}
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(testPNGBytes)
			case "/reactions.add":
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			default:
				t.Fatalf("unexpected slack test path %s", r.URL.Path)
			}
		}),
	)
	defer slackServer.Close()
	fixture := newSlackEventsIntegrationFixture(
		t,
		ctx,
		pool,
		slackServer,
		"slack-events-dm-file",
	)
	body := `{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
		`"event_id":"Ev-dm-file","authorizations":[{"team_id":"T123","user_id":"U_BOT",` +
		`"is_bot":true}],"event":{"type":"message","subtype":"file_share","user":"U123",` +
		`"text":"what is this?","channel":"D_FILE","channel_type":"im","ts":"111.222",` +
		`"team":"T123","user_profile":{"display_name":"Asher"},"files":[` +
		`{"id":"F123","name":"stub","file_access":"check_file_info"}` +
		`]}}`
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
	if response["ok"] != "accepted" {
		t.Fatalf("dm file response=%v want accepted", response)
	}
	integrationTarget, err := fixture.Project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
		"D_FILE",
	)
	if err != nil {
		t.Fatalf("get dm file integration target: %v", err)
	}
	input, found, err := fixture.Project.Store.Execution().GetIntegrationTargetInputByIdempotency(
		ctx,
		executionstore.GetIntegrationTargetInputByIdempotencyInput{
			IntegrationInstallID: fixture.Install.ID,
			IntegrationTargetID:  integrationTarget.ID,
			IdempotencyKey: slackCurrentEventKey(slack.EventsEnvelope{
				TeamID: "T123",
				Event: slack.Event{
					Channel: "D_FILE",
					TS:      "111.222",
					Files:   []slack.File{{ID: "F123"}},
				},
			}),
		},
	)
	if err != nil {
		t.Fatalf("get dm file input: %v", err)
	} else if !found {
		t.Fatal("dm file input missing")
	}
	assertAgentInputText(
		t,
		ctx,
		pool,
		input,
		"<@U123> (Ada) in Slack DM:\nwhat is this?",
		[]bool{true, false},
	)
	artifactID := assertAgentInputArtifactBlock(t, ctx, pool, input)
	content, artifact, err := fixture.Project.Store.Artifacts().GetArtifactBlob(
		ctx,
		fixture.Project.ProjectUUID,
		integrationTarget.AgentID,
		artifactID,
	)
	if err != nil {
		t.Fatalf("get slack artifact blob: %v", err)
	}
	if artifact.ContentType != "image/png" || artifact.Filename != "pixel.png" {
		t.Fatalf("artifact metadata=%+v want image/png pixel.png", artifact)
	}
	if string(content) != string(testPNGBytes) {
		t.Fatalf("artifact content mismatch")
	}
}

func TestSlackEventsSkippedFileCreatesInputWithoutArtifact(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	slackServer := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/oauth.v2.access":
				writeJSON(
					w,
					http.StatusOK,
					slackOAuthTestResponse("xoxb-events-token"),
				)
			case "/users.info", "/conversations.info":
				writeSlackLookupTestResponse(t, w, r)
			case "/reactions.add":
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			default:
				t.Fatalf("unexpected slack test path %s", r.URL.Path)
			}
		}),
	)
	defer slackServer.Close()
	fixture := newSlackEventsIntegrationFixture(
		t,
		ctx,
		pool,
		slackServer,
		"slack-events-skipped-file",
	)
	body := `{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
		`"event_id":"Ev-skipped-file","authorizations":[{"team_id":"T123","user_id":"U_BOT",` +
		`"is_bot":true}],"event":{"type":"message","subtype":"file_share","user":"U123",` +
		`"text":"please inspect","channel":"D_SKIP","channel_type":"im","ts":"111.333",` +
		`"team":"T123","files":[{"id":"F_LARGE","name":"large.png","mimetype":"image/png",` +
		`"size":` + strconv.Itoa(maxAttachmentBytes+1) + `,` +
		`"url_private_download":"` + slackServer.URL + `/files/large.png"}]}}`
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
	if response["ok"] != "accepted" {
		t.Fatalf("skipped file response=%v want accepted", response)
	}
	integrationTarget, err := fixture.Project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
		"D_SKIP",
	)
	if err != nil {
		t.Fatalf("get skipped file integration target: %v", err)
	}
	input, found, err := fixture.Project.Store.Execution().GetIntegrationTargetInputByIdempotency(
		ctx,
		executionstore.GetIntegrationTargetInputByIdempotencyInput{
			IntegrationInstallID: fixture.Install.ID,
			IntegrationTargetID:  integrationTarget.ID,
			IdempotencyKey: slackCurrentEventKey(slack.EventsEnvelope{
				TeamID: "T123",
				Event: slack.Event{
					Channel: "D_SKIP",
					TS:      "111.333",
					Files:   []slack.File{{ID: "F_LARGE"}},
				},
			}),
		},
	)
	if err != nil {
		t.Fatalf("get skipped file input: %v", err)
	} else if !found {
		t.Fatal("skipped file input missing")
	}
	assertAgentInputText(
		t,
		ctx,
		pool,
		input,
		"<@U123> (Ada) in Slack DM:\nplease inspect\nSlack files not included:\n- large.png skipped: too large",
		[]bool{true, false, false},
	)
	var artifactBlocks int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::bigint
			FROM content_blocks block
			JOIN agent_inputs input
			  ON input.agent_id = block.agent_id
			 AND input.id = block.owner_agent_input_id
			WHERE input.project_id = $1
			  AND block.agent_id = $2
			  AND block.owner_agent_input_id = $3
			  AND block.block_kind = 'artifact'
	`, input.ProjectID, input.AgentID, input.ID).Scan(&artifactBlocks); err != nil {
		t.Fatalf("count skipped file artifact blocks: %v", err)
	}
	if artifactBlocks != 0 {
		t.Fatalf("skipped file artifact blocks = %d, want 0", artifactBlocks)
	}
	var metadata map[string]any
	if err := json.Unmarshal(input.Metadata, &metadata); err != nil {
		t.Fatalf("unmarshal skipped file metadata: %v", err)
	}
	files, ok := metadata["files"].([]any)
	if !ok || len(files) != 1 {
		t.Fatalf("metadata files = %#v, want one skipped file", metadata["files"])
	}
	file, ok := files[0].(map[string]any)
	if !ok {
		t.Fatalf("file metadata = %#v", files[0])
	}
	if file["id"] != "F_LARGE" ||
		file["name"] != "large.png" ||
		file["status"] != slack.EventFileStatusSkipped ||
		file["reason"] != "too_large" ||
		file["ordinal"] != float64(0) {
		t.Fatalf("file metadata = %#v, want skipped F_LARGE too_large", file)
	}
	if _, ok := file["content"]; ok {
		t.Fatalf("file metadata serialized content: %#v", file)
	}
}

func TestSlackEventsDelayedFileShareCreatesNeutralArtifactInput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	var slackServer *httptest.Server
	slackServer = httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/oauth.v2.access":
				writeJSON(
					w,
					http.StatusOK,
					slackOAuthTestResponse("xoxb-events-token"),
				)
			case "/users.info", "/conversations.info":
				writeSlackLookupTestResponse(t, w, r)
			case "/files/delayed.png":
				if r.Header.Get("Authorization") != "Bearer xoxb-events-token" {
					t.Fatalf("file download authorization = %q", r.Header.Get("Authorization"))
				}
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(testPNGBytes)
			case "/conversations.history", "/conversations.replies":
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "messages": []any{}})
			case "/reactions.add":
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			default:
				t.Fatalf("unexpected slack test path %s", r.URL.Path)
			}
		}),
	)
	defer slackServer.Close()
	fixture := newSlackEventsIntegrationFixture(
		t,
		ctx,
		pool,
		slackServer,
		"slack-events-delayed-file",
	)
	mention := `{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
		`"event_id":"Ev-delayed-mention","authorizations":[{"team_id":"T123",` +
		`"user_id":"U_BOT","is_bot":true}],"event":{"type":"app_mention","user":"U123",` +
		`"text":"<@U_BOT> inspect this","channel":"CDELAY","channel_type":"channel",` +
		`"ts":"222.333","team":"T123"}}`
	requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		mention,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(mention, "signing-secret"),
	)
	integrationTarget, err := fixture.Project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
		"CDELAY:222.333",
	)
	if err != nil {
		t.Fatalf("get delayed file integration target: %v", err)
	}
	fileShare := `{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
		`"event_id":"Ev-delayed-file","authorizations":[{"team_id":"T123",` +
		`"user_id":"U_BOT","is_bot":true}],"event":{"type":"message","subtype":"file_share",` +
		`"user":"U123","text":"inspect this","channel":"CDELAY",` +
		`"channel_type":"channel","ts":"222.333","team":"T123","files":[` +
		`{"id":"F_DELAY","name":"delayed.png","mimetype":"image/png","size":` +
		strconv.Itoa(len(testPNGBytes)) + `,"url_private_download":"` + slackServer.URL + `/files/delayed.png"}` +
		`]}}`
	response := requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		fileShare,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(fileShare, "signing-secret"),
	)
	if response["ok"] != "accepted" {
		t.Fatalf("delayed file response=%v want accepted", response)
	}
	input, found, err := fixture.Project.Store.Execution().GetIntegrationTargetInputByIdempotency(
		ctx,
		executionstore.GetIntegrationTargetInputByIdempotencyInput{
			IntegrationInstallID: fixture.Install.ID,
			IntegrationTargetID:  integrationTarget.ID,
			IdempotencyKey: slackCurrentEventKey(slack.EventsEnvelope{
				TeamID: "T123",
				Event: slack.Event{
					Channel: "CDELAY",
					TS:      "222.333",
					Files:   []slack.File{{ID: "F_DELAY"}},
				},
			}),
		},
	)
	if err != nil {
		t.Fatalf("get delayed file input: %v", err)
	} else if !found {
		t.Fatal("delayed file input missing")
	}
	assertAgentInputText(
		t,
		ctx,
		pool,
		input,
		"This Slack thread may include multiple participants, and not every message is necessarily directed at you. Use your judgment to decide whether to call `send_integration_message` at all.\n\n"+
			"<@U123> (Ada) in <#CDELAY> (#delayed), thread 222.333:\n"+
			"Files for the previous Slack message.",
		[]bool{true, true},
	)
	assertAgentInputArtifactBlock(t, ctx, pool, input)
	response = requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		fileShare,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(fileShare, "signing-secret"),
	)
	if response["ok"] != "accepted" {
		t.Fatalf("delayed file replay response=%v want accepted", response)
	}
	var inputCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::int
		FROM agent_inputs
		WHERE project_id = $1
		  AND agent_id = $2
		  AND integration_target_id = $3
	`, fixture.Project.ProjectUUID, integrationTarget.AgentID, integrationTarget.ID).Scan(&inputCount); err != nil {
		t.Fatalf("count delayed file inputs: %v", err)
	}
	if inputCount != 2 {
		t.Fatalf("agent inputs = %d want 2", inputCount)
	}
}

func TestSlackEventsFileShareBeforeAppMentionSuppressesMentionDuplicate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	var slackServer *httptest.Server
	slackServer = httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/oauth.v2.access":
				writeJSON(
					w,
					http.StatusOK,
					slackOAuthTestResponse("xoxb-events-token"),
				)
			case "/users.info", "/conversations.info":
				writeSlackLookupTestResponse(t, w, r)
			case "/files/first.png":
				if r.Header.Get("Authorization") != "Bearer xoxb-events-token" {
					t.Fatalf("file download authorization = %q", r.Header.Get("Authorization"))
				}
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(testPNGBytes)
			case "/conversations.history", "/conversations.replies":
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "messages": []any{}})
			case "/reactions.add":
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			default:
				t.Fatalf("unexpected slack test path %s", r.URL.Path)
			}
		}),
	)
	defer slackServer.Close()
	fixture := newSlackEventsIntegrationFixture(
		t,
		ctx,
		pool,
		slackServer,
		"slack-events-file-before-mention",
	)
	fileShare := `{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
		`"event_id":"Ev-file-before-mention-file","authorizations":[{"team_id":"T123",` +
		`"user_id":"U_BOT","is_bot":true}],"event":{"type":"message","subtype":"file_share",` +
		`"user":"U123","text":"<@U_BOT> inspect this","channel":"CFILEFIRST",` +
		`"channel_type":"channel","ts":"222.333","team":"T123","files":[` +
		`{"id":"F_FIRST","name":"first.png","mimetype":"image/png","size":` +
		strconv.Itoa(len(testPNGBytes)) + `,"url_private_download":"` + slackServer.URL + `/files/first.png"}` +
		`]}}`
	response := requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		fileShare,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(fileShare, "signing-secret"),
	)
	if response["ok"] != "accepted" {
		t.Fatalf("file share response=%v want accepted", response)
	}
	integrationTarget, err := fixture.Project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
		"CFILEFIRST:222.333",
	)
	if err != nil {
		t.Fatalf("get file-first integration target: %v", err)
	}
	appMention := `{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
		`"event_id":"Ev-file-before-mention-app","authorizations":[{"team_id":"T123",` +
		`"user_id":"U_BOT","is_bot":true}],"event":{"type":"app_mention","user":"U123",` +
		`"text":"<@U_BOT> inspect this","channel":"CFILEFIRST","channel_type":"channel",` +
		`"ts":"222.333","team":"T123"}}`
	response = requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		appMention,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(appMention, "signing-secret"),
	)
	if response["ok"] != "accepted" {
		t.Fatalf("app mention duplicate response=%v want accepted", response)
	}
	var inputCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)::int
		FROM agent_inputs
		WHERE project_id = $1
		  AND agent_id = $2
		  AND integration_target_id = $3
	`, fixture.Project.ProjectUUID, integrationTarget.AgentID, integrationTarget.ID).Scan(&inputCount); err != nil {
		t.Fatalf("count file-first inputs: %v", err)
	}
	if inputCount != 1 {
		t.Fatalf("agent inputs = %d want 1", inputCount)
	}
}

func TestSlackEventsMentionedThreadFileShareCreatesArtifactInput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	var slackServer *httptest.Server
	slackServer = httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/oauth.v2.access":
				writeJSON(
					w,
					http.StatusOK,
					slackOAuthTestResponse("xoxb-events-token"),
				)
			case "/users.info", "/conversations.info":
				writeSlackLookupTestResponse(t, w, r)
			case "/files/thread.png":
				if r.Header.Get("Authorization") != "Bearer xoxb-events-token" {
					t.Fatalf("file download authorization = %q", r.Header.Get("Authorization"))
				}
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(testPNGBytes)
			case "/conversations.replies":
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "messages": []any{}})
			case "/reactions.add":
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			default:
				t.Fatalf("unexpected slack test path %s", r.URL.Path)
			}
		}),
	)
	defer slackServer.Close()
	fixture := newSlackEventsIntegrationFixture(
		t,
		ctx,
		pool,
		slackServer,
		"slack-events-mentioned-thread-file",
	)
	body := `{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
		`"event_id":"Ev-mentioned-thread-file","authorizations":[{"team_id":"T123",` +
		`"user_id":"U_BOT","is_bot":true}],"event":{"type":"message","subtype":"file_share",` +
		`"user":"U123","text":"<@U_BOT> inspect this","channel":"CTHREADFILE",` +
		`"channel_type":"channel","ts":"333.444","thread_ts":"111.222","team":"T123",` +
		`"files":[{"id":"F_THREAD","name":"thread.png","mimetype":"image/png","size":` +
		strconv.Itoa(len(testPNGBytes)) + `,"url_private_download":"` + slackServer.URL + `/files/thread.png"}]}}`
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
	if response["ok"] != "accepted" {
		t.Fatalf("mentioned thread file response=%v want accepted", response)
	}
	integrationTarget, err := fixture.Project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
		"CTHREADFILE:111.222",
	)
	if err != nil {
		t.Fatalf("get mentioned thread file integration: %v", err)
	}
	input, found, err := fixture.Project.Store.Execution().GetIntegrationTargetInputByIdempotency(
		ctx,
		executionstore.GetIntegrationTargetInputByIdempotencyInput{
			IntegrationInstallID: fixture.Install.ID,
			IntegrationTargetID:  integrationTarget.ID,
			IdempotencyKey: slackCurrentEventKey(slack.EventsEnvelope{
				TeamID: "T123",
				Event: slack.Event{
					Channel: "CTHREADFILE",
					TS:      "333.444",
					Files:   []slack.File{{ID: "F_THREAD"}},
				},
			}),
		},
	)
	if err != nil {
		t.Fatalf("get mentioned thread file input: %v", err)
	} else if !found {
		t.Fatal("mentioned thread file input missing")
	}
	assertAgentInputText(
		t,
		ctx,
		pool,
		input,
		"This message was routed to a Slack thread attached to this agent.\n\n"+
			"<@U123> (Ada) in <#CTHREADFILE> (#thread-file), thread 111.222:\n"+
			"<@U_BOT> (Omnara) inspect this",
		[]bool{true, false},
	)
	artifactID := assertAgentInputArtifactBlock(t, ctx, pool, input)
	if _, _, err := fixture.Project.Store.Artifacts().GetArtifactBlob(
		ctx,
		fixture.Project.ProjectUUID,
		integrationTarget.AgentID,
		artifactID,
	); err != nil {
		t.Fatalf("get mentioned thread file artifact: %v", err)
	}
}

func TestSlackEventsUnmappedRootFileShareIgnored(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	slackServer := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/oauth.v2.access":
				writeJSON(
					w,
					http.StatusOK,
					slackOAuthTestResponse("xoxb-events-token"),
				)
			case "/users.info", "/conversations.info":
				writeSlackLookupTestResponse(t, w, r)
			default:
				t.Fatalf("unexpected slack test path %s", r.URL.Path)
			}
		}),
	)
	defer slackServer.Close()
	fixture := newSlackEventsIntegrationFixture(
		t,
		ctx,
		pool,
		slackServer,
		"slack-events-unmapped-root-file",
	)
	body := `{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
		`"event_id":"Ev-unmapped-root-file","authorizations":[{"team_id":"T123",` +
		`"user_id":"U_BOT","is_bot":true}],"event":{"type":"message","subtype":"file_share",` +
		`"user":"U123","text":"not for bot","channel":"CUNMAPPED","channel_type":"channel",` +
		`"ts":"111.222","team":"T123","files":[{"id":"F_UNMAPPED","name":"ignored.png"}]}}`
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
	if response["ok"] != "ignored" {
		t.Fatalf("unmapped root file response=%v want ignored", response)
	}
	if _, err := fixture.Project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
		"CUNMAPPED:111.222",
	); !storeerr.IsNotFound(err) {
		t.Fatalf("unmapped root file integration err=%v want not found", err)
	}
}

func TestSlackEventsReactionFailureStillAcceptsInput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	reactionAttempts := make(chan struct{}, 1)
	slackServer := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/oauth.v2.access":
				writeJSON(
					w,
					http.StatusOK,
					slackOAuthTestResponse("xoxb-events-token"),
				)
			case "/users.info", "/conversations.info":
				writeSlackLookupTestResponse(t, w, r)
			case "/reactions.add":
				reactionAttempts <- struct{}{}
				writeJSON(
					w,
					http.StatusOK,
					map[string]any{"ok": false, "error": "missing_scope"},
				)
			default:
				t.Fatalf("unexpected slack test path %s", r.URL.Path)
			}
		}),
	)
	defer slackServer.Close()
	fixture := newSlackEventsIntegrationFixture(
		t,
		ctx,
		pool,
		slackServer,
		"slack-events-reaction-failure",
	)

	body := `{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
		`"event_id":"Ev-reaction-failure","authorizations":[{"team_id":"T123",` +
		`"user_id":"U_BOT","is_bot":true}],"event":{"type":"message","user":"U123",` +
		`"text":"hello from dm","channel":"D123","channel_type":"im","ts":"111.222",` +
		`"team":"T123","user_profile":{"display_name":"Asher"}}}`
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
	if response["ok"] != "accepted" {
		t.Fatalf("event response=%v want accepted", response)
	}
	select {
	case <-reactionAttempts:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for slack reaction attempt")
	}
	integrationTarget, err := fixture.Project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
		"D123",
	)
	if err != nil {
		t.Fatalf("get dm integration target: %v", err)
	}
	if _, found, err := fixture.Project.Store.Execution().GetIntegrationTargetInputByIdempotency(
		ctx,
		executionstore.GetIntegrationTargetInputByIdempotencyInput{
			IntegrationInstallID: fixture.Install.ID,
			IntegrationTargetID:  integrationTarget.ID,
			IdempotencyKey:       "slack:message:T123:D123:111.222",
		},
	); err != nil {
		t.Fatalf("get integration input: %v", err)
	} else if !found {
		t.Fatal("reaction failure event did not create input")
	}
}

func TestSlackEventsThreadStartHistoryFetchBoundsBeforeTrigger(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	var historyForm map[string]string
	historyCalls := 0
	slackServer := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/oauth.v2.access":
				writeJSON(
					w,
					http.StatusOK,
					slackOAuthTestResponse("xoxb-events-token"),
				)
			case "/conversations.replies":
				historyCalls++
				if err := r.ParseForm(); err != nil {
					t.Fatalf("parse history form: %v", err)
				}
				historyForm = map[string]string{
					"channel":   r.Form.Get("channel"),
					"ts":        r.Form.Get("ts"),
					"latest":    r.Form.Get("latest"),
					"inclusive": r.Form.Get("inclusive"),
					"limit":     r.Form.Get("limit"),
				}
				writeJSON(
					w,
					http.StatusOK,
					map[string]any{
						"ok": true,
						"messages": []any{
							map[string]string{
								"user": "U999",
								"text": "earlier",
								"ts":   "111.100",
							},
						},
					},
				)
			case "/users.info", "/conversations.info":
				writeSlackLookupTestResponse(t, w, r)
			case "/reactions.add":
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			default:
				t.Fatalf("unexpected slack test path %s", r.URL.Path)
			}
		}),
	)
	defer slackServer.Close()
	fixture := newSlackEventsIntegrationFixture(
		t,
		ctx,
		pool,
		slackServer,
		"slack-events-thread-history",
	)
	body := `{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
		`"event_id":"Ev-thread-history","authorizations":[{"team_id":"T123",` +
		`"user_id":"U_BOT","is_bot":true}],"event":{"type":"app_mention","user":"U123",` +
		`"text":"<@U_BOT> use the prior context","channel":"C123",` +
		`"channel_type":"channel","ts":"222.333","thread_ts":"111.222","team":"T123"}}`
	requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		body,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(body, "signing-secret"),
	)
	if historyForm["channel"] != "C123" || historyForm["ts"] != "111.222" ||
		historyForm["latest"] != "222.333" ||
		historyForm["inclusive"] != "false" ||
		historyForm["limit"] != strconv.Itoa(slack.DefaultHistoryContextLimit) {
		t.Fatalf("unexpected conversations.replies form: %+v", historyForm)
	}
	requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		body,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(body, "signing-secret"),
	)
	if historyCalls != 1 {
		t.Fatalf(
			"replayed event fetched history %d times, want 1",
			historyCalls,
		)
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
	input, found, err := fixture.Project.Store.Execution().GetIntegrationTargetInputByIdempotency(
		ctx,
		executionstore.GetIntegrationTargetInputByIdempotencyInput{
			IntegrationInstallID: fixture.Install.ID,
			IntegrationTargetID:  integrationTarget.ID,
			IdempotencyKey:       "slack:message:T123:C123:222.333",
		},
	)
	if err != nil {
		t.Fatalf("get history input: %v", err)
	} else if !found {
		t.Fatal("thread history event input missing")
	}
	assertAgentInputText(
		t,
		ctx,
		pool,
		input,
		"This message directly mentioned the agent inside an existing Slack thread.\n\n"+
			"Recent Slack context:\n"+
			"<@U999> (Grace): earlier\n\n"+
			"<@U123> (Ada) in <#C123> (#general), thread 111.222:\n"+
			"<@U_BOT> (Omnara) use the prior context",
		[]bool{true, false},
	)
}

func TestSlackEventsHistoryRateLimitContinues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	slackServer := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/oauth.v2.access":
				writeJSON(
					w,
					http.StatusOK,
					slackOAuthTestResponse("xoxb-events-token"),
				)
			case "/conversations.history":
				w.WriteHeader(http.StatusTooManyRequests)
			case "/users.info", "/conversations.info":
				writeSlackLookupTestResponse(t, w, r)
			case "/reactions.add":
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			default:
				t.Fatalf("unexpected slack test path %s", r.URL.Path)
			}
		}),
	)
	defer slackServer.Close()
	fixture := newSlackEventsIntegrationFixture(
		t,
		ctx,
		pool,
		slackServer,
		"slack-events-history-rate-limit",
	)
	body := `{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
		`"event_id":"Ev-history-rate-limit","authorizations":[{"team_id":"T123",` +
		`"user_id":"U_BOT","is_bot":true}],"event":{"type":"app_mention","user":"U123",` +
		`"text":"<@U_BOT> run","channel":"C123","channel_type":"channel",` +
		`"ts":"111.222","team":"T123"}}`
	requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		body,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(body, "signing-secret"),
	)
	integrationTarget, err := fixture.Project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
		"C123:111.222",
	)
	if err != nil {
		t.Fatalf("get integration target: %v", err)
	}
	input, found, err := fixture.Project.Store.Execution().GetIntegrationTargetInputByIdempotency(
		ctx,
		executionstore.GetIntegrationTargetInputByIdempotencyInput{
			IntegrationInstallID: fixture.Install.ID,
			IntegrationTargetID:  integrationTarget.ID,
			IdempotencyKey:       "slack:message:T123:C123:111.222",
		},
	)
	if err != nil {
		t.Fatalf("get history input: %v", err)
	}
	if !found {
		t.Fatal("history rate-limit input missing")
	}
	var metadata map[string]any
	if err := json.Unmarshal(input.Metadata, &metadata); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if metadata["history_status"] != "rate_limited" {
		t.Fatalf(
			"history_status=%v want rate_limited metadata=%v",
			metadata["history_status"],
			metadata,
		)
	}
}

func TestSlackEventsLaunchesWithoutExplicitIntegrationSendTool(
	t *testing.T,
) {
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
		"slack-events-missing-send-tool",
	)

	body := `{"type":"event_callback","team_id":"T123","api_app_id":"A123","event_id":"Ev-missing-send-tool","authorizations":[{"team_id":"T123","user_id":"U_BOT","is_bot":true}],"event":{"type":"app_mention","user":"U123","text":"<@U_BOT> run","channel":"C123","channel_type":"channel","ts":"111.222","team":"T123"}}`
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
	if response["ok"] != "accepted" {
		t.Fatalf("response=%v want accepted", response)
	}
	_, err := fixture.Project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
		"C123:111.222",
	)
	if err != nil {
		t.Fatalf("get integration target: %v", err)
	}
}

func TestSlackEventsPostsMessageWhenAgentLaunchFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	postedMessages := make(chan map[string]any, 1)
	slackServer := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/oauth.v2.access":
				writeJSON(
					w,
					http.StatusOK,
					slackOAuthTestResponse("xoxb-events-token"),
				)
			case "/users.info":
				writeSlackLookupTestResponse(t, w, r)
			case "/chat.postMessage":
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatalf("decode slack launch failure message: %v", err)
				}
				postedMessages <- payload
				writeJSON(w, http.StatusOK, map[string]any{
					"ok":      true,
					"channel": "C123",
					"ts":      "222.333",
				})
			default:
				t.Fatalf("unexpected slack test path %s", r.URL.Path)
			}
		}),
	)
	defer slackServer.Close()
	fixture := newSlackEventsIntegrationFixture(
		t,
		ctx,
		pool,
		slackServer,
		"slack-events-launch-failure",
	)

	providerAuthSecret, _, err := fixture.Project.Store.Secrets().CreateSecret(
		ctx,
		secretstore.CreateSecretInput{
			OrgID:     fixture.Project.OrgUUID,
			OwnerKind: secretstore.SecretOwnerOrg,
			Name:      "slack-launch-failure-provider-auth",
			Material:  secrets.GenericMaterial{Value: "test-token"},
			Actor:     httpUserPrincipal(fixture.Project.AdminUserUUID),
		},
	)
	if err != nil {
		t.Fatalf("create machine pool provider auth secret: %v", err)
	}
	one := 1
	memoryMB := 1024
	machinePool, err := fixture.Project.Store.Execution().CreateMachinePool(
		ctx,
		executionstore.CreateMachinePoolInput{
			OrgID:                         fixture.Project.OrgUUID,
			Name:                          "Slack Launch Failure Pool",
			Provider:                      "unikraft",
			DefaultMachineCPU:             &one,
			DefaultMachineMemoryMB:        &memoryMB,
			DefaultMachineEnv:             json.RawMessage(`{}`),
			DefaultMachineProviderOptions: json.RawMessage(`{"image":"test","metro":"sfo"}`),
			ProviderAuthSecretID:          providerAuthSecret.ID,
			MaxTotalMachines:              1,
			MaxTotalCPU:                   &one,
			MaxTotalMemoryMB:              &memoryMB,
			MaxMachineCPU:                 &one,
			MaxMachineMemoryMB:            &memoryMB,
		},
	)
	if err != nil {
		t.Fatalf("create launch failure machine pool: %v", err)
	}
	zero := 0
	if _, err := fixture.Project.Store.Execution().CreateProjectMachinePoolGrant(
		ctx,
		executionstore.CreateProjectMachinePoolGrantInput{
			OrgID:            fixture.Project.OrgUUID,
			ProjectID:        fixture.Project.ProjectUUID,
			MachinePoolID:    machinePool.ID,
			MaxTotalMachines: &zero,
			IdempotencyKey:   "slack-launch-failure-pool-grant",
		},
	); err != nil {
		t.Fatalf("create launch failure pool grant: %v", err)
	}

	profileID, err := publicid.Encode(
		publicid.KindAgentProfile,
		fixture.Install.AgentProfileID,
	)
	if err != nil {
		t.Fatalf("encode profile id: %v", err)
	}
	profile, err := fixture.Project.Store.Execution().GetAgentProfile(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.AgentProfileID,
	)
	if err != nil {
		t.Fatalf("get agent profile: %v", err)
	}
	currentConfigID, err := publicid.Encode(
		publicid.KindAgentConfig,
		profile.CurrentConfigID,
	)
	if err != nil {
		t.Fatalf("encode current config id: %v", err)
	}
	sourceYAML := "instruction: Help the user make progress.\n" +
		"model:\n" +
		"  provider_config: openai-prod\n" +
		"  name: gpt-test\n" +
		"machine_sources:\n" +
		"  - machine_pool_name: Slack Launch Failure Pool\n" +
		"    max_machines: 1\n" +
		"    initial_num_machines: 1\n" +
		"tools:\n" +
		"  send_integration_message: {}\n"
	config := createPublicHTTPAgentConfig(
		t,
		fixture.Handler,
		fixture.Project,
		"slack-events-launch-failure",
		"yaml",
		sourceYAML,
		fixture.Project.AdminToken,
		http.StatusCreated,
	)
	requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		fixture.Project.ProjectPath+"/agent-profiles/"+profileID+"/config",
		`{"config":"`+config["id"].(string)+`","expected_current_config_id":"`+currentConfigID+`"}`,
		"idem-slack-events-launch-failure-config",
		http.StatusOK,
		authHeaders(fixture.Project.AdminToken),
	)

	body := `{"type":"event_callback","team_id":"T123","api_app_id":"A123","event_id":"Ev-launch-failure","authorizations":[{"team_id":"T123","user_id":"U_BOT","is_bot":true}],"event":{"type":"app_mention","user":"U123","text":"<@U_BOT> run","channel":"C123","channel_type":"channel","ts":"111.444","team":"T123"}}`
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
	if response["ok"] != "launch_failed" {
		t.Fatalf("response=%v want launch_failed", response)
	}
	select {
	case message := <-postedMessages:
		if message["channel"] != "C123" || message["thread_ts"] != "111.444" ||
			message["text"] != slackAgentLaunchFailureMessage {
			t.Fatalf("slack launch failure message=%v", message)
		}
		if _, ok := message["metadata"]; ok {
			t.Fatalf("slack launch failure message contains agent metadata: %v", message)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Slack launch failure message")
	}
	if _, err := fixture.Project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
		"C123:111.444",
	); !storeerr.IsNotFound(err) {
		t.Fatalf("launch failure integration target err=%v want not found", err)
	}
	var agentCount int
	idempotencyKey := "integration:" + fixture.Install.Provider + ":" +
		fixture.Install.ID.String() + ":C123:111.444"
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM agents WHERE project_id = $1 AND idempotency_key = $2`,
		fixture.Project.ProjectUUID,
		idempotencyKey,
	).Scan(&agentCount); err != nil {
		t.Fatalf("count failed launch agents: %v", err)
	}
	if agentCount != 0 {
		t.Fatalf("failed launch agent count=%d want 0", agentCount)
	}
}

func TestSlackEventsRejectsNewTargetWhenIntegrationSendToolIsDisabled(
	t *testing.T,
) {
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
		"slack-events-disabled-send-tool",
	)

	profileID, err := publicid.Encode(
		publicid.KindAgentProfile,
		fixture.Install.AgentProfileID,
	)
	if err != nil {
		t.Fatalf("encode profile id: %v", err)
	}
	profile, err := fixture.Project.Store.Execution().GetAgentProfile(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.AgentProfileID,
	)
	if err != nil {
		t.Fatalf("get agent profile: %v", err)
	}
	currentConfigID, err := publicid.Encode(
		publicid.KindAgentConfig,
		profile.CurrentConfigID,
	)
	if err != nil {
		t.Fatalf("encode current config id: %v", err)
	}
	sourceYAML := "instruction: Help the user make progress.\n" +
		"model:\n" +
		"  provider_config: openai-prod\n" +
		"  name: gpt-test\n" +
		"tools:\n" +
		"  send_integration_message:\n" +
		"    enabled: false\n"
	config := createPublicHTTPAgentConfig(
		t,
		fixture.Handler,
		fixture.Project,
		"slack-events-disabled-send-tool",
		"yaml",
		sourceYAML,
		fixture.Project.AdminToken,
		http.StatusCreated,
	)
	requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		fixture.Project.ProjectPath+"/agent-profiles/"+profileID+"/config",
		`{"config":"`+config["id"].(string)+`","expected_current_config_id":"`+currentConfigID+`"}`,
		"idem-slack-events-disabled-send-tool-config",
		http.StatusOK,
		authHeaders(fixture.Project.AdminToken),
	)
	body := `{"type":"event_callback","team_id":"T123","api_app_id":"A123","event_id":"Ev-disabled-send-tool","authorizations":[{"team_id":"T123","user_id":"U_BOT","is_bot":true}],"event":{"type":"app_mention","user":"U123","text":"<@U_BOT> run","channel":"C123","channel_type":"channel","ts":"111.333","team":"T123"}}`
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
	if response["ok"] != "ignored" {
		t.Fatalf("response=%v want ignored", response)
	}
	_, err = fixture.Project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
		"C123:111.333",
	)
	if !storeerr.IsNotFound(err) {
		t.Fatalf("disabled send tool event created integration target, err=%v", err)
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
	mention := `{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
		`"event_id":"Ev-name-update-mention","authorizations":[{"team_id":"T123",` +
		`"user_id":"U_BOT","is_bot":true}],"event":{"type":"app_mention","user":"U123",` +
		`"text":"<@U_BOT> start","channel":"C123","channel_type":"channel",` +
		`"ts":"111.222","team":"T123"}}`
	response := requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		mention,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(mention, "signing-secret"),
	)
	if response["ok"] != "accepted" {
		t.Fatalf("mention response=%v want accepted", response)
	}
	userChange := `{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
		`"event_id":"Ev-name-update-user","authorizations":[{"team_id":"T123",` +
		`"user_id":"U_BOT","is_bot":true}],"event":{"type":"user_profile_changed",` +
		`"user":{"id":"U123","name":"ada","profile":{"display_name":"Ada Lovelace"}}}}`
	response = requestJSONWithHeaders(
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

func TestSlackEventsSlowLookupsStillAcceptInput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	releaseSlowLookups := make(chan struct{})
	blockUntilReleased := func(r *http.Request) {
		select {
		case <-releaseSlowLookups:
		case <-r.Context().Done():
		}
	}
	slackServer := httptest.NewServer(
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
			case "/users.info":
				if err := r.ParseForm(); err != nil {
					t.Fatalf("parse users.info form: %v", err)
				}
				if r.Form.Get("user") != "U_BOT" {
					blockUntilReleased(r)
				}
				writeSlackLookupTestResponse(t, w, r)
			case "/conversations.info":
				blockUntilReleased(r)
				writeSlackLookupTestResponse(t, w, r)
			case "/reactions.add":
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			default:
				t.Fatalf("unexpected slack test path %s", r.URL.Path)
			}
		}),
	)
	defer slackServer.Close()
	defer close(releaseSlowLookups)
	fixture := newSlackEventsIntegrationFixture(
		t,
		ctx,
		pool,
		slackServer,
		"slack-events-slow-lookups",
	)
	body := `{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
		`"event_id":"Ev-slow-lookups","authorizations":[{"team_id":"T123",` +
		`"user_id":"U_BOT","is_bot":true}],"event":{"type":"app_mention","user":"U123",` +
		`"text":"<@U_BOT> hello","channel":"C123","channel_type":"channel",` +
		`"ts":"111.222","team":"T123"}}`
	started := time.Now()
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
	if response["ok"] != "accepted" {
		t.Fatalf("slow lookup response=%v want accepted", response)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("slow lookups delayed event handling by %s", elapsed)
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
	input, found, err := fixture.Project.Store.Execution().GetIntegrationTargetInputByIdempotency(
		ctx,
		executionstore.GetIntegrationTargetInputByIdempotencyInput{
			IntegrationInstallID: fixture.Install.ID,
			IntegrationTargetID:  integrationTarget.ID,
			IdempotencyKey:       "slack:message:T123:C123:111.222",
		},
	)
	if err != nil {
		t.Fatalf("get input: %v", err)
	} else if !found {
		t.Fatal("input missing")
	}
	assertAgentInputText(
		t,
		ctx,
		pool,
		input,
		"The agent was mentioned in a Slack channel, so this message starts a new Slack thread for communicating with the agent.\n\n"+
			"<@U123> in <#C123>, thread 111.222:\n"+
			"<@U_BOT> (Omnara) hello",
		[]bool{true, false},
	)
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
	eventBody := `{
		"type":"event_callback",
		"team_id":"T123",
		"api_app_id":"A123",
		"event_id":"Ev-actions-source",
		"authorizations":[{"team_id":"T123","user_id":"U_BOT","is_bot":true}],
		"event":{
			"type":"app_mention",
			"user":"U123",
			"text":"<@U_BOT> ask",
			"channel":"C123",
			"channel_type":"channel",
			"ts":"111.444",
			"team":"T123"
		}
	}`
	requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		eventBody,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(eventBody, "signing-secret"),
	)
	integrationTarget, err := fixture.Project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
		"C123:111.444",
	)
	if err != nil {
		t.Fatalf("get integration target: %v", err)
	}
	interaction := createQuestionInteractionForAgent(
		t,
		ctx,
		fixture.Project.Store,
		fixture.Project,
		integrationTarget.AgentID,
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
			integrationTarget.AgentID,
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
		integrationTarget.AgentID,
		interaction.ID,
	)
	if err != nil {
		t.Fatalf("get resolved interaction: %v", err)
	}
	if !found || resolved.State != executionstore.AgentInteractionStateResolved ||
		resolved.ResolvedByInputID == storage.NilID {
		t.Fatalf("resolved interaction found=%v record=%+v", found, resolved)
	}
	resolvingActorID, resolvingInputKind := interactionResolvingInput(
		t,
		ctx,
		pool,
		fixture.Project.ProjectUUID,
		integrationTarget.AgentID,
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
	eventBody := `{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
		`"event_id":"Ev-empty-answer-source","authorizations":[{"team_id":"T123",` +
		`"user_id":"U_BOT","is_bot":true}],"event":{"type":"app_mention","user":"U123",` +
		`"text":"<@U_BOT> ask","channel":"CEMPTY","channel_type":"channel",` +
		`"ts":"111.444","team":"T123"}}`
	requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		eventBody,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(eventBody, "signing-secret"),
	)
	integrationTarget, err := fixture.Project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
		"CEMPTY:111.444",
	)
	if err != nil {
		t.Fatalf("get integration target: %v", err)
	}
	interaction := createQuestionInteractionForAgent(
		t,
		ctx,
		fixture.Project.Store,
		fixture.Project,
		integrationTarget.AgentID,
		"slack-actions-empty-answer",
	)
	body := slackActionFormBody(t, slackActionPayloadInput{
		Install:             fixture.Install,
		AgentID:             integrationTarget.AgentID,
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
		integrationTarget.AgentID,
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
	eventBody := `{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
		`"event_id":"Ev-permission-source","authorizations":[{"team_id":"T123",` +
		`"user_id":"U_BOT","is_bot":true}],"event":{"type":"app_mention","user":"U123",` +
		`"text":"<@U_BOT> ask","channel":"CPERMISSION","channel_type":"channel",` +
		`"ts":"111.444","team":"T123"}}`
	requestJSONWithHeaders(
		t,
		fixture.Handler,
		http.MethodPost,
		integrationEventsPath,
		eventBody,
		"",
		http.StatusOK,
		unitSlackSignedHeaders(eventBody, "signing-secret"),
	)
	integrationTarget, err := fixture.Project.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx,
		fixture.Project.ProjectUUID,
		fixture.Install.ID,
		"CPERMISSION:111.444",
	)
	if err != nil {
		t.Fatalf("get integration target: %v", err)
	}
	interaction := createPermissionInteractionForAgent(
		t,
		ctx,
		fixture.Project.Store,
		fixture.Project,
		integrationTarget.AgentID,
		"slack-actions-permission",
	)
	body := slackActionFormBody(t, slackActionPayloadInput{
		Install:             fixture.Install,
		AgentID:             integrationTarget.AgentID,
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
		integrationTarget.AgentID,
		interaction.ID,
	)
	if err != nil {
		t.Fatalf("get interaction: %v", err)
	}
	if !found || resolved.State != executionstore.AgentInteractionStateResolved ||
		resolved.ResolvedByInputID == storage.NilID {
		t.Fatalf("interaction=%+v found=%v", resolved, found)
	}
	resolvingActorID, _ := interactionResolvingInput(
		t,
		ctx,
		pool,
		fixture.Project.ProjectUUID,
		integrationTarget.AgentID,
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
	handler := newIntegrationServerWithStoreOptions(
		pool,
		[]storage.Option{storage.WithBlobStore(integrationblob.MustOpen(t, ctx))},
		WithPublicURL("https://omnara.test"),
		WithSlackOAuth(
			SlackOAuthConfig{
				AccessURL:  slackServer.URL + "/oauth.v2.access",
				APIURL:     slackServer.URL,
				HTTPClient: client,
			},
		),
	)
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
		profile["id"].(string),
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
			case "/reactions.add":
				if reactionAttempts != nil {
					reactionAttempts <- struct{}{}
				}
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			default:
				t.Fatalf("unexpected slack test path %s", r.URL.Path)
			}
		}),
	)
}

func writeSlackLookupTestResponse(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	if err := r.ParseForm(); err != nil {
		t.Fatalf("parse slack lookup form: %v", err)
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
		t.Fatalf("unexpected slack lookup path %s", r.URL.Path)
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

func assertAgentInputText(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	input executionstore.AgentInputRecord,
	want string,
	wantHidden []bool,
) {
	t.Helper()
	var got string
	var hidden []bool
	if err := pool.QueryRow(ctx, `
		SELECT coalesce(string_agg(text_content, '' ORDER BY ordinal), ''),
		       array_agg(coalesce(block.metadata->>'omnara_hidden' = 'true', false) ORDER BY ordinal)
			FROM content_blocks block
			JOIN agent_inputs owner
			  ON owner.agent_id = block.agent_id
			 AND owner.id = block.owner_agent_input_id
			WHERE owner.project_id = $1
			  AND block.agent_id = $2
			  AND block.owner_agent_input_id = $3
			  AND block.block_kind = 'text'
	`, input.ProjectID, input.AgentID, input.ID).Scan(&got, &hidden); err != nil {
		t.Fatalf("load agent input text content: %v", err)
	}
	if len(hidden) == 0 {
		t.Fatal("agent input has no text blocks")
	}
	if got != want {
		t.Fatalf("agent input text =\n%s\nwant\n%s", got, want)
	}
	if !slices.Equal(hidden, wantHidden) {
		t.Fatalf("agent input text block visibility = %v, want %v", hidden, wantHidden)
	}
}

func assertAgentInputArtifactBlock(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	input executionstore.AgentInputRecord,
) storage.ID {
	t.Helper()
	var artifactID storage.ID
	var blocks int64
	if err := pool.QueryRow(ctx, `
		SELECT artifact_id, count(*) OVER ()::bigint
			FROM content_blocks block
			JOIN agent_inputs owner
			  ON owner.agent_id = block.agent_id
			 AND owner.id = block.owner_agent_input_id
			WHERE owner.project_id = $1
			  AND block.agent_id = $2
			  AND block.owner_agent_input_id = $3
			  AND block.block_kind = 'artifact'
		ORDER BY ordinal
	`, input.ProjectID, input.AgentID, input.ID).Scan(&artifactID, &blocks); err != nil {
		t.Fatalf("load agent input artifact block: %v", err)
	}
	if blocks != 1 {
		t.Fatalf("agent input artifact blocks = %d, want 1", blocks)
	}
	return artifactID
}

func createSlackHTTPInstall(
	t *testing.T,
	ctx context.Context,
	project publicHTTPProject,
	profileID storage.ID,
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
	install, err := project.Store.Integrations().UpsertIntegrationInstall(
		ctx,
		integrationstore.UpsertIntegrationInstallInput{
			OrgID:              project.OrgUUID,
			ProjectID:          project.ProjectUUID,
			AgentProfileID:     profileID,
			InstalledByUserID:  project.AdminUserUUID,
			Provider:           integrationstore.IntegrationProviderSlack,
			IntegrationKind:    slack.IntegrationKindAgentProfile,
			ConnectionMode:     slack.ConnectionModeWebhook,
			State:              integrationstore.IntegrationInstallStateActive,
			ProviderTenantID:   workspaceID,
			ProviderAccountRef: appID,
			CredentialSecretID: credentialSecret,
			ProviderIdentity:   json.RawMessage(fmt.Sprintf(`{"bot_user_id":%q}`, botUserID)),
			ProviderMetadata:   json.RawMessage(`{}`),
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
) storage.ID {
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
	AgentID             storage.ID
	IntegrationTargetID storage.ID
	InteractionID       storage.ID
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

func slackCurrentEventKey(envelope slack.EventsEnvelope) string {
	key, _ := slack.InputIdempotencyKeyPair(envelope)
	return key
}

func createQuestionInteractionForAgent(
	t *testing.T,
	ctx context.Context,
	store *storage.Store,
	project publicHTTPProject,
	agentID storage.ID,
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
	agentID storage.ID,
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
	agentID storage.ID,
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
		[]storage.ID{admitted.Inputs[0].ID},
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
