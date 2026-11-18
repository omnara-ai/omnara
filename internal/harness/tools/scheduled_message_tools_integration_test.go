//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func scheduledMessageToolTurn(f *integrationToolFixture) Turn {
	turn := f.turn()
	for _, name := range toolcatalog.ScheduledMessageToolNames() {
		turn.Tools[name] = ToolSpec{Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow)}
	}
	return turn
}

func dispatchScheduledMessageTool(
	t *testing.T,
	ctx context.Context,
	f *integrationToolFixture,
	turn Turn,
	call model.ToolCall,
) map[string]any {
	t.Helper()
	result, err := (Executor{Store: f.Store}).Dispatch(ctx, turn, call)
	require.NoError(t, err)
	require.Equal(t, DispatchCompleted, result.Disposition)
	var parts []struct {
		Type  string
		Value map[string]any
	}
	require.NoError(t, json.Unmarshal(result.ContentParts, &parts))
	require.Len(t, parts, 1)
	require.Equal(t, "structured_data", parts[0].Type)
	return parts[0].Value
}

func scheduleInputAs(
	t *testing.T,
	ctx context.Context,
	f *integrationToolFixture,
	actor *executionstore.ActorParams,
	key string,
) executionstore.AgentInputRecord {
	t.Helper()
	scheduledFor := time.Now().Add(2 * time.Hour)
	input, _, _, err := f.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:          toolsTestProjectID,
		AgentID:            f.Agent.ID,
		Actor:              actor,
		ContentBlocks:      json.RawMessage(`[{"type":"text","text":"` + key + `"}]`),
		IdempotencyKey:     key,
		SourceScheduledFor: &scheduledFor,
	})
	require.NoError(t, err)
	return input
}

func scheduledInputState(t *testing.T, ctx context.Context, f *integrationToolFixture, id uuid.UUID) string {
	t.Helper()
	var state string
	require.NoError(t, f.Pool.QueryRow(ctx, `SELECT state FROM agent_inputs WHERE id=$1`, id).Scan(&state))
	return state
}

func TestScheduledMessageToolsScheduleAsTheAgentAndHideOtherActors(t *testing.T) {
	ctx := t.Context()
	fixture := newIntegrationToolFixture(t, ctx, "scheduled-message-tools")
	external := "External"
	foreign := scheduleInputAs(t, ctx, &fixture, &executionstore.ActorParams{
		Provider: executionstore.ActorProviderExternal, ProviderTenantID: "tenant", ProviderUserID: "someone",
		DisplayName: &external,
	}, "foreign-scheduled")
	foreignID, err := publicid.Encode(publicid.KindAgentInput, foreign.ID)
	require.NoError(t, err)

	calls := []model.ToolCall{
		{ID: "call_now", Name: toolcatalog.ToolNameGetCurrentTime, Input: json.RawMessage(`{}`)},
		{ID: "call_create", Name: toolcatalog.ToolNameCreateScheduledMessage,
			Input: json.RawMessage(`{"message":"Check the deploy.","delay_seconds":3600}`)},
		{ID: "call_list", Name: toolcatalog.ToolNameGetScheduledMessages, Input: json.RawMessage(`{}`)},
		{ID: "call_delete_foreign", Name: toolcatalog.ToolNameDeleteScheduledMessage,
			Input: json.RawMessage(`{"id":"` + foreignID + `"}`)},
		{ID: "call_past", Name: toolcatalog.ToolNameCreateScheduledMessage,
			Input: json.RawMessage(`{"message":"Too late.","timestamp":"2020-01-01T00:00:00Z"}`)},
	}
	fixture.recordToolCalls(t, ctx, calls, fixture.Now.Add(20*time.Second))
	turn := scheduledMessageToolTurn(&fixture)

	now := dispatchScheduledMessageTool(t, ctx, &fixture, turn, calls[0])
	current, err := time.Parse(time.RFC3339, testutil.RequireType[string](t, now["current_time"]))
	require.NoError(t, err)
	require.WithinDuration(t, time.Now(), current, time.Minute)
	require.Equal(t, "UTC", now["timezone"])

	created := dispatchScheduledMessageTool(t, ctx, &fixture, turn, calls[1])
	require.Equal(t, true, created["scheduled"])
	scheduledFor, err := time.Parse(time.RFC3339, testutil.RequireType[string](t, created["scheduled_for"]))
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(time.Hour), scheduledFor, time.Minute)
	createdID, err := publicid.Decode(publicid.KindAgentInput, testutil.RequireType[string](t, created["id"]))
	require.NoError(t, err)

	agentPublicID, err := publicid.Encode(publicid.KindAgent, fixture.Agent.ID)
	require.NoError(t, err)
	var state, deliveryMode, provider, providerUserID, text string
	require.NoError(t, fixture.Pool.QueryRow(ctx, `
SELECT i.state, i.delivery_mode, a.provider, a.provider_user_id, b.text_content
FROM agent_inputs i JOIN actors a ON a.id = i.actor_id
JOIN content_blocks b ON b.agent_id = i.agent_id AND b.owner_agent_input_id = i.id
WHERE i.agent_id = $1 AND i.id = $2`, fixture.Agent.ID, createdID,
	).Scan(&state, &deliveryMode, &provider, &providerUserID, &text))
	require.Equal(t, "scheduled", state)
	require.Equal(t, "queued", deliveryMode)
	require.Equal(t, string(executionstore.ActorProviderOmnara), provider)
	require.Equal(t, agentPublicID, providerUserID)
	require.Contains(t, text, "Scheduled message: you scheduled this message for yourself at ")
	require.Contains(t, text, "for delivery at "+testutil.RequireType[string](t, created["scheduled_for"]))
	require.Contains(t, text, "\n\nCheck the deploy.")

	listed := dispatchScheduledMessageTool(t, ctx, &fixture, turn, calls[2])
	messages := testutil.RequireType[[]any](t, listed["scheduled_messages"])
	require.Len(t, messages, 1)
	only := testutil.RequireType[map[string]any](t, messages[0])
	require.Equal(t, created["id"], only["id"])
	require.Equal(t, created["scheduled_for"], only["scheduled_for"])
	require.Equal(t, text, only["message"])

	denied := dispatchScheduledMessageTool(t, ctx, &fixture, turn, calls[3])
	require.Equal(t, "delete_scheduled_message_failed", denied["error_code"])
	require.Equal(t, "scheduled", scheduledInputState(t, ctx, &fixture, foreign.ID))

	past := dispatchScheduledMessageTool(t, ctx, &fixture, turn, calls[4])
	require.Equal(t, "create_scheduled_message_failed", past["error_code"])
	require.Contains(t, past["error"], "must be in the future")
}

func TestDeleteScheduledMessageToolCancelsTheAgentsOwnMessage(t *testing.T) {
	ctx := t.Context()
	fixture := newIntegrationToolFixture(t, ctx, "scheduled-message-delete")
	self, err := executionstore.SubagentActorParams(fixture.Agent.OrgID, fixture.Agent)
	require.NoError(t, err)
	own := scheduleInputAs(t, ctx, &fixture, self, "own-scheduled")
	ownID, err := publicid.Encode(publicid.KindAgentInput, own.ID)
	require.NoError(t, err)

	calls := []model.ToolCall{
		{ID: "call_delete_own", Name: toolcatalog.ToolNameDeleteScheduledMessage,
			Input: json.RawMessage(`{"id":"` + ownID + `"}`)},
		{ID: "call_list_after", Name: toolcatalog.ToolNameGetScheduledMessages, Input: json.RawMessage(`{}`)},
		{ID: "call_delete_again", Name: toolcatalog.ToolNameDeleteScheduledMessage,
			Input: json.RawMessage(`{"id":"` + ownID + `"}`)},
	}
	fixture.recordToolCalls(t, ctx, calls, fixture.Now.Add(20*time.Second))
	turn := scheduledMessageToolTurn(&fixture)

	deleted := dispatchScheduledMessageTool(t, ctx, &fixture, turn, calls[0])
	require.Equal(t, true, deleted["deleted"])
	require.Equal(t, ownID, deleted["id"])
	require.Equal(t, "canceled", scheduledInputState(t, ctx, &fixture, own.ID))

	listed := dispatchScheduledMessageTool(t, ctx, &fixture, turn, calls[1])
	require.Empty(t, listed["scheduled_messages"])

	again := dispatchScheduledMessageTool(t, ctx, &fixture, turn, calls[2])
	require.Equal(t, "delete_scheduled_message_failed", again["error_code"])
}

func TestScheduledMessageToolsManageCronSchedulesForTheAgent(t *testing.T) {
	ctx := t.Context()
	fixture := newIntegrationToolFixture(t, ctx, "scheduled-message-cron")
	// A schedule created through the API for this agent is visible to, and removable by, the agent.
	existing, err := fixture.Store.Execution().CreateCronTrigger(ctx, executionstore.CreateCronTriggerInput{
		ProjectID: toolsTestProjectID, Name: "api-made",
		Target:         executionstore.CronTriggerTarget{Kind: executionstore.CronTriggerTargetAgent, ID: fixture.Agent.ID},
		CronExpression: "0 12 * * *", Timezone: "UTC", MessageTemplate: "Midday check.", Enabled: true,
	})
	require.NoError(t, err)
	existingID, err := publicid.Encode(publicid.KindCronTrigger, existing.ID)
	require.NoError(t, err)
	missingID, err := publicid.Encode(publicid.KindCronTrigger, uuid.New())
	require.NoError(t, err)

	calls := []model.ToolCall{
		{ID: "call_cron", Name: toolcatalog.ToolNameCreateScheduledMessage,
			Input: json.RawMessage(`{"message":"Post the summary.","cron":"0 9 * * 1-5","timezone":"America/New_York"}`)},
		{ID: "call_list_cron", Name: toolcatalog.ToolNameGetScheduledMessages, Input: json.RawMessage(`{}`)},
		{ID: "call_delete_existing", Name: toolcatalog.ToolNameDeleteScheduledMessage,
			Input: json.RawMessage(`{"id":"` + existingID + `"}`)},
		{ID: "call_delete_missing", Name: toolcatalog.ToolNameDeleteScheduledMessage,
			Input: json.RawMessage(`{"id":"` + missingID + `"}`)},
	}
	fixture.recordToolCalls(t, ctx, calls, fixture.Now.Add(20*time.Second))
	turn := scheduledMessageToolTurn(&fixture)
	executor := Executor{Store: fixture.Store}
	dispatchAsync := func(call model.ToolCall) map[string]any {
		t.Helper()
		result, err := dispatchAsyncToolToTerminal(t, ctx, executor, turn, call)
		require.NoError(t, err)
		require.Equal(t, DispatchCompleted, result.Disposition)
		return toolResultMapFromTestParts(t, result.ContentParts)
	}

	created := dispatchAsync(calls[0])
	require.Equal(t, true, created["recurring"])
	require.Equal(t, "0 9 * * 1-5", created["cron"])
	require.Equal(t, "America/New_York", created["timezone"])
	require.NotEmpty(t, created["next_run_at"])
	createdID, err := publicid.Decode(publicid.KindCronTrigger, testutil.RequireType[string](t, created["id"]))
	require.NoError(t, err)
	trigger, err := fixture.Store.Execution().GetCronTrigger(ctx, toolsTestProjectID, createdID)
	require.NoError(t, err)
	require.Equal(t, executionstore.CronTriggerTargetAgent, trigger.Target.Kind)
	require.Equal(t, fixture.Agent.ID, trigger.Target.ID)
	require.Equal(t, executionstore.CronTriggerDeliveryModeQueued, trigger.Target.DeliveryMode)
	require.True(t, trigger.Enabled)
	require.Contains(t, trigger.MessageTemplate, "fired at {{.trigger.fired_at}}.\n\nPost the summary.")

	listed := dispatchScheduledMessageTool(t, ctx, &fixture, turn, calls[1])
	require.Empty(t, listed["scheduled_messages"])
	schedules := testutil.RequireType[[]any](t, listed["schedules"])
	require.Len(t, schedules, 2)
	first := testutil.RequireType[map[string]any](t, schedules[0])
	second := testutil.RequireType[map[string]any](t, schedules[1])
	require.Equal(t, existingID, first["id"])
	require.Equal(t, "Midday check.", first["message"])
	require.Equal(t, created["id"], second["id"])
	require.Equal(t, "0 9 * * 1-5", second["cron"])

	deleted := dispatchAsync(calls[2])
	require.Equal(t, true, deleted["deleted"])
	_, err = fixture.Store.Execution().GetCronTrigger(ctx, toolsTestProjectID, existing.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = fixture.Store.Execution().GetCronTrigger(ctx, toolsTestProjectID, createdID)
	require.NoError(t, err)

	missing := dispatchAsync(calls[3])
	require.Equal(t, "delete_scheduled_message_failed", missing["error_code"])
}
