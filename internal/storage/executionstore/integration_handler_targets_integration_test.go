//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
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
			executionstore.SelectInteractionHandlerInput{HandlerKey: key, Args: json.RawMessage(args)},
			executionstore.ToolCallCompletionInput{
				Outcome:            executionstore.ToolResultOutcomeSucceeded,
				ResultContentParts: json.RawMessage(`[{"type":"text","text":"selected"}]`),
			},
		), nil
	}
}

func (f integrationInteractionFixture) selectionCall(t *testing.T) executionstore.ExecuteToolCallInput {
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

func TestIntegrationHandlerSelectionRequiresAssignedTarget(t *testing.T) {
	t.Parallel()
	for _, test := range []string{"assigned", "unassigned", "invalid-state", "invalid-address", "missing-target"} {
		t.Run(test, func(t *testing.T) {
			t.Parallel()
			f := newIntegrationInteractionFixture(t)
			switch test {
			case "unassigned":
				_, err := f.store.pool.Exec(
					f.ctx,
					`DELETE FROM integration_states WHERE integration_id=$1 AND kind='agent_conversation'`,
					f.integration.ID,
				)
				require.NoError(t, err)
			case "invalid-state", "invalid-address":
				data := `{"unexpected":true}`
				if test == "invalid-address" {
					data = `{"kind":"thread","ref":"invalid"}`
				}
				_, err := f.store.pool.Exec(
					f.ctx,
					`UPDATE integration_states SET data=$2 WHERE integration_id=$1 AND kind='agent_conversation'`,
					f.integration.ID,
					data,
				)
				require.NoError(t, err)
			case "missing-target":
				_, err := f.store.pool.Exec(
					f.ctx,
					`UPDATE integration_targets SET deleted_at=now() WHERE id=$1`,
					f.a.ID,
				)
				require.NoError(t, err)
			}
			var before, after int
			require.NoError(
				t,
				f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM integration_targets WHERE agent_id=$1`, f.process.AgentID).
					Scan(&before),
			)
			if test != "assigned" && test != "missing-target" {
				require.Equal(t, executionstore.InteractionSelection{}, f.selectOrigin(t, f.a.ID))
			}
			call := f.selectionCall(t)
			for attempt := range 2 {
				if attempt > 0 && test == "assigned" {
					call = f.selectionCall(t)
				}
				_, err := f.store.Execution().ExecuteToolCall(f.ctx, call, handlerSelectionPlan("chat", `{}`))
				if test == "assigned" {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, storeerr.ErrUnauthorized)
				}
			}
			require.NoError(
				t,
				f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM integration_targets WHERE agent_id=$1`, f.process.AgentID).
					Scan(&after),
			)
			require.Equal(t, before, after)
			selected, err := f.store.Execution().GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
			require.NoError(t, err)
			if test == "assigned" {
				require.Equal(t, f.a.ID, selected.IntegrationTargetID)
			} else {
				require.Equal(t, executionstore.InteractionSelection{}, selected)
			}
		})
	}
}

func TestIntegrationHandlerActivationClearsRevokedSelectionAndPreservesCapture(t *testing.T) {
	t.Parallel()
	f := newIntegrationInteractionFixture(t)
	f.selectOrigin(t, f.a.ID)
	prompt := f.question(t)
	delete(f.handlers, "chat")
	f.change(t, f.handlers)
	selection, err := f.store.Execution().
		GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
	require.NoError(t, err)
	require.Equal(t, executionstore.InteractionSelection{}, selection)
	require.JSONEq(t, string(prompt.Destination), string(f.read(t, prompt.ID).Destination))
	_, err = f.store.Integrations().GetIntegrationTarget(f.ctx, testProjectID, f.a.ID)
	require.NoError(t, err, "revocation preserves attribution/history")
}

func TestIntegrationHandlerPendingSelectionCannotAcquireChangedAuthority(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"removed", "replacement-integration", "unrelated"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			f := newIntegrationInteractionFixture(t)
			input := f.selectionCall(t)
			next := make(map[string]agentconfig.IntegrationCapabilityCompiled, len(f.handlers))
			for key, handler := range f.handlers {
				next[key] = handler
			}
			switch change {
			case "removed":
				delete(next, "chat")
			case "replacement-integration":
				replacement := f.createIntegration(t, "replacement")
				handler := next["chat"]
				handler.IntegrationID = replacement.ID
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
					ExecuteToolCall(f.ctx, input, handlerSelectionPlan("chat", `{}`))
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

func TestIntegrationHandlerSelectionIntegrationGatePrecedesAgentLock(t *testing.T) {
	t.Parallel()
	f := newIntegrationInteractionFixture(t)
	input := f.selectionCall(t)
	revocation := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	q := dbsqlc.New(revocation)
	require.NoError(
		t,
		q.LockProjectIntegrationLifecycleExclusive(
			f.ctx,
			dbsqlc.LockProjectIntegrationLifecycleExclusiveParams{IntegrationID: f.integration.ID},
		),
	)
	done := integrationdb.RunAsync(func() (executionstore.ExecuteToolCallResult, error) {
		return f.store.Execution().
			ExecuteToolCall(f.ctx, input, handlerSelectionPlan("chat", `{}`))
	})
	integrationdb.WaitForNamedLockWaiters(
		t,
		f.ctx,
		f.store.pool,
		"LockProjectIntegrationLifecycleShared",
		1,
	)
	lockCtx, cancel := context.WithTimeout(f.ctx, 2*time.Second)
	defer cancel()
	_, err := q.LockAgentInProject(
		lockCtx,
		dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: f.process.AgentID},
	)
	require.NoError(t, err, "selection cannot hold the agent while waiting for the integration")
	_, err = revocation.Exec(
		f.ctx,
		`UPDATE project_integrations SET state='disconnected' WHERE project_id=$1 AND id=$2`,
		testProjectID,
		f.integration.ID,
	)
	require.NoError(t, err)
	require.NoError(t, revocation.Commit(f.ctx))
	result := integrationdb.Await(t, done, "pending selection after integration revocation")
	require.ErrorIs(t, result.Err, storeerr.ErrUnauthorized)
	selection, err := f.store.Execution().
		GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
	require.NoError(t, err)
	require.Equal(t, executionstore.InteractionSelection{}, selection)
}

func TestIntegrationHandlerSelectionValidatesArgsWithoutMutation(t *testing.T) {
	t.Parallel()
	f := newIntegrationInteractionFixture(t)
	initial := f.selectOrigin(t, f.a.ID)
	input := f.selectionCall(t)
	for _, args := range []string{
		`{"channel_id":"C123","thread_ts":"111.222"}`, `{"channel_id":"C123","thread_ts":"invalid"}`, `{"integration_id":"replacement"}`, `{"extra":true}`, `null`, `[]`,
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
