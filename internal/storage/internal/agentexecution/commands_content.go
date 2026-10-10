package agentexecution

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/omnara-ai/omnara/internal/jsoncanonical"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type Content struct {
	Ordinal    int32
	Kind       string
	Text       string
	Data       json.RawMessage
	ArtifactID uuid.UUID
	ToolID     uuid.UUID
	Exclude    bool
	Metadata   json.RawMessage
}

type ContentConflictError struct{ Stored []Content }

func (e *ContentConflictError) Error() string { return storeerr.ErrIdempotencyConflict.Error() }
func (e *ContentConflictError) Unwrap() error { return storeerr.ErrIdempotencyConflict }

type ExecutionEvent struct {
	ID     uuid.UUID
	TurnID uuid.UUID
	EventBoundary
}

func objectJSON(data json.RawMessage) (json.RawMessage, error) {
	if len(data) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("JSON object is required")
	}
	return json.Marshal(object)
}

func equalJSON(a, b json.RawMessage) bool {
	return jsoncanonical.Equal(a, b)
}

func optionalText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
func optionalSequence(n int64) *int64 {
	if n == 0 {
		return nil
	}
	return &n
}

func (h *Handle) appendEvent(
	ctx context.Context,
	turn uuid.UUID,
	kind string,
	input, output, checkpoint uuid.UUID,
	opening bool,
) (ExecutionEvent, error) {
	args := executiondb.AppendExecutionEventParams{ID: uuid.New(),
		AgentID:   h.route.AgentID,
		ProjectID: h.route.ProjectID,
		TurnID:    nullableID(turn),
		Kind:      kind,
		InputID: nullableID(
			input,
		), OutputID: nullableID(output), CheckpointID: nullableID(checkpoint), Opening: opening}
	if input != uuid.Nil {
		args.IdempotencyKey = optionalText("agent_input:" + input.String())
	}
	row, err := executiondb.New().AppendExecutionEvent(ctx, h.unit.DB(), args)
	if err != nil {
		return ExecutionEvent{}, err
	}
	h.unit.Notifications().AddAgentEvent(h.route.AgentID, row.Sequence, kind)
	return ExecutionEvent{
		ID:            row.ID,
		TurnID:        turn,
		EventBoundary: EventBoundary{Sequence: row.Sequence, Time: row.CreatedAt},
	}, nil
}

func (h *Handle) writeContent(ctx context.Context, kind string, id uuid.UUID, parts []Content) error {
	for n, part := range parts {
		ordinal := int32(n)
		if kind == "model_output" {
			ordinal = part.Ordinal
		}
		if err := dbsafe.Text(part.Text); err != nil {
			return err
		}
		metadata, err := objectJSON(part.Metadata)
		if err != nil {
			return err
		}
		if err := dbsafe.JSONStrings(metadata); err != nil {
			return err
		}
		args := executiondb.InsertExecutionContentParams{AgentID: h.route.AgentID,
			ProjectID:  h.route.ProjectID,
			OwnerKind:  kind,
			Ordinal:    ordinal,
			Kind:       part.Kind,
			ArtifactID: nullableID(part.ArtifactID),
			ToolID:     nullableID(part.ToolID),
			Exclude:    part.Exclude,
			Metadata:   metadata}
		switch kind {
		case "agent_input":
			args.InputID = &id
		case "model_output":
			args.OutputID = &id
		case "tool_call_result":
			args.ResultID = &id
		}
		if part.Kind == "text" || part.Kind == "reasoning" || part.Kind == "error" {
			args.Text = &part.Text
		}
		if len(part.Data) > 0 {
			if !json.Valid(part.Data) {
				return errors.New("invalid structured content")
			}
			if err := dbsafe.JSONStrings(part.Data); err != nil {
				return err
			}
			args.Data = &part.Data
		}
		if err := executiondb.New().InsertExecutionContent(ctx, h.unit.DB(), args); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handle) contentMatches(ctx context.Context, input, output uuid.UUID, parts []Content) error {
	return h.matchContent(ctx, executiondb.ReadExecutionContentParams{
		AgentID: h.route.AgentID, InputID: nullableID(input), OutputID: nullableID(output)}, parts)
}

func (h *Handle) matchContent(
	ctx context.Context,
	args executiondb.ReadExecutionContentParams,
	parts []Content,
) error {
	rows, err := executiondb.New().ReadExecutionContent(ctx, h.unit.DB(), args)
	if err != nil {
		return err
	}
	conflict := &ContentConflictError{Stored: make([]Content, len(rows))}
	for i, row := range rows {
		conflict.Stored[i] = Content{
			Ordinal:    row.Ordinal,
			Kind:       row.BlockKind,
			Text:       valueOrZero(row.TextContent),
			Data:       valueOrZero(row.StructuredData),
			ArtifactID: valueOrZero(row.ArtifactID),
			ToolID:     valueOrZero(row.ToolCallID),
			Exclude:    row.ExcludeFromModelContext,
			Metadata:   row.Metadata,
		}
	}
	if len(rows) != len(parts) {
		return conflict
	}
	for i, row := range rows {
		part := parts[i]
		metadata, err := objectJSON(part.Metadata)
		if err != nil {
			return err
		}
		ordinal := int32(i)
		if args.OutputID != nil {
			ordinal = part.Ordinal
		}
		if row.Ordinal != ordinal || row.BlockKind != part.Kind || valueOrZero(row.TextContent) != part.Text ||
			valueOrZero(row.ArtifactID) != part.ArtifactID ||
			valueOrZero(row.ToolCallID) != part.ToolID ||
			row.ExcludeFromModelContext != part.Exclude ||
			!equalJSON(row.Metadata, metadata) ||
			((len(part.Data) > 0 ||
				row.StructuredData != nil) &&
				!equalJSON(valueOrZero(row.StructuredData),
					part.Data)) {
			return conflict
		}
	}
	return nil
}

func (h *Handle) advanceTurn(ctx context.Context, event ExecutionEvent, semantic bool) error {
	n, err := executiondb.New().AdvanceExecutionTurn(ctx, h.unit.DB(), executiondb.AdvanceExecutionTurnParams{
		AgentID: h.route.AgentID, TurnID: event.TurnID, EventID: event.ID, Semantic: semantic})
	if err == nil && n != 1 {
		return ErrInvalidState
	}
	return err
}

func (m *executionMutation) openTurn(id uuid.UUID) {
	m.head.CurrentTurnID = id
	m.head.NormalContextID = uuid.Nil
	m.head.CompactionContextID = uuid.Nil
	m.head.PendingToolOutputID = uuid.Nil
	m.head.PendingOutputLimitID = uuid.Nil
	m.head.PendingConfigInputID = uuid.Nil
	m.head.PendingCheckpointID = uuid.Nil
	m.changed()
}
