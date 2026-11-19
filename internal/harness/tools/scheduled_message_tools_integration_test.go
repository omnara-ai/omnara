//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"errors"
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
		{ID: "call_list", Name: toolcatalog.ToolNameListScheduledMessages, Input: json.RawMessage(`{}`)},
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
	var state, deliveryMode, provider, providerUserID, note, text string
	var noteHidden, textHidden bool
	require.NoError(t, fixture.Pool.QueryRow(ctx, `
SELECT i.state, i.delivery_mode, a.provider, a.provider_user_id,
       note.text_content, coalesce(note.metadata->>'omnara_hidden' = 'true', false),
       body.text_content, coalesce(body.metadata->>'omnara_hidden' = 'true', false)
FROM agent_inputs i JOIN actors a ON a.id = i.actor_id
JOIN content_blocks note ON note.agent_id = i.agent_id AND note.owner_agent_input_id = i.id AND note.ordinal = 0
JOIN content_blocks body ON body.agent_id = i.agent_id AND body.owner_agent_input_id = i.id AND body.ordinal = 1
WHERE i.agent_id = $1 AND i.id = $2`, fixture.Agent.ID, createdID,
	).Scan(&state, &deliveryMode, &provider, &providerUserID, &note, &noteHidden, &text, &textHidden))
	require.Equal(t, "scheduled", state)
	require.Equal(t, "queued", deliveryMode)
	require.Equal(t, string(executionstore.ActorProviderOmnara), provider)
	require.Equal(t, agentPublicID, providerUserID)
	require.Contains(t, note, "Scheduled message: you scheduled this message for yourself at ")
	require.Contains(t, note, "for delivery at "+testutil.RequireType[string](t, created["scheduled_for"]))
	require.True(t, noteHidden)
	require.Equal(t, "Check the deploy.", text)
	require.False(t, textHidden)

	listed := dispatchScheduledMessageTool(t, ctx, &fixture, turn, calls[2])
	messages := testutil.RequireType[[]any](t, listed["scheduled_messages"])
	require.Len(t, messages, 1)
	only := testutil.RequireType[map[string]any](t, messages[0])
	require.Equal(t, created["id"], only["id"])
	require.Equal(t, created["scheduled_for"], only["scheduled_for"])
	require.Equal(t, text, only["message_preview"])

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
		{ID: "call_list_after", Name: toolcatalog.ToolNameListScheduledMessages, Input: json.RawMessage(`{}`)},
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
		{ID: "call_list_cron", Name: toolcatalog.ToolNameListScheduledMessages, Input: json.RawMessage(`{}`)},
		{ID: "call_delete_existing", Name: toolcatalog.ToolNameDeleteScheduledMessage,
			Input: json.RawMessage(`{"id":"` + existingID + `"}`)},
		{ID: "call_delete_missing", Name: toolcatalog.ToolNameDeleteScheduledMessage,
			Input: json.RawMessage(`{"id":"` + missingID + `"}`)},
	}
	fixture.recordToolCalls(t, ctx, calls, fixture.Now.Add(20*time.Second))
	turn := scheduledMessageToolTurn(&fixture)
	dispatch := func(call model.ToolCall) map[string]any {
		t.Helper()
		return dispatchScheduledMessageTool(t, ctx, &fixture, turn, call)
	}

	created := dispatch(calls[0])
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
	require.Len(t, schedules, 1)
	own := testutil.RequireType[map[string]any](t, schedules[0])
	require.Equal(t, created["id"], own["id"])
	require.Equal(t, "0 9 * * 1-5", own["cron"])
	require.Equal(t, "Post the summary.", own["message_preview"])

	denied := dispatch(calls[2])
	require.Equal(t, "delete_scheduled_message_failed", denied["error_code"])
	_, err = fixture.Store.Execution().GetCronTrigger(ctx, toolsTestProjectID, existing.ID)
	require.NoError(t, err)

	missing := dispatch(calls[3])
	require.Equal(t, "delete_scheduled_message_failed", missing["error_code"])
}

func agentScheduleCount(t *testing.T, ctx context.Context, f *integrationToolFixture) int {
	t.Helper()
	var count int
	require.NoError(t, f.Pool.QueryRow(ctx,
		`SELECT count(*) FROM cron_triggers WHERE agent_id=$1 AND deleted_at IS NULL`, f.Agent.ID).Scan(&count))
	return count
}

func executeScheduleCommand(
	ctx context.Context,
	f *integrationToolFixture,
	toolCallID uuid.UUID,
	command executionstore.ToolCallCommand,
) (executionstore.ExecuteToolCallResult, error) {
	return f.Store.Execution().ExecuteToolCall(ctx, executionstore.ExecuteToolCallInput{
		ProjectID: toolsTestProjectID, AgentID: f.Agent.ID, ToolCallID: toolCallID, RuntimeLockID: f.Lock.ID,
	}, func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) { return command, nil })
}

func TestAgentScheduleChangesCommitOnlyWithTheirToolResult(t *testing.T) {
	ctx := t.Context()
	fixture := newIntegrationToolFixture(t, ctx, "scheduled-message-cron-cancel")
	calls := []model.ToolCall{
		{ID: "call_keep", Name: toolcatalog.ToolNameCreateScheduledMessage,
			Input: json.RawMessage(`{"message":"Keep.","cron":"0 9 * * *"}`)},
		{ID: "call_remove", Name: toolcatalog.ToolNameCreateScheduledMessage,
			Input: json.RawMessage(`{"message":"Remove.","cron":"0 8 * * *"}`)},
		{ID: "call_delete", Name: toolcatalog.ToolNameDeleteScheduledMessage, Input: json.RawMessage(`{}`)},
		{ID: "call_unfinished", Name: toolcatalog.ToolNameCreateScheduledMessage,
			Input: json.RawMessage(`{"message":"Unfinished.","cron":"0 10 * * *"}`)},
		{ID: "call_create_late", Name: toolcatalog.ToolNameCreateScheduledMessage,
			Input: json.RawMessage(`{"message":"Late.","cron":"0 11 * * *"}`)},
		{ID: "call_delete_late", Name: toolcatalog.ToolNameDeleteScheduledMessage, Input: json.RawMessage(`{}`)},
	}
	fixture.recordToolCalls(t, ctx, calls, fixture.Now.Add(20*time.Second))
	turn := scheduledMessageToolTurn(&fixture)
	kept := dispatchScheduledMessageTool(t, ctx, &fixture, turn, calls[0])
	keptID, err := publicid.Decode(publicid.KindCronTrigger, testutil.RequireType[string](t, kept["id"]))
	require.NoError(t, err)
	removed := dispatchScheduledMessageTool(t, ctx, &fixture, turn, calls[1])
	removedID, err := publicid.Decode(publicid.KindCronTrigger, testutil.RequireType[string](t, removed["id"]))
	require.NoError(t, err)

	create := executionstore.CreateAgentScheduleInput{CronExpression: "0 10 * * *", MessageTemplate: "Later."}
	content, err := structuredToolResultContent(map[string]any{"scheduled": true})
	require.NoError(t, err)
	completion, err := successfulToolCallCompletion(content)
	require.NoError(t, err)
	complete := func(executionstore.AgentSchedule) (executionstore.ToolCallCompletionInput, error) {
		return completion, nil
	}

	_, err = executeScheduleCommand(ctx, &fixture, fixture.toolCallID(t, ctx, "call_delete"),
		executionstore.DeleteAgentScheduleForToolCall(removedID, completion))
	require.NoError(t, err)
	_, err = fixture.Store.Execution().GetCronTrigger(ctx, toolsTestProjectID, removedID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)

	resultFailed := errors.New("result could not be built")
	_, err = executeScheduleCommand(ctx, &fixture, fixture.toolCallID(t, ctx, "call_unfinished"),
		executionstore.CreateAgentScheduleForToolCall(create,
			func(executionstore.AgentSchedule) (executionstore.ToolCallCompletionInput, error) {
				return executionstore.ToolCallCompletionInput{}, resultFailed
			}))
	require.ErrorIs(t, err, resultFailed)
	require.Equal(t, 1, agentScheduleCount(t, ctx, &fixture), "a schedule must not outlive its failed tool result")

	// The runtime loses authority after dispatch checked it but before the schedule change runs.
	require.NoError(t, fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx, toolsTestProjectID, fixture.Agent.ID, fixture.Lock.ID))
	_, err = executeScheduleCommand(ctx, &fixture, fixture.toolCallID(t, ctx, "call_create_late"),
		executionstore.CreateAgentScheduleForToolCall(create, complete))
	require.ErrorIs(t, err, storeerr.ErrRuntimeLockInactive)
	_, err = executeScheduleCommand(ctx, &fixture, fixture.toolCallID(t, ctx, "call_delete_late"),
		executionstore.DeleteAgentScheduleForToolCall(keptID, completion))
	require.ErrorIs(t, err, storeerr.ErrRuntimeLockInactive)
	require.Equal(t, 1, agentScheduleCount(t, ctx, &fixture))
	_, err = fixture.Store.Execution().GetCronTrigger(ctx, toolsTestProjectID, keptID)
	require.NoError(t, err)
}

func TestScheduledMessageDelayStartsAfterTheAgentLockWait(t *testing.T) {
	ctx := t.Context()
	fixture := newIntegrationToolFixture(t, ctx, "scheduled-message-lock-wait")
	call := model.ToolCall{ID: "call_delay", Name: toolcatalog.ToolNameCreateScheduledMessage,
		Input: json.RawMessage(`{"message":"Soon.","delay_seconds":1}`)}
	fixture.recordToolCalls(t, ctx, []model.ToolCall{call}, fixture.Now.Add(20*time.Second))
	toolCallID := fixture.toolCallID(t, ctx, call.ID)
	content, err := structuredToolResultContent(map[string]any{"scheduled": true})
	require.NoError(t, err)
	completion, err := successfulToolCallCompletion(content)
	require.NoError(t, err)

	holder, err := fixture.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = holder.Rollback(ctx) }()
	_, err = holder.Exec(ctx, `SELECT 1 FROM agents WHERE id = $1 FOR UPDATE`, fixture.Agent.ID)
	require.NoError(t, err)

	type outcome struct {
		result executionstore.ExecuteToolCallResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := executeScheduleCommand(ctx, &fixture, toolCallID,
			executionstore.CreateScheduledMessageForToolCall(
				executionstore.CreateScheduledMessageInput{
					Delay: time.Second, Message: "Soon.", Note: scheduledMessageNote,
				},
				func(executionstore.ScheduledMessage) (executionstore.ToolCallCompletionInput, error) {
					return completion, nil
				},
			))
		done <- outcome{result: result, err: err}
	}()
	// Hold the agent lock for longer than the delay, so a delay measured from the transaction start has elapsed.
	require.Eventually(t, func() bool {
		var waiting int
		require.NoError(t, fixture.Pool.QueryRow(ctx,
			`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()
			 AND wait_event_type = 'Lock' AND clock_timestamp() - xact_start > interval '1500 milliseconds'`,
		).Scan(&waiting))
		return waiting > 0
	}, 10*time.Second, 50*time.Millisecond)
	var releasedAt time.Time
	require.NoError(t, fixture.Pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&releasedAt))
	require.NoError(t, holder.Rollback(ctx))

	finished := <-done
	require.NoError(t, finished.err)
	message := testutil.RequireType[executionstore.ScheduledMessage](t, finished.result.CommandResult)
	require.True(t, message.ScheduledFor.After(releasedAt),
		"scheduled_for %s must fall after the lock was released at %s", message.ScheduledFor, releasedAt)
}

func TestAgentScheduleLimitRejectsOnlyNewSchedules(t *testing.T) {
	ctx := t.Context()
	fixture := newIntegrationToolFixture(t, ctx, "scheduled-message-cron-limit")
	create := func(name string) error {
		_, err := fixture.Store.Execution().CreateCronTrigger(ctx, executionstore.CreateCronTriggerInput{
			ProjectID: toolsTestProjectID, Name: name,
			Target:         executionstore.CronTriggerTarget{Kind: executionstore.CronTriggerTargetAgent, ID: fixture.Agent.ID},
			CronExpression: "0 12 * * *", Timezone: "UTC", MessageTemplate: "Midday check.", Enabled: true,
			IdempotencyKey: name, CreatedByAgentID: fixture.Agent.ID, MaxTriggersCreatedByAgent: 1,
		})
		return err
	}

	require.NoError(t, create("first"))
	require.NoError(t, create("first"), "an idempotent replay must not count against the limit")
	err := create("second")
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	require.ErrorContains(t, err, "already have 1 recurring schedules")
}
