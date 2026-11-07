package tools

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/publicevents"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestReadAgentTimelinePreservesEventContent(t *testing.T) {
	mediaID, toolCallID := uuid.New(), uuid.New()
	for _, kind := range []string{"agent_input", "model_output", "tool_result", "context_checkpoint"} {
		t.Run(kind, func(t *testing.T) {
			record := readAgentEventFixture(kind)
			record.RootAgentID = record.AgentID
			switch kind {
			case "agent_input":
				record.ContentBlocks = json.RawMessage(`[{"type":"text","text":"Please investigate"},` +
					`{"type":"media_ref","artifact_id":"` + mediaID.String() + `"}]`)
			case "model_output":
				record.ContentBlocks = json.RawMessage(`[{"type":"reasoning","text":"Checking the data"},` +
					`{"type":"tool_call","tool_call_id":"` + toolCallID.String() + `",` +
					`"tool_type":"custom","name":"lookup","input":{"project_id":"user data","n":9007199254740993}},` +
					`{"type":"text","text":"Found the answer"}]`)
			case "tool_result":
				record.ContentBlocks = json.RawMessage(`[{"type":"structured_data",` +
					`"value":{"agent_id":"user data","nested":{"agent_profile_id":"keep this"}}},` +
					`{"type":"media_ref","artifact_id":"` + mediaID.String() + `"}]`)
			}
			got, err := readAgentEventFromRecord(record)
			require.NoError(t, err)
			public, err := publicevents.EventFromReadRecord(record)
			require.NoError(t, err)
			publicJSON := readAgentJSONFields(t, public)
			fields := []string{"event_kind", "sequence", "created_at"}
			switch kind {
			case "agent_input":
				fields = append(fields, "input_kind", "control_type", "content_blocks")
			case "model_output":
				fields = append(fields, "stop_reason", "content_blocks")
			case "tool_result":
				fields = append(fields, "tool_call_id", "outcome", "content_blocks")
			case "context_checkpoint":
				fields = append(fields, "summarized_through_event_sequence", "summary")
			}
			want := make(map[string]json.RawMessage, len(fields))
			for _, field := range fields {
				want[field] = publicJSON[field]
			}
			require.Equal(t, want, readAgentJSONFields(t, got))
		})
	}
}

func TestReadAgentTimelinePages(t *testing.T) {
	status := executionstore.SubagentStatus{
		AgentID: uuid.New(), Name: "Researcher", Key: "research", State: "working", LastActivityAt: time.Now(),
	}
	event := readAgentEventFixture("agent_input")
	event.AgentID = status.AgentID
	answer := readAgentEventFixture("model_output")
	answer.Sequence = 8
	answer.ContentBlocks = json.RawMessage(`[{"type":"text","text":"The build failed because a dependency is missing"}]`)
	checkpoint := readAgentEventFixture("context_checkpoint")
	checkpoint.Sequence = 9
	turn := executionstore.AgentTurnReadRecord{
		AgentTurnRecord: executionstore.AgentTurnRecord{ID: event.TurnID, AgentID: status.AgentID, TurnSequence: 3},
		EventCount:      4, StartedAt: event.CreatedAt, UpdatedAt: event.CreatedAt,
		OpeningEvents: []executionstore.AgentEventReadRecord{event}, LatestEvent: checkpoint, LatestSemanticEvent: answer,
	}
	older := turn
	older.ID = uuid.New()
	older.TurnSequence = 2
	oldest := older
	oldest.ID = uuid.New()
	oldest.TurnSequence = 1

	content, err := readAgentTurnsContent(status, []executionstore.AgentTurnReadRecord{turn, older, oldest}, 2)
	require.NoError(t, err)
	value := readAgentStructuredValue(t, content)
	var page struct {
		Agent subagentSummary              `json:"agent"`
		Turns []map[string]json.RawMessage `json:"turns"`
		Next  *int64                       `json:"next_before_turn_sequence"`
	}
	require.NoError(t, json.Unmarshal(value, &page))
	require.Equal(t, "Researcher", page.Agent.Name)
	id, err := publicid.Encode(publicid.KindAgent, status.AgentID)
	require.NoError(t, err)
	require.Equal(t, id, page.Agent.AgentID)
	require.Len(t, page.Turns, 2)
	require.NotNil(t, page.Next)
	require.EqualValues(t, 2, *page.Next)
	require.JSONEq(t, `3`, string(page.Turns[0]["turn_sequence"]))
	require.JSONEq(t, `2`, string(page.Turns[1]["turn_sequence"]))
	require.Len(t, page.Turns[0], 8)
	var latest, semantic map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(page.Turns[0]["latest_event"], &latest))
	require.NoError(t, json.Unmarshal(page.Turns[0]["latest_semantic_event"], &semantic))
	require.JSONEq(t, `"context_checkpoint"`, string(latest["event_kind"]))
	require.JSONEq(t, `"model_output"`, string(semantic["event_kind"]))
	require.Contains(t, string(semantic["content_blocks"]), "The build failed because a dependency is missing")

	events := make([]executionstore.AgentEventReadRecord, 3)
	for index := range events {
		events[index] = event
		events[index].Sequence = int64(index + 5)
	}
	turnID, err := publicid.Encode(publicid.KindAgentTurn, event.TurnID)
	require.NoError(t, err)
	content, err = readAgentTurnEventsContent(status, turnID, events, 2)
	require.NoError(t, err)
	var eventPage struct {
		Agent  subagentSummary `json:"agent"`
		TurnID string          `json:"turn_id"`
		Events []struct {
			Sequence int64 `json:"sequence"`
		} `json:"events"`
		Next *int64 `json:"next_before_sequence"`
	}
	require.NoError(t, json.Unmarshal(readAgentStructuredValue(t, content), &eventPage))
	require.Equal(t, id, eventPage.Agent.AgentID)
	require.Equal(t, turnID, eventPage.TurnID)
	require.Len(t, eventPage.Events, 2)
	require.EqualValues(t, 6, eventPage.Events[0].Sequence)
	require.EqualValues(t, 7, eventPage.Events[1].Sequence)
	require.NotNil(t, eventPage.Next)
	require.EqualValues(t, 6, *eventPage.Next)
}

func TestReadAgentEmptyTimelinePages(t *testing.T) {
	status := executionstore.SubagentStatus{AgentID: uuid.New()}
	turns, err := readAgentTurnsContent(status, nil, 10)
	require.NoError(t, err)
	fields := readAgentJSONFields(t, readAgentStructuredValue(t, turns))
	require.JSONEq(t, `[]`, string(fields["turns"]))
	require.JSONEq(t, `null`, string(fields["next_before_turn_sequence"]))
	events, err := readAgentTurnEventsContent(status, "trn_example", nil, 20)
	require.NoError(t, err)
	fields = readAgentJSONFields(t, readAgentStructuredValue(t, events))
	require.JSONEq(t, `[]`, string(fields["events"]))
	require.JSONEq(t, `null`, string(fields["next_before_sequence"]))
}

func readAgentEventFixture(kind string) executionstore.AgentEventReadRecord {
	return executionstore.AgentEventReadRecord{
		ID: uuid.New(), OrgID: uuid.New(), ProjectID: uuid.New(), AgentID: uuid.New(), AgentProfileID: uuid.New(),
		TurnID: uuid.New(), TurnSequence: 3, Sequence: 7, EventKind: kind, IsOpeningEvent: kind == "agent_input",
		AgentInputID: uuid.New(), InputKind: "content", ActorID: uuid.New(), InputIdempotencyKey: "input-key",
		AgentConfigID: uuid.New(), TargetInteractionID: uuid.New(), ControlType: "cancel_current",
		ModelCallContextID: uuid.New(), ModelStopReason: "tool_use", ModelUsage: modelenvelope.Usage{InputTokens: 12},
		ToolCallID: uuid.New(), ToolOutcome: executionstore.ToolResultOutcomeFailed,
		ContextCheckpointID: uuid.New(), SummarizedThroughEventSequence: 5, CheckpointSummary: "Work so far",
		ContentBlocks: json.RawMessage(`[]`), CreatedAt: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC),
	}
}

func readAgentJSONFields(t *testing.T, value any) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields))
	return fields
}

func readAgentStructuredValue(t *testing.T, content toolResultContent) json.RawMessage {
	t.Helper()
	raw, err := content.contentParts()
	require.NoError(t, err)
	var blocks []struct {
		Type   string          `json:"type"`
		Value  json.RawMessage `json:"value"`
		Hidden bool            `json:"exclude_from_model_context"`
	}
	require.NoError(t, json.Unmarshal(raw, &blocks))
	require.Len(t, blocks, 1)
	require.Equal(t, "structured_data", blocks[0].Type)
	require.False(t, blocks[0].Hidden)
	return blocks[0].Value
}

func TestReadAgentTimelineRejectsInvalidEvents(t *testing.T) {
	for _, test := range []struct {
		name   string
		kind   string
		change func(*executionstore.AgentEventReadRecord)
		want   string
	}{
		{name: "unknown event", kind: "future_event", want: "unsupported agent event kind"},
		{name: "invalid input kind", kind: "agent_input", change: func(r *executionstore.AgentEventReadRecord) {
			r.InputKind = "unknown"
		}, want: "invalid agent input kind"},
		{name: "invalid control", kind: "agent_input", change: func(r *executionstore.AgentEventReadRecord) {
			r.ControlType = "unknown"
		}, want: "invalid agent control type"},
		{name: "invalid stop reason", kind: "model_output", change: func(r *executionstore.AgentEventReadRecord) {
			r.ModelStopReason = "unknown"
		}, want: "invalid model stop reason"},
		{name: "invalid opening event", kind: "context_checkpoint", change: func(r *executionstore.AgentEventReadRecord) {
			r.IsOpeningEvent = true
		}, want: "cannot open a turn"},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := readAgentEventFixture(test.kind)
			if test.change != nil {
				test.change(&record)
			}
			_, err := readAgentEventFromRecord(record)
			require.ErrorContains(t, err, test.want)
		})
	}
}
