package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

type AgentInteractionState string
type AgentInteractionKind string

const (
	AgentInteractionStateOpen     AgentInteractionState = "open"
	AgentInteractionStateResolved AgentInteractionState = "resolved"
	AgentInteractionStateCanceled AgentInteractionState = "canceled"

	AgentInteractionKindPermission AgentInteractionKind = "permission"
	AgentInteractionKindQuestion   AgentInteractionKind = "question"
)

type CreatePermissionInteractionInput struct {
	ProjectID     uuid.UUID
	AgentID       uuid.UUID
	ToolCallID    uuid.UUID
	RuntimeLockID uuid.UUID
	Request       toolpermission.Request
}

type CreateQuestionInteractionInput struct {
	Form interactionform.Form
}

type ResolveAgentInteractionInput struct {
	ProjectID           uuid.UUID
	AgentID             uuid.UUID
	ID                  uuid.UUID
	Resolution          interactionform.Resolution
	Actor               *ActorParams
	IntegrationTargetID uuid.UUID
}

type AgentInteractionRecord struct {
	ID                  uuid.UUID
	ProjectID           uuid.UUID
	AgentID             uuid.UUID
	TurnID              uuid.UUID
	ModelCallContextID  uuid.UUID
	ToolCallID          uuid.UUID
	ProviderCallID      string
	InteractionKind     AgentInteractionKind
	State               AgentInteractionState
	Request             json.RawMessage
	Resolution          json.RawMessage
	Destination         json.RawMessage
	PresentationReceipt json.RawMessage
	ResolvedByInputID   uuid.UUID
	CreatedAt           time.Time
	ResolvedAt          time.Time
}

func (record AgentInteractionRecord) Form() (interactionform.Form, error) {
	return interactionFormForInteraction(record.InteractionKind, record.Request)
}

func (s *Store) CreatePermissionInteraction(
	ctx context.Context,
	input CreatePermissionInteractionInput,
) (AgentInteractionRecord, error) {
	unit, h, err := beginExecution(ctx, s, input.ProjectID, input.AgentID)
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	defer func() { _ = unit.Rollback(ctx) }()
	destination, err := captureInteractionDestinationTx(ctx, unit.DB(), input.ProjectID, input.AgentID)
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	result, err := h.OpenInteraction(ctx, agentexecution.OpenInteractionInput{ToolRef: agentexecution.ToolRef{
		ID:            input.ToolCallID,
		RuntimeLockID: input.RuntimeLockID},
		Permission:  &input.Request,
		Destination: destination})
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	row, err := dbsqlc.New(unit.DB()).GetAgentInteraction(ctx, dbsqlc.GetAgentInteractionParams{
		ProjectID: input.ProjectID, AgentID: input.AgentID, ID: result.ID})
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	if err = unit.Commit(ctx, "open permission"); err != nil {
		return AgentInteractionRecord{}, err
	}
	return agentInteractionRecordFromSQLC(row), nil
}

func (t *toolCallTransaction) createQuestionInteraction(
	ctx context.Context,
	input CreateQuestionInteractionInput,
) (AgentInteractionRecord, error) {
	plan, err := t.unit.PlanAgentFamily(ctx, t.input.ProjectID, t.input.AgentID, agentexecution.LifecycleAuthority{})
	if err == nil {
		err = t.unit.LockAgents(ctx, plan)
	}
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	destination, err := captureInteractionDestinationTx(ctx, t.tx, t.input.ProjectID, t.input.AgentID)
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	h, err := t.unit.Handle(t.input.ProjectID, t.input.AgentID)
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	result, err := h.OpenInteraction(ctx, agentexecution.OpenInteractionInput{ToolRef: agentexecution.ToolRef{
		ID:            t.input.ToolCallID,
		RuntimeLockID: t.input.RuntimeLockID},
		Question:    &input.Form,
		Destination: destination})
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	row, err := t.q.GetAgentInteraction(
		ctx,
		dbsqlc.GetAgentInteractionParams{
			ProjectID: t.input.ProjectID,
			AgentID:   t.input.AgentID,
			ID:        result.ID,
		},
	)
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	t.hasDurableCompletionOwner = true
	t.disposition = ToolCallDispositionWaiting
	t.applied = result.Created
	if result.State != "open" {
		t.disposition = ToolCallDispositionCompleted
	}
	return agentInteractionRecordFromSQLC(row), nil
}

func (s *Store) ResolveAgentInteraction(
	ctx context.Context,
	input ResolveAgentInteractionInput,
) (AgentInteractionRecord, error) {
	if input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil || input.ID == uuid.Nil {
		return AgentInteractionRecord{}, errors.New("project, agent, and interaction are required")
	}
	unit, err := s.cell.Begin(ctx)
	if err != nil {
		return AgentInteractionRecord{}, fmt.Errorf("begin resolve agent interaction: %w", err)
	}
	defer func() { _ = unit.Rollback(ctx) }()
	record, err := resolveExecutionInteraction(ctx, unit, input)
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	if err := unit.Commit(ctx, "resolve agent interaction"); err != nil {
		return AgentInteractionRecord{}, err
	}
	return record, nil
}

func resolveExecutionInteraction(
	ctx context.Context,
	unit *agentexecution.Unit,
	input ResolveAgentInteractionInput,
) (AgentInteractionRecord, error) {
	if err := unit.LockAgentRefs(ctx,
		[]lifecyclelock.AgentRef{{ProjectID: input.ProjectID,
			AgentID: input.AgentID}},
		agentexecution.ExternalAuthority{}); err != nil {
		return AgentInteractionRecord{}, err
	}
	q := dbsqlc.New(unit.DB())
	actor, err := resolveActorTx(ctx, q, input.ProjectID, input.Actor)
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	h, err := unit.Handle(input.ProjectID, input.AgentID)
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	result, err := h.ResolveInteraction(
		ctx,
		agentexecution.ResolveInteractionInput{ID: input.ID, ActorID: actor, Resolution: input.Resolution},
	)
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	row, err := q.GetAgentInteraction(
		ctx,
		dbsqlc.GetAgentInteractionParams{ProjectID: input.ProjectID, AgentID: input.AgentID, ID: result.ID},
	)
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	return agentInteractionRecordFromSQLC(row), nil
}

func interactionFormForInteraction(
	kind AgentInteractionKind,
	request json.RawMessage,
) (interactionform.Form, error) {
	switch kind {
	case AgentInteractionKindPermission:
		permissionRequest, err := toolpermission.ParseRequest(request)
		if err != nil {
			return interactionform.Form{}, fmt.Errorf(
				"stored permission request is invalid: %w",
				err,
			)
		}
		return permissionRequest.Form, nil
	case AgentInteractionKindQuestion:
		value, err := interactionform.Parse(request)
		if err != nil {
			return interactionform.Form{}, fmt.Errorf(
				"stored question request is invalid: %w",
				err,
			)
		}
		return value, nil
	default:
		return interactionform.Form{}, fmt.Errorf(
			"invalid agent interaction kind: %s",
			kind,
		)
	}
}

type ListAgentInteractionsForAgentInput struct {
	ProjectID uuid.UUID
	AgentID   uuid.UUID
	State     AgentInteractionState
	Limit     int
	After     listing.KeysetCursor
}

type ListAgentInteractionsForAgentResult struct {
	Interactions []AgentInteractionRecord
	HasMore      bool
}

func (s *Store) ListAgentInteractionsForAgent(
	ctx context.Context,
	input ListAgentInteractionsForAgentInput,
) (ListAgentInteractionsForAgentResult, error) {
	if input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil {
		return ListAgentInteractionsForAgentResult{}, errors.New("project and agent are required")
	}
	if input.Limit <= 0 {
		return ListAgentInteractionsForAgentResult{}, errors.New("limit must be positive")
	}
	page, err := s.ListAgentInteractions(ctx, ListAgentInteractionsInput{
		ProjectID: input.ProjectID,
		AgentIDs:  []uuid.UUID{input.AgentID},
		State:     input.State,
		Limit:     input.Limit,
		After:     input.After,
	})
	if err != nil {
		return ListAgentInteractionsForAgentResult{}, err
	}
	result := ListAgentInteractionsForAgentResult{HasMore: page.HasMore}
	result.Interactions = make([]AgentInteractionRecord, 0, len(page.Interactions))
	for _, item := range page.Interactions {
		result.Interactions = append(result.Interactions, item.AgentInteractionRecord)
	}
	return result, nil
}

func (s *Store) GetAgentInteraction(
	ctx context.Context,
	projectID, agentID, id uuid.UUID,
) (AgentInteractionRecord, bool, error) {
	row, err := s.q.GetAgentInteraction(
		ctx,
		dbsqlc.GetAgentInteractionParams{ProjectID: projectID, AgentID: agentID, ID: id},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentInteractionRecord{}, false, nil
	}
	if err != nil {
		return AgentInteractionRecord{}, false, fmt.Errorf("get agent interaction: %w", err)
	}
	return agentInteractionRecordFromSQLC(row), true, nil
}

func (s *Store) GetAgentInteractionByToolCallKind(
	ctx context.Context,
	projectID, agentID, toolCallID uuid.UUID,
	interactionKind AgentInteractionKind,
) (AgentInteractionRecord, bool, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || toolCallID == uuid.Nil ||
		(interactionKind != AgentInteractionKindPermission && interactionKind != AgentInteractionKindQuestion) {
		return AgentInteractionRecord{}, false, errors.New(
			"project, agent, tool call id, and interaction kind are required",
		)
	}
	return getAgentInteractionByToolCallKind(
		ctx,
		s.q,
		projectID,
		agentID,
		toolCallID,
		interactionKind,
	)
}

func (r *ToolCallReader) GetAgentInteractionByToolCallKind(
	ctx context.Context,
	interactionKind AgentInteractionKind,
) (AgentInteractionRecord, bool, error) {
	t := r.transaction
	return getAgentInteractionByToolCallKind(
		ctx,
		t.q,
		t.input.ProjectID,
		t.input.AgentID,
		t.input.ToolCallID,
		interactionKind,
	)
}

func getAgentInteractionByToolCallKind(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID, toolCallID uuid.UUID,
	interactionKind AgentInteractionKind,
) (AgentInteractionRecord, bool, error) {
	row, err := q.GetAgentInteractionByToolCallKind(
		ctx,
		dbsqlc.GetAgentInteractionByToolCallKindParams{
			ProjectID:       projectID,
			AgentID:         agentID,
			ToolCallID:      toolCallID,
			InteractionKind: string(interactionKind),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentInteractionRecord{}, false, nil
	}
	if err != nil {
		return AgentInteractionRecord{}, false, fmt.Errorf(
			"get agent interaction by tool call and kind: %w",
			err,
		)
	}
	return agentInteractionRecordFromSQLC(row), true, nil
}
