package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	logpkg "github.com/omnara-ai/omnara/observability/wideevent"
)

// Program tool calls are tools a run_command process calls through
// `omnarad call`, relayed by its machine's daemon.

const (
	programToolCallMaxKeyBytes   = 200
	programToolCallMaxInputBytes = 1024 * 1024
	// programToolCallSubmitWait lets fast calls finish in the submit round trip.
	programToolCallSubmitWait = 2 * time.Second
	// Notifications wake waiters; polls only cover missed or unavailable ones.
	programToolCallPoll         = 2 * time.Second
	programToolCallFallbackPoll = 250 * time.Millisecond
)

func (s strictOpenAPIServer) CreateDaemonProgramToolCall(
	ctx context.Context,
	request openapi.CreateDaemonProgramToolCallRequestObject,
) (openapi.CreateDaemonProgramToolCallResponseObject, error) {
	scope, processID, apiErr := daemonProgramToolCallTarget(ctx, request.ProcessID)
	if apiErr != nil {
		return nil, *apiErr
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeUnprocessable, "request body is required")
	}
	body := *request.Body
	switch {
	case body.Key == "" || len(body.Key) > programToolCallMaxKeyBytes:
		return nil, apierror.FromCode(openapi.ErrorCodeUnprocessable,
			fmt.Sprintf("key must be 1 to %d bytes", programToolCallMaxKeyBytes))
	case strings.TrimSpace(body.Name) == "":
		return nil, apierror.FromCode(openapi.ErrorCodeUnprocessable, "name is required")
	case len(body.Input) > programToolCallMaxInputBytes:
		return nil, apierror.FromCode(openapi.ErrorCodeRequestTooLarge, "input exceeds 1 MiB")
	case !programToolInputObject(body.Input):
		return nil, apierror.FromCode(openapi.ErrorCodeUnprocessable, "input must be a JSON object")
	}
	program, err := s.server.daemonProgramToolScope(ctx, scope, processID)
	if err != nil {
		return nil, daemonProgramToolCallError(ctx, err)
	}
	specs, contract, err := tools.AgentConfigToolSpecs(
		ctx, s.server.store, program.ProjectID, program.AgentID, program.AgentConfigID)
	if err != nil {
		return nil, daemonProgramToolCallError(ctx, err)
	}
	if body.Name == toolcatalog.ToolNameToolSearch {
		callable, err := tools.ProgramCallableToolSpecs(contract, specs)
		if err != nil {
			return nil, daemonProgramToolCallError(ctx, err)
		}
		search, err := tools.ProgramToolSearch(callable, body.Input)
		if err != nil {
			return nil, apierror.FromCode(openapi.ErrorCodeUnprocessable, err.Error())
		}
		encoded, err := json.Marshal(search)
		if err != nil {
			return nil, err
		}
		value := json.RawMessage(encoded)
		outcome := openapi.ToolCallOutcomeSucceeded
		return openapi.CreateDaemonProgramToolCall200JSONResponse{
			State: openapi.DaemonProgramToolCallStateCompleted, Outcome: &outcome, Structured: &value,
			Content: &[]openapi.DaemonProgramToolCallContent{
				{Type: openapi.DaemonProgramToolCallContentTypeStructuredData, Value: &value},
			},
		}, nil
	}
	spec, found := modelcontext.ToolSpecByName(specs, body.Name)
	if !found {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound,
			fmt.Sprintf("tool %q is not available to this agent", body.Name))
	}
	if !tools.ProgramToolCallable(contract, spec) {
		return nil, apierror.FromCode(openapi.ErrorCodeForbidden,
			fmt.Sprintf("tool %q cannot be called from commands", body.Name))
	}
	record, err := s.server.store.Execution().CreateProgramToolCall(ctx, executionstore.CreateProgramToolCallInput{
		ProjectID: program.ProjectID, AgentID: program.AgentID, ProcessID: program.ProcessID,
		Key: body.Key, Name: body.Name, Type: spec.Type, Input: body.Input,
	})
	if err != nil {
		return nil, daemonProgramToolCallError(ctx, err)
	}
	result, err := s.server.waitDaemonProgramToolCall(ctx, program, record.ID, programToolCallSubmitWait,
		func(state openapi.DaemonProgramToolCallState) bool {
			return state != openapi.DaemonProgramToolCallStatePending
		})
	if err != nil {
		return nil, daemonProgramToolCallError(ctx, err)
	}
	return openapi.CreateDaemonProgramToolCall200JSONResponse(result), nil
}

func (s strictOpenAPIServer) GetDaemonProgramToolCall(
	ctx context.Context,
	request openapi.GetDaemonProgramToolCallRequestObject,
) (openapi.GetDaemonProgramToolCallResponseObject, error) {
	scope, processID, apiErr := daemonProgramToolCallTarget(ctx, request.ProcessID)
	if apiErr != nil {
		return nil, *apiErr
	}
	toolCallID, ok := parseOpenAPIPublicID(publicid.KindToolCall, request.ToolCallID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "tool call not found")
	}
	wait := time.Duration(0)
	if request.Params.WaitMs != nil {
		if *request.Params.WaitMs < 0 {
			return nil, apierror.FromCode(openapi.ErrorCodeUnprocessable, "wait_ms must not be negative")
		}
		wait = time.Duration(min(*request.Params.WaitMs, 25000)) * time.Millisecond
	}
	program, err := s.server.daemonProgramToolScope(ctx, scope, processID)
	if err != nil {
		return nil, daemonProgramToolCallError(ctx, err)
	}
	var initial openapi.DaemonProgramToolCallState
	result, err := s.server.waitDaemonProgramToolCall(ctx, program, toolCallID, wait,
		func(state openapi.DaemonProgramToolCallState) bool {
			if initial == "" {
				initial = state
			}
			return state != initial || state == openapi.DaemonProgramToolCallStateCompleted
		})
	if err != nil {
		return nil, daemonProgramToolCallError(ctx, err)
	}
	return openapi.GetDaemonProgramToolCall200JSONResponse(result), nil
}

func daemonProgramToolCallTarget(
	ctx context.Context, rawProcessID string,
) (machineDaemonScope, uuid.UUID, *apierror.ResponseError) {
	scope, scopeErr := machineDaemonScopeFromContext(ctx)
	if scopeErr != nil {
		return machineDaemonScope{}, uuid.Nil, scopeErr
	}
	processID, ok := parseOpenAPIPublicID(publicid.KindProcess, rawProcessID)
	if !ok {
		err := apierror.FromCode(openapi.ErrorCodeNotFound, "process not found")
		return machineDaemonScope{}, uuid.Nil, &err
	}
	return scope, processID, nil
}

var errProgramToolCallsDisabledOnMachine = errors.New("programmatic tool calls are disabled on this machine")

func (s *Server) daemonProgramToolScope(
	ctx context.Context, scope machineDaemonScope, processID uuid.UUID,
) (executionstore.ProgramToolScope, error) {
	program, found, err := s.store.Execution().GetDaemonProgramToolScope(ctx, scope.OrgID, scope.MachineID, processID)
	if err != nil || found {
		return program, err
	}
	// The scope does not say why it is missing; the machine flag is the
	// common case worth naming.
	machine, err := s.store.Execution().GetMachine(ctx, scope.OrgID, scope.MachineID)
	if err == nil && !machine.ProgrammaticToolCalls {
		return program, errProgramToolCallsDisabledOnMachine
	}
	return program, storeerr.ErrNotFound
}

// waitDaemonProgramToolCall reads the call until done accepts its state, wait
// elapses, or the request ends, woken by the agent's tool call updates.
func (s *Server) waitDaemonProgramToolCall(
	ctx context.Context,
	program executionstore.ProgramToolScope,
	toolCallID uuid.UUID,
	wait time.Duration,
	done func(openapi.DaemonProgramToolCallState) bool,
) (openapi.DaemonProgramToolCall, error) {
	deadline := time.Now().Add(wait)
	poll := programToolCallFallbackPoll
	updates := make(chan struct{}, 1)
	if wait > 0 && s.agentToolCallUpdateSubscriber != nil {
		subscription, err := s.agentToolCallUpdateSubscriber.SubscribeAgentToolCallUpdates(ctx, program.AgentID,
			func(_ context.Context, update notifications.ToolCallUpdatedCommitted) {
				if update.ToolCallID != toolCallID {
					return
				}
				select {
				case updates <- struct{}{}:
				default:
				}
			})
		if err != nil {
			s.log.Warn("program tool call update subscribe failed", "agent_id", program.AgentID, "error", err)
		} else {
			defer func() { _ = subscription.Unsubscribe() }()
			poll = programToolCallPoll
		}
	}
	for {
		record, found, err := s.store.Execution().GetProgramToolCall(
			ctx, program.ProjectID, program.AgentID, program.ProcessID, toolCallID)
		if err != nil {
			return openapi.DaemonProgramToolCall{}, err
		}
		if !found {
			return openapi.DaemonProgramToolCall{}, storeerr.ErrNotFound
		}
		result, err := daemonProgramToolCallResponse(record.ToolCallRecord)
		if err != nil {
			return openapi.DaemonProgramToolCall{}, err
		}
		remaining := time.Until(deadline)
		if done(result.State) || remaining <= 0 {
			return result, nil
		}
		timer := time.NewTimer(min(poll, remaining))
		select {
		case <-ctx.Done():
			timer.Stop()
			return openapi.DaemonProgramToolCall{}, ctx.Err()
		case <-updates:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func daemonProgramToolCallResponse(record executionstore.ToolCallRecord) (openapi.DaemonProgramToolCall, error) {
	id, err := publicid.Encode(publicid.KindToolCall, record.ID)
	if err != nil {
		return openapi.DaemonProgramToolCall{}, err
	}
	result := openapi.DaemonProgramToolCall{ToolCallId: &id, State: openapi.DaemonProgramToolCallStatePending}
	switch record.State {
	case executionstore.ToolCallStateAwaitingPermission:
		result.State = openapi.DaemonProgramToolCallStateAwaitingApproval
		return result, nil
	case executionstore.ToolCallStateCompleted:
	default:
		return result, nil
	}
	result.State = openapi.DaemonProgramToolCallStateCompleted
	outcome := openapi.ToolCallOutcome(record.Outcome)
	result.Outcome = &outcome
	var parts []struct {
		Type       string          `json:"type"`
		Text       string          `json:"text"`
		Value      json.RawMessage `json:"value"`
		ArtifactID string          `json:"artifact_id"`
	}
	if len(record.ResultContentParts) > 0 {
		if err := json.Unmarshal(record.ResultContentParts, &parts); err != nil {
			return openapi.DaemonProgramToolCall{}, fmt.Errorf("decode program tool call result: %w", err)
		}
	}
	content := make([]openapi.DaemonProgramToolCallContent, 0, len(parts))
	for _, part := range parts {
		item := openapi.DaemonProgramToolCallContent{Type: openapi.DaemonProgramToolCallContentType(part.Type)}
		switch item.Type {
		case openapi.DaemonProgramToolCallContentTypeText:
			item.Text = &part.Text
		case openapi.DaemonProgramToolCallContentTypeStructuredData:
			item.Value = &part.Value
			if result.Structured == nil {
				result.Structured = &part.Value
			}
		case openapi.DaemonProgramToolCallContentTypeMediaRef:
			artifactID, err := uuid.Parse(part.ArtifactID)
			if err != nil {
				return openapi.DaemonProgramToolCall{}, fmt.Errorf("decode program tool call artifact: %w", err)
			}
			encoded, err := publicid.Encode(publicid.KindArtifact, artifactID)
			if err != nil {
				return openapi.DaemonProgramToolCall{}, err
			}
			item.ArtifactId = &encoded
		default:
			continue
		}
		content = append(content, item)
	}
	result.Content = &content
	return result, nil
}

func daemonProgramToolCallError(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, errProgramToolCallsDisabledOnMachine):
		return apierror.FromCode(openapi.ErrorCodeForbidden, err.Error())
	case errors.Is(err, storeerr.ErrNotFound), errors.Is(err, storeerr.ErrProcessTerminal):
		return apierror.FromCode(openapi.ErrorCodeNotFound,
			"no running process can call tools here; the process may have ended, or programmatic tool "+
				"calls may be disabled for its agent")
	case errors.Is(err, storeerr.ErrIdempotencyConflict):
		return apierror.FromCode(openapi.ErrorCodeIdempotencyKeyConflict,
			"key was already used for a different tool call")
	case errors.Is(err, storeerr.ErrInvalidRequest):
		return apierror.FromCode(openapi.ErrorCodeUnprocessable, "tool call request is invalid")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "tool call request was interrupted")
	}
	logpkg.Error(ctx, fmt.Errorf("daemon program tool call: %w", err))
	return apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "tool calls are temporarily unavailable")
}

func programToolInputObject(input json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(input, &object) == nil && object != nil
}
