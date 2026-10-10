//go:build integration

package agentexecution_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/stretchr/testify/require"
)

func TestOrdinaryExecutionLoadStatementBound(t *testing.T) {
	f := releasedExecutionFixture(t)
	ids := make(map[string]uuid.UUID)
	for _, name := range []string{"idle", "initial", "incomplete", "complete", "retry"} {
		a := f.agent(t)
		ids[name] = a
		if name == "idle" {
			continue
		}
		turn, _ := f.opening(t, a, 1, uuid.Nil, "content")
		if name == "initial" {
			continue
		}
		c := f.modelContext(t, a, 1, "normal", nil, 1)
		if name == "retry" {
			f.fail(t, c, "retry")
			continue
		}
		_, call := f.output(t, a, turn, c, 2, "tool_use", "awaiting_authorization")
		if name == "complete" {
			f.complete(t, a, turn, call, 3)
		}
	}
	require.NoError(t, applyExecutionCutover(t.Context(), f.pool))
	for name, id := range ids {
		t.Run(name, func(t *testing.T) {
			_, h := f.handle(t, id)
			snapshot, count, err := agentexecution.CountOrdinaryLoad(t.Context(), h)
			require.NoError(t, err)
			require.Equal(t, 2, count)
			_, count, err = agentexecution.CountOrdinaryLoad(t.Context(), h)
			require.NoError(t, err)
			expected := 0
			if snapshot.Selection.Wait == agentexecution.WaitModelDeadline {
				expected = 1
			}
			require.Equal(t, expected, count)
			plan, err := agentexecution.ExplainOrdinaryLoad(t.Context(), h)
			require.NoError(t, err)
			t.Log(plan)
			if snapshot.View.Turn != nil && name != "initial" {
				require.Empty(t, snapshot.View.Turn.InitialOpening.InputIDs)
			}
		})
	}
}
