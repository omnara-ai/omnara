//go:build integration

package executionstore_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestToolCallNarrowReads(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, t.Name())
	id := createTypedToolCallForProcessTest(t, ctx, f, "narrow-read", "lookup_customer", "custom", true)
	for _, state := range []executionstore.ToolCallState{
		executionstore.ToolCallStateReady, executionstore.ToolCallStateCompleted,
	} {
		if state == executionstore.ToolCallStateCompleted {
			_, err := f.Store.Execution().CompleteCustomToolCall(ctx, executionstore.CompleteCustomToolCallInput{
				ProjectID: testProjectID, AgentID: f.AgentID, ID: id,
				Outcome:       executionstore.ToolResultOutcomeSucceeded,
				ContentBlocks: json.RawMessage(`[{"type":"text","text":"customer found"}]`),
			})
			require.NoError(t, err)
		}
		full, err := f.Store.Execution().GetToolCall(ctx, testProjectID, f.AgentID, id)
		require.NoError(t, err)
		summary, err := f.Store.Execution().GetToolCallSummary(ctx, testProjectID, f.AgentID, id)
		require.NoError(t, err)
		require.Equal(t, full.Summary(), summary)
		status, err := f.Store.Execution().GetToolCallStatus(ctx, testProjectID, f.AgentID, id)
		require.NoError(t, err)
		require.Equal(t, executionstore.ToolCallStatus{Type: "custom", State: state}, status)
	}
	for _, scope := range [][3]uuid.UUID{
		{uuid.New(), f.AgentID, id},
		{testProjectID, uuid.New(), id},
		{testProjectID, f.AgentID, uuid.New()},
	} {
		_, err := f.Store.Execution().GetToolCallSummary(ctx, scope[0], scope[1], scope[2])
		require.ErrorIs(t, err, storeerr.ErrNotFound)
		_, err = f.Store.Execution().GetToolCallStatus(ctx, scope[0], scope[1], scope[2])
		require.ErrorIs(t, err, storeerr.ErrNotFound)
	}

	// Check the database work as well as the values: a completed tool's result
	// blocks and its source model output must not be visited by either preflight
	// path; the source event directly supplies the public summary's turn ID.
	tx := integrationdb.BeginTx(t, ctx, f.Store.pool)
	for _, queryName := range []string{"GetToolCallSummary", "GetToolCallStatus"} {
		query := integrationdb.GeneratedQuery(t, "../internal/dbsqlc/tool_calls.sql.go", queryName)
		var raw []byte
		require.NoError(t, tx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+query,
			testProjectID, f.AgentID, id).Scan(&raw))
		plan := integrationdb.ReadPlan(t, raw)
		require.EqualValues(t, 1, plan.Rows)
		plan.Walk(func(node integrationdb.PlanNode) {
			require.NotEqual(t, "content_blocks", node.Relation, "%s: %s", queryName, raw)
			require.NotEqual(t, "model_outputs", node.Relation, "%s: %s", queryName, raw)
			if queryName == "GetToolCallStatus" && node.Relation != "" {
				require.Equal(t, "tool_calls", node.Relation, "%s", raw)
			}
		})
	}
}

func TestRecoveryListFilterValidation(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, t.Name())
	id := createTypedToolCallForProcessTest(t, ctx, f, "filters", "lookup_customer", "custom", true)
	want, err := f.Store.Execution().GetToolCallSummary(ctx, testProjectID, f.AgentID, id)
	require.NoError(t, err)
	for _, projectWide := range []bool{false, true} {
		list := func(state executionstore.ToolCallState, kind toolcatalog.Type) (executionstore.ListToolCallsResult, error) {
			if projectWide {
				return f.Store.Execution().ListUnfinishedProjectToolCalls(ctx, executionstore.ListUnfinishedProjectToolCallsInput{
					ProjectID: testProjectID, State: state, Type: kind, Limit: 10,
				})
			}
			return f.Store.Execution().ListToolCalls(ctx, executionstore.ListToolCallsInput{
				ProjectID: testProjectID, AgentIDs: []uuid.UUID{f.AgentID}, State: state, Type: kind, Limit: 10,
			})
		}
		for _, filter := range []struct {
			state executionstore.ToolCallState
			kind  toolcatalog.Type
		}{
			{state: "unknown"}, {kind: "unknown"},
		} {
			_, err := list(filter.state, filter.kind)
			require.Error(t, err)
		}
		for _, state := range []executionstore.ToolCallState{"", executionstore.ToolCallStateReady} {
			page, err := list(state, toolcatalog.ToolTypeCustom)
			require.NoError(t, err)
			require.Equal(t, []executionstore.ToolCallSummary{want}, page.ToolCalls)
		}
		_, err := list(executionstore.ToolCallStateCompleted, "")
		if projectWide {
			require.ErrorContains(t, err, "not unfinished")
		} else {
			require.NoError(t, err)
		}
	}
	for _, state := range []executionstore.AgentInteractionState{
		"", executionstore.AgentInteractionStateOpen, executionstore.AgentInteractionStateResolved,
		executionstore.AgentInteractionStateCanceled, "unknown",
	} {
		_, err := f.Store.Execution().ListAgentInteractions(ctx, executionstore.ListAgentInteractionsInput{
			ProjectID: testProjectID, AgentIDs: []uuid.UUID{f.AgentID}, State: state, Limit: 10,
		})
		if state == "unknown" {
			require.ErrorContains(t, err, "invalid interaction state")
		} else {
			require.NoError(t, err)
		}
	}
	for _, kind := range []executionstore.AgentInteractionKind{
		"", executionstore.AgentInteractionKindQuestion, executionstore.AgentInteractionKindPermission, "unknown",
	} {
		_, err := f.Store.Execution().ListOpenProjectInteractions(ctx, executionstore.ListOpenProjectInteractionsInput{
			ProjectID: testProjectID, InteractionKind: kind, Limit: 10,
		})
		if kind == "unknown" {
			require.ErrorContains(t, err, "invalid interaction kind")
		} else {
			require.NoError(t, err)
		}
	}
}
