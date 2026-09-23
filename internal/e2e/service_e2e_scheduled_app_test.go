//go:build integration && servicee2e

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/apps"
	"github.com/omnara-ai/omnara/internal/crontrigger"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceE2EScheduledSlackAppLaunch(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	for _, key := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "OPENROUTER_API_KEY"} {
		t.Setenv(key, "service-e2e-test-key")
	}
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "scheduled-slack-app")
	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPI(t, ctx, "scheduled-slack-app", "openai-prod", "service-e2e-local")
	projectID := uuid.MustParse(mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID))
	keyWrapper, err := secrets.NewLocalKeyWrapper("e2e-local", map[string][]byte{
		"e2e-local": []byte("0123456789abcdef0123456789abcdef"),
	})
	require.NoError(t, err)
	store := storage.NewStore(env.db, storage.WithSecretKeyWrapper(keyWrapper))
	app := seedServiceSlackApp(t, ctx, env, project, store)
	appID, err := publicid.Encode(publicid.KindProjectApp, app.ID)
	require.NoError(t, err)
	const source = `instruction: Ask for review, then post the scheduled update in this thread.
model:
  provider_config: openai-prod
  name: service-e2e-local
tools:
  ask_question: {}
`
	config := env.requestJSON(t, ctx, http.MethodPost, project.projectPath+"/agent-configs",
		map[string]any{"source_format": "yaml", "source": source}, "", project.adminToken, http.StatusCreated)
	profile := env.requestJSON(t, ctx, http.MethodPost, project.projectPath+"/agent-profiles",
		map[string]any{"name": "Scheduled Slack", "config": config["id"]}, "", project.adminToken, http.StatusCreated)
	target := map[string]any{"type": "app", "app_id": appID, "settings": map[string]any{
		"agent_profile_id": profile["id"], "channel_id": "C123", "opening_message_template": "Daily update — {{.trigger.local_date}}", "message_template": "Prepare the update for {{.trigger.local_date}}.",
	}}
	trigger := env.requestJSON(t, ctx, http.MethodPost, project.projectPath+"/cron-triggers", map[string]any{
		"name": "daily-update", "cron": "0 0 1 1 *", "timezone": "America/Los_Angeles",
		"target": target,
	}, "scheduled-app", project.adminToken, http.StatusCreated)
	require.Equal(t, target, trigger["target"])
	triggerID := mustDecodeServiceE2EPublicID(t, publicid.KindCronTrigger,
		testutil.RequireType[string](t, trigger["id"]))

	dueTimes := []time.Time{
		time.Date(2025, 1, 2, 7, 30, 0, 0, time.UTC),
		time.Date(2025, 1, 3, 7, 30, 0, 0, time.UTC),
	}
	dates := []string{"2025-01-01", "2025-01-02"}
	roots := []string{"111.100", "222.100"}
	reports := []string{"First scheduled report.", "Second scheduled report."}
	replies := []string{"Proceed with the first report.", "Proceed with the second report."}
	finals := []string{"First report posted.", "Second report posted."}
	const question = "What should this report cover?"
	const mention = "<@U_BOT> Please start a separate review."
	const mentionDone = "The mention launcher still works."
	var modelCalls, rootPosts, reportPosts, questionPosts, dismissals atomic.Int32
	logs := &safeLogBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	cron := crontrigger.NewService(store.Execution(), nil, log)

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
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			assert.Equal(t, "C123", body["channel"])
			var ts string
			if _, threaded := body["thread_ts"]; !threaded {
				i := int(rootPosts.Add(1)) - 1
				if !assert.Less(t, i, len(roots), "only cron openings may create roots") {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				assert.Equal(t, "Daily update — "+dates[i], body["text"])
				var agents int
				if assert.NoError(t, env.db.QueryRow(r.Context(),
					`SELECT count(*) FROM agents WHERE project_id=$1`, projectID).Scan(&agents)) {
					assert.Equal(t, i, agents, "opening must be published before its fresh agent exists")
				}
				ts = roots[i]
			} else {
				i := int(rootPosts.Load()) - 1
				if !assert.True(t, i >= 0 && i < len(roots), "thread requires a published opening") {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				assert.Equal(t, roots[i], body["thread_ts"], "question and report must use the prepared thread")
				if body["text"] == reports[i] {
					reportPosts.Add(1)
					ts = fmt.Sprintf("%d.300", (i+1)*111)
				} else {
					assert.Contains(t, mustJSONString(body), question)
					assert.NotEmpty(t, body["blocks"])
					questionPosts.Add(1)
					ts = fmt.Sprintf("%d.200", (i+1)*111)
				}
			}
			response = map[string]any{"ok": true, "channel": "C123", "ts": ts}
		case "chat.update":
			var body map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, "C123", body["channel"])
			assert.Equal(t, fmt.Sprintf("%d.200", rootPosts.Load()*111), body["ts"])
			assert.Empty(t, body["blocks"], "accepted human reply removes question controls")
			dismissals.Add(1)
			response = map[string]any{"ok": true, "channel": "C123", "ts": body["ts"]}
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
	providerURL, err := url.Parse(provider.URL)
	require.NoError(t, err)
	client := &http.Client{Transport: serviceSlackTransport{target: providerURL}, Timeout: 5 * time.Second}
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
		call := int(modelCalls.Add(1))
		if call == 7 {
			assert.Contains(t, mustJSONString(body["input"]), "Please start a separate review.")
			assert.NotContains(t, mustJSONString(body["input"]), reports[1])
			writeOpenAIMessage(w, fail, "resp_mention", mentionDone)
			return
		}
		i := (call - 1) / 3
		if i >= len(roots) {
			fail(w, http.StatusBadRequest, "unexpected extra model request %d", call)
			return
		}
		callID := fmt.Sprintf("call_scheduled_post_%d", i)
		switch (call - 1) % 3 {
		case 0:
			assert.EqualValues(t, i+1, rootPosts.Load(), "opening precedes the first model step")
			assert.Contains(t, mustJSONString(body["input"]), "Prepare the update for "+dates[i]+".")
			assert.NotContains(t, mustJSONString(body["input"]), replies[0], "new occurrence has fresh history")
			assert.True(t, requestContainsTool(body, "ask_question"))
			assert.True(t, requestContainsTool(body, "list_interaction_handlers"))
			assert.True(t, requestContainsTool(body, "set_interaction_handler"))
			writeOpenAIFunctionCall(w, fail, fmt.Sprintf("resp_question_%d", i),
				fmt.Sprintf("call_question_%d", i), "ask_question", map[string]any{
					"questions": []any{map[string]any{"prompt": question,
						"options": []any{map[string]any{"label": "Today's progress"}}}},
				})
		case 1:
			assert.Contains(t, mustJSONString(body["input"]), replies[i])
			assert.True(t, requestContainsTool(body, "app__chat__post_message"))
			writeOpenAIFunctionCall(w, fail, fmt.Sprintf("resp_post_%d", i), callID,
				"app__chat__post_message", map[string]any{"text": reports[i]})
		case 2:
			assert.True(t, requestContainsToolResult(body, callID, fmt.Sprintf("%d.300", (i+1)*111)))
			writeOpenAIMessage(w, fail, fmt.Sprintf("resp_done_%d", i), finals[i])
		}
	}))
	t.Cleanup(model.Close)
	env.updateServiceE2EProviderBaseURL(t, ctx, project.projectID, "openai-prod", model.URL)

	var previousAgent, previousReceipt uuid.UUID
	for i, dueAt := range dueTimes {
		if i == 1 {
			env.requestJSON(t, ctx, http.MethodPut, project.projectPath+"/apps/"+appID, map[string]any{
				"name": "chat", "app_type": appdefinition.SlackThread,
				"settings": map[string]any{"launcher": map[string]any{
					"trigger": "mention", "scope_kind": "workspace", "scope_ref": "T123",
					"slots": []any{map[string]any{"key": "default", "agent_profile_id": profile["id"]}},
				}},
			}, "", project.adminToken, http.StatusOK)
		}
		_, err := env.db.Exec(ctx, `UPDATE cron_triggers SET next_fire_after=$2 WHERE id=$1`, triggerID, dueAt)
		require.NoError(t, err)
		fired, err := cron.FireDueTriggers(ctx)
		require.NoError(t, err)
		require.Equal(t, crontrigger.FireStats{Claimed: 1, Queued: 1}, fired, logs.Excerpt(20))
		if i == 0 {
			var agents int
			require.NoError(t, env.db.QueryRow(ctx, `SELECT count(*) FROM agents WHERE project_id=$1`, projectID).Scan(&agents))
			require.Zero(t, agents, "cron hands off work without creating an agent")
			require.Zero(t, rootPosts.Load(), "cron performs no provider publication")
			require.Zero(t, modelCalls.Load())
		}
		var receiptID uuid.UUID
		require.NoError(t, env.db.QueryRow(
			ctx, `SELECT last_app_receipt_id FROM cron_triggers WHERE id=$1`, triggerID,
		).Scan(&receiptID))
		require.NotEqual(t, previousReceipt, receiptID)
		receipt, err := store.Apps().GetAppInbox(ctx, projectID, receiptID)
		require.NoError(t, err)
		event, err := receipt.ScheduledEvent()
		require.NoError(t, err)
		launch, err := appdefinition.PrepareThreadSchedule(appdefinition.SlackThread, event.Settings, event.Occurrence)
		require.NoError(t, err)
		require.Equal(t, "Daily update — "+dates[i], launch.OpeningMessage)
		require.Equal(t, "Prepare the update for "+dates[i]+".", launch.Message)
		require.True(t, dueAt.Equal(event.Occurrence.DueAt))
		if i == 0 {
			require.Equal(t, appstore.AppInboxPending, receipt.State)
			startServiceSlackWorkers(t, ctx, store, client, log)
		}

		var agentID, questionID uuid.UUID
		waitForServiceE2ECondition(t, ctx, func() (bool, string) {
			err := env.db.QueryRow(ctx, `SELECT interaction.agent_id, interaction.id FROM agent_interactions interaction
JOIN agents agent ON agent.id=interaction.agent_id
WHERE agent.project_id=$1 AND interaction.interaction_kind='question' AND interaction.state='open'`, projectID).
				Scan(&agentID, &questionID)
			if !errors.Is(err, pgx.ErrNoRows) {
				require.NoError(t, err)
			}
			return err == nil, fmt.Sprintf("waiting for scheduled question: %v %s", err, logs.Excerpt(20))
		})
		require.NotEqual(t, previousAgent, agentID, "each occurrence starts a fresh agent")
		interaction, found, err := store.Execution().GetAgentInteraction(ctx, projectID, agentID, questionID)
		require.NoError(t, err)
		require.True(t, found)
		destination, err := interaction.CapturedDestination()
		require.NoError(t, err)
		require.NotNil(t, destination, "first interaction already knows the scheduled conversation")
		require.Equal(t, app.ID, destination.AppID)
		require.Equal(t, appstore.ConversationAddress{Kind: "thread", Ref: "C123:" + roots[i]}, destination.Address)
		var actorProvider, actorUser string
		require.NoError(t, env.db.QueryRow(ctx, `SELECT actor.provider, actor.provider_user_id
FROM agent_inputs input JOIN actors actor ON actor.id=input.actor_id
WHERE input.agent_id=$1 AND input.input_kind='content'`, agentID).Scan(&actorProvider, &actorUser))
		require.Equal(t, "omnara", actorProvider, "the scheduled task is not attributed to a Slack user")
		require.Equal(t, trigger["id"], actorUser)
		var scope string
		require.NoError(t, env.db.QueryRow(ctx, `SELECT scope_ref FROM app_subscriptions
WHERE agent_id=$1 AND app_id=$2 AND subscription_type='thread_messages'`, agentID, app.ID).Scan(&scope))
		require.Equal(t, "C123:"+roots[i], scope, "replies are subscribed before any model post or human reply")
		presenter := apps.InteractionPresenter{Store: store, HTTPClient: client}
		require.NoError(t, presenter.Present(ctx, projectID, agentID, questionID))
		require.EqualValues(t, i+1, questionPosts.Load())
		require.EqualValues(t, i, reportPosts.Load(), "the first interaction precedes the model's first post")

		eventID := fmt.Sprintf("EvScheduledReply%d", i)
		messageTS := fmt.Sprintf("%d.250", (i+1)*111)
		sendServiceSlackReply(t, ctx, env, eventID, roots[i], messageTS, replies[i])
		waitForAssistantText(t, ctx, env, projectID.String(), agentID.String(), finals[i])
		sendServiceSlackReply(t, ctx, env, eventID, roots[i], messageTS, replies[i])
		waitForServiceE2ECondition(t, ctx, func() (bool, string) {
			var pending, locks int
			err := env.db.QueryRow(ctx, `SELECT
  (SELECT count(*) FROM app_inbox WHERE app_id=$1 AND state <> 'completed'),
  (SELECT count(*) FROM agent_runtime_locks WHERE agent_id=$2)`, app.ID, agentID).Scan(&pending, &locks)
			return err == nil && pending == 0 && locks == 0 && dismissals.Load() == int32(i+1),
				fmt.Sprintf("inbox=%d locks=%d dismissals=%d err=%v %s", pending, locks, dismissals.Load(), err, logs.Excerpt(20))
		})
		interaction, found, err = store.Execution().GetAgentInteraction(ctx, projectID, agentID, questionID)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, executionstore.AgentInteractionStateCanceled, interaction.State)
		var inputTexts []string
		require.NoError(t, env.db.QueryRow(ctx, `SELECT array_agg(block.text_content ORDER BY input.id, block.ordinal)
FROM agent_inputs input
JOIN content_blocks block ON block.agent_id=input.agent_id AND block.owner_agent_input_id=input.id
WHERE input.agent_id=$1 AND input.input_kind='content' AND block.block_kind='text'
  AND coalesce(block.metadata->>'omnara_hidden', 'false') <> 'true'`, agentID).Scan(&inputTexts))
		require.ElementsMatch(t, []string{launch.Message, replies[i]}, inputTexts,
			"human reply reaches the scheduled agent exactly once")
		require.EqualValues(t, (i+1)*3, modelCalls.Load())
		require.EqualValues(t, i+1, reportPosts.Load())
		refired, err := cron.FireDueTriggers(ctx)
		require.NoError(t, err)
		require.Zero(t, refired.Claimed, "completed occurrence must not queue again")
		previousAgent, previousReceipt = agentID, receiptID
	}

	sendServiceSlackReply(t, ctx, env, "EvMentionAlongsideSchedule", "333.100", "333.100", mention)
	var mentionAgent uuid.UUID
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		err := env.db.QueryRow(ctx, `SELECT agent_id FROM app_subscriptions
WHERE app_id=$1 AND scope_ref='C123:333.100'`, app.ID).Scan(&mentionAgent)
		if !errors.Is(err, pgx.ErrNoRows) {
			require.NoError(t, err)
		}
		return err == nil, fmt.Sprintf("waiting for mention launch: %v %s", err, logs.Excerpt(20))
	})
	waitForAssistantText(t, ctx, env, projectID.String(), mentionAgent.String(), mentionDone)
	listed := env.requestJSON(
		t, ctx, http.MethodGet, project.projectPath+"/agents", nil, "", project.adminToken, http.StatusOK,
	)
	require.Len(t, testutil.RequireType[[]any](t, listed["data"]), 3,
		"two occurrences and one mention create exactly three agents")
	require.EqualValues(t, 7, modelCalls.Load())
	require.EqualValues(t, 2, rootPosts.Load(), "replies and mentions never publish another scheduled heading")
}
