package agentexecution

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

type PreparedContent struct {
	data  json.RawMessage
	count int64
}

type contentValue struct {
	Ordinal    int32           `json:"ordinal"`
	Kind       string          `json:"kind"`
	Text       *string         `json:"text"`
	Data       *string         `json:"data"`
	ArtifactID *uuid.UUID      `json:"artifact_id"`
	ToolID     *uuid.UUID      `json:"tool_id"`
	Exclude    bool            `json:"exclude"`
	Metadata   json.RawMessage `json:"metadata"`
}

func PrepareContent(kind string, parts []Content) (PreparedContent, error) {
	values := make([]contentValue, len(parts))
	for n, part := range parts {
		if err := dbsafe.Text(part.Text); err != nil {
			return PreparedContent{}, err
		}
		metadata, err := objectJSON(part.Metadata)
		if err != nil {
			return PreparedContent{}, err
		}
		if err := dbsafe.JSONStrings(metadata); err != nil {
			return PreparedContent{}, err
		}
		value := contentValue{Ordinal: int32(n), Kind: part.Kind, ArtifactID: storeutil.IDFromNil(part.ArtifactID),
			ToolID: storeutil.IDFromNil(part.ToolID), Exclude: part.Exclude, Metadata: metadata}
		if kind == "model_output" {
			value.Ordinal = part.Ordinal
		}
		if part.Kind == "text" || part.Kind == "reasoning" || part.Kind == "error" {
			value.Text = &part.Text
		}
		if len(part.Data) > 0 {
			if !json.Valid(part.Data) {
				return PreparedContent{}, errors.New("invalid structured content")
			}
			if err := dbsafe.JSONStrings(part.Data); err != nil {
				return PreparedContent{}, err
			}
			data := string(part.Data)
			value.Data = &data
		}
		values[n] = value
	}
	data, err := json.Marshal(values)
	return PreparedContent{data: data, count: int64(len(parts))}, err
}

func (h *Handle) writeContent(
	ctx context.Context, kind string, id uuid.UUID, parts []Content,
) error {
	prepared, err := PrepareContent(kind, parts)
	if err != nil {
		return err
	}
	return h.writePreparedContent(ctx, kind, id, prepared)
}

func (h *Handle) writePreparedContent(
	ctx context.Context, kind string, id uuid.UUID, parts PreparedContent,
) error {
	if parts.count == 0 {
		return nil
	}
	args := executiondb.InsertExecutionContentParams{AgentID: h.route.AgentID, ProjectID: h.route.ProjectID,
		OwnerKind: kind, Parts: parts.data}
	switch kind {
	case "agent_input":
		args.InputID = &id
	case "model_output":
		args.OutputID = &id
	case "tool_call_result":
		args.ResultID = &id
	}
	count, err := executiondb.New().InsertExecutionContent(ctx, h.unit.DB(), args)
	if err == nil && count != parts.count {
		return ErrInvalidState
	}
	return err
}

func (h *Handle) advanceTurn(ctx context.Context, event ExecutionEvent, semantic bool) error {
	batch := factsBatch{DBTX: h.unit.DB()}
	h.queueTurnAdvance(ctx, &batch, event, semantic)
	return batch.read(ctx)
}

func (h *Handle) queueTurnAdvance(ctx context.Context, batch *factsBatch, event ExecutionEvent, semantic bool) {
	rows := executiondb.New().AdvanceExecutionTurn(ctx, batch, []executiondb.AdvanceExecutionTurnParams{{
		AgentID: h.route.AgentID, TurnID: event.TurnID, EventID: event.ID, Semantic: semantic,
	}})
	batch.readers = append(batch.readers, func() {
		rows.QueryRow(func(_ int, id uuid.UUID, err error) {
			if errors.Is(err, pgx.ErrNoRows) || err == nil && id != event.TurnID {
				err = ErrInvalidState
			}
			batch.err = errors.Join(batch.err, err)
		})
		batch.err = errors.Join(batch.err, rows.Close())
	})
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
