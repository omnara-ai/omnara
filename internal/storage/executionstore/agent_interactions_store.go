package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

const interactionResponseIdempotencyScope = "agent_interaction_response"

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
	ProjectID     ID
	AgentID       ID
	ToolCallID    ID
	RuntimeLockID ID
	Request       toolpermission.Request
}

type CreateQuestionInteractionInput struct {
	Form interactionform.Form
}

type ResolveAgentInteractionInput struct {
	ProjectID           ID
	AgentID             ID
	ID                  ID
	Resolution          interactionform.Resolution
	Actor               *ActorParams
	IntegrationTargetID ID
}

type AgentInteractionRecord struct {
	ID                 ID
	ProjectID          ID
	AgentID            ID
	TurnID             ID
	ModelCallContextID ID
	ToolCallID         ID
	ProviderCallID     string
	InteractionKind    AgentInteractionKind
	State              AgentInteractionState
	Request            json.RawMessage
	Resolution         json.RawMessage
	ResolvedByInputID  ID
	CreatedAt          time.Time
	ResolvedAt         time.Time
}

func (record AgentInteractionRecord) Form() (interactionform.Form, error) {
	return interactionFormForInteraction(record.InteractionKind, record.Request)
}

func (s *Store) CreatePermissionInteraction(
	ctx context.Context,
	input CreatePermissionInteractionInput,
) (AgentInteractionRecord, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.ToolCallID) ||
		isNilID(input.RuntimeLockID) {
		return AgentInteractionRecord{}, errors.New(
			"agent, tool call, and runtime lock are required",
		)
	}
	if err := input.Request.Validate(); err != nil {
		return AgentInteractionRecord{}, fmt.Errorf("permission request: %w", err)
	}
	request, err := marshalJSON(input.Request)
	if err != nil {
		return AgentInteractionRecord{}, fmt.Errorf("marshal permission request: %w", err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AgentInteractionRecord{}, fmt.Errorf("begin create agent interaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if err := ensureRuntimeLockActiveTx(
		ctx,
		tx,
		input.ProjectID,
		input.AgentID,
		input.RuntimeLockID,
	); err != nil {
		return AgentInteractionRecord{}, err
	}
	interactionID, err := qtx.InsertAgentInteraction(ctx, dbsqlc.InsertAgentInteractionParams{
		ProjectID:       input.ProjectID,
		AgentID:         input.AgentID,
		ToolCallID:      input.ToolCallID,
		InteractionKind: string(AgentInteractionKindPermission),
		Request:         request,
	})
	if err != nil {
		if storeutil.IsUniqueViolation(err) {
			return AgentInteractionRecord{}, storeerr.ErrIdempotencyConflict
		}
		return AgentInteractionRecord{}, fmt.Errorf("create agent interaction: %w", err)
	}
	row, err := qtx.GetAgentInteraction(ctx, dbsqlc.GetAgentInteractionParams{
		ProjectID: input.ProjectID,
		AgentID:   input.AgentID,
		ID:        interactionID,
	})
	if err != nil {
		return AgentInteractionRecord{}, fmt.Errorf("load created agent interaction: %w", err)
	}
	record := agentInteractionRecordFromSQLC(row)
	if err := markToolCallAwaitingPermissionTx(
		ctx,
		tx,
		qtx,
		input.ProjectID,
		input.AgentID,
		input.ToolCallID,
		input.RuntimeLockID,
	); err != nil {
		return AgentInteractionRecord{}, fmt.Errorf("link tool call to agent interaction: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return AgentInteractionRecord{}, fmt.Errorf("commit create agent interaction: %w", err)
	}
	return record, nil
}

func (t *toolCallTransaction) createQuestionInteraction(
	ctx context.Context,
	input CreateQuestionInteractionInput,
) (AgentInteractionRecord, error) {
	if err := input.Form.Validate(); err != nil {
		return AgentInteractionRecord{}, fmt.Errorf("interaction form: %w", err)
	}
	request, err := marshalJSON(input.Form)
	if err != nil {
		return AgentInteractionRecord{}, fmt.Errorf("marshal interaction form: %w", err)
	}
	existing, found, err := getAgentInteractionByToolCallKind(
		ctx,
		t.q,
		t.input.ProjectID,
		t.input.AgentID,
		t.input.ToolCallID,
		"question",
	)
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	if found {
		if !sameJSON(existing.Request, request) {
			return AgentInteractionRecord{}, storeerr.ErrIdempotencyConflict
		}
		t.hasDurableCompletionOwner = true
		if err := t.lockOrAcceptExisting(ctx); err != nil {
			return AgentInteractionRecord{}, err
		}
		return existing, nil
	}
	if err := t.lockForMutation(ctx); err != nil {
		return AgentInteractionRecord{}, err
	}
	interactionID, err := t.q.InsertAgentInteraction(ctx, dbsqlc.InsertAgentInteractionParams{
		ProjectID:       t.input.ProjectID,
		AgentID:         t.input.AgentID,
		ToolCallID:      t.input.ToolCallID,
		InteractionKind: string(AgentInteractionKindQuestion),
		Request:         request,
	})
	if err != nil {
		if storeutil.IsUniqueViolation(err) {
			return AgentInteractionRecord{}, storeerr.ErrIdempotencyConflict
		}
		return AgentInteractionRecord{}, fmt.Errorf("create question interaction: %w", err)
	}
	row, err := t.q.GetAgentInteraction(ctx, dbsqlc.GetAgentInteractionParams{
		ProjectID: t.input.ProjectID,
		AgentID:   t.input.AgentID,
		ID:        interactionID,
	})
	if err != nil {
		return AgentInteractionRecord{}, fmt.Errorf("load created question interaction: %w", err)
	}
	t.hasDurableCompletionOwner = true
	return agentInteractionRecordFromSQLC(row), nil
}

func markToolCallAwaitingPermissionTx(
	ctx context.Context,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	projectID, agentID, toolCallID, runtimeLockID ID,
) error {
	_, err := qtx.MarkToolCallAwaitingPermission(
		ctx,
		dbsqlc.MarkToolCallAwaitingPermissionParams{
			ProjectID:     projectID,
			AgentID:       agentID,
			ID:            toolCallID,
			RuntimeLockID: runtimeLockID,
		},
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil {
		return nil
	}
	if runtimeErr := ensureRuntimeLockActiveTx(
		ctx,
		tx,
		projectID,
		agentID,
		runtimeLockID,
	); runtimeErr != nil {
		return runtimeErr
	}
	return storeerr.ErrIdempotencyConflict
}

func (s *Store) ResolveAgentInteraction(
	ctx context.Context,
	input ResolveAgentInteractionInput,
) (AgentInteractionRecord, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.ID) {
		return AgentInteractionRecord{}, errors.New("project, agent, and interaction are required")
	}
	ctx, artifactScope := withArtifactRollbackScope(ctx)
	defer artifactScope.rollback(ctx)
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AgentInteractionRecord{}, fmt.Errorf("begin resolve agent interaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if _, err := qtx.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: input.ProjectID, ID: input.AgentID},
	); err != nil {
		return AgentInteractionRecord{}, fmt.Errorf("lock agent for interaction resolution: %w", err)
	}
	existing, err := qtx.GetAgentInteraction(
		ctx,
		dbsqlc.GetAgentInteractionParams{
			ProjectID: input.ProjectID,
			AgentID:   input.AgentID,
			ID:        input.ID,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AgentInteractionRecord{}, storeerr.ErrIdempotencyConflict
		}
		return AgentInteractionRecord{}, fmt.Errorf("get agent interaction: %w", err)
	}
	resolution, err := normalizeAgentInteractionResolution(
		AgentInteractionKind(existing.InteractionKind),
		existing.Request,
		input.Resolution,
	)
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	var record AgentInteractionRecord
	if AgentInteractionState(existing.State) == AgentInteractionStateOpen {
		stopped, err := qtx.AgentInteractionHasLaterStop(
			ctx,
			dbsqlc.AgentInteractionHasLaterStopParams{ProjectID: input.ProjectID, AgentID: input.AgentID, ID: input.ID},
		)
		if err != nil {
			return AgentInteractionRecord{}, fmt.Errorf("check interaction stop lineage: %w", err)
		}
		if stopped {
			return AgentInteractionRecord{}, storeerr.ErrStateTransitionConflict
		}
		resolvedByActorID, err := resolveActorTx(
			ctx,
			qtx,
			input.ProjectID,
			input.AgentID,
			input.Actor,
			input.IntegrationTargetID,
		)
		if err != nil {
			return AgentInteractionRecord{}, err
		}
		responseInput, err := insertInteractionResponseInputTx(ctx, qtx, input, resolvedByActorID)
		if err != nil {
			return AgentInteractionRecord{}, err
		}
		eventRecord, err := appendTypedAgentEventTx(ctx, txNotifications, tx, AppendTypedAgentEventInput{
			ProjectID:      input.ProjectID,
			AgentID:        input.AgentID,
			TurnID:         existing.TurnID,
			Kind:           events.KindAgentInput,
			IdempotencyKey: "agent_input:" + responseInput.ID.String(),
			AgentInputID:   responseInput.ID,
		})
		if err != nil {
			return AgentInteractionRecord{}, err
		}
		if err := updateAgentTurnLatestEventTx(
			ctx,
			tx,
			input.ProjectID,
			input.AgentID,
			existing.TurnID,
			eventRecord.Event.ID,
			eventRecord.Event.ID,
		); err != nil {
			return AgentInteractionRecord{}, err
		}
		_, err = qtx.ResolveAgentInteraction(
			ctx,
			dbsqlc.ResolveAgentInteractionParams{
				ProjectID:         input.ProjectID,
				AgentID:           input.AgentID,
				ID:                input.ID,
				Resolution:        resolution,
				ResolvedByInputID: sqlcIDFromNil(responseInput.ID),
			},
		)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return AgentInteractionRecord{}, storeerr.ErrIdempotencyConflict
			}
			return AgentInteractionRecord{}, fmt.Errorf("resolve agent interaction: %w", err)
		}
		row, err := qtx.GetAgentInteraction(ctx, dbsqlc.GetAgentInteractionParams{
			ProjectID: input.ProjectID,
			AgentID:   input.AgentID,
			ID:        input.ID,
		})
		if err != nil {
			return AgentInteractionRecord{}, fmt.Errorf("load resolved agent interaction: %w", err)
		}
		if _, err := qtx.ResolveInteractionResponseAgentInput(
			ctx,
			dbsqlc.ResolveInteractionResponseAgentInputParams{
				ProjectID:           input.ProjectID,
				AgentID:             input.AgentID,
				ID:                  responseInput.ID,
				TargetInteractionID: sqlcIDFromNil(input.ID),
				EventID:             sqlcIDFromNil(eventRecord.Event.ID),
			},
		); err != nil {
			return AgentInteractionRecord{}, fmt.Errorf("resolve interaction response agent input: %w", err)
		}
		record = agentInteractionRecordFromSQLC(row)
		if err := applyPermissionInteractionResolutionTx(ctx, txNotifications, tx, qtx, record); err != nil {
			return AgentInteractionRecord{}, err
		}
		if err := s.completeQuestionToolCallTx(ctx, txNotifications, tx, qtx, record); err != nil {
			return AgentInteractionRecord{}, err
		}
	} else {
		record = agentInteractionRecordFromSQLC(existing)
		if record.State != AgentInteractionStateResolved || !sameJSON(record.Resolution, resolution) {
			return AgentInteractionRecord{}, storeerr.ErrIdempotencyConflict
		}
		requestActorID, actorFound, err := lookupActorIDTx(ctx, qtx, input.ProjectID, input.Actor)
		if err != nil {
			return AgentInteractionRecord{}, err
		}
		responseInput, responseFound, err := loadAgentInputByIdempotencyMaybeTx(
			ctx,
			tx,
			input.ProjectID,
			input.AgentID,
			interactionResponseIdempotencyScope,
			input.ID.String(),
		)
		if err != nil {
			return AgentInteractionRecord{}, err
		}
		if !actorFound || !responseFound ||
			record.ResolvedByInputID != responseInput.ID ||
			responseInput.ActorID != requestActorID {
			return AgentInteractionRecord{}, storeerr.ErrIdempotencyConflict
		}
		if err := s.completeQuestionToolCallTx(ctx, txNotifications, tx, qtx, record); err != nil {
			return AgentInteractionRecord{}, err
		}
	}
	if err := qtx.MarkAgentWakeup(
		ctx,
		dbsqlc.MarkAgentWakeupParams{
			ProjectID: record.ProjectID,
			AgentID:   record.AgentID,
			Metadata:  []byte(`{"reason":"interaction_resolved"}`),
		},
	); err != nil {
		return AgentInteractionRecord{}, fmt.Errorf("mark interaction resolution wakeup: %w", err)
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "resolve agent interaction"); err != nil {
		return AgentInteractionRecord{}, err
	}
	artifactScope.commit()
	return record, nil
}

func applyPermissionInteractionResolutionTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	record AgentInteractionRecord,
) error {
	if record.InteractionKind != AgentInteractionKindPermission {
		return nil
	}
	resolution, err := permissionInteractionResolution(record)
	if err != nil {
		return err
	}
	if resolution.Decision == toolpermission.DecisionAllow {
		if _, err := qtx.MarkToolCallReadyFromInteraction(
			ctx,
			dbsqlc.MarkToolCallReadyFromInteractionParams{
				ProjectID:     record.ProjectID,
				AgentID:       record.AgentID,
				InteractionID: record.ID,
			},
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				existing, loadErr := qtx.GetToolCall(
					ctx,
					dbsqlc.GetToolCallParams{
						ProjectID: record.ProjectID,
						AgentID:   record.AgentID,
						ID:        record.ToolCallID,
					},
				)
				if loadErr != nil {
					return loadErr
				}
				if existing.State == string(ToolCallStateReady) ||
					existing.State == string(ToolCallStateRunning) ||
					existing.State == string(ToolCallStateWaiting) ||
					(existing.State == string(ToolCallStateCompleted) &&
						existing.Outcome != string(ToolResultOutcomeDenied) &&
						existing.Outcome != string(ToolResultOutcomeCanceled)) {
					return nil
				}
				return storeerr.ErrIdempotencyConflict
			}
			return fmt.Errorf("mark permitted tool call ready: %w", err)
		}
		return nil
	}
	return completePermissionDeniedToolCallTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		record,
		resolution.Reason,
	)
}

func completePermissionDeniedToolCallTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	record AgentInteractionRecord,
	reason string,
) error {
	result, err := marshalJSON(map[string]any{"reason": reason})
	if err != nil {
		return fmt.Errorf("marshal permission denied tool result: %w", err)
	}
	contentParts, err := ToolResultContentParts(result)
	if err != nil {
		return err
	}
	outcome := permissionToolResultOutcome(record)
	row, err := qtx.CompleteToolCallFromPermissionInteraction(
		ctx,
		dbsqlc.CompleteToolCallFromPermissionInteractionParams{
			ProjectID:     record.ProjectID,
			AgentID:       record.AgentID,
			InteractionID: record.ID,
			Outcome:       string(outcome),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, loadErr := qtx.GetToolCall(
			ctx,
			dbsqlc.GetToolCallParams{
				ProjectID: record.ProjectID,
				AgentID:   record.AgentID,
				ID:        record.ToolCallID,
			},
		)
		if loadErr != nil {
			return loadErr
		}
		if existing.State != string(ToolCallStateCompleted) ||
			existing.Outcome != string(outcome) {
			return storeerr.ErrIdempotencyConflict
		}
		ok, checkErr := completedToolCallMatchesTx(
			ctx,
			qtx,
			record.ProjectID,
			record.AgentID,
			record.ToolCallID,
			outcome,
			contentParts,
		)
		if checkErr != nil {
			return checkErr
		}
		if ok {
			return nil
		}
		return storeerr.ErrIdempotencyConflict
	}
	if err != nil {
		return fmt.Errorf("complete tool call from permission interaction: %w", err)
	}
	resultRecord := toolCallRecordFromPermissionInteractionCompleteSQLC(row)
	resultRecord.ResultContentParts = contentParts
	if _, err := appendToolResultEventTx(ctx, txNotifications, tx, resultRecord); err != nil {
		return err
	}
	return nil
}

func completeQuestionToolCallTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	interaction AgentInteractionRecord,
) error {
	var store *Store
	return store.completeQuestionToolCallTx(ctx, txNotifications, tx, qtx, interaction)
}

func (s *Store) completeQuestionToolCallTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	interaction AgentInteractionRecord,
) error {
	if interaction.InteractionKind != AgentInteractionKindQuestion {
		return nil
	}
	outcome := ToolResultOutcomeSucceeded
	var result json.RawMessage
	var err error
	switch interaction.State {
	case AgentInteractionStateResolved:
		form, parseErr := interaction.Form()
		if parseErr != nil {
			return parseErr
		}
		response, parseErr := interactionform.ParseResolution(form, interaction.Resolution)
		if parseErr != nil {
			return parseErr
		}
		result, err = marshalJSON(newQuestionToolResult(form, response))
	case AgentInteractionStateCanceled:
		outcome = ToolResultOutcomeCanceled
		result, err = marshalJSON(map[string]any{"reason": "question interaction canceled"})
	case AgentInteractionStateOpen:
		return nil
	default:
		return fmt.Errorf(
			"question interaction %s has unsupported state %q",
			interaction.ID,
			interaction.State,
		)
	}
	if err != nil {
		return fmt.Errorf("marshal question tool result: %w", err)
	}
	contentParts, err := ToolResultContentParts(result)
	if err != nil {
		return err
	}
	contentParts, err = s.prepareToolResult(
		ctx,
		tx,
		interaction.ProjectID,
		interaction.AgentID,
		interaction.ToolCallID,
		toolcatalog.ToolNameAskQuestion,
		outcome,
		contentParts,
	)
	if err != nil {
		return fmt.Errorf("prepare question tool result: %w", err)
	}
	row, err := qtx.CompleteToolCallFromQuestionInteraction(
		ctx,
		dbsqlc.CompleteToolCallFromQuestionInteractionParams{
			ProjectID:     interaction.ProjectID,
			AgentID:       interaction.AgentID,
			InteractionID: interaction.ID,
			Outcome:       string(outcome),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		ok, checkErr := completedToolCallMatchesTx(
			ctx,
			qtx,
			interaction.ProjectID,
			interaction.AgentID,
			interaction.ToolCallID,
			outcome,
			contentParts,
		)
		if checkErr != nil {
			return checkErr
		}
		if ok {
			return nil
		}
		return storeerr.ErrIdempotencyConflict
	}
	if err != nil {
		return fmt.Errorf("complete tool call from question interaction: %w", err)
	}
	resultRecord := toolCallRecordFromQuestionInteractionCompleteSQLC(row)
	resultRecord.ResultContentParts = contentParts
	if _, err := appendToolResultEventTx(ctx, txNotifications, tx, resultRecord); err != nil {
		return err
	}
	return nil
}

type questionToolResult struct {
	Answers []questionToolResultAnswer `json:"answers"`
}

type questionToolResultAnswer struct {
	QuestionIndex   int                                `json:"question_index"`
	Question        string                             `json:"question"`
	SelectedOptions []questionToolResultSelectedOption `json:"selected_options"`
	Text            string                             `json:"text,omitempty"`
}

type questionToolResultSelectedOption struct {
	OptionIndex int    `json:"option_index"`
	Label       string `json:"label"`
}

func newQuestionToolResult(
	form interactionform.Form,
	resolution interactionform.Resolution,
) questionToolResult {
	result := questionToolResult{
		Answers: make([]questionToolResultAnswer, 0, len(resolution.Answers)),
	}
	for questionIndex, answer := range resolution.Answers {
		question := form.Questions[questionIndex]
		selectedOptions := make(
			[]questionToolResultSelectedOption,
			0,
			len(answer.OptionIndices),
		)
		for _, optionIndex := range answer.OptionIndices {
			selectedOptions = append(selectedOptions, questionToolResultSelectedOption{
				OptionIndex: optionIndex,
				Label:       question.Options[optionIndex].Label,
			})
		}
		result.Answers = append(result.Answers, questionToolResultAnswer{
			QuestionIndex:   questionIndex,
			Question:        question.Prompt,
			SelectedOptions: selectedOptions,
			Text:            answer.Text,
		})
	}
	return result
}

func insertInteractionResponseInputTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input ResolveAgentInteractionInput,
	resolvedByActorID ID,
) (AgentInputRecord, error) {
	scope := interactionResponseIdempotencyScope
	key := input.ID.String()
	row, err := qtx.InsertInteractionResponseAgentInput(
		ctx,
		dbsqlc.InsertInteractionResponseAgentInputParams{
			ProjectID:           input.ProjectID,
			AgentID:             input.AgentID,
			TargetInteractionID: input.ID,
			ActorID:             sqlcIDFromNil(resolvedByActorID),
			IdempotencyScope:    sqlcTextFromEmpty(scope),
			InputIdempotencyKey: sqlcTextFromEmpty(key),
			Metadata:            json.RawMessage(`{}`),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		existingInput, getErr := qtx.GetAgentInputByIdempotency(
			ctx,
			dbsqlc.GetAgentInputByIdempotencyParams{
				ProjectID:           input.ProjectID,
				AgentID:             input.AgentID,
				IdempotencyScope:    scope,
				InputIdempotencyKey: key,
			},
		)
		if getErr != nil {
			return AgentInputRecord{}, fmt.Errorf(
				"load interaction response input by idempotency: %w",
				getErr,
			)
		}
		record := agentInputRecordFromIdempotencySQLC(existingInput)
		if record.InputKind != "interaction_response" || record.TargetInteractionID != input.ID {
			return AgentInputRecord{}, storeerr.ErrIdempotencyConflict
		}
		return record, nil
	}
	if err != nil {
		return AgentInputRecord{}, fmt.Errorf("insert interaction response agent input: %w", err)
	}
	return agentInputRecordFromInteractionResponseInsertSQLC(row), nil
}

func normalizeAgentInteractionResolution(
	kind AgentInteractionKind,
	request json.RawMessage,
	resolution interactionform.Resolution,
) (json.RawMessage, error) {
	value, err := interactionFormForInteraction(kind, request)
	if err != nil {
		return nil, err
	}
	normalized, err := interactionform.NormalizeResolution(value, resolution)
	if err != nil {
		return nil, err
	}
	raw, err := marshalJSON(normalized)
	if err != nil {
		return nil, fmt.Errorf("marshal interaction resolution: %w", err)
	}
	return raw, nil
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

func permissionInteractionResolution(
	record AgentInteractionRecord,
) (toolpermission.Resolution, error) {
	if record.InteractionKind != AgentInteractionKindPermission {
		return toolpermission.Resolution{}, errors.New("permission interaction is required")
	}
	if record.State == AgentInteractionStateCanceled {
		return toolpermission.Resolution{
			Decision: toolpermission.DecisionDeny,
			Reason:   "permission interaction canceled",
		}, nil
	}
	if record.State != AgentInteractionStateResolved {
		return toolpermission.Resolution{}, fmt.Errorf(
			"permission interaction has unsupported state %q",
			record.State,
		)
	}
	request, err := toolpermission.ParseRequest(record.Request)
	if err != nil {
		return toolpermission.Resolution{}, err
	}
	resolution, err := interactionform.ParseResolution(request.Form, record.Resolution)
	if err != nil {
		return toolpermission.Resolution{}, err
	}
	return toolpermission.Resolve(request, resolution)
}

func permissionToolResultOutcome(record AgentInteractionRecord) ToolResultOutcome {
	if record.State == AgentInteractionStateCanceled {
		return ToolResultOutcomeCanceled
	}
	return ToolResultOutcomeDenied
}

type ListAgentInteractionsForAgentInput struct {
	ProjectID ID
	AgentID   ID
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
	if isNilID(input.ProjectID) || isNilID(input.AgentID) {
		return ListAgentInteractionsForAgentResult{}, errors.New("project and agent are required")
	}
	if input.Limit <= 0 {
		return ListAgentInteractionsForAgentResult{}, errors.New("limit must be positive")
	}
	params := dbsqlc.ListAgentInteractionsForAgentParams{
		ProjectID: input.ProjectID,
		AgentID:   input.AgentID,
		State:     string(input.State),
		RowLimit:  int64(input.Limit) + 1,
	}
	if input.After.Set {
		createdAt := input.After.CreatedAt
		id := input.After.ID
		params.CursorCreatedAt = &createdAt
		params.CursorID = &id
	}
	rows, err := s.q.ListAgentInteractionsForAgent(ctx, params)
	if err != nil {
		return ListAgentInteractionsForAgentResult{}, fmt.Errorf("list agent interactions: %w", err)
	}
	result := ListAgentInteractionsForAgentResult{}
	if len(rows) > input.Limit {
		result.HasMore = true
		rows = rows[:input.Limit]
	}
	result.Interactions = make([]AgentInteractionRecord, 0, len(rows))
	for _, row := range rows {
		result.Interactions = append(result.Interactions, agentInteractionRecordFromSQLC(row))
	}
	return result, nil
}

func (s *Store) GetAgentInteraction(
	ctx context.Context,
	projectID, agentID, id ID,
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
	projectID, agentID, toolCallID ID,
	interactionKind AgentInteractionKind,
) (AgentInteractionRecord, bool, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(toolCallID) ||
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
	projectID, agentID, toolCallID ID,
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
