//go:build integration

package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/publicevents"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestReadAgentDispatchReturnsCompactTimeline(t *testing.T) {
	ctx := t.Context()
	fixture := newIntegrationToolFixtureWithOptions(t, ctx, "read-child", toolFixtureOptions{withSubagents: true},
		storage.WithBlobStore(integrationblob.MustOpen(t, ctx)))
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
	admitted := claim.Model.AdmittedInputTurn
	inputIDs := make([]uuid.UUID, 0, len(admitted.Inputs))
	for _, input := range admitted.Inputs {
		inputIDs = append(inputIDs, input.ID)
	}
	modelCall := claimNormalModelCallForToolsTest(t, ctx, fixture.Store, toolsTestProjectID, child.Agent.ID,
		claim.RuntimeLock, inputIDs, child.AgentConfig.ID, admitted.Events[0].Sequence, uuid.Nil)
	childFixture := fixture
	childFixture.Agent, childFixture.Lock = child.Agent, claim.RuntimeLock
	childFixture.ModelCallContextID, childFixture.ModelOutputEventID = modelCall.Context.ID, uuid.Nil
	hiddenArtifact, err := fixture.Store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
		ProjectID: toolsTestProjectID, AgentID: child.Agent.ID,
		ContentType: "text/plain", Content: []byte("internal bookkeeping"),
	})
	require.NoError(t, err)
	hiddenArtifactID, err := publicid.Encode(publicid.KindArtifact, hiddenArtifact.ID)
	require.NoError(t, err)
	childFixture.recordToolCalls(t, ctx, []model.ToolCall{{ID: "read_file", Name: "storage_read", Input: json.RawMessage(`{"path":"/memory/notes/build.txt"}`)}}, fixture.Now)
	_, err = fixture.Store.Execution().CompleteToolCall(ctx, executionstore.CompleteToolCallInput{
		ProjectID: toolsTestProjectID, AgentID: child.Agent.ID,
		ID: childFixture.toolCallID(t, ctx, "read_file"), RuntimeLockID: claim.RuntimeLock.ID,
		Outcome: executionstore.ToolResultOutcomeSucceeded,
		ResultContentParts: json.RawMessage(`[{
			"type":"text","text":"Visible build details"
		},{"type":"structured_data","value":{"digest":"hidden-bookkeeping"},"exclude_from_model_context":true},
		{"type":"media_ref","artifact_id":"` + hiddenArtifact.ID.String() + `","exclude_from_model_context":true}]`),
	})
	require.NoError(t, err)
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
	// The public timeline and durable result still retain bookkeeping content.
	storedEvents, err := fixture.Store.Execution().ListTurnEventsForRead(
		ctx, toolsTestProjectID, child.Agent.ID, turns[0].ID, 0, 20)
	require.NoError(t, err)
	var publicJSON []byte
	for _, event := range storedEvents {
		public, err := publicevents.EventFromReadRecord(event)
		require.NoError(t, err)
		raw, err := json.Marshal(public)
		require.NoError(t, err)
		var timeline struct {
			ContentBlocks []struct {
				Type    string `json:"type"`
				Exclude *bool  `json:"exclude_from_model_context"`
			} `json:"content_blocks"`
		}
		require.NoError(t, json.Unmarshal(raw, &timeline))
		for _, block := range timeline.ContentBlocks {
			if block.Type == "structured_data" {
				require.Nil(t, block.Exclude, "public structured data keeps its existing shape")
			}
		}
		publicJSON = append(publicJSON, raw...)
	}
	require.Contains(t, string(publicJSON), "hidden-bookkeeping")
	require.Contains(t, string(publicJSON), hiddenArtifactID)
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
			require.Contains(t, string(result.ContentParts), "Visible build details")
			require.NotContains(t, string(result.ContentParts), "hidden-bookkeeping")
			require.NotContains(t, string(result.ContentParts), hiddenArtifactID)
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
				for _, field := range []string{"sequence", "event_kind", "content_blocks", "created_at"} {
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
