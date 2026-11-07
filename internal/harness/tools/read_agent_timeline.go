package tools

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/httpapi/publicevents"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

type readAgentTurnsResult struct {
	Agent                  subagentSummary `json:"agent"`
	Turns                  []readAgentTurn `json:"turns"`
	NextBeforeTurnSequence *int64          `json:"next_before_turn_sequence"`
}

type readAgentTurnEventsResult struct {
	Agent              subagentSummary  `json:"agent"`
	TurnID             string           `json:"turn_id"`
	Events             []readAgentEvent `json:"events"`
	NextBeforeSequence *int64           `json:"next_before_sequence"`
}

type readAgentTurn struct {
	ID                  string           `json:"id"`
	TurnSequence        int64            `json:"turn_sequence"`
	EventCount          int64            `json:"event_count"`
	OpeningEvents       []readAgentEvent `json:"opening_events"`
	LatestEvent         readAgentEvent   `json:"latest_event,omitempty"`
	LatestSemanticEvent readAgentEvent   `json:"latest_semantic_event,omitempty"`
	StartedAt           time.Time        `json:"started_at"`
	UpdatedAt           time.Time        `json:"updated_at"`
}

type readAgentEvent interface {
	readAgentTimelineEvent()
}

type readAgentEventHeader struct {
	EventKind string    `json:"event_kind"`
	Sequence  int64     `json:"sequence"`
	CreatedAt time.Time `json:"created_at"`
}

func (readAgentEventHeader) readAgentTimelineEvent() {}

type readAgentInputEvent struct {
	readAgentEventHeader
	InputKind     openapi.AgentInputKind           `json:"input_kind"`
	ControlType   openapi.AgentControlType         `json:"control_type,omitempty"`
	ContentBlocks []openapi.AgentInputContentBlock `json:"content_blocks"`
}

type readAgentModelOutputEvent struct {
	readAgentEventHeader
	StopReason    openapi.ModelOutputStopReason     `json:"stop_reason"`
	ContentBlocks []openapi.ModelOutputContentBlock `json:"content_blocks"`
}

type readAgentToolResultEvent struct {
	readAgentEventHeader
	ToolCallID    string                           `json:"tool_call_id"`
	Outcome       openapi.ToolCallOutcome          `json:"outcome"`
	ContentBlocks []openapi.ToolResultContentBlock `json:"content_blocks"`
}

type readAgentCheckpointEvent struct {
	readAgentEventHeader
	SummarizedThroughEventSequence int64  `json:"summarized_through_event_sequence"`
	Summary                        string `json:"summary"`
}

func readAgentTurnsContent(
	status executionstore.SubagentStatus,
	records []executionstore.AgentTurnReadRecord,
	limit int32,
) (toolResultContent, error) {
	summary, err := subagentSummaryFromStatus(status)
	if err != nil {
		return toolResultContent{}, err
	}
	result := readAgentTurnsResult{Agent: summary, Turns: make([]readAgentTurn, 0, min(len(records), int(limit)))}
	if len(records) > int(limit) {
		records = records[:limit]
		result.NextBeforeTurnSequence = &records[len(records)-1].TurnSequence
	}
	for _, record := range records {
		turn, err := readAgentTurnFromRecord(record)
		if err != nil {
			return toolResultContent{}, err
		}
		result.Turns = append(result.Turns, turn)
	}
	return structuredToolResultContent(result)
}

func readAgentTurnEventsContent(
	status executionstore.SubagentStatus,
	turnID string,
	records []executionstore.AgentEventReadRecord,
	limit int32,
) (toolResultContent, error) {
	summary, err := subagentSummaryFromStatus(status)
	if err != nil {
		return toolResultContent{}, err
	}
	records, next := publicevents.TrimEventsBeforePage(records, limit)
	events, err := readAgentEventsFromRecords(records)
	if err != nil {
		return toolResultContent{}, err
	}
	return structuredToolResultContent(readAgentTurnEventsResult{
		Agent: summary, TurnID: turnID, Events: events, NextBeforeSequence: next,
	})
}

func readAgentTurnFromRecord(record executionstore.AgentTurnReadRecord) (readAgentTurn, error) {
	id, err := publicid.Encode(publicid.KindAgentTurn, record.ID)
	if err != nil {
		return readAgentTurn{}, err
	}
	opening, err := readAgentEventsFromRecords(record.OpeningEvents)
	if err != nil {
		return readAgentTurn{}, err
	}
	turn := readAgentTurn{
		ID: id, TurnSequence: record.TurnSequence, EventCount: record.EventCount,
		OpeningEvents: opening, StartedAt: record.StartedAt, UpdatedAt: record.UpdatedAt,
	}
	// Keep each event's content once, preferring opening events and then latest_event.
	seen := make(map[uuid.UUID]struct{}, len(record.OpeningEvents)+1)
	for _, event := range record.OpeningEvents {
		seen[event.ID] = struct{}{}
	}
	if _, duplicate := seen[record.LatestEvent.ID]; record.LatestEvent.ID != uuid.Nil && !duplicate {
		turn.LatestEvent, err = readAgentEventFromRecord(record.LatestEvent)
		if err != nil {
			return readAgentTurn{}, err
		}
		seen[record.LatestEvent.ID] = struct{}{}
	}
	if _, duplicate := seen[record.LatestSemanticEvent.ID]; record.LatestSemanticEvent.ID != uuid.Nil && !duplicate {
		turn.LatestSemanticEvent, err = readAgentEventFromRecord(record.LatestSemanticEvent)
		if err != nil {
			return readAgentTurn{}, err
		}
	}
	return turn, nil
}

func readAgentEventsFromRecords(records []executionstore.AgentEventReadRecord) ([]readAgentEvent, error) {
	events := make([]readAgentEvent, 0, len(records))
	for _, record := range records {
		event, err := readAgentEventFromRecord(record)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func readAgentEventFromRecord(record executionstore.AgentEventReadRecord) (readAgentEvent, error) {
	header := readAgentEventHeader{
		EventKind: record.EventKind, Sequence: record.Sequence, CreatedAt: record.CreatedAt,
	}
	if record.IsOpeningEvent && record.EventKind != "agent_input" {
		return nil, fmt.Errorf("%s cannot open a turn", record.EventKind)
	}
	switch record.EventKind {
	case "agent_input":
		inputKind := openapi.AgentInputKind(record.InputKind)
		if !inputKind.Valid() {
			return nil, fmt.Errorf("invalid agent input kind %q", record.InputKind)
		}
		controlType := openapi.AgentControlType(record.ControlType)
		if controlType != "" && !controlType.Valid() {
			return nil, fmt.Errorf("invalid agent control type %q", record.ControlType)
		}
		blocks, err := publicevents.AgentInputContentBlocks(record.ContentBlocks)
		if err != nil {
			return nil, err
		}
		return readAgentInputEvent{
			readAgentEventHeader: header, InputKind: inputKind, ControlType: controlType, ContentBlocks: blocks,
		}, nil
	case "model_output":
		stopReason := openapi.ModelOutputStopReason(record.ModelStopReason)
		if !stopReason.Valid() {
			return nil, fmt.Errorf("invalid model stop reason %q", record.ModelStopReason)
		}
		blocks, err := publicevents.ModelOutputContentBlocks(record.ContentBlocks)
		if err != nil {
			return nil, err
		}
		return readAgentModelOutputEvent{
			readAgentEventHeader: header, StopReason: stopReason, ContentBlocks: blocks,
		}, nil
	case "tool_result":
		toolCallID, err := publicid.Encode(publicid.KindToolCall, record.ToolCallID)
		if err != nil {
			return nil, err
		}
		outcome, err := publicevents.ToolCallOutcome(record.ToolOutcome)
		if err != nil {
			return nil, err
		}
		blocks, err := publicevents.ToolResultContentBlocks(record.ContentBlocks)
		if err != nil {
			return nil, err
		}
		return readAgentToolResultEvent{
			readAgentEventHeader: header, ToolCallID: toolCallID, Outcome: outcome, ContentBlocks: blocks,
		}, nil
	case "context_checkpoint":
		return readAgentCheckpointEvent{
			readAgentEventHeader:           header,
			SummarizedThroughEventSequence: record.SummarizedThroughEventSequence, Summary: record.CheckpointSummary,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported agent event kind %q", record.EventKind)
	}
}
