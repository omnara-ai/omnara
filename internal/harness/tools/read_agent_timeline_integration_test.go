//go:build integration

package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestReadAgentDispatchReturnsCompactTimeline(t *testing.T) {
	ctx := t.Context()
	fixture := newIntegrationToolFixtureWithOptions(t, ctx, "read-child", toolFixtureOptions{withSubagents: true})
	child, err := fixture.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: toolsTestProjectID, AgentConfigID: fixture.AgentConfig.ID,
		LaunchedBy: toolsTestUserPrincipal(fixture.User.ID),
		Subagent:   &executionstore.SubagentLaunch{ParentAgentID: fixture.Agent.ID, Key: "fork", MaxDepth: 4},
		InitialInput: &executionstore.LaunchInitialInput{
			ContentBlocks: json.RawMessage(`[{"type":"text","text":"Investigate the failing build"}]`),
		},
	})
	require.NoError(t, err)
	claim, found, err := fixture.Store.Execution().ClaimNextAgentWork(ctx, toolsTestClaimInput())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, child.Agent.ID, claim.RuntimeLock.AgentID)
	turns, err := fixture.Store.Execution().ListAgentTurnsForRead(ctx, toolsTestProjectID, child.Agent.ID, 0, 10)
	require.NoError(t, err)
	require.NotEmpty(t, turns)
	childID, err := publicid.Encode(publicid.KindAgent, child.Agent.ID)
	require.NoError(t, err)
	turnID, err := publicid.Encode(publicid.KindAgentTurn, turns[0].ID)
	require.NoError(t, err)
	parentID, err := publicid.Encode(publicid.KindAgent, fixture.Agent.ID)
	require.NoError(t, err)
	calls := []model.ToolCall{
		{ID: "read_turns", Name: "read_agent", Input: json.RawMessage(`{"agent_id":"` + childID + `"}`)},
		{ID: "read_events", Name: "read_agent", Input: json.RawMessage(
			`{"agent_id":"` + childID + `","turn_id":"` + turnID + `"}`)},
		{ID: "read_non_child", Name: "read_agent", Input: json.RawMessage(`{"agent_id":"` + parentID + `"}`)},
	}
	fixture.recordToolCalls(t, ctx, calls, fixture.Now.Add(20*time.Second))
	turn := fixture.turn()
	turn.Tools["read_agent"] = ToolSpec{Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow)}
	for _, call := range calls {
		t.Run(call.ID, func(t *testing.T) {
			result, err := (Executor{Store: fixture.Store}).Dispatch(ctx, turn, call)
			require.NoError(t, err)
			require.Equal(t, DispatchCompleted, result.Disposition)
			var blocks []struct {
				Type  string `json:"type"`
				Value struct {
					Agent subagentSummary `json:"agent"`
					Turns []struct {
						ID            string                       `json:"id"`
						OpeningEvents []map[string]json.RawMessage `json:"opening_events"`
					} `json:"turns"`
					TurnID    string                       `json:"turn_id"`
					Events    []map[string]json.RawMessage `json:"events"`
					ErrorCode string                       `json:"error_code"`
				} `json:"value"`
			}
			require.NoError(t, json.Unmarshal(result.ContentParts, &blocks))
			require.Len(t, blocks, 1)
			require.Equal(t, "structured_data", blocks[0].Type)
			value := blocks[0].Value
			if call.ID == "read_non_child" {
				require.Equal(t, "read_agent_failed", value.ErrorCode)
				return
			}
			require.Equal(t, childID, value.Agent.AgentID)
			require.Equal(t, 1, strings.Count(string(result.ContentParts), "Investigate the failing build"))
			events := value.Events
			if call.ID == "read_turns" {
				require.NotEmpty(t, value.Turns)
				require.Equal(t, turnID, value.Turns[0].ID)
				events = value.Turns[0].OpeningEvents
			} else {
				require.Equal(t, turnID, value.TurnID)
			}
			require.NotEmpty(t, events)
			var previous int64
			for _, event := range events {
				require.Len(t, event, 5)
				for _, field := range []string{"sequence", "event_kind", "input_kind", "content_blocks", "created_at"} {
					require.Contains(t, event, field)
				}
				var sequence int64
				require.NoError(t, json.Unmarshal(event["sequence"], &sequence))
				require.Greater(t, sequence, previous)
				previous = sequence
			}
		})
	}
}
