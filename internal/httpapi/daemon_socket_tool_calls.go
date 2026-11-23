package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
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
	daemonToolCallLimit = 128
	// Bounds the cancels a connection holds for a free slot. Past it a cancel goes unacknowledged,
	// and the daemon sends it again on its next connection.
	daemonToolCallCancelQueueLimit = 1024
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

// WithProgramToolCapacity bounds the tool calls from commands this server runs at once, across
// every machine connection.
func WithProgramToolCapacity(capacity int) Option {
	return func(s *Server) {
		if capacity > 0 {
			s.programToolRuns.slots = make(chan struct{}, capacity)
		}
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

// servedToolCall is one key of one process, served by a single goroutine however often it is
// offered.
type servedToolCall struct {
	canceled  chan struct{}
	done      chan struct{}
	reoffered chan struct{}
	// offerID and next are guarded by the socket's workMu. next is the latest offer that arrived
	// while the key was being served.
	offerID string
	next    *daemonprotocol.ToolCallOffer
}

func newServedToolCall(offer daemonprotocol.ToolCallOffer) *servedToolCall {
	return &servedToolCall{canceled: make(chan struct{}), done: make(chan struct{}),
		reoffered: make(chan struct{}, 1), offerID: offer.OfferID}
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
	s.workMu.Lock()
	if serving, ok := s.toolCalls[id]; ok {
		// The same offer sent twice is served once.
		if serving.offerID != offer.OfferID {
			serving.next = &offer
			select {
			case serving.reoffered <- struct{}{}:
			default:
			}
		}
		s.workMu.Unlock()
		return nil
	}
	if s.toolCallsActive >= daemonToolCallLimit {
		s.workMu.Unlock()
		s.reportToolCall(ctx, offer, daemonprotocol.ToolCallReport{
			AckStatus: daemonprotocol.AckStatusTransientError, ErrorCode: daemonprotocol.ErrorCodeToolCallLimitReached,
			Error: "this machine has too many tool calls in flight; wait for one to finish"})
		return nil
	}
	call := newServedToolCall(offer)
	// The same key offered again after a cancel: let that cancel finish first, so this offer sees
	// what it left.
	canceling := s.toolCallsCanceling[id]
	s.toolCalls[id] = call
	s.toolCallsActive++
	s.toolCallsDone.Add(1)
	s.workMu.Unlock()
	go func() {
		defer s.toolCallsDone.Done()
		defer func() {
			s.workMu.Lock()
			if s.toolCalls[id] == call {
				delete(s.toolCalls, id)
			}
			if s.toolCallsCanceling[id] == call {
				delete(s.toolCallsCanceling, id)
			}
			queued := s.releaseToolCallSlotLocked()
			next := call.next
			call.next = nil
			s.workMu.Unlock()
			close(call.done)
			// An offer that arrived too late for this goroutine to answer is served afresh.
			if next != nil && ctx.Err() == nil {
				_ = s.handleToolCallOffer(ctx, daemonprotocol.Message{ToolCallOffer: next})
			}
			s.runQueuedToolCallCancel(ctx, queued)
		}()
		if canceling != nil {
			select {
			case <-canceling.done:
			case <-ctx.Done():
				return
			}
		}
		s.serveToolCall(ctx, offer, call)
	}()
	return nil
}

func (s *daemonSocket) handleToolCallCancel(ctx context.Context, msg daemonprotocol.Message) error {
	if msg.ToolCallCancel == nil {
		return errors.New("tool_call_cancel payload is required")
	}
	s.cancelToolCall(ctx, *msg.ToolCallCancel)
	return nil
}

func (s *daemonSocket) cancelToolCall(ctx context.Context, cancel daemonprotocol.ToolCallCancel) {
	id := cancel.ProcessID + "\x00" + cancel.Key
	s.workMu.Lock()
	call, serving := s.toolCalls[id]
	switch {
	case serving && call.next != nil && call.next.OfferID == cancel.OfferID:
		call.next = nil
		s.workMu.Unlock()
		s.reportToolCallCanceled(ctx, cancel)
	case serving && (cancel.OfferID == "" || cancel.OfferID == call.offerID):
		// The serving goroutine acknowledges the cancel once the call has ended.
		close(call.canceled)
		delete(s.toolCalls, id)
		s.toolCallsCanceling[id] = call
		s.workMu.Unlock()
	case serving:
		// A cancel for an offer this key no longer answers: nothing of it is left.
		s.workMu.Unlock()
		s.reportToolCallCanceled(ctx, cancel)
	case s.toolCallsActive >= daemonToolCallLimit:
		// Held until a slot frees. Unacknowledged, it is sent again on the next connection.
		s.queueToolCallCancelLocked(cancel)
		s.workMu.Unlock()
	default:
		// The offer was made on an earlier connection. The cancel still has to end the stored call.
		s.toolCallsActive++
		s.toolCallsDone.Add(1)
		s.workMu.Unlock()
		go func() {
			defer s.toolCallsDone.Done()
			s.cancelStoredToolCall(ctx, cancel)
			s.workMu.Lock()
			queued := s.releaseToolCallSlotLocked()
			s.workMu.Unlock()
			s.runQueuedToolCallCancel(ctx, queued)
		}()
	}
}

func (s *daemonSocket) queueToolCallCancelLocked(cancel daemonprotocol.ToolCallCancel) {
	if len(s.toolCallCancelsQueued) >= daemonToolCallCancelQueueLimit ||
		slices.Contains(s.toolCallCancelsQueued, cancel) {
		return
	}
	s.toolCallCancelsQueued = append(s.toolCallCancelsQueued, cancel)
}

// releaseToolCallSlotLocked frees a slot and hands back the oldest cancel waiting for one.
func (s *daemonSocket) releaseToolCallSlotLocked() *daemonprotocol.ToolCallCancel {
	s.toolCallsActive--
	if len(s.toolCallCancelsQueued) == 0 {
		return nil
	}
	cancel := s.toolCallCancelsQueued[0]
	s.toolCallCancelsQueued = slices.Delete(s.toolCallCancelsQueued, 0, 1)
	return &cancel
}

// runQueuedToolCallCancel handles a queued cancel afresh, since what its key is serving may have
// changed while it waited.
func (s *daemonSocket) runQueuedToolCallCancel(ctx context.Context, cancel *daemonprotocol.ToolCallCancel) {
	if cancel != nil && ctx.Err() == nil {
		s.cancelToolCall(ctx, *cancel)
	}
}

// cancelStoredToolCall acknowledges a cancel only once storage has ended the call, or shown there is
// nothing of it to end. A daemon that can no longer report gets no acknowledgment, so the cancel is
// sent again on a connection that can.
func (s *daemonSocket) cancelStoredToolCall(ctx context.Context, cancel daemonprotocol.ToolCallCancel) {
	server := s.server
	processID, decodeErr := publicid.Decode(publicid.KindProcess, cancel.ProcessID)
	if decodeErr != nil || !daemonprotocol.ValidProgramToolCallKey(cancel.Key) {
		// It cannot name a call, so there is nothing to end.
		s.reportToolCallCanceled(ctx, cancel)
		return
	}
	for {
		err := server.store.Execution().CancelDaemonProgramToolCallByKey(ctx, s.authority(), processID, cancel.Key)
		if err == nil {
			s.reportToolCallCanceled(ctx, cancel)
			return
		}
		if ctx.Err() != nil || errors.Is(err, storeerr.ErrDaemonRuntimeUnregistered) {
			return
		}
		server.log.Error("cancel program tool call", "process_id", cancel.ProcessID, "error", err)
		timer := time.NewTimer(programToolCallPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.done:
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *daemonSocket) reportToolCallCanceled(ctx context.Context, cancel daemonprotocol.ToolCallCancel) {
	offer := daemonprotocol.ToolCallOffer{ProcessID: cancel.ProcessID, OfferID: cancel.OfferID}
	offer.Key = cancel.Key
	s.reportToolCall(ctx, offer, daemonprotocol.ToolCallReport{CancelRecorded: true})
}

func (s *daemonSocket) reportToolCall(
	ctx context.Context, offer daemonprotocol.ToolCallOffer, report daemonprotocol.ToolCallReport,
) {
	report.ProcessID, report.Key, report.OfferID = offer.ProcessID, offer.Key, offer.OfferID
	msg := daemonprotocol.Message{Type: daemonprotocol.MessageToolCallReport, ToolCallReport: &report}
	report.Error = boundedToolCallError(report.Error)
	if encoded, err := json.Marshal(msg); err != nil || len(encoded) > daemonprotocol.MaxMessageBytes {
		// Every other field of a report is bounded, so dropping the content always makes it fit.
		report.Content = nil
		if report.State == daemonprotocol.ProgramToolCallStateCompleted {
			report.Outcome = daemonprotocol.ProgramToolCallOutcomeFailed
			report.Content = []daemonprotocol.ProgramToolCallContent{{
				Type:  daemonprotocol.ProgramToolCallContentStructuredData,
				Value: json.RawMessage(programToolCallResultTooLarge),
			}}
		}
	}
	select {
	case s.send <- daemonSocketOutbound{msg: msg}:
		s.recordSocketEvent("tool_call_report", report.State, string(report.AckStatus))
	case <-s.done:
	case <-ctx.Done():
	}
}

func boundedToolCallError(message string) string {
	if len(message) <= daemonprotocol.ProgramToolCallMaxErrorBytes {
		return message
	}
	return strings.ToValidUTF8(message[:daemonprotocol.ProgramToolCallMaxErrorBytes], "")
}

func (s *daemonSocket) rejectToolCall(ctx context.Context, offer daemonprotocol.ToolCallOffer, err error) {
	rejection := programToolCallRejection(s.server, err)
	s.reportToolCall(ctx, offer, daemonprotocol.ToolCallReport{
		AckStatus: rejection.status, ErrorCode: rejection.code, Error: rejection.message})
}

func (s *daemonSocket) nextToolCallOffer(call *servedToolCall) *daemonprotocol.ToolCallOffer {
	s.workMu.Lock()
	defer s.workMu.Unlock()
	next := call.next
	call.next = nil
	return next
}

func (s *daemonSocket) serveToolCall(
	ctx context.Context, offer daemonprotocol.ToolCallOffer, call *servedToolCall,
) {
	server := s.server
	ref, result, err := server.offerProgramToolCall(ctx, s.authority(), offer)
	if err != nil {
		s.rejectToolCall(ctx, offer, err)
		return
	}
	if result != nil {
		s.reportToolCall(ctx, offer, daemonprotocol.ToolCallReport{ProgramToolCallResult: *result})
		return
	}
	poll := programToolCallFallbackPoll
	updates := make(chan struct{}, 1)
	var watching atomic.Value
	watching.Store(ref.ToolCallID)
	if server.agentToolCallUpdateSubscriber != nil {
		subscription, err := server.agentToolCallUpdateSubscriber.SubscribeAgentToolCallUpdates(ctx, ref.AgentID,
			func(_ context.Context, update notifications.ToolCallUpdatedCommitted) {
				if update.ToolCallID != watching.Load() {
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
	// A run this goroutine started that is still waiting for capacity stops when the goroutine
	// exits, so runs waiting their turn are bounded by the calls being served.
	abandoned := make(chan struct{})
	defer close(abandoned)
	// stopRun stops a run this goroutine started once its call has been canceled. A run on another
	// server stops when it next renews its claim.
	stopRun := make(chan struct{})
	var stopOnce sync.Once
	canceled := call.canceled
	var running <-chan struct{}
	claim, awaitingApproval, awaitingResult, canceling := true, false, false, false
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
				s.rejectToolCall(ctx, offer, err)
			}
			return
		}
		select {
		case <-canceled:
			canceling, canceled = true, nil
		default:
		}
		if canceling {
			// A cancel dropped on an error would leave the call running with no waiter, so it stays
			// pending until storage records it.
			over := record.State == executionstore.ToolCallStateCompleted
			if !over {
				over, err = server.store.Execution().CancelDaemonProgramToolCall(ctx, s.authority(), ref)
				if errors.Is(err, storeerr.ErrDaemonRuntimeUnregistered) {
					// The daemon can no longer report, so its cancel goes unacknowledged. The call is
					// not this connection's to end, and keeps going.
					return
				}
				if err != nil && ctx.Err() == nil {
					server.log.Error("cancel program tool call", "tool_call_id", ref.ToolCallID, "error", err)
				}
			}
			if over {
				stopOnce.Do(func() { close(stopRun) })
				s.reportToolCallCanceled(ctx, daemonprotocol.ToolCallCancel{
					ProcessID: offer.ProcessID, Key: offer.Key, OfferID: offer.OfferID})
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
		case executionstore.ToolCallStateReady:
			if record.Type == toolcatalog.ToolTypeCustom {
				// Nothing here runs a custom tool: its runner submits the result, which completes the call.
				if !awaitingResult {
					awaitingResult = true
					s.reportToolCall(ctx, offer, daemonprotocol.ToolCallReport{ProgramToolCallResult: report})
				}
				break
			}
			fallthrough
		case executionstore.ToolCallStateAwaitingAuthorization:
			if claim && running == nil && !canceling {
				//nolint:contextcheck // the run outlives this socket's context
				running = server.runProgramToolCall(ref, abandoned, stopRun)
			}
		case executionstore.ToolCallStateWaiting, executionstore.ToolCallStateRunning:
			// A call another run started is never run again: if that run stops before it completes,
			// the reaper ends the call as outcome_unknown.
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
		case <-call.reoffered:
			timer.Stop()
			next := s.nextToolCallOffer(call)
			if next == nil {
				continue
			}
			// Admit the new offer against the stored call and answer it under its own offer ID.
			nextRef, result, err := server.offerProgramToolCall(ctx, s.authority(), *next)
			switch {
			case err != nil:
				s.rejectToolCall(ctx, *next, err)
			case result != nil:
				s.reportToolCall(ctx, *next, daemonprotocol.ToolCallReport{ProgramToolCallResult: *result})
			default:
				s.workMu.Lock()
				call.offerID = next.OfferID
				s.workMu.Unlock()
				offer, awaitingApproval, awaitingResult = *next, false, false
				if nextRef.ToolCallID != ref.ToolCallID {
					ref = nextRef
					watching.Store(ref.ToolCallID)
					running, claim = nil, true
				}
			}
		case <-timer.C:
		}
	}
}

var (
	errProgramToolCallsUnavailable = errors.New("programmatic tool calls are unavailable")
	errDaemonRuntimeNotOnline      = errors.New("daemon runtime is not online")
)

func (s *Server) offerProgramToolCall(
	ctx context.Context, authority executionstore.DaemonRuntimeAuthority, offer daemonprotocol.ToolCallOffer,
) (executionstore.ProgramToolCallRef, *daemonprotocol.ProgramToolCallResult, error) {
	var ref executionstore.ProgramToolCallRef
	invalid := func(format string, args ...any) error {
		return rejectToolCall(daemonprotocol.ErrorCodeValidationFailed, format, args...)
	}
	if s.programToolExecutor == nil {
		return ref, nil, errProgramToolCallsUnavailable
	}
	processID, err := publicid.Decode(publicid.KindProcess, offer.ProcessID)
	if err != nil {
		return ref, nil, storeerr.ErrNotFound
	}
	key := offer.Key
	var input map[string]json.RawMessage
	switch {
	case !daemonprotocol.ValidProgramToolCallKey(key):
		return ref, nil, invalid("key must be 1 to %d printable characters with no spaces",
			daemonprotocol.ProgramToolCallMaxKeyBytes)
	case strings.TrimSpace(offer.Name) == "":
		return ref, nil, invalid("name is required")
	case len(offer.Name) > daemonprotocol.ProgramToolCallMaxNameBytes:
		return ref, nil, invalid("name exceeds %d bytes", daemonprotocol.ProgramToolCallMaxNameBytes)
	case len(offer.Input) > daemonprotocol.ProgramToolCallMaxInputBytes:
		return ref, nil, invalid("input exceeds %d bytes", daemonprotocol.ProgramToolCallMaxInputBytes)
	case json.Unmarshal(offer.Input, &input) != nil || input == nil:
		return ref, nil, invalid("input must be a JSON object")
	}
	// The socket was authenticated when it connected. Its runtime can have ended and its token been
	// revoked since, and neither closes the socket, so every offer checks them again.
	online, err := s.store.Execution().OnlineDaemonRuntimeExists(ctx, authority)
	if err != nil {
		return ref, nil, err
	}
	if !online {
		return ref, nil, errDaemonRuntimeNotOnline
	}
	program, found, err := s.store.Execution().GetDaemonProgramToolScope(
		ctx, authority.OrgID, authority.MachineID, processID)
	if err != nil {
		return ref, nil, err
	}
	if !found {
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
		if !contract.CodeModeEnabled() {
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
	// A key that already names a call returns it before the checks for starting a new one. Whoever
	// runs it checks the agent's current config again, so it always reaches a result.
	create := executionstore.CreateProgramToolCallInput{
		ProjectID: program.ProjectID, AgentID: program.AgentID, ProcessID: program.ProcessID,
		Key: key, Named: offer.KeepOnDisconnect, Name: offer.Name, Input: offer.Input,
	}
	existing, resumed, err := s.store.Execution().ResumeProgramToolCall(ctx, create)
	if err != nil {
		return ref, nil, err
	}
	if resumed {
		return executionstore.ProgramToolCallRef{ProjectID: program.ProjectID, AgentID: program.AgentID,
			ProcessID: existing.ProcessID, ToolCallID: existing.ID}, nil, nil
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
	create.Type = spec.Type
	record, err := s.store.Execution().CreateProgramToolCall(ctx, create)
	if err != nil {
		return ref, nil, err
	}
	return executionstore.ProgramToolCallRef{ProjectID: program.ProjectID, AgentID: program.AgentID,
		ProcessID: record.ProcessID, ToolCallID: record.ID}, nil, nil
}

// abandoned stops a run still waiting for capacity. stop ends a canceled run at any point.
func (s *Server) runProgramToolCall(
	ref executionstore.ProgramToolCallRef, abandoned, stop <-chan struct{},
) <-chan struct{} {
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
		go func() {
			select {
			case <-stop:
				cancel()
			case <-ctx.Done():
			}
		}()
		if !s.programToolRuns.acquire(ctx, abandoned) {
			return
		}
		defer s.programToolRuns.release()
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
	// slots is nil when the server runs calls without a shared bound.
	slots chan struct{}
}

func (r *programToolRuns) acquire(ctx context.Context, abandoned <-chan struct{}) bool {
	if r.slots == nil {
		return true
	}
	select {
	case r.slots <- struct{}{}:
		return true
	case <-abandoned:
		return false
	case <-ctx.Done():
		return false
	}
}

func (r *programToolRuns) release() {
	if r.slots != nil {
		<-r.slots
	}
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
	switch {
	case record.State == executionstore.ToolCallStateAwaitingPermission:
		result.State = daemonprotocol.ProgramToolCallStateAwaitingApproval
	case record.State == executionstore.ToolCallStateReady && record.Type == toolcatalog.ToolTypeCustom:
		result.State = daemonprotocol.ProgramToolCallStateAwaitingResult
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
	case errors.Is(err, errProgramToolCallsUnavailable):
		return permanent(daemonprotocol.ErrorCodeToolCallUnavailable, err.Error())
	case errors.Is(err, errDaemonRuntimeNotOnline):
		return permanent(daemonprotocol.ErrorCodeToolCallUnavailable,
			"this machine's daemon is no longer authorized to call tools; restart it or reconnect it")
	case errors.Is(err, storeerr.ErrNotFound), errors.Is(err, storeerr.ErrProcessTerminal):
		return permanent(daemonprotocol.ErrorCodeToolCallUnavailable,
			"no running process can call tools here; the process may have ended, or programmatic tool "+
				"calls may be disabled for its agent")
	case errors.Is(err, storeerr.ErrIdempotencyConflict):
		return permanent(daemonprotocol.ErrorCodeIdempotencyConflict,
			"idempotency key was already used for a different tool or input")
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
