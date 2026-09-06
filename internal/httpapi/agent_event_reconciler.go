package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

const (
	defaultAgentEventReconciliationInterval  = 10 * time.Second
	defaultAgentEventReconciliationBatchSize = 1000
	agentEventReconciliationQueryTimeout     = 5 * time.Second
)

type agentEventFrontierReader interface {
	ListAgentEventFrontiers(
		context.Context,
		[]executionstore.ID,
	) ([]executionstore.AgentEventFrontier, error)
}

type agentEventStreamReconciler struct {
	log       *slog.Logger
	reader    agentEventFrontierReader
	timer     clock.Clock
	interval  time.Duration
	batchSize int

	ctx    context.Context //nolint:containedctx // the reconciler owns this process-lifecycle context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	started bool
	closed  bool
	streams map[executionstore.ID]map[*agentEventStreamRegistration]struct{}
}

type agentEventStreamRegistration struct {
	reconciler *agentEventStreamReconciler
	agentID    executionstore.ID
	notify     chan<- struct{}
	cursor     atomic.Int64
	once       sync.Once
}

func newAgentEventStreamReconciler(
	log *slog.Logger,
	reader agentEventFrontierReader,
	timer clock.Clock,
	interval time.Duration,
	batchSize int,
) *agentEventStreamReconciler {
	if interval <= 0 {
		interval = defaultAgentEventReconciliationInterval
	}
	if batchSize <= 0 {
		batchSize = defaultAgentEventReconciliationBatchSize
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &agentEventStreamReconciler{
		log:       log,
		reader:    reader,
		timer:     timer,
		interval:  interval,
		batchSize: batchSize,
		ctx:       ctx,
		cancel:    cancel,
		streams:   make(map[executionstore.ID]map[*agentEventStreamRegistration]struct{}),
	}
}

func (r *agentEventStreamReconciler) register(
	agentID executionstore.ID,
	cursor int64,
	notify chan<- struct{},
) (*agentEventStreamRegistration, bool) {
	registration := &agentEventStreamRegistration{
		reconciler: r,
		agentID:    agentID,
		notify:     notify,
	}
	registration.cursor.Store(cursor)

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, false
	}
	registrations := r.streams[agentID]
	if registrations == nil {
		registrations = make(map[*agentEventStreamRegistration]struct{})
		r.streams[agentID] = registrations
	}
	registrations[registration] = struct{}{}
	if !r.started {
		r.started = true
		r.wg.Add(1)
		go r.run()
	}
	return registration, true
}

func (r *agentEventStreamReconciler) run() {
	defer r.wg.Done()
	ticker := r.timer.Ticker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			if err := r.reconcile(r.ctx); err != nil &&
				!errors.Is(err, context.Canceled) && r.log != nil {
				r.log.Warn("agent event stream reconciliation failed", "error", err)
			}
		}
	}
}

func (r *agentEventStreamReconciler) reconcile(ctx context.Context) error {
	agentIDs := r.activeAgentIDs()
	if len(agentIDs) == 0 {
		return nil
	}
	queryCtx, cancel := context.WithTimeout(ctx, agentEventReconciliationQueryTimeout)
	defer cancel()
	for start := 0; start < len(agentIDs); start += r.batchSize {
		end := min(start+r.batchSize, len(agentIDs))
		frontiers, err := r.reader.ListAgentEventFrontiers(queryCtx, agentIDs[start:end])
		if err != nil {
			return err
		}
		r.signalBehindStreams(frontiers)
	}
	return nil
}

func (r *agentEventStreamReconciler) activeAgentIDs() []executionstore.ID {
	r.mu.Lock()
	defer r.mu.Unlock()
	agentIDs := make([]executionstore.ID, 0, len(r.streams))
	for agentID := range r.streams {
		agentIDs = append(agentIDs, agentID)
	}
	return agentIDs
}

func (r *agentEventStreamReconciler) signalBehindStreams(
	frontiers []executionstore.AgentEventFrontier,
) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, frontier := range frontiers {
		for registration := range r.streams[frontier.AgentID] {
			if frontier.EventSequence <= registration.cursor.Load() {
				continue
			}
			select {
			case registration.notify <- struct{}{}:
			default:
			}
		}
	}
}

func (r *agentEventStreamReconciler) close() {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		r.cancel()
	}
	r.mu.Unlock()
	r.wg.Wait()
}

func (r *agentEventStreamRegistration) advance(cursor int64) {
	for {
		current := r.cursor.Load()
		if cursor <= current || r.cursor.CompareAndSwap(current, cursor) {
			return
		}
	}
}

func (r *agentEventStreamRegistration) closeContext() context.Context {
	return r.reconciler.ctx
}

func (r *agentEventStreamRegistration) unregister() {
	r.once.Do(func() {
		reconciler := r.reconciler
		reconciler.mu.Lock()
		defer reconciler.mu.Unlock()
		registrations := reconciler.streams[r.agentID]
		delete(registrations, r)
		if len(registrations) == 0 {
			delete(reconciler.streams, r.agentID)
		}
	})
}
