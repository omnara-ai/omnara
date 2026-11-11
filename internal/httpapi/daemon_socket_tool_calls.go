package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/httpapi/publicevents"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const (
	daemonToolCallLimit         = 128
	programToolCallLease        = time.Minute
	programToolCallPoll         = 2 * time.Second
	programToolCallFallbackPoll = 250 * time.Millisecond

	programToolCallResultTooLarge = `{"error_code":"result_too_large",` +
		`"error":"The tool result is too large for a tool call from a command."}`
)

type ProgramToolExecutor interface {
	RunProgramToolCall(context.Context, executionstore.ProgramToolCallClaim, time.Duration) error
}

func WithProgramToolExecutor(executor ProgramToolExecutor) Option {
	return func(s *Server) {
		s.programToolExecutor = executor
	}
}

type toolCallRejectedError struct {
	status  daemonprotocol.AckStatus
	code    string
	message string
}

func (r toolCallRejectedError) Error() string { return r.message }

func rejectToolCall(code, format string, args ...any) error {
	return toolCallRejectedError{status: daemonprotocol.AckStatusPermanentReject, code: code,
		message: fmt.Sprintf(format, args...)}
}

func (s *daemonSocket) handleToolCallOffer(ctx context.Context, msg daemonprotocol.Message) error {
	if msg.ToolCallOffer == nil {
		return errors.New("tool_call_offer payload is required")
	}
	offer := *msg.ToolCallOffer
	if offer.ProcessID == "" || offer.Key == "" {
		return errors.New("tool_call_offer process_id and key are required")
	}
	id := offer.ProcessID + "\x00" + offer.Key
	canceled := make(chan struct{})
	s.workMu.Lock()
	_, serving := s.toolCalls[id]
	full := len(s.toolCalls) >= daemonToolCallLimit
	if !serving && !full {
		s.toolCalls[id] = canceled
		s.toolCallsDone.Add(1)
	}
	s.workMu.Unlock()
	switch {
	case serving:
	case full:
		s.reportToolCall(ctx, offer, daemonprotocol.ToolCallReport{
			AckStatus: daemonprotocol.AckStatusTransientError, ErrorCode: daemonprotocol.ErrorCodeToolCallLimitReached,
			Error: "this machine has too many tool calls in flight; wait for one to finish"})
	default:
		go func() {
			defer s.toolCallsDone.Done()
			defer func() {
				s.workMu.Lock()
				delete(s.toolCalls, id)
				s.workMu.Unlock()
			}()
			s.serveToolCall(ctx, offer, canceled)
		}()
	}
	return nil
}

func (s *daemonSocket) handleToolCallCancel(msg daemonprotocol.Message) error {
	if msg.ToolCallCancel == nil {
		return errors.New("tool_call_cancel payload is required")
	}
	id := msg.ToolCallCancel.ProcessID + "\x00" + msg.ToolCallCancel.Key
	s.workMu.Lock()
	if canceled := s.toolCalls[id]; canceled != nil {
		close(canceled)
		s.toolCalls[id] = nil
	}
	s.workMu.Unlock()
	return nil
}

func (s *daemonSocket) reportToolCall(
	ctx context.Context, offer daemonprotocol.ToolCallOffer, report daemonprotocol.ToolCallReport,
) {
	report.ProcessID, report.Key = offer.ProcessID, offer.Key
	msg := daemonprotocol.Message{Type: daemonprotocol.MessageToolCallReport, ToolCallReport: &report}
	if encoded, err := json.Marshal(msg); err != nil || len(encoded) > daemonprotocol.MaxMessageBytes {
		report.Outcome = daemonprotocol.ProgramToolCallOutcomeFailed
		report.Content = []daemonprotocol.ProgramToolCallContent{{
			Type:  daemonprotocol.ProgramToolCallContentStructuredData,
			Value: json.RawMessage(programToolCallResultTooLarge),
		}}
	}
	select {
	case s.send <- daemonSocketOutbound{msg: msg}:
		s.recordSocketEvent("tool_call_report", report.State, string(report.AckStatus))
	case <-s.done:
	case <-ctx.Done():
	}
}

func (s *daemonSocket) serveToolCall(
	ctx context.Context, offer daemonprotocol.ToolCallOffer, canceled <-chan struct{},
) {
	server := s.server
	reject := func(err error) {
		rejection := programToolCallRejection(server, err)
		s.reportToolCall(ctx, offer, daemonprotocol.ToolCallReport{
			AckStatus: rejection.status, ErrorCode: rejection.code, Error: rejection.message})
	}
	ref, result, err := server.offerProgramToolCall(ctx, s.orgID, s.machineID, offer)
	if err != nil {
		reject(err)
		return
	}
	if result != nil {
		s.reportToolCall(ctx, offer, daemonprotocol.ToolCallReport{ProgramToolCallResult: *result})
		return
	}
	poll := programToolCallFallbackPoll
	updates := make(chan struct{}, 1)
	if server.agentToolCallUpdateSubscriber != nil {
		subscription, err := server.agentToolCallUpdateSubscriber.SubscribeAgentToolCallUpdates(ctx, ref.AgentID,
			func(_ context.Context, update notifications.ToolCallUpdatedCommitted) {
				if update.ToolCallID != ref.ToolCallID {
					return
				}
				select {
				case updates <- struct{}{}:
				default:
				}
			})
		if err != nil {
			server.log.Warn("program tool call update subscribe failed", "agent_id", ref.AgentID, "error", err)
		} else {
			defer func() { _ = subscription.Unsubscribe() }()
			poll = programToolCallPoll
		}
	}
	var running <-chan struct{}
	claim, awaitingApproval, canceling := true, false, false
	var reclaimAfter time.Time
	for {
		record, found, err := server.store.Execution().GetProgramToolCall(
			ctx, ref.ProjectID, ref.AgentID, ref.ProcessID, ref.ToolCallID)
		if err == nil && !found {
			err = storeerr.ErrNotFound
		}
		var report daemonprotocol.ProgramToolCallResult
		if err == nil {
			report, err = programToolCallResult(record.ToolCallRecord)
		}
		if err != nil {
			if ctx.Err() == nil {
				reject(err)
			}
			return
		}
		select {
		case <-canceled:
			canceling, canceled = true, nil
		default:
		}
		if canceling {
			if record.State == executionstore.ToolCallStateCompleted {
				return
			}
			over, err := server.store.Execution().CancelProgramToolCall(ctx, ref)
			if err != nil && ctx.Err() == nil {
				server.log.Error("cancel program tool call", "tool_call_id", ref.ToolCallID, "error", err)
			}
			if err != nil || over || record.State != executionstore.ToolCallStateAwaitingAuthorization {
				return
			}
		}
		switch record.State {
		case executionstore.ToolCallStateCompleted:
			s.reportToolCall(ctx, offer, daemonprotocol.ToolCallReport{ProgramToolCallResult: report})
			return
		case executionstore.ToolCallStateAwaitingPermission:
			if !awaitingApproval {
				awaitingApproval = true
				s.reportToolCall(ctx, offer, daemonprotocol.ToolCallReport{ProgramToolCallResult: report})
			}
		case executionstore.ToolCallStateAwaitingAuthorization, executionstore.ToolCallStateReady:
			if claim && running == nil && !canceling {
				//nolint:contextcheck // the run outlives this socket's context
				running = server.runProgramToolCall(ref)
			}
		case executionstore.ToolCallStateWaiting:
			if claim && running == nil && !canceling && record.Type == toolcatalog.ToolTypeBuiltIn &&
				!time.Now().Before(reclaimAfter) {
				reclaimAfter = time.Now().Add(programToolCallLease / 3)
				//nolint:contextcheck // the run outlives this socket's context
				running = server.runProgramToolCall(ref)
			}
		case executionstore.ToolCallStateRunning:
		}
		claim = true
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-running:
			timer.Stop()
			running, claim = nil, false
		case <-updates:
			timer.Stop()
		case <-canceled:
			timer.Stop()
			canceling, canceled = true, nil
		case <-timer.C:
		}
	}
}

var errProgramToolCallsDisabledOnMachine = errors.New("programmatic tool calls are disabled on this machine")

func (s *Server) offerProgramToolCall(
	ctx context.Context, orgID, machineID uuid.UUID, offer daemonprotocol.ToolCallOffer,
) (executionstore.ProgramToolCallRef, *daemonprotocol.ProgramToolCallResult, error) {
	var ref executionstore.ProgramToolCallRef
	invalid := func(format string, args ...any) error {
		return rejectToolCall(daemonprotocol.ErrorCodeValidationFailed, format, args...)
	}
	if s.programToolExecutor == nil {
		return ref, nil, errProgramToolCallsDisabledOnMachine
	}
	processID, err := publicid.Decode(publicid.KindProcess, offer.ProcessID)
	if err != nil {
		return ref, nil, storeerr.ErrNotFound
	}
	key, err := uuid.Parse(offer.Key)
	var input map[string]json.RawMessage
	switch {
	case err != nil || key == uuid.Nil:
		return ref, nil, invalid("key is required")
	case strings.TrimSpace(offer.Name) == "":
		return ref, nil, invalid("name is required")
	case len(offer.Input) > daemonprotocol.ProgramToolCallMaxInputBytes:
		return ref, nil, invalid("input exceeds %d bytes", daemonprotocol.ProgramToolCallMaxInputBytes)
	case json.Unmarshal(offer.Input, &input) != nil || input == nil:
		return ref, nil, invalid("input must be a JSON object")
	}
	program, found, err := s.store.Execution().GetDaemonProgramToolScope(ctx, orgID, machineID, processID)
	if err != nil {
		return ref, nil, err
	}
	if !found {
		machine, err := s.store.Execution().GetMachine(ctx, orgID, machineID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return ref, nil, err
		case !machine.ProgrammaticToolCalls:
			return ref, nil, errProgramToolCallsDisabledOnMachine
		}
		return ref, nil, storeerr.ErrNotFound
	}
	uncallable := rejectToolCall(daemonprotocol.ErrorCodeToolCallUnavailable,
		"tool %q cannot be called from commands", offer.Name)
	if offer.Name == toolcatalog.ToolNameToolSearch {
		specs, contract, err := tools.AgentConfigToolSpecs(
			ctx, s.store, program.ProjectID, program.AgentID, program.AgentConfigID)
		if err != nil {
			return ref, nil, err
		}
		if !contract.CodeMode() {
			return ref, nil, uncallable
		}
		search, err := tools.ProgramToolSearch(modelcontext.ProgramCallableTools(specs), offer.Input)
		if err != nil {
			return ref, nil, invalid("%s", err.Error())
		}
		value, err := json.Marshal(search)
		if err != nil {
			return ref, nil, err
		}
		return ref, &daemonprotocol.ProgramToolCallResult{
			State: daemonprotocol.ProgramToolCallStateCompleted, Outcome: daemonprotocol.ProgramToolCallOutcomeSucceeded,
			Content: []daemonprotocol.ProgramToolCallContent{
				{Type: daemonprotocol.ProgramToolCallContentStructuredData, Value: value}},
		}, nil
	}
	spec, _, found, err := tools.AgentConfigToolSpec(
		ctx, s.store, program.ProjectID, program.AgentID, program.AgentConfigID, offer.Name)
	if err != nil {
		return ref, nil, err
	}
	if !found {
		return ref, nil, rejectToolCall(daemonprotocol.ErrorCodeToolCallUnavailable,
			"tool %q is not available to this agent", offer.Name)
	}
	if !modelcontext.ProgramCallable(spec) {
		return ref, nil, uncallable
	}
	record, err := s.store.Execution().CreateProgramToolCall(ctx, executionstore.CreateProgramToolCallInput{
		ProjectID: program.ProjectID, AgentID: program.AgentID, ProcessID: program.ProcessID,
		Key: key, Name: offer.Name, Type: spec.Type, Input: offer.Input,
	})
	if err != nil {
		return ref, nil, err
	}
	return executionstore.ProgramToolCallRef{ProjectID: program.ProjectID, AgentID: program.AgentID,
		ProcessID: program.ProcessID, ToolCallID: record.ID}, nil, nil
}

func (s *Server) runProgramToolCall(ref executionstore.ProgramToolCallRef) <-chan struct{} {
	done := make(chan struct{})
	if !s.programToolRuns.start() {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		defer s.programToolRuns.done()
		log := s.log.With("agent_id", ref.AgentID, "tool_call_id", ref.ToolCallID)
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Error("program tool call panicked", "panic", recovered)
			}
		}()
		ctx, cancel := context.WithCancel(s.daemonHub.ctx)
		defer cancel()
		ref.ClaimID = uuid.New()
		claim, found, err := s.store.Execution().ClaimProgramToolCall(ctx, ref, programToolCallLease)
		if err != nil || !found {
			if err != nil && ctx.Err() == nil {
				log.Error("claim program tool call", "error", err)
			}
			return
		}
		renewed := make(chan struct{})
		go func() {
			defer close(renewed)
			ticker := time.NewTicker(programToolCallLease / 3)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
				attemptCtx, cancelAttempt := context.WithTimeout(ctx, programToolCallLease/3)
				err := s.store.Execution().RenewProgramToolCallClaim(attemptCtx, ref, programToolCallLease)
				cancelAttempt()
				switch {
				case err == nil, ctx.Err() != nil:
				case programToolClaimGone(err):
					cancel()
					return
				default:
					log.Warn("renew program tool call claim", "error", err)
				}
			}
		}()
		scope := tools.NewAsyncExecutionScope(nil)
		err = s.programToolExecutor.RunProgramToolCall(tools.WithAsyncExecutionScope(ctx, scope), claim,
			programToolCallLease)
		scope.Seal()
		<-scope.Done()
		for _, err := range []error{err, scope.Err()} {
			if err != nil && !programToolClaimGone(err) && ctx.Err() == nil {
				log.Error("program tool call failed", "tool", claim.Name, "error", err)
			}
		}
		cancel()
		<-renewed
	}()
	return done
}

const programToolRunStopGrace = 10 * time.Second

type programToolRuns struct {
	mu      sync.Mutex
	closing bool
	running sync.WaitGroup
}

func (r *programToolRuns) start() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing {
		return false
	}
	r.running.Add(1)
	return true
}

func (r *programToolRuns) done() { r.running.Done() }

func (r *programToolRuns) stop(grace time.Duration) {
	r.mu.Lock()
	r.closing = true
	r.mu.Unlock()
	idle := make(chan struct{})
	go func() {
		r.running.Wait()
		close(idle)
	}()
	select {
	case <-idle:
	case <-time.After(grace):
	}
}

func programToolClaimGone(err error) bool {
	return errors.Is(err, storeerr.ErrProgramToolCallClaimLost) || errors.Is(err, storeerr.ErrProcessTerminal) ||
		errors.Is(err, storeerr.ErrRuntimeLockInactive) || errors.Is(err, context.Canceled)
}

func programToolCallResult(record executionstore.ToolCallRecord) (daemonprotocol.ProgramToolCallResult, error) {
	var result daemonprotocol.ProgramToolCallResult
	id, err := publicID(publicid.KindToolCall, record.ID)
	if err != nil {
		return result, err
	}
	result.ToolCallID = id
	if record.State == executionstore.ToolCallStateAwaitingPermission {
		result.State = daemonprotocol.ProgramToolCallStateAwaitingApproval
	}
	if record.State != executionstore.ToolCallStateCompleted {
		return result, nil
	}
	result.State = daemonprotocol.ProgramToolCallStateCompleted
	result.Outcome = string(record.Outcome)
	if len(record.ResultContentParts) == 0 {
		return result, nil
	}
	blocks, err := publicevents.ToolResultContentBlocks(record.ResultContentParts)
	if err != nil {
		return result, fmt.Errorf("decode program tool call result: %w", err)
	}
	for _, block := range blocks {
		encoded, err := block.MarshalJSON()
		if err != nil {
			return result, err
		}
		var item daemonprotocol.ProgramToolCallContent
		if err := json.Unmarshal(encoded, &item); err != nil {
			return result, fmt.Errorf("decode program tool call result: %w", err)
		}
		result.Content = append(result.Content, item)
	}
	return result, nil
}

func programToolCallRejection(s *Server, err error) toolCallRejectedError {
	var rejection toolCallRejectedError
	permanent := func(code, message string) toolCallRejectedError {
		return toolCallRejectedError{status: daemonprotocol.AckStatusPermanentReject, code: code, message: message}
	}
	switch {
	case errors.As(err, &rejection):
		return rejection
	case errors.Is(err, errProgramToolCallsDisabledOnMachine):
		return permanent(daemonprotocol.ErrorCodeToolCallUnavailable, err.Error())
	case errors.Is(err, storeerr.ErrNotFound), errors.Is(err, storeerr.ErrProcessTerminal):
		return permanent(daemonprotocol.ErrorCodeToolCallUnavailable,
			"no running process can call tools here; the process may have ended, or programmatic tool "+
				"calls may be disabled for its agent")
	case errors.Is(err, storeerr.ErrIdempotencyConflict):
		return permanent(daemonprotocol.ErrorCodeIdempotencyConflict, "key was already used for a different tool call")
	case errors.Is(err, storeerr.ErrInvalidRequest):
		return permanent(daemonprotocol.ErrorCodeValidationFailed, "tool call request is invalid")
	case errors.Is(err, storeerr.ErrProgramToolCallLimitReached):
		return toolCallRejectedError{status: daemonprotocol.AckStatusTransientError,
			code: daemonprotocol.ErrorCodeToolCallLimitReached, message: fmt.Sprintf(
				"this command already has %d tool calls in flight; wait for one to finish",
				executionstore.MaxOpenProgramToolCalls)}
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		s.log.Error("daemon program tool call", "error", err)
	}
	return toolCallRejectedError{status: daemonprotocol.AckStatusTransientError,
		code: daemonprotocol.ErrorCodeStorageUnavailable, message: "tool calls are temporarily unavailable"}
}
