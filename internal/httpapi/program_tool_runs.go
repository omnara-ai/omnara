package httpapi

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	defaultProgramToolCapacity     = 64
	defaultProgramToolPendingLimit = 1024
	programToolRecoveryInterval    = 10 * time.Second
	programToolRecoveryBatch       = 64
)

type programToolRun struct {
	done chan struct{}
	stop context.CancelFunc
}

var stoppedProgramToolRun = func() *programToolRun {
	run := &programToolRun{done: make(chan struct{}), stop: func() {}}
	close(run.done)
	return run
}()

type programToolRuns struct {
	mu      sync.Mutex
	closing bool
	running sync.WaitGroup
	active  map[uuid.UUID]*programToolRun
	slots   chan struct{}
	pending chan struct{}
}

func (r *programToolRuns) admit() bool {
	if r.pending == nil {
		return true
	}
	select {
	case r.pending <- struct{}{}:
		return true
	default:
		return false
	}
}

func (r *programToolRuns) leave() {
	if r.pending != nil {
		<-r.pending
	}
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

func (r *programToolRuns) tryAcquire() bool {
	if r.slots == nil {
		return true
	}
	select {
	case r.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (r *programToolRuns) release() {
	if r.slots != nil {
		<-r.slots
	}
}

func (r *programToolRuns) free(limit int) int {
	if r.slots == nil {
		return limit
	}
	return min(limit, cap(r.slots)-len(r.slots))
}

func (r *programToolRuns) begin(parent context.Context, id uuid.UUID) (*programToolRun, context.Context, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing {
		return nil, nil, false
	}
	if run, ok := r.active[id]; ok {
		return run, nil, false
	}
	ctx, cancel := context.WithCancel(parent)
	run := &programToolRun{done: make(chan struct{}), stop: cancel}
	if r.active == nil {
		r.active = map[uuid.UUID]*programToolRun{}
	}
	r.active[id] = run
	r.running.Add(1)
	return run, ctx, true
}

func (r *programToolRuns) end(id uuid.UUID, run *programToolRun) {
	r.mu.Lock()
	if r.active[id] == run {
		delete(r.active, id)
	}
	r.mu.Unlock()
	run.stop()
	close(run.done)
	r.running.Done()
}

func (r *programToolRuns) runs(id uuid.UUID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.active[id]
	return ok
}

func (r *programToolRuns) stop() {
	r.mu.Lock()
	r.closing = true
	r.mu.Unlock()
	r.running.Wait()
}

func (s *Server) runProgramToolCall(ref executionstore.ProgramToolCallRef, abandoned <-chan struct{}) *programToolRun {
	run, _ := s.startProgramToolRun(ref, abandoned, false)
	return run
}

func (s *Server) recoverProgramToolCall(ref executionstore.ProgramToolCallRef) bool {
	if s.programToolRuns.runs(ref.ToolCallID) {
		return true
	}
	if !s.programToolRuns.tryAcquire() {
		return false
	}
	if _, started := s.startProgramToolRun(ref, nil, true); !started {
		s.programToolRuns.release()
	}
	return true
}

func (s *Server) startProgramToolRun(
	ref executionstore.ProgramToolCallRef, abandoned <-chan struct{}, acquired bool,
) (*programToolRun, bool) {
	run, ctx, started := s.programToolRuns.begin(s.daemonHub.ctx, ref.ToolCallID)
	if run == nil {
		return stoppedProgramToolRun, false
	}
	if !started {
		return run, false
	}
	go func() {
		defer s.programToolRuns.end(ref.ToolCallID, run)
		log := s.log.With("agent_id", ref.AgentID, "tool_call_id", ref.ToolCallID)
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Error("program tool call panicked", "panic", recovered)
			}
		}()
		if !acquired && !s.programToolRuns.acquire(ctx, abandoned) {
			return
		}
		defer s.programToolRuns.release()
		s.executeProgramToolCall(ctx, run.stop, ref)
	}()
	return run, true
}

func (s *Server) executeProgramToolCall(
	ctx context.Context, cancel context.CancelFunc, ref executionstore.ProgramToolCallRef,
) {
	log := s.log.With("agent_id", ref.AgentID, "tool_call_id", ref.ToolCallID)
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
}

type programToolDriver struct {
	server   *Server
	interval time.Duration
	wakeups  chan struct{}
	cancel   context.CancelFunc
	done     chan struct{}
}

func (s *Server) startProgramToolDriver() {
	interval := s.programToolRecoveryInterval
	if interval <= 0 {
		interval = programToolRecoveryInterval
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &programToolDriver{server: s, interval: interval, wakeups: make(chan struct{}, 1), cancel: cancel,
		done: make(chan struct{})}
	s.programToolDriver = d
	go d.loop(ctx)
}

func (d *programToolDriver) wake() {
	if d == nil {
		return
	}
	select {
	case d.wakeups <- struct{}{}:
	default:
	}
}

func (d *programToolDriver) close() {
	if d == nil {
		return
	}
	d.cancel()
	<-d.done
}

func (d *programToolDriver) loop(ctx context.Context) {
	defer close(d.done)
	for {
		d.recover(ctx)
		timer := d.server.timer.Timer(d.interval + rand.N(d.interval/4+1))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-d.wakeups:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (d *programToolDriver) recover(ctx context.Context) {
	s := d.server
	limit := s.programToolRuns.free(programToolRecoveryBatch)
	if limit == 0 {
		return
	}
	refs, err := s.store.Execution().ListRunnableProgramToolCalls(ctx, limit)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("list runnable program tool calls", "error", err)
		}
		return
	}
	for _, ref := range refs {
		//nolint:contextcheck // the run outlives the poll
		if !s.recoverProgramToolCall(ref) {
			return
		}
	}
}
