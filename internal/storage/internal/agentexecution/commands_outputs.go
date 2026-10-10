package agentexecution

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ToolProposal struct {
	ID             uuid.UUID
	ProviderCallID string
	Type           string
}
type AcceptOutputInput struct {
	RuntimeLockID uuid.UUID
	ContextID     uuid.UUID
	RequestID     string
	Response      modelenvelope.ResponseEnvelope
	Tools         []ToolProposal
}

type AcceptedOutput struct {
	ID      uuid.UUID
	Event   ExecutionEvent
	Tools   []ToolProposal
	Created bool
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

func (h *Handle) AcceptOutput(ctx context.Context, input AcceptOutputInput) (AcceptedOutput, error) {
	return executeCommand(ctx, h, func(m *executionMutation) (AcceptedOutput, error) {
		if err := input.Response.Validate(); err != nil {
			return AcceptedOutput{}, err
		}
		if err := h.FenceRuntime(ctx, input.RuntimeLockID); err != nil {
			return AcceptedOutput{}, err
		}
		q := executiondb.New()
		source, err := q.ReadExecutionAttempt(
			ctx,
			h.unit.DB(),
			executiondb.ReadExecutionAttemptParams{AgentID: h.route.AgentID, ID: input.ContextID},
		)
		if err != nil {
			return AcceptedOutput{}, err
		}
		if source.OperationKind != "normal" ||
			source.ProviderModelSlug != input.Response.RequestedProviderModelSlug {
			return AcceptedOutput{}, storeerr.ErrStateTransitionConflict
		}
		if source.RuntimeLockID != input.RuntimeLockID {
			return AcceptedOutput{}, storeerr.ErrRuntimeLockInactive
		}
		if source.State != "started" && source.State != "succeeded" {
			return AcceptedOutput{}, storeerr.ErrStateTransitionConflict
		}
		evidence := ModelEvidence{APIFormat: input.Response.APIFormat, APIVariant: input.Response.APIVariant,
			RequestID:  input.RequestID,
			ResponseID: input.Response.Normalized.ID,
			Usage:      input.Response.Normalized.Usage,
			Cost:       input.Response.ProviderReportedCostUSD, Metadata: input.Response.ProviderMetadata}
		if source.State == "succeeded" {
			return h.replayOutput(ctx, m, source, input, evidence)
		}
		parts, proposals, err := outputParts(input.Response, input.Tools)
		if err != nil {
			return AcceptedOutput{}, err
		}
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
		result := AcceptedOutput{ID: output.ID, Event: event, Created: true}
		for _, call := range proposals {
			if err = q.CreateExecutionToolProposal(ctx, h.unit.DB(), executiondb.CreateExecutionToolProposalParams{
				ID: call.ID, AgentID: h.route.AgentID, OutputID: output.ID, ProviderCallID: call.ProviderCallID,
				Name: call.Name, Input: call.Input, Type: call.Type}); err != nil {
				return AcceptedOutput{}, err
			}
			h.unit.Notifications().AddToolCallUpdate(h.route.AgentID, call.ID, "awaiting_authorization", nil)
			result.Tools = append(result.Tools, call.ToolProposal)
		}
		if err = h.writeContent(ctx, "model_output", output.ID, parts); err != nil {
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
		if err = applyAcceptedOutput(m,
			snapshot,
			attemptRecord(source).Context,
			output.ID,
			event,
			len(proposals) > 0,
			input.Response.Normalized.StopReason == modelenvelope.StopReasonMaxTokens); err != nil {
			return AcceptedOutput{}, err
		}
		if !input.Response.HasToolCalls() {
			kind := map[modelenvelope.StopReason]string{modelenvelope.StopReasonEndTurn: "result",
				modelenvelope.StopReasonRefusal:       "refused",
				modelenvelope.StopReasonContentFilter: "content_filtered"}[input.Response.Normalized.StopReason]
			if kind != "" {
				if err = h.parentEffect(ctx,
					kind,
					strings.TrimSpace(input.Response.Text()),
					"model_output:"+output.ID.String(),
					uuid.Nil); err != nil {
					return AcceptedOutput{}, err
				}
			}
		}
		return result, nil
	})
}

func (h *Handle) replayOutput(
	ctx context.Context,
	m *executionMutation,
	source executiondb.ReadExecutionAttemptRow,
	input AcceptOutputInput,
	evidence ModelEvidence,
) (AcceptedOutput, error) {
	if _, err := h.finishAttempt(
		ctx, m, source, ContextSucceeded, ModelFailure{RuntimeLockID: input.RuntimeLockID}, evidence,
	); err != nil {
		return AcceptedOutput{}, err
	}
	q := executiondb.New()
	output, err := q.FindExecutionOutput(
		ctx,
		h.unit.DB(),
		executiondb.FindExecutionOutputParams{AgentID: h.route.AgentID, ContextID: source.ID},
	)
	if err != nil {
		return AcceptedOutput{}, err
	}
	replayPresent := len(input.Response.ProviderReplay) > 0 || output.ProviderReplay != nil
	replayMatches := equalJSON(valueOrZero(output.ProviderReplay), input.Response.ProviderReplay)
	if output.StopReason != string(input.Response.Normalized.StopReason) ||
		output.ServedProviderModelSlug != input.Response.ServedProviderModelSlug ||
		(replayPresent && !replayMatches) {
		return AcceptedOutput{}, storeerr.ErrIdempotencyConflict
	}
	stored, err := q.ReadExecutionProposals(
		ctx,
		h.unit.DB(),
		executiondb.ReadExecutionProposalsParams{AgentID: h.route.AgentID, OutputID: output.ID},
	)
	if err != nil {
		return AcceptedOutput{}, err
	}
	bindings := make([]ToolProposal, len(input.Tools))
	copy(bindings, input.Tools)
	for i, binding := range bindings {
		for _, call := range stored {
			if binding.ProviderCallID == call.ProviderCallID && binding.ID == uuid.Nil {
				bindings[i].ID = call.ID
			}
		}
	}
	parts, proposals, err := outputParts(input.Response, bindings)
	if err != nil {
		return AcceptedOutput{}, err
	}
	if len(stored) != len(proposals) {
		return AcceptedOutput{}, storeerr.ErrIdempotencyConflict
	}
	for _, call := range stored {
		matched := false
		for _, proposal := range proposals {
			if proposal.ID == call.ID && proposal.ProviderCallID == call.ProviderCallID &&
				proposal.Name == call.Name &&
				proposal.Type == call.Type &&
				equalJSON(proposal.Input, call.Input) {
				if proposal.Error != "" {
					row, err := h.tool(ctx, call.ID)
					if err != nil {
						return AcceptedOutput{}, err
					}
					body, err := json.Marshal(
						map[string]string{"error": proposal.Error, "error_code": "malformed"},
					)
					if err != nil {
						return AcceptedOutput{}, err
					}
					if row.ResultID == nil || valueOrZero(row.Outcome) != "failed" {
						return AcceptedOutput{}, storeerr.ErrIdempotencyConflict
					}
					if err = h.matchContent(ctx,
						executiondb.ReadExecutionContentParams{AgentID: h.route.AgentID,
							ResultID: row.ResultID},
						[]Content{{Kind: "structured_data",
							Data: body}}); err != nil {
						return AcceptedOutput{}, err
					}
				}
				matched = true
				break
			}
		}
		if !matched {
			return AcceptedOutput{}, storeerr.ErrIdempotencyConflict
		}
	}
	if err = h.contentMatches(ctx, uuid.Nil, output.ID, parts); err != nil {
		return AcceptedOutput{}, err
	}
	return AcceptedOutput{ID: output.ID, Event: ExecutionEvent{ID: output.EventID, TurnID: output.TurnID,
		EventBoundary: EventBoundary{
			Sequence: output.Sequence,
			Time:     output.CreatedAt,
		}}, Tools: bindings}, nil
}

func applyAcceptedOutput(
	m *executionMutation,
	snapshot ExecutionSnapshot,
	source ModelContext,
	id uuid.UUID,
	event ExecutionEvent,
	tools, limit bool,
) error {
	m.head.AnsweredThroughSequence = max(m.head.AnsweredThroughSequence, event.Sequence)
	m.changed()
	if source.TurnID != m.head.CurrentTurnID || event.Sequence < m.head.StopSequence {
		return nil
	}
	if snapshot.View.ToolBatch != nil {
		completion := snapshot.View.ToolBatch.Completion
		if completion == nil || source.InputEventSequence < completion.LastResultSequence {
			return errors.New("accepted model output does not cover pending tool results")
		}
		m.head.PendingToolOutputID = uuid.Nil
	}
	m.head.PendingOutputLimitID = uuid.Nil
	if tools {
		m.head.PendingToolOutputID = id
	} else if limit {
		m.head.PendingOutputLimitID = id
	}
	return nil
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
