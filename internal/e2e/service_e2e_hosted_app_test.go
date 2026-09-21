//go:build integration && servicee2e

package e2e

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/crontrigger"
	"github.com/omnara-ai/omnara/internal/harness/kernel"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/harness/worker"
	"github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprovider"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/integrationredis"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceE2EHostedSlackCronPostFollowsReply(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	for _, key := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "OPENROUTER_API_KEY"} {
		t.Setenv(key, "service-e2e-test-key")
	}
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "hosted-slack-cron")
	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPI(t, ctx, "hosted-slack-cron", "openai-prod", "service-e2e-local")
	keyWrapper, err := secrets.NewLocalKeyWrapper("e2e-local", map[string][]byte{
		"e2e-local": []byte("0123456789abcdef0123456789abcdef"),
	})
	require.NoError(t, err)
	store := storage.NewStore(env.db, storage.WithSecretKeyWrapper(keyWrapper))
	app := seedServiceSlackApp(t, ctx, env, project, store)
	const source = `instruction: Post the scheduled update and follow its replies.
model:
  provider_config: openai-prod
  name: service-e2e-local
tools:
  app__chat__post_message:
    config: {channel_id: C123}
    permission: {mode: always_allow}
`
	config := env.requestJSON(t, ctx, http.MethodPost, project.projectPath+"/agent-configs",
		map[string]any{"source_format": "yaml", "source": source}, "", project.adminToken, http.StatusCreated)
	profile := env.requestJSON(t, ctx, http.MethodPost, project.projectPath+"/agent-profiles",
		map[string]any{"name": "Scheduled Slack", "config": config["id"]}, "", project.adminToken, http.StatusCreated)
	const cronText = "Send the scheduled status update."
	trigger := env.requestJSON(t, ctx, http.MethodPost, project.projectPath+"/cron-triggers", map[string]any{
		"name": "slack-status", "cron": "0 0 1 1 *", "message_template": cronText,
		"target": map[string]any{"type": "profile", "agent_profile_id": profile["id"]},
	}, "scheduled-slack", project.adminToken, http.StatusCreated)
	triggerID := mustDecodeServiceE2EPublicID(t, publicid.KindCronTrigger,
		testutil.RequireType[string](t, trigger["id"]))
	// Only advance the saved schedule's due time. The production cron service
	// still claims the occurrence, launches the profile, and creates its input.
	_, err = env.db.Exec(ctx,
		`UPDATE cron_triggers SET next_fire_after=transaction_timestamp()-interval '1 minute' WHERE id=$1`, triggerID)
	require.NoError(t, err)
	logs := &safeLogBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	cron := crontrigger.NewService(store.Execution(), nil, log)
	fired, err := cron.FireDueTriggers(ctx)
	require.NoError(t, err)
	require.Equal(t, crontrigger.FireStats{Claimed: 1, Launched: 1}, fired)
	listed := env.requestJSON(t, ctx, http.MethodGet, project.projectPath+"/agents",
		nil, "", project.adminToken, http.StatusOK)
	agents := testutil.RequireType[[]any](t, listed["data"])
	require.Len(t, agents, 1)
	agent := testutil.RequireType[map[string]any](t, agents[0])
	agentID := testutil.RequireType[string](t, agent["id"])
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	var subscriptions int
	require.NoError(t, env.db.QueryRow(ctx,
		`SELECT count(*) FROM app_subscriptions WHERE agent_id=$1`, agentUUID).Scan(&subscriptions))
	require.Zero(t, subscriptions, "launching a profile with a sending tool must not create receive routes")

	const postText = "Scheduled status: ready for review."
	const postedText = "The scheduled status is posted."
	const replyText = "The status looks good; continue."
	const finalText = "The reply reached the scheduled agent."
	const callID = "call_scheduled_slack_post"
	var modelCalls, posts atomic.Int32
	provider := newServiceSlackProvider(t, postText, &posts)
	fail := func(w http.ResponseWriter, status int, format string, args ...any) {
		t.Errorf(format, args...)
		http.Error(w, "unexpected model request", status)
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" || r.Header.Get("Authorization") != "Bearer service-e2e-test-key" {
			fail(w, http.StatusBadRequest, "unexpected model endpoint or credentials")
			return
		}
		var body map[string]any
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch modelCalls.Add(1) {
		case 1:
			assert.Contains(t, mustJSONString(body["input"]), cronText)
			assert.True(t, requestContainsTool(body, "app__chat__post_message"))
			writeOpenAIFunctionCall(w, fail, "resp_scheduled_post", callID, "app__chat__post_message",
				map[string]any{"text": postText, "follow_replies": true})
		case 2:
			assert.True(t, requestContainsToolResult(body, callID, "111.222"), "post receipt must reach the model")
			writeOpenAIMessage(w, fail, "resp_posted", postedText)
		case 3:
			assert.Contains(t, mustJSONString(body["input"]), replyText)
			assert.Contains(t, mustJSONString(body["input"]), postedText, "reply must retain the original agent history")
			assert.NotContains(t, mustJSONString(body["input"]), "Unfollowed thread")
			writeOpenAIMessage(w, fail, "resp_reply", finalText)
		default:
			fail(w, http.StatusBadRequest, "unexpected extra model request")
		}
	}))
	t.Cleanup(model.Close)
	env.updateServiceE2EProviderBaseURL(t, ctx, project.projectID, "openai-prod", model.URL)
	startServiceSlackWorkers(t, ctx, store, provider, log)
	waitForAssistantText(t, ctx, env, projectUUID, agentUUID, postedText)
	var scope, providerCallID string
	var events []string
	require.NoError(t, env.db.QueryRow(ctx, `
SELECT subscription.scope_ref, subscription.events, call.provider_call_id FROM app_subscriptions subscription
JOIN tool_calls call ON call.agent_id=subscription.agent_id AND call.id=subscription.tool_call_id
WHERE subscription.agent_id=$1 AND subscription.app_id=$2 AND subscription.subscription_type='thread_messages'`,
		agentUUID, app.ID).Scan(&scope, &events, &providerCallID))
	require.Equal(t, "C123:111.222", scope)
	require.Equal(t, []string{"message"}, events)
	require.Equal(t, callID, providerCallID, "the confirmed post must own subscription provenance")
	require.EqualValues(t, 1, posts.Load())

	// No launcher or initial attachment can route these messages. Only the
	// successful post's app subscription can admit the followed reply.
	sendServiceSlackReply(t, ctx, env, "EvOther", "999.111", "999.222", "Unfollowed thread")
	sendServiceSlackReply(t, ctx, env, "EvReply", "111.222", "111.333", replyText)
	waitForAssistantText(t, ctx, env, projectUUID, agentUUID, finalText)
	sendServiceSlackReply(t, ctx, env, "EvReply", "111.222", "111.333", replyText)
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var pending, locks int
		err := env.db.QueryRow(ctx, `SELECT
  (SELECT count(*) FROM integration_inbox WHERE app_id=$1 AND state <> 'completed'),
  (SELECT count(*) FROM agent_runtime_locks WHERE agent_id=$2)`, app.ID, agentUUID).Scan(&pending, &locks)
		return err == nil && pending == 0 && locks == 0, fmt.Sprintf("inbox=%d locks=%d err=%v %s",
			pending, locks, err, logs.Excerpt(20))
	})
	var inputs, agentCount, receipts int
	require.NoError(t, env.db.QueryRow(ctx, `SELECT
  (SELECT count(*) FROM agent_inputs WHERE agent_id=$1 AND input_kind='content'),
  (SELECT count(*) FROM agents WHERE project_id=$2),
  (SELECT count(*) FROM integration_inbox WHERE app_id=$3)`, agentUUID, projectUUID, app.ID).
		Scan(&inputs, &agentCount, &receipts))
	require.Equal(t, 2, inputs, "only the cron input and the followed reply belong to this agent")
	var inputTexts []string
	require.NoError(t, env.db.QueryRow(ctx, `
SELECT array_agg(block.text_content ORDER BY input.id, block.ordinal)
FROM agent_inputs input
JOIN content_blocks block ON block.agent_id=input.agent_id AND block.owner_agent_input_id=input.id
WHERE input.agent_id=$1 AND input.input_kind='content' AND block.block_kind='text'
  AND coalesce(block.metadata->>'omnara_hidden', 'false') <> 'true'`, agentUUID).Scan(&inputTexts))
	require.ElementsMatch(t, []string{cronText, replyText}, inputTexts)
	require.Equal(t, 1, agentCount, "reply routing must reuse the cron-created agent")
	require.Equal(t, 2, receipts, "provider replay must reuse its durable receipt")
	require.EqualValues(t, 3, modelCalls.Load())
	require.EqualValues(t, 1, posts.Load(), "neither inbound delivery nor replay may repost the proactive message")
	refired, err := cron.FireDueTriggers(ctx)
	require.NoError(t, err)
	require.Zero(t, refired.Claimed, "a completed scheduled occurrence must not fire twice")
}

func seedServiceSlackApp(
	t *testing.T, ctx context.Context, env *serviceE2EEnvironment, project deterministicProject, store *storage.Store,
) integrationstore.ProjectAppRecord {
	t.Helper()
	created := env.requestJSON(t, ctx, http.MethodPost, project.projectPath+"/apps",
		map[string]any{"name": "chat", "definition_id": appdefinition.Slack, "settings": map[string]any{}},
		"", project.adminToken, http.StatusCreated)
	appID, err := publicid.Decode(publicid.KindProjectApp, testutil.RequireType[string](t, created["id"]))
	require.NoError(t, err)
	projectID, err := publicid.Decode(publicid.KindProject, project.projectID)
	require.NoError(t, err)
	app, err := store.Integrations().GetProjectApp(ctx, projectID, appID)
	require.NoError(t, err)
	userID, err := publicid.Decode(publicid.KindUser, project.adminUserID)
	require.NoError(t, err)
	// OAuth is covered at the HTTP boundary separately. Seed only its verified
	// credential binding; targets, subscriptions, receipts, and inputs remain real work.
	credential, version, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: app.OrgID, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: projectID,
		Name: "local-slack", Actor: identitystore.NewUserPrincipal(userID),
		Material: secrets.SlackAppCredentialsMaterial{
			AccessToken: "xoxb-local-only", ClientID: "local-client", ClientSecret: "local-client-secret",
			SigningSecret: "local-signing-secret",
		},
	})
	require.NoError(t, err)
	app, err = store.Integrations().ConfigureProjectApp(ctx, integrationstore.ConfigureProjectAppInput{
		OrgID: app.OrgID, ProjectID: projectID, AppID: app.ID, ExpectedSetupRevision: app.SetupRevision,
		InstalledByUserID: userID, Provider: "slack", ProviderTenantID: "T123", ProviderAccountRef: "A123",
		ProviderAgentDisplayName: "Scheduled bot", CredentialSecretID: credential.ID, CredentialVersionID: version.ID,
		OAuthFlowID: uuid.Must(uuid.NewV7()), ProviderIdentity: json.RawMessage(`{"bot_user_id":"U_BOT"}`),
	})
	require.NoError(t, err)
	return app
}

func startServiceSlackWorkers(
	t *testing.T, ctx context.Context, store *storage.Store, client *http.Client, log *slog.Logger,
) {
	t.Helper()
	bus, err := notifications.NewRedisBus(integrationredis.OpenClient(t), log)
	require.NoError(t, err)
	kernelWorker := worker.NewWorker(store.Execution(), kernel.AgentExecutor{
		Store: store,
		ContextBuilder: modelcontext.Builder{
			Store: modelcontext.NewStore(store.Execution(), store.Artifacts(), store.Integrations()),
		},
		ModelResolver:   modelprovider.Resolver{Models: store.Models(), Secrets: store.Secrets(), AllowLoopback: true},
		ToolExecutor:    tools.Executor{Store: store, IntegrationHTTPClient: client, Log: log},
		StreamPublisher: bus, StreamLog: log,
	}, worker.Options{Log: log, Capacity: 1, ControlSubscriber: bus})
	router := integration.NewAppRouter(store.Execution(), store.Integrations())
	providers := map[string]integration.AppInboxProvider{
		"slack": integration.NewSlackAppInboxProvider(slack.OAuthConfig{HTTPClient: client},
			store.Secrets(), store.Integrations(), store.Execution()),
	}
	launcher := integration.NewChatAppLauncher(store.Integrations(), store.Execution(), providers)
	launches := integration.NewAppLaunchWorkflow(router, map[string]integration.AppLauncher{
		appdefinition.Slack: launcher.Decide,
	})
	consumer := integration.NewAppInboxConsumer(router, store.Integrations(), store.Artifacts(), providers,
		integration.InteractionPresenter{Store: store, HTTPClient: client}, launches)
	appWorker := integration.NewAppInboxWorker(store.Integrations(), consumer,
		integration.AppInboxWorkerOptions{Log: log, Capacity: 1})
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 2)
	go func() { done <- kernelWorker.Run(workerCtx) }()
	go func() { done <- appWorker.Run(workerCtx) }()
	t.Cleanup(func() {
		cancel()
		for range 2 {
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					assert.NoError(t, err)
				}
			case <-time.After(10 * time.Second):
				t.Error("hosted app worker did not stop")
			}
		}
	})
}

func newServiceSlackProvider(t *testing.T, postText string, posts *atomic.Int32) *http.Client {
	t.Helper()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !assert.Equal(t, "Bearer xoxb-local-only", r.Header.Get("Authorization")) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var response any
		switch strings.TrimPrefix(r.URL.Path, "/api/") {
		case "auth.test":
			response = map[string]any{"ok": true, "team_id": "T123", "user_id": "U_BOT", "bot_id": "B123"}
		case "chat.postMessage":
			var body map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, "C123", body["channel"])
			assert.Equal(t, postText, body["text"])
			assert.NotContains(t, body, "thread_ts", "proactive post starts a new thread")
			posts.Add(1)
			response = map[string]any{"ok": true, "channel": "C123", "ts": "111.222"}
		case "conversations.replies", "conversations.history":
			response = map[string]any{"ok": true, "messages": []any{}}
		case "conversations.info":
			response = map[string]any{"ok": true, "channel": map[string]string{"id": "C123", "name": "status"}}
		case "users.info":
			assert.NoError(t, r.ParseForm())
			response = map[string]any{"ok": true, "user": map[string]any{
				"id": r.Form.Get("user"), "name": "reviewer", "profile": map[string]string{"display_name": "Reviewer"},
			}}
		case "reactions.add":
			response = map[string]any{"ok": true}
		default:
			t.Errorf("unexpected local Slack endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		assert.NoError(t, json.NewEncoder(w).Encode(response))
	}))
	t.Cleanup(provider.Close)
	target, err := url.Parse(provider.URL)
	require.NoError(t, err)
	return &http.Client{Transport: serviceSlackTransport{target: target}, Timeout: 5 * time.Second}
}

type serviceSlackTransport struct{ target *url.URL }

func (transport serviceSlackTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Host != "slack.com" {
		return nil, fmt.Errorf("unexpected hosted provider host %q", request.URL.Host)
	}
	local := request.Clone(request.Context())
	local.URL.Scheme, local.URL.Host = transport.target.Scheme, transport.target.Host
	local.Host = transport.target.Host
	return http.DefaultTransport.RoundTrip(local)
}

func sendServiceSlackReply(
	t *testing.T, ctx context.Context, env *serviceE2EEnvironment, eventID, threadTS, messageTS, text string,
) {
	t.Helper()
	body := mustJSON(map[string]any{
		"type": "event_callback", "team_id": "T123", "api_app_id": "A123", "event_id": eventID,
		"authorizations": []any{map[string]any{"team_id": "T123", "user_id": "U_BOT", "is_bot": true}},
		"event": map[string]any{"type": "message", "user": "U123", "team": "T123", "channel": "C123",
			"channel_type": "channel", "thread_ts": threadTS, "ts": messageTS, "text": text},
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(env.apiURL, "/api/v1")+"/api/integrations/slack/events", strings.NewReader(string(body)))
	require.NoError(t, err)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte("local-signing-secret"))
	_, err = mac.Write(append([]byte("v0:"+timestamp+":"), body...))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(slack.TimestampHeader, timestamp)
	request.Header.Set(slack.SignatureHeader, "v0="+hex.EncodeToString(mac.Sum(nil)))
	response := doServiceJSONRequest(t, request, http.StatusOK)
	require.Equal(t, "received", response["ok"])
}
