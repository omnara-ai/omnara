package agentexecution

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
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
		TurnID:    storeutil.IDFromNil(turn),
		Kind:      kind,
		InputID: storeutil.IDFromNil(
			input,
		), OutputID: storeutil.IDFromNil(output), CheckpointID: storeutil.IDFromNil(checkpoint), Opening: opening}
	if input != uuid.Nil {
		args.IdempotencyKey = storeutil.TextFromEmpty("agent_input:" + input.String())
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
			ArtifactID: storeutil.IDFromNil(part.ArtifactID),
			ToolID:     storeutil.IDFromNil(part.ToolID),
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
