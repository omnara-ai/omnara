package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type AgentTurnRecord struct {
	ID                    ID    `json:"id"`
	ProjectID             ID    `json:"project_id"`
	AgentID               ID    `json:"agent_id"`
	TurnSequence          int64 `json:"turn_sequence"`
	LatestEventID         ID    `json:"latest_event_id,omitempty"`
	LatestSemanticEventID ID    `json:"latest_semantic_event_id,omitempty"`
}

type AgentEventReadRecord struct {
	ID             ID     `json:"id"`
	OrgID          ID     `json:"org_id"`
	ProjectID      ID     `json:"project_id"`
	AgentID        ID     `json:"agent_id"`
	TurnID         ID     `json:"turn_id"`
	TurnSequence   int64  `json:"turn_sequence,omitempty"`
	IsOpeningEvent bool   `json:"is_opening_event"`
	Sequence       int64  `json:"sequence"`
	EventKind      string `json:"event_kind"`
	InputKind      string `json:"input_kind,omitempty"`
	ActorID        ID     `json:"actor_id,omitzero"`
	AgentInputID   ID     `json:"agent_input_id,omitzero"`
	// InputIdempotencyKey echoes the caller-chosen idempotency key of the
	// content input behind this event, so producers can correlate their own
	// submissions. Empty for events from any other input path.
	InputIdempotencyKey            string                   `json:"input_idempotency_key,omitempty"`
	ControlType                    string                   `json:"control_type,omitempty"`
	TargetInteractionID            ID                       `json:"target_interaction_id,omitzero"`
	AgentConfigID                  ID                       `json:"agent_config_id,omitzero"`
	ToolCallID                     ID                       `json:"tool_call_id,omitempty"`
	ToolOutcome                    ToolResultOutcome        `json:"tool_outcome,omitempty"`
	ModelCallContextID             ID                       `json:"model_call_context_id,omitempty"`
	ModelStopReason                modelenvelope.StopReason `json:"model_stop_reason,omitempty"`
	ContextCheckpointID            ID                       `json:"context_checkpoint_id,omitempty"`
	SummarizedThroughEventSequence int64                    `json:"summarized_through_event_sequence,omitempty"`
	CheckpointSummary              string                   `json:"checkpoint_summary,omitempty"`
	ContentBlocks                  json.RawMessage          `json:"content_blocks"`
	CreatedAt                      time.Time                `json:"created_at"`
}

type AgentEventFrontier struct {
	AgentID       ID
	EventSequence int64
}

type AgentTurnReadRecord struct {
	AgentTurnRecord
	EventCount          int64
	StartedAt           time.Time
	UpdatedAt           time.Time
	OpeningEvents       []AgentEventReadRecord
	LatestEvent         AgentEventReadRecord
	LatestSemanticEvent AgentEventReadRecord
}

const (
	// DefaultAgentEventsReadPageLimit is the public default page size for agent event timelines.
	DefaultAgentEventsReadPageLimit int32 = 100
	// MaxAgentEventsReadPageLimit is the public maximum page size for agent event timelines.
	MaxAgentEventsReadPageLimit int32 = 500
	// DefaultAgentTurnsReadPageLimit is the public default page size for agent turn timelines.
	DefaultAgentTurnsReadPageLimit int32 = 25
	// MaxAgentTurnsReadPageLimit is the public maximum page size for agent turn timelines.
	MaxAgentTurnsReadPageLimit int32 = 100

	defaultAgentEventsReadLimit = DefaultAgentEventsReadPageLimit
	maxAgentEventsReadLimit     = MaxAgentEventsReadPageLimit + 1
	defaultAgentTurnsReadLimit  = DefaultAgentTurnsReadPageLimit
	maxAgentTurnsReadLimit      = MaxAgentTurnsReadPageLimit + 1
)

func updateAgentTurnLatestEventTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID, turnID, latestEventID, latestSemanticEventID ID,
) error {
	if isNilID(projectID) || isNilID(agentID) || isNilID(turnID) || isNilID(latestEventID) {
		return errors.New("project id, agent id, turn id, and latest event id are required")
	}
	return updateAgentTurnLatestEventQuery(
		ctx,
		dbsqlc.New(tx),
		projectID,
		agentID,
		turnID,
		latestEventID,
		latestSemanticEventID,
	)
}

func updateAgentTurnLatestEventQuery(
	ctx context.Context,
	queries *dbsqlc.Queries,
	projectID, agentID, turnID, latestEventID, latestSemanticEventID ID,
) error {
	changed, err := queries.UpdateAgentTurnLatestEvent(
		ctx,
		dbsqlc.UpdateAgentTurnLatestEventParams{
			ProjectID:             projectID,
			AgentID:               agentID,
			ID:                    turnID,
			LatestEventID:         latestEventID,
			LatestSemanticEventID: sqlcIDFromNil(latestSemanticEventID),
		},
	)
	if err != nil {
		return fmt.Errorf("update agent turn latest event: %w", err)
	}
	if changed != 1 {
		return storeerr.ErrStateTransitionConflict
	}
	return nil
}

func createSingleEventAgentTurnTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID, turnID ID,
	event events.Event,
) (AgentTurnRecord, error) {
	sequence, err := qtx.NextTurnSequence(ctx, dbsqlc.NextTurnSequenceParams{ProjectID: projectID, AgentID: agentID})
	if err != nil {
		return AgentTurnRecord{}, fmt.Errorf("next turn sequence: %w", err)
	}
	row, err := qtx.InsertAgentTurn(
		ctx,
		dbsqlc.InsertAgentTurnParams{
			ID:                    turnID,
			ProjectID:             projectID,
			AgentID:               agentID,
			TurnSequence:          sequence,
			LatestEventID:         event.ID,
			LatestSemanticEventID: event.ID,
		},
	)
	if err != nil {
		return AgentTurnRecord{}, fmt.Errorf("insert agent turn: %w", err)
	}
	return agentTurnRecordFromInsertSQLC(row), nil
}

func appendEventToCurrentOrNewAgentTurnTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	eventInput AppendTypedAgentEventInput,
	semantic bool,
) (TypedAgentEventRecord, AgentTurnRecord, bool, error) {
	projectID := eventInput.ProjectID
	agentID := eventInput.AgentID
	turn, err := qtx.CurrentContinuableAgentTurn(
		ctx,
		dbsqlc.CurrentContinuableAgentTurnParams{ProjectID: projectID, AgentID: agentID},
	)
	if err == nil {
		eventInput.TurnID = turn.ID
		eventInput.IsOpeningEvent = false
		eventRecord, err := appendTypedAgentEventTx(ctx, txNotifications, tx, eventInput)
		if err != nil {
			return TypedAgentEventRecord{}, AgentTurnRecord{}, false, err
		}
		semanticEventID := NilID
		if semantic {
			semanticEventID = eventRecord.Event.ID
		}
		if err := updateAgentTurnLatestEventQuery(
			ctx,
			qtx,
			projectID,
			agentID,
			turn.ID,
			eventRecord.Event.ID,
			semanticEventID,
		); err != nil {
			return TypedAgentEventRecord{}, AgentTurnRecord{}, false, err
		}
		return eventRecord, agentTurnRecordFromCurrentContinuableSQLC(turn), false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return TypedAgentEventRecord{}, AgentTurnRecord{}, false, fmt.Errorf("load current continuable turn: %w", err)
	}
	turnID, err := uuid.NewV7()
	if err != nil {
		return TypedAgentEventRecord{}, AgentTurnRecord{}, false, fmt.Errorf("generate turn id: %w", err)
	}
	eventInput.TurnID = turnID
	eventInput.IsOpeningEvent = true
	eventRecord, err := appendTypedAgentEventTx(ctx, txNotifications, tx, eventInput)
	if err != nil {
		return TypedAgentEventRecord{}, AgentTurnRecord{}, false, err
	}
	turnRecord, err := createSingleEventAgentTurnTx(
		ctx,
		qtx,
		projectID,
		agentID,
		turnID,
		eventRecord.Event,
	)
	if err != nil {
		return TypedAgentEventRecord{}, AgentTurnRecord{}, false, err
	}
	return eventRecord, turnRecord, true, nil
}

func (s *Store) ListAgentEventsForRead(
	ctx context.Context,
	projectID, agentID ID,
	afterSequence int64,
	limit int32,
) ([]AgentEventReadRecord, error) {
	if isNilID(projectID) || isNilID(agentID) {
		return nil, errors.New("project and agent are required")
	}
	if limit <= 0 {
		limit = defaultAgentEventsReadLimit
	}
	if limit > maxAgentEventsReadLimit {
		limit = maxAgentEventsReadLimit
	}
	if err := s.requireAgentInProject(ctx, projectID, agentID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListAgentEventsForRead(
		ctx,
		dbsqlc.ListAgentEventsForReadParams{
			ProjectID:     projectID,
			AgentID:       agentID,
			AfterSequence: afterSequence,
			PageLimit:     limit,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list agent events for read: %w", err)
	}
	out := make([]AgentEventReadRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, agentEventReadRecordFromSQLC(row))
	}
	return out, nil
}

func (s *Store) ListAgentEventFrontiers(
	ctx context.Context,
	agentIDs []ID,
) ([]AgentEventFrontier, error) {
	if len(agentIDs) == 0 {
		return nil, nil
	}
	rows, err := s.q.ListAgentEventFrontiers(
		ctx,
		dbsqlc.ListAgentEventFrontiersParams{AgentIds: agentIDs},
	)
	if err != nil {
		return nil, fmt.Errorf("list agent event frontiers: %w", err)
	}
	frontiers := make([]AgentEventFrontier, 0, len(rows))
	for _, row := range rows {
		frontiers = append(frontiers, AgentEventFrontier{
			AgentID:       row.AgentID,
			EventSequence: row.EventSequence,
		})
	}
	return frontiers, nil
}

// ListAgentEventsBeforeForRead returns one older event page for an agent,
// chronological within the page. A beforeSequence of 0 means the latest
// events; page beyond limit rows to detect older history.
func (s *Store) ListAgentEventsBeforeForRead(
	ctx context.Context,
	projectID, agentID ID,
	beforeSequence int64,
	limit int32,
) ([]AgentEventReadRecord, error) {
	if isNilID(projectID) || isNilID(agentID) {
		return nil, errors.New("project and agent are required")
	}
	if limit <= 0 {
		limit = defaultAgentEventsReadLimit
	}
	if limit > maxAgentEventsReadLimit {
		limit = maxAgentEventsReadLimit
	}
	if err := s.requireAgentInProject(ctx, projectID, agentID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListAgentEventsBeforeForRead(
		ctx,
		dbsqlc.ListAgentEventsBeforeForReadParams{
			ProjectID:      projectID,
			AgentID:        agentID,
			BeforeSequence: beforeSequence,
			PageLimit:      limit,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list agent events before for read: %w", err)
	}
	out := make([]AgentEventReadRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, agentEventReadRecordFromSQLC(row))
	}
	slices.Reverse(out)
	return out, nil
}

func (s *Store) ListTurnEventsForRead(
	ctx context.Context,
	projectID, agentID, turnID ID,
	beforeSequence int64,
	limit int32,
) ([]AgentEventReadRecord, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(turnID) {
		return nil, errors.New("project, agent, and turn are required")
	}
	if limit <= 0 {
		limit = defaultAgentEventsReadLimit
	}
	if limit > maxAgentEventsReadLimit {
		limit = maxAgentEventsReadLimit
	}
	if err := s.requireAgentTurnInProject(ctx, projectID, agentID, turnID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListTurnEventsForRead(
		ctx,
		dbsqlc.ListTurnEventsForReadParams{
			ProjectID:      projectID,
			AgentID:        agentID,
			TurnID:         turnID,
			BeforeSequence: beforeSequence,
			PageLimit:      limit,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list turn events for read: %w", err)
	}
	out := make([]AgentEventReadRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, agentEventReadRecordFromSQLC(row))
	}
	slices.Reverse(out)
	return out, nil
}

func (s *Store) ListAgentTurnsForRead(
	ctx context.Context,
	projectID, agentID ID,
	beforeTurnSequence int64,
	limit int32,
) ([]AgentTurnReadRecord, error) {
	if isNilID(projectID) || isNilID(agentID) {
		return nil, errors.New("project and agent are required")
	}
	if limit <= 0 {
		limit = defaultAgentTurnsReadLimit
	}
	if limit > maxAgentTurnsReadLimit {
		limit = maxAgentTurnsReadLimit
	}
	if err := s.requireAgentInProject(ctx, projectID, agentID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListAgentTurnsForRead(
		ctx,
		dbsqlc.ListAgentTurnsForReadParams{
			ProjectID:          projectID,
			AgentID:            agentID,
			BeforeTurnSequence: beforeTurnSequence,
			PageLimit:          limit,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list agent turns for read: %w", err)
	}
	out := make([]AgentTurnReadRecord, 0, len(rows))
	turnIDs := make([]ID, 0, len(rows))
	for _, row := range rows {
		turn := AgentTurnReadRecord{
			AgentTurnRecord: AgentTurnRecord{
				ID:                    row.ID,
				ProjectID:             row.ProjectID,
				AgentID:               row.AgentID,
				TurnSequence:          row.TurnSequence,
				LatestEventID:         row.LatestEventID,
				LatestSemanticEventID: row.LatestSemanticEventID,
			},
			EventCount: row.EventCount,
		}
		out = append(out, turn)
		turnIDs = append(turnIDs, row.ID)
	}
	if len(turnIDs) == 0 {
		return out, nil
	}
	boundaryRows, err := s.q.ListTurnBoundaryEventsForRead(
		ctx,
		dbsqlc.ListTurnBoundaryEventsForReadParams{
			ProjectID: projectID,
			AgentID:   agentID,
			TurnIds:   turnIDs,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list turn boundary events for read: %w", err)
	}
	turnIndex := make(map[ID]int, len(out))
	for i := range out {
		turnIndex[out[i].ID] = i
	}
	for _, row := range boundaryRows {
		event := agentEventReadRecordFromSQLC(row)
		i, ok := turnIndex[event.TurnID]
		if !ok {
			continue
		}
		if event.IsOpeningEvent {
			if out[i].StartedAt.IsZero() {
				out[i].StartedAt = event.CreatedAt
			}
			out[i].OpeningEvents = append(out[i].OpeningEvents, event)
		}
		if event.ID == out[i].LatestEventID {
			out[i].LatestEvent = event
			out[i].UpdatedAt = event.CreatedAt
		}
		if event.ID == out[i].LatestSemanticEventID {
			out[i].LatestSemanticEvent = event
		}
	}
	return out, nil
}

func (s *Store) requireAgentInProject(ctx context.Context, projectID, agentID ID) error {
	owned, err := s.q.AgentExistsInProject(
		ctx,
		dbsqlc.AgentExistsInProjectParams{ProjectID: projectID, ID: agentID},
	)
	if err != nil {
		return fmt.Errorf("check agent project ownership: %w", err)
	}
	if !owned {
		return storeerr.ErrNotFound
	}
	return nil
}

func (s *Store) requireAgentTurnInProject(ctx context.Context, projectID, agentID, turnID ID) error {
	owned, err := s.q.AgentTurnExistsInProject(
		ctx,
		dbsqlc.AgentTurnExistsInProjectParams{ProjectID: projectID, AgentID: agentID, ID: turnID},
	)
	if err != nil {
		return fmt.Errorf("check agent turn project ownership: %w", err)
	}
	if !owned {
		return storeerr.ErrNotFound
	}
	return nil
}

func agentEventReadRecordFromSQLC(row dbsqlc.AgentEventReadProjection) AgentEventReadRecord {
	record := AgentEventReadRecord{
		ID:                  row.ID,
		OrgID:               row.OrgID,
		ProjectID:           row.ProjectID,
		AgentID:             row.AgentID,
		TurnID:              row.TurnID,
		TurnSequence:        row.TurnSequence,
		IsOpeningEvent:      row.IsOpeningEvent,
		Sequence:            row.Sequence,
		EventKind:           row.EventKind,
		InputKind:           stringFromSQLCText(row.InputKind),
		ControlType:         stringFromSQLCText(row.ControlType),
		ToolCallID:          idFromSQLCPtr(row.ToolCallID),
		ToolOutcome:         ToolResultOutcome(stringFromSQLCText(row.ToolOutcome)),
		ModelCallContextID:  idFromSQLCPtr(row.ModelCallContextID),
		ModelStopReason:     modelenvelope.StopReason(stringFromSQLCText(row.ModelStopReason)),
		ContextCheckpointID: idFromSQLCPtr(row.ContextCheckpointID),
		CheckpointSummary:   stringFromSQLCText(row.CheckpointSummary),
		ContentBlocks:       normalizedJSONArray(row.ContentBlocks),
		CreatedAt:           row.CreatedAt,
	}
	record.ActorID = idFromSQLCPtr(row.ActorID)
	record.AgentInputID = idFromSQLCPtr(row.AgentInputID)
	record.TargetInteractionID = idFromSQLCPtr(row.TargetInteractionID)
	record.AgentConfigID = idFromSQLCPtr(row.AgentConfigID)
	if stringFromSQLCText(row.IdempotencyScope) == "content_input" {
		record.InputIdempotencyKey = stringFromSQLCText(row.InputIdempotencyKey)
	}
	if row.SummarizedThroughEventSequence != nil {
		record.SummarizedThroughEventSequence = *row.SummarizedThroughEventSequence
	}
	return record
}
