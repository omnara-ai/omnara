package publicevents

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestEventsIncludeEmittingAgentContext(t *testing.T) {
	for _, profile := range []uuid.UUID{uuid.Nil, uuid.New()} {
		for _, kind := range []string{
			"agent_input", "model_output", "tool_result", "context_checkpoint", "tool_call_update",
		} {
			t.Run(kind+"/"+profile.String(), func(t *testing.T) {
				agent := AgentContext{OrgID: uuid.New(), ProjectID: uuid.New(), AgentID: uuid.New(), AgentProfileID: profile}
				record := executionstore.AgentEventReadRecord{
					ID: uuid.New(), OrgID: agent.OrgID, ProjectID: agent.ProjectID, AgentID: agent.AgentID, AgentProfileID: profile,
					TurnID: uuid.New(), TurnSequence: 1, Sequence: 2, EventKind: kind,
					AgentInputID: uuid.New(), InputKind: "content", ContentBlocks: json.RawMessage(`[]`),
					ModelCallContextID: uuid.New(), ModelStopReason: "end_turn", ToolCallID: uuid.New(), ToolOutcome: "succeeded",
					ContextCheckpointID: uuid.New(), SummarizedThroughEventSequence: 1,
					CheckpointSummary: "summary", CreatedAt: time.Now(),
				}
				var event any
				var err error
				if kind == "tool_call_update" {
					event, err = ToolCallUpdate(notifications.ToolCallUpdatedCommitted{
						AgentID: agent.AgentID, ToolCallID: record.ToolCallID, State: "completed",
					}, agent)
				} else {
					event, err = EventFromReadRecord(record)
				}
				require.NoError(t, err)
				encoded, err := json.Marshal(event)
				require.NoError(t, err)
				var payload map[string]any
				require.NoError(t, json.Unmarshal(encoded, &payload))
				for key, expected := range map[string]struct {
					kind publicid.Kind
					id   uuid.UUID
				}{
					"org_id":           {publicid.KindOrganization, agent.OrgID},
					"project_id":       {publicid.KindProject, agent.ProjectID},
					"agent_id":         {publicid.KindAgent, agent.AgentID},
					"agent_profile_id": {publicid.KindAgentProfile, profile},
				} {
					require.Contains(t, payload, key)
					if expected.id == uuid.Nil {
						require.Nil(t, payload[key])
						continue
					}
					id, err := publicid.Encode(expected.kind, expected.id)
					require.NoError(t, err)
					require.Equal(t, id, payload[key], key)
				}
			})
		}
	}
}

func TestToolCallUpdateRejectsAnotherAgentsContext(t *testing.T) {
	_, err := ToolCallUpdate(
		notifications.ToolCallUpdatedCommitted{AgentID: uuid.New(), ToolCallID: uuid.New(), State: "ready"},
		AgentContext{OrgID: uuid.New(), ProjectID: uuid.New(), AgentID: uuid.New()},
	)
	require.ErrorContains(t, err, "does not match routing context")
}
