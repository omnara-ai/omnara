package agentexecution

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ToolProposal struct {
	ID             uuid.UUID
	ProviderCallID string
	Type           string
}
type AcceptOutputInput struct {
	prepared      *preparedOutput
	FinishClaim   bool
	RuntimeLockID uuid.UUID
	ContextID     uuid.UUID
	RequestID     string
	Response      modelenvelope.ResponseEnvelope
	Tools         []ToolProposal
}

type AcceptedOutput struct {
	Projections map[uuid.UUID]dbsqlc.GetToolCallRow
	ID          uuid.UUID
	Event       ExecutionEvent
	Tools       []ToolProposal
	Created     bool
}

type outputProposal struct {
	ToolProposal
	Name  string
	Input json.RawMessage
	Error string
}

func outputParts(
	response modelenvelope.ResponseEnvelope,
	bindings []ToolProposal,
) ([]Content, []outputProposal, error) {
	byID := make(map[string]ToolProposal, len(bindings))
	for _, binding := range bindings {
		if binding.ProviderCallID == "" ||
			binding.Type != "built_in" && binding.Type != "custom" && binding.Type != "mcp" {
			return nil, nil, errors.New("invalid tool proposal")
		}
		if _, exists := byID[binding.ProviderCallID]; exists {
			return nil, nil, errors.New("duplicate tool proposal")
		}
		if binding.ID == uuid.Nil {
			binding.ID = uuid.New()
		}
		byID[binding.ProviderCallID] = binding
	}
	var content []Content
	var proposals []outputProposal
	for ordinal, part := range response.Normalized.Content {
		switch part.Type {
		case modelenvelope.ResponsePartTypeText,
			modelenvelope.ResponsePartTypeReasoning,
			modelenvelope.ResponsePartTypeError:
			if part.Text != "" {
				content = append(
					content,
					Content{Ordinal: int32(ordinal), Kind: string(part.Type), Text: part.Text},
				)
			}
		case modelenvelope.ResponsePartTypeToolCall:
			binding, found := byID[part.ProviderCallID]
			if !found {
				return nil, nil, errors.New("provider tool call has no tool call binding")
			}
			if err := modelenvelope.ValidateToolInput(part.ToolInput); err != nil {
				return nil, nil, err
			}
			delete(byID, part.ProviderCallID)
			proposals = append(
				proposals,
				outputProposal{
					ToolProposal: binding,
					Name:         part.ToolName,
					Input:        part.ToolInput,
					Error:        part.ToolCallError,
				},
			)
			content = append(content, Content{Ordinal: int32(ordinal), Kind: "tool_call", ToolID: binding.ID})
		default:
			return nil, nil, errors.New("unsupported output content")
		}
	}
	if len(byID) > 0 {
		return nil, nil, errors.New("unused tool bindings")
	}
	return content, proposals, nil
}

type preparedOutput struct {
	content   PreparedContent
	proposals []outputProposal
	evidence  ModelEvidence
	err       error
}

func PrepareOutput(input AcceptOutputInput) AcceptOutputInput {
	prepared := &preparedOutput{}
	input.prepared = prepared
	prepared.err = input.Response.Validate()
	if prepared.err != nil {
		return input
	}
	parts, proposals, err := outputParts(input.Response, input.Tools)
	if err != nil {
		prepared.err = err
		return input
	}
	prepared.proposals = proposals
	prepared.content, prepared.err = PrepareContent("model_output", parts)
	prepared.evidence = ModelEvidence{APIFormat: input.Response.APIFormat, APIVariant: input.Response.APIVariant,
		InputFingerprint: successfulInputFingerprint(input.Response), RequestID: input.RequestID,
		ResponseID: input.Response.Normalized.ID, Usage: input.Response.Normalized.Usage,
		Cost: input.Response.ProviderReportedCostUSD, Metadata: input.Response.ProviderMetadata}
	return input
}

func (h *Handle) AcceptOutput(ctx context.Context, input AcceptOutputInput) (AcceptedOutput, error) {
	if input.prepared == nil {
		input = PrepareOutput(input)
	}
	return executeCommand(ctx, h, func(m *executionMutation) (AcceptedOutput, error) {
		q := executiondb.New()
		source, err := q.ReadExecutionAttempt(
			ctx,
			h.unit.DB(),
			executiondb.ReadExecutionAttemptParams{AgentID: h.route.AgentID, ID: input.ContextID},
		)
		if err != nil {
			return AcceptedOutput{}, err
		}
		if source.OperationKind != "normal" {
			return AcceptedOutput{}, storeerr.ErrStateTransitionConflict
		}
		if source.RuntimeLockID != input.RuntimeLockID {
			return AcceptedOutput{}, storeerr.ErrRuntimeLockInactive
		}
		if source.State == "succeeded" {
			return h.replayOutput(ctx, source.ID, input.Tools)
		}
		if err := h.FenceRuntime(ctx, input.RuntimeLockID); err != nil {
			return AcceptedOutput{}, err
		}
		if source.State != "started" {
			return AcceptedOutput{}, storeerr.ErrStateTransitionConflict
		}
		if input.prepared.err != nil {
			return AcceptedOutput{}, input.prepared.err
		}
		if source.ProviderModelSlug != input.Response.RequestedProviderModelSlug {
			return AcceptedOutput{}, storeerr.ErrStateTransitionConflict
		}
		evidence, proposals := input.prepared.evidence, input.prepared.proposals
		m.knownContexts = []executiondb.LoadExecutionContextsRow{{
			ID: source.ID, AgentID: source.AgentID, TurnID: source.TurnID,
			OperationKind: source.OperationKind, AttemptNumber: source.AttemptNumber,
			InputEventSequence:     source.InputEventSequence,
			SourceEventSequenceEnd: source.SourceEventSequenceEnd, State: source.State, RecoveryKind: source.RecoveryKind,
			RetryAt: source.RetryAt, OpeningInputIds: source.OpeningInputIds, OpeningEventSequence: source.OpeningEventSequence}}
		snapshot, err := h.LoadExecution(ctx)
		if err != nil {
			return AcceptedOutput{}, err
		}
		var replay *json.RawMessage
		if len(input.Response.ProviderReplay) > 0 {
			replay = &input.Response.ProviderReplay
		}
		output, err := q.CreateExecutionOutput(ctx, h.unit.DB(), executiondb.CreateExecutionOutputParams{
			AgentID: h.route.AgentID, ContextID: input.ContextID, ServedModel: input.Response.ServedProviderModelSlug,
			StopReason: string(input.Response.Normalized.StopReason), Replay: replay})
		if err != nil {
			return AcceptedOutput{}, err
		}
		event, err := h.appendEvent(
			ctx,
			source.TurnID,
			"model_output",
			uuid.Nil,
			output.ID,
			uuid.Nil,
			false,
		)
		if err != nil {
			return AcceptedOutput{}, err
		}
		result := AcceptedOutput{ID: output.ID, Event: event, Created: true,
			Projections: make(map[uuid.UUID]dbsqlc.GetToolCallRow)}
		writes := make([]executiondb.CreateExecutionToolProposalParams, len(proposals))
		for i, call := range proposals {
			writes[i] = executiondb.CreateExecutionToolProposalParams{ID: call.ID, AgentID: h.route.AgentID, OutputID: output.ID,
				ProviderCallID: call.ProviderCallID, Name: call.Name, Input: call.Input, Type: call.Type}
		}
		if len(writes) > 0 {
			inserted := q.CreateExecutionToolProposal(ctx, h.unit.DB(), writes)
			inserted.QueryRow(func(i int, proposal executiondb.CreateExecutionToolProposalRow, queryErr error) {
				if queryErr != nil {
					err = errors.Join(err, queryErr)
					return
				}
				call := proposals[i]
				if call.Error == "" {
					result.Projections[call.ID] = dbsqlc.GetToolCallRow{ID: proposal.ID,
						ProjectID: h.route.ProjectID, AgentID: h.route.AgentID,
						TurnID: source.TurnID, SourceEventID: event.ID, ModelCallContextID: source.ID,
						ProviderCallID: call.ProviderCallID,
						Name:           call.Name, Input: call.Input, Type: call.Type,
						CreatedAt: proposal.CreatedAt, State: proposal.State, ResultContentParts: []byte(`[]`)}
				}
				h.unit.Notifications().AddToolCallUpdate(h.route.AgentID, call.ID, "awaiting_authorization", nil)
				result.Tools = append(result.Tools, call.ToolProposal)
			})
			if err = errors.Join(err, inserted.Close()); err != nil {
				return AcceptedOutput{}, err
			}
		}
		if err = h.writePreparedContent(
			ctx, "model_output", output.ID, input.prepared.content,
		); err != nil {
			return AcceptedOutput{}, err
		}
		if err = h.advanceTurn(ctx, event, len(proposals) == 0); err != nil {
			return AcceptedOutput{}, err
		}
		for _, call := range proposals {
			if call.Error != "" {
				if err = h.rejectProposal(ctx, m, call); err != nil {
					return AcceptedOutput{}, err
				}
			}
		}
		if _, err = h.finishAttempt(
			ctx, m, source, ContextSucceeded, ModelFailure{RuntimeLockID: input.RuntimeLockID}, evidence,
		); err != nil {
			return AcceptedOutput{}, err
		}
		sourceContext := attemptRecord(source).Context
		sourceContext.State = ContextSucceeded
		if err = applyAcceptedOutput(m,
			snapshot,
			sourceContext,
			output.ID,
			event,
			len(proposals) > 0,
			input.Response.Normalized.StopReason == modelenvelope.StopReasonMaxTokens); err != nil {
			return AcceptedOutput{}, err
		}
		for _, call := range proposals {
			if call.Error != "" {
				m.changed()
				break
			}
		}
		if kind := ParentEffectKind(input.Response); kind != "" {
			if err = h.parentEffect(ctx,
				kind,
				strings.TrimSpace(input.Response.Text()),
				"model_output:"+output.ID.String(),
				uuid.Nil); err != nil {
				return AcceptedOutput{}, err
			}
		}
		if input.FinishClaim {
			if err := h.finishRuntime(ctx, m, input.RuntimeLockID); err != nil {
				return AcceptedOutput{}, err
			}
		}
		return result, nil
	})
}

func (h *Handle) replayOutput(ctx context.Context, contextID uuid.UUID, tools []ToolProposal) (AcceptedOutput, error) {
	q := executiondb.New()
	batch := factsBatch{DBTX: h.unit.DB()}
	var output executiondb.FindExecutionOutputRow
	var stored []executiondb.ReadExecutionProposalsRow
	outputs := q.FindExecutionOutput(ctx, &batch, []executiondb.FindExecutionOutputParams{
		{AgentID: h.route.AgentID, ContextID: contextID},
	})
	proposals := q.ReadExecutionProposals(ctx, &batch, []executiondb.ReadExecutionProposalsParams{
		{AgentID: h.route.AgentID, ContextID: contextID},
	})
	batch.readers = append(batch.readers, func() {
		outputs.QueryRow(func(_ int, row executiondb.FindExecutionOutputRow, err error) {
			output = row
			batch.err = errors.Join(batch.err, err)
		})
		batch.err = errors.Join(batch.err, outputs.Close())
	}, func() {
		proposals.Query(func(_ int, rows []executiondb.ReadExecutionProposalsRow, err error) {
			stored = rows
			batch.err = errors.Join(batch.err, err)
		})
		batch.err = errors.Join(batch.err, proposals.Close())
	})
	if err := batch.read(ctx); err != nil {
		return AcceptedOutput{}, err
	}
	bindings := make([]ToolProposal, len(stored))
	for i, call := range stored {
		for _, tool := range tools {
			if tool.ProviderCallID == call.ProviderCallID && tool.ID != uuid.Nil && tool.ID != call.ID {
				return AcceptedOutput{}, storeerr.ErrIdempotencyConflict
			}
		}
		bindings[i] = ToolProposal{ID: call.ID, ProviderCallID: call.ProviderCallID, Type: call.Type}
	}
	return AcceptedOutput{ID: output.ID, Event: ExecutionEvent{ID: output.EventID, TurnID: output.TurnID,
		EventBoundary: EventBoundary{Sequence: output.Sequence, Time: output.CreatedAt}}, Tools: bindings}, nil
}

func applyAcceptedOutput(
	m *executionMutation,
	snapshot ExecutionSnapshot,
	source ModelContext,
	id uuid.UUID,
	event ExecutionEvent,
	tools, limit bool,
) error {
	if snapshot.View.NormalContext != nil && snapshot.View.NormalContext.ID == source.ID {
		snapshot.View.NormalContext = &source
	}
	if snapshot.View.CompactionContext != nil && snapshot.View.CompactionContext.ID == source.ID {
		snapshot.View.CompactionContext = &source
	}
	if snapshot.View.ToolBatch != nil && snapshot.View.ToolBatch.Output.Context.ID == source.ID {
		snapshot.View.ToolBatch.Output.Context = source
	}
	if snapshot.View.OutputLimit != nil && snapshot.View.OutputLimit.Context.ID == source.ID {
		snapshot.View.OutputLimit.Context = source
	}
	if source.TurnID != m.head.CurrentTurnID || event.Sequence < m.head.StopSequence {
		return m.updated(snapshot)
	}
	if snapshot.View.ToolBatch != nil {
		completion := snapshot.View.ToolBatch.Completion
		if completion == nil || source.InputEventSequence < completion.LastResultSequence {
			return errors.New("accepted model output does not cover pending tool results")
		}
		m.head.PendingToolOutputID = uuid.Nil
		snapshot.View.ToolBatch = nil
	}
	m.head.PendingOutputLimitID = uuid.Nil
	snapshot.View.OutputLimit = nil
	output := ModelOutput{ID: id, EventID: event.ID, Event: event.EventBoundary, Context: source}
	if tools {
		m.head.PendingToolOutputID = id
		snapshot.View.ToolBatch = &ToolBatch{Output: output, HasIncomplete: true, HasRunnable: true}
	} else if limit {
		m.head.PendingOutputLimitID = id
		snapshot.View.OutputLimit = &output
	}
	if !tools && snapshot.View.Turn != nil {
		snapshot.View.Turn.LatestSemantic = event.EventBoundary
	}
	snapshot.databaseNow = event.Time
	return m.updated(snapshot)
}

func (h *Handle) rejectProposal(ctx context.Context, m *executionMutation, call outputProposal) error {
	row, err := h.tool(ctx, call.ID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(map[string]string{"error": call.Error, "error_code": "malformed"})
	if err != nil {
		return err
	}
	_, err = h.completeTool(
		ctx,
		m,
		row,
		"failed",
		[]Content{{Kind: "structured_data", Data: data}},
		uuid.Nil,
		nil,
	)
	return err
}

func ParentEffectKind(response modelenvelope.ResponseEnvelope) string {
	if response.HasToolCalls() {
		return ""
	}
	return map[modelenvelope.StopReason]string{modelenvelope.StopReasonEndTurn: "result",
		modelenvelope.StopReasonRefusal:       "refused",
		modelenvelope.StopReasonContentFilter: "content_filtered"}[response.Normalized.StopReason]
}

func successfulInputFingerprint(response modelenvelope.ResponseEnvelope) string {
	if normalizedUsage(response.Normalized.Usage).InputTokens <= 0 {
		return ""
	}
	return response.InputFingerprint
}

func (h *Handle) ResolveOutput(ctx context.Context, input AcceptOutputInput) (AcceptedOutput, bool, error) {
	source, err := executiondb.New().ReadExecutionAttempt(ctx, h.unit.DB(), executiondb.ReadExecutionAttemptParams{
		AgentID: h.route.AgentID, ID: input.ContextID,
	})
	if err != nil {
		return AcceptedOutput{}, false, err
	}
	if source.RuntimeLockID != input.RuntimeLockID {
		return AcceptedOutput{}, false, storeerr.ErrRuntimeLockInactive
	}
	if source.State != "succeeded" || source.OperationKind != "normal" {
		return AcceptedOutput{}, false, nil
	}
	result, err := h.replayOutput(ctx, source.ID, input.Tools)
	return result, err == nil, err
}
