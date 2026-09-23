//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func handlerSelectionPlan(key, args string) executionstore.ToolCallPlan {
	return func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
		return executionstore.SetInteractionHandlerForToolCall(
			executionstore.InteractionSelection{HandlerKey: key, Args: json.RawMessage(args)},
			executionstore.ToolCallCompletionInput{
				Outcome:            executionstore.ToolResultOutcomeSucceeded,
				ResultContentParts: json.RawMessage(`[{"type":"text","text":"selected"}]`),
			},
		), nil
	}
}

func (f appInteractionFixture) selectionCall(t *testing.T) executionstore.ExecuteToolCallInput {
	t.Helper()
	id := createToolCallForProcessTest(
		t,
		f.ctx,
		f.process,
		uuid.NewString(),
		toolcatalog.ToolNameSetInteractionHandler,
	)
	return executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       f.process.AgentID,
		ToolCallID:    id,
		RuntimeLockID: f.process.Lock.ID,
	}
}

func TestAppHandlerSelectionCreatesAndReusesTargets(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	config, err := f.store.Execution().
		CreateAgentConfig(f.ctx, f.definition(t, "handler without catalog", f.handlers))
	require.NoError(t, err)
	launch, err := f.store.Execution().LaunchAgent(f.ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		AgentConfigID:  config.ID,
		LaunchedBy:     userPrincipal(f.user.ID),
		IdempotencyKey: "handler",
	})
	require.NoError(t, err)
	var count int
	require.NoError(
		t,
		f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM app_targets WHERE agent_id=$1`, launch.Agent.ID).
			Scan(&count),
	)
	require.Zero(t, count, "config save and activation create no handler targets")
	lock, err := f.store.Execution().
		AcquireAgentRuntimeLock(f.ctx, testProjectID, launch.Agent.ID, testWorkerProcessID, testAgentRuntimeLockLeaseDuration)
	require.NoError(t, err)
	f.process = processDaemonFixture{
		Store:   f.store,
		AgentID: launch.Agent.ID,
		UserID:  f.user.ID,
		Lock:    lock,
	}
	for range 2 {
		_, err = f.store.Execution().
			ExecuteToolCall(f.ctx, f.selectionCall(t),
				handlerSelectionPlan("chat", `{"channel_id":"C777","thread_ts":"111.222"}`))
		require.NoError(t, err)
	}
	require.NoError(
		t,
		f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM app_targets WHERE agent_id=$1`, launch.Agent.ID).
			Scan(&count),
	)
	require.Equal(t, 1, count, "selection creates and reuses canonical attribution")
	f.disable(t)
	replay, err := f.store.Execution().LaunchAgent(f.ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		AgentConfigID:  config.ID,
		LaunchedBy:     userPrincipal(f.user.ID),
		IdempotencyKey: "handler",
	})
	require.NoError(t, err)
	require.False(t, replay.Created)
}

func TestAppHandlerActivationClearsRevokedSelectionAndPreservesCapture(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	f.selectOrigin(t, f.a.ID)
	prompt := f.question(t)
	delete(f.handlers, "chat")
	f.change(t, f.handlers)
	selection, err := f.store.Execution().
		GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
	require.NoError(t, err)
	require.Equal(t, executionstore.InteractionSelection{}, selection)
	require.JSONEq(t, string(prompt.Destination), string(f.read(t, prompt.ID).Destination))
	_, err = f.store.Apps().GetAppTarget(f.ctx, testProjectID, f.a.ID)
	require.NoError(t, err, "revocation preserves attribution/history")
}

func TestAppHandlerSelectionConversationGatePrecedesAgentLock(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	input := f.selectionCall(t)
	blocker := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	require.NoError(t, appstore.LockConversationTx(f.ctx, blocker, testProjectID, f.app.ID,
		appstore.ConversationAddress{Kind: "thread", Ref: "C123:555.666"}))
	done := integrationdb.RunAsync(func() (executionstore.ExecuteToolCallResult, error) {
		return f.store.Execution().
			ExecuteToolCall(f.ctx, input, handlerSelectionPlan("chat", `{"channel_id":"C123","thread_ts":"555.666"}`))
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockAppConversation", 1)
	lockCtx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
	defer cancel()
	_, err := dbsqlc.New(blocker).
		LockAgentInProject(lockCtx, dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: f.process.AgentID})
	require.NoError(t, err, "selection must not hold the agent while waiting for a conversation")
	require.NoError(t, blocker.Commit(f.ctx))
	integrationdb.AwaitSuccess(t, done, "handler selection")
	selection, err := f.store.Execution().
		GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
	require.NoError(t, err)
	require.NotEqual(t, f.a.ID, selection.AppTargetID)
}

func TestAppHandlerPendingSelectionCannotAcquireChangedAuthority(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"removed", "replacement-app", "unrelated"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			f := newAppInteractionFixture(t)
			input := f.selectionCall(t)
			next := make(map[string]agentconfig.AppCapabilityCompiled, len(f.handlers))
			for key, handler := range f.handlers {
				next[key] = handler
			}
			switch change {
			case "removed":
				delete(next, "chat")
			case "replacement-app":
				replacement := f.createApp(t, "replacement")
				handler := next["chat"]
				handler.AppID = replacement.ID
				next["chat"] = handler
			}
			config, err := f.store.Execution().
				CreateAgentConfig(f.ctx, f.definition(t, "pending handler authority", next))
			require.NoError(t, err)
			activation := integrationdb.BeginTx(t, f.ctx, f.store.pool)
			_, err = dbsqlc.New(activation).
				LockAgentInProject(f.ctx, dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: f.process.AgentID})
			require.NoError(t, err)
			done := integrationdb.RunAsync(func() (executionstore.ExecuteToolCallResult, error) {
				return f.store.Execution().
					ExecuteToolCall(f.ctx, input, handlerSelectionPlan("chat", `{"channel_id":"C123","thread_ts":"111.222"}`))
			})
			integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockAgentInProject", 1)
			_, err = activation.Exec(
				f.ctx,
				`UPDATE agents SET current_config_id=$3 WHERE project_id=$1 AND id=$2`,
				testProjectID,
				f.process.AgentID,
				config.ID,
			)
			require.NoError(t, err)
			require.NoError(t, activation.Commit(f.ctx))
			result := integrationdb.Await(
				t,
				done,
				"selection after config changed while waiting for agent lock",
			)
			if change == "unrelated" {
				require.NoError(t, result.Err)
			} else {
				require.ErrorIs(t, result.Err, storeerr.ErrUnauthorized)
			}
			selection, err := f.store.Execution().
				GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
			require.NoError(t, err)
			if change == "unrelated" {
				require.Equal(t, "chat", selection.HandlerKey)
			} else {
				require.Equal(t, executionstore.InteractionSelection{}, selection)
			}
		})
	}
}

func TestAppHandlerSelectionAppGatePrecedesAgentLock(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	input := f.selectionCall(t)
	revocation := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	q := dbsqlc.New(revocation)
	require.NoError(
		t,
		q.LockProjectAppLifecycleExclusive(
			f.ctx,
			dbsqlc.LockProjectAppLifecycleExclusiveParams{AppID: f.app.ID},
		),
	)
	done := integrationdb.RunAsync(func() (executionstore.ExecuteToolCallResult, error) {
		return f.store.Execution().
			ExecuteToolCall(f.ctx, input, handlerSelectionPlan("chat", `{"channel_id":"C123","thread_ts":"555.666"}`))
	})
	integrationdb.WaitForNamedLockWaiters(
		t,
		f.ctx,
		f.store.pool,
		"LockProjectAppLifecycleShared",
		1,
	)
	lockCtx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
	defer cancel()
	_, err := q.LockAgentInProject(
		lockCtx,
		dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: f.process.AgentID},
	)
	require.NoError(t, err, "selection cannot hold the agent while waiting for the app")
	_, err = revocation.Exec(
		f.ctx,
		`UPDATE project_apps SET state='disconnected' WHERE project_id=$1 AND id=$2`,
		testProjectID,
		f.app.ID,
	)
	require.NoError(t, err)
	require.NoError(t, revocation.Commit(f.ctx))
	result := integrationdb.Await(t, done, "pending selection after app revocation")
	require.ErrorIs(t, result.Err, storeerr.ErrUnauthorized)
	selection, err := f.store.Execution().
		GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
	require.NoError(t, err)
	require.Equal(t, executionstore.InteractionSelection{}, selection)
}

func TestAppHandlerSelectionValidatesArgsWithoutMutation(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	initial := f.selectOrigin(t, f.a.ID)
	input := f.selectionCall(t)
	for _, args := range []string{
		`{}`, `{"channel_id":"C123","thread_ts":"invalid"}`, `{"app_id":"replacement"}`, `{"extra":true}`, `null`, `[]`,
	} {
		_, err := f.store.Execution().
			ExecuteToolCall(f.ctx, input, handlerSelectionPlan("chat", args))
		require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
		selected, err := f.store.Execution().
			GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
		require.NoError(t, err)
		require.Equal(t, initial, selected)
	}
	_, err := f.store.Execution().
		ExecuteToolCall(f.ctx, input, handlerSelectionPlan("", `{"channel_id":"C123","thread_ts":"111.222"}`))
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	_, err = f.store.Execution().ExecuteToolCall(f.ctx, input, handlerSelectionPlan("", `{}`))
	require.NoError(t, err)
	selected, err := f.store.Execution().
		GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
	require.NoError(t, err)
	require.Equal(t, executionstore.InteractionSelection{}, selected)
}
