package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

const publisherQueueSize = 4096
const publisherPublishTimeout = 2 * time.Second
const workerControlPublishTimeout = 500 * time.Millisecond
const publisherCloseDrainTimeout = 2 * time.Second
const publisherCoalesceLimit = 256

var errInvalidNotificationIntent = errors.New("invalid notification intent")

type RoutedPublisher struct {
	daemonWakeupPublisher     DaemonWakeupPublisher
	agentEventWakeupPublisher AgentEventWakeupPublisher
	toolCallUpdatePublisher   AgentToolCallUpdatePublisher
	workerControlPublisher    WorkerControlPublisher
	presence                  DaemonPresenceStore
	log                       *slog.Logger
	recorder                  Recorder

	publishMu  sync.RWMutex
	closing    bool
	shutdownBy time.Time

	queue     chan PostCommitIntent
	cancelRun context.CancelFunc
	closed    chan struct{}
	close     sync.Once
}

type RoutedPublisherPorts struct {
	DaemonWakeups     DaemonWakeupPublisher
	AgentEventWakeups AgentEventWakeupPublisher
	ToolCallUpdates   AgentToolCallUpdatePublisher
	WorkerControls    WorkerControlPublisher
}

func NewRoutedPublisher(
	ports RoutedPublisherPorts,
	presence DaemonPresenceStore,
	log *slog.Logger,
	recorder Recorder,
) (*RoutedPublisher, error) {
	if ports.DaemonWakeups == nil {
		return nil, errors.New("daemon wakeup publisher is required")
	}
	if ports.AgentEventWakeups == nil {
		return nil, errors.New("agent event wakeup publisher is required")
	}
	if ports.ToolCallUpdates == nil {
		return nil, errors.New("tool call update publisher is required")
	}
	if ports.WorkerControls == nil {
		return nil, errors.New("worker control publisher is required")
	}
	if presence == nil {
		return nil, errors.New("daemon presence store is required")
	}
	runCtx, cancelRun := context.WithCancel(context.Background())
	p := &RoutedPublisher{
		daemonWakeupPublisher:     ports.DaemonWakeups,
		agentEventWakeupPublisher: ports.AgentEventWakeups,
		toolCallUpdatePublisher:   ports.ToolCallUpdates,
		workerControlPublisher:    ports.WorkerControls,
		presence:                  presence,
		log:                       log,
		recorder:                  recorder,
		queue:                     make(chan PostCommitIntent, publisherQueueSize),
		cancelRun:                 cancelRun,
		closed:                    make(chan struct{}),
	}
	go p.run(runCtx)
	return p, nil
}

func (p *RoutedPublisher) PublishPostCommit(ctx context.Context, intent PostCommitIntent) {
	if p == nil || intent == nil {
		return
	}
	unlock, ok := p.beginPublish(intent)
	if !ok {
		return
	}
	defer unlock()

	if control, ok := intent.(WorkerControlCommitted); ok {
		p.publishWorkerControlWithTimeout(ctx, control, workerControlPublishTimeout)
		return
	}
	switch intent.(type) {
	case DaemonWorkCommitted,
		DaemonRuntimeEndedCommitted,
		DaemonProcessTerminationCommitted,
		AgentEventCommitted,
		ToolCallUpdatedCommitted:
	default:
		return
	}
	select {
	case p.queue <- intent:
		p.record(intent, "queued", "none")
	case <-ctx.Done():
		p.record(intent, "dropped", "context_canceled")
		if p.log != nil {
			p.log.Warn("drop notification intent after context cancellation", "error", ctx.Err())
		}
	default:
		p.record(intent, "dropped", "queue_full")
		if p.log != nil {
			p.log.Warn("drop notification intent because publisher queue is full")
		}
	}
}

func (p *RoutedPublisher) beginPublish(intent PostCommitIntent) (func(), bool) {
	p.publishMu.RLock()
	if p.closing {
		p.publishMu.RUnlock()
		p.record(intent, "dropped", "closed")
		return nil, false
	}
	return p.publishMu.RUnlock, true
}

// Close drains queued notifications until one shared deadline, then abandons
// the remainder. Process termination is the only queued intent without a
// durable recovery path.
func (p *RoutedPublisher) Close() {
	if p == nil {
		return
	}
	p.close.Do(func() {
		p.publishMu.Lock()
		p.closing = true
		p.shutdownBy = time.Now().Add(publisherCloseDrainTimeout)
		cancelRun := p.cancelRun
		p.publishMu.Unlock()
		if cancelRun != nil {
			cancelRun()
		}
	})
	if p.closed != nil {
		<-p.closed
	}
}

func (p *RoutedPublisher) run(runCtx context.Context) {
	defer close(p.closed)
	for {
		select {
		case <-runCtx.Done():
			p.drainOnClose(runCtx, p.shutdownDeadline())
			return
		default:
		}
		select {
		case <-runCtx.Done():
			p.drainOnClose(runCtx, p.shutdownDeadline())
			return
		case intent := <-p.queue:
			p.publishCoalesced(runCtx, intent)
		}
	}
}

func (p *RoutedPublisher) drainOnClose(ctx context.Context, deadline time.Time) {
	if deadline.IsZero() {
		return
	}
	shutdown, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	defer cancel()
	for {
		select {
		case <-shutdown.Done():
			return
		default:
		}
		select {
		case <-shutdown.Done():
			return
		case intent := <-p.queue:
			p.publishWithTimeout(shutdown, intent, publisherCloseDrainTimeout)
		default:
			return
		}
	}
}

func (p *RoutedPublisher) shutdownDeadline() time.Time {
	p.publishMu.RLock()
	defer p.publishMu.RUnlock()
	return p.shutdownBy
}

func (p *RoutedPublisher) publishCoalesced(ctx context.Context, first PostCommitIntent) {
	workByMachine := map[uuid.UUID]struct{}{}
	eventByAgent := map[uuid.UUID]struct{}{}
	var direct []PostCommitIntent

	accumulate := func(intent PostCommitIntent) {
		switch v := intent.(type) {
		case DaemonWorkCommitted:
			workByMachine[v.MachineID] = struct{}{}
		case AgentEventCommitted:
			eventByAgent[v.AgentID] = struct{}{}
		default:
			direct = append(direct, intent)
		}
	}

	accumulate(first)
	for i := 1; i < publisherCoalesceLimit; i++ {
		select {
		case intent := <-p.queue:
			accumulate(intent)
		default:
			p.flushCoalesced(ctx, workByMachine, eventByAgent, direct)
			return
		}
	}
	p.flushCoalesced(ctx, workByMachine, eventByAgent, direct)
}

func (p *RoutedPublisher) flushCoalesced(
	ctx context.Context,
	workByMachine, eventByAgent map[uuid.UUID]struct{},
	direct []PostCommitIntent,
) {
	for machineID := range workByMachine {
		if !p.publishDuringRun(ctx, DaemonWorkCommitted{MachineID: machineID}) {
			return
		}
	}
	for agentID := range eventByAgent {
		if !p.publishDuringRun(ctx, AgentEventCommitted{AgentID: agentID}) {
			return
		}
	}
	for _, intent := range direct {
		if !p.publishDuringRun(ctx, intent) {
			return
		}
	}
}

func (p *RoutedPublisher) publishDuringRun(ctx context.Context, intent PostCommitIntent) bool {
	if ctx.Err() == nil {
		p.publishWithTimeout(ctx, intent, publisherPublishTimeout)
		return true
	}
	deadline := p.shutdownDeadline()
	if deadline.IsZero() {
		return false
	}
	shutdown, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	defer cancel()
	if shutdown.Err() != nil {
		return false
	}
	p.publishWithTimeout(shutdown, intent, publisherPublishTimeout)
	return true
}

func (p *RoutedPublisher) publishWithTimeout(ctx context.Context, intent PostCommitIntent, timeout time.Duration) {
	if timeout <= 0 {
		p.publish(ctx, intent)
		return
	}
	publishCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	p.publish(publishCtx, intent)
}

func (p *RoutedPublisher) publish(ctx context.Context, intent PostCommitIntent) {
	switch v := intent.(type) {
	case DaemonWorkCommitted, DaemonRuntimeEndedCommitted, DaemonProcessTerminationCommitted:
		p.publishDaemon(ctx, intent)
	case AgentEventCommitted:
		p.publishAgentEvent(ctx, v)
	case ToolCallUpdatedCommitted:
		p.publishToolCallUpdate(ctx, v)
	}
}

func (p *RoutedPublisher) publishDaemon(ctx context.Context, intent PostCommitIntent) {
	replicaID, msg, ok, err := p.encodeDaemonIntent(ctx, intent)
	if err != nil {
		if errors.Is(err, errInvalidNotificationIntent) {
			p.record(intent, "skipped", "invalid_intent")
			if p.log != nil {
				p.log.Warn("skip invalid daemon notification intent", "error", err)
			}
			return
		}
		if errors.Is(err, errInvalidPresence) {
			p.record(intent, "skipped", "invalid_presence")
			if p.log != nil {
				p.log.Warn("skip daemon notification intent with invalid presence", "error", err)
			}
			return
		}
		p.record(intent, "skipped", "encode_error")
		if p.log != nil {
			p.log.Warn("encode daemon notification intent failed", "error", err)
		}
		return
	}
	if !ok {
		p.record(intent, "skipped", "presence_miss")
		return
	}
	if err := p.daemonWakeupPublisher.PublishDaemonReplicaWakeup(ctx, replicaID, msg); err != nil {
		p.record(intent, "error", "publish_failed")
		if p.log != nil {
			p.log.Warn("publish daemon notification intent failed", "replica_id", replicaID, "error", err)
		}
		return
	}
	p.record(intent, "published", "none")
}

func (p *RoutedPublisher) publishAgentEvent(ctx context.Context, event AgentEventCommitted) {
	if event.AgentID == uuid.Nil {
		p.record(event, "skipped", "invalid_intent")
		return
	}
	if err := p.agentEventWakeupPublisher.PublishAgentEventWakeup(ctx, event.AgentID); err != nil {
		p.record(event, "error", "publish_failed")
		if p.log != nil {
			p.log.Warn("publish agent-event notification intent failed", "agent_id", event.AgentID, "error", err)
		}
		return
	}
	p.record(event, "published", "none")
}

func (p *RoutedPublisher) publishToolCallUpdate(ctx context.Context, update ToolCallUpdatedCommitted) {
	if update.AgentID == uuid.Nil || update.ToolCallID == uuid.Nil || update.State == "" {
		p.record(update, "skipped", "invalid_intent")
		return
	}
	if err := p.toolCallUpdatePublisher.PublishAgentToolCallUpdate(ctx, update); err != nil {
		p.record(update, "error", "publish_failed")
		if p.log != nil {
			p.log.Warn(
				"publish tool-call update notification intent failed",
				"agent_id",
				update.AgentID,
				"tool_call_id",
				update.ToolCallID,
				"error",
				err,
			)
		}
		return
	}
	p.record(update, "published", "none")
}

func (p *RoutedPublisher) publishWorkerControlWithTimeout(
	ctx context.Context,
	intent WorkerControlCommitted,
	timeout time.Duration,
) {
	if timeout <= 0 {
		p.publishWorkerControl(ctx, intent)
		return
	}
	publishCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	p.publishWorkerControl(publishCtx, intent)
}

func (p *RoutedPublisher) publishWorkerControl(ctx context.Context, intent WorkerControlCommitted) {
	if intent.WorkerProcessID == uuid.Nil {
		p.record(intent, "skipped", "invalid_intent")
		return
	}
	if err := intent.Control.Validate(); err != nil {
		p.record(intent, "skipped", "invalid_intent")
		if p.log != nil {
			p.log.Warn("skip invalid worker-control intent", "worker_process_id", intent.WorkerProcessID, "error", err)
		}
		return
	}
	if err := p.workerControlPublisher.PublishWorkerControl(ctx, intent.WorkerProcessID, intent.Control); err != nil {
		p.record(intent, "error", "publish_failed")
		if p.log != nil {
			p.log.Warn(
				"publish worker-control intent failed",
				"worker_process_id",
				intent.WorkerProcessID,
				"kind",
				intent.Control.Kind,
				"error",
				err,
			)
		}
		return
	}
	p.record(intent, "published", "none")
}

func (p *RoutedPublisher) encodeDaemonIntent(
	ctx context.Context,
	intent PostCommitIntent,
) (uuid.UUID, WakeupMessage, bool, error) {
	var msg WakeupMessage
	var presenceID uuid.UUID
	runtimeScoped := false
	switch v := intent.(type) {
	case DaemonWorkCommitted:
		msg = WakeupMessage{Type: WakeupTypeDaemonWork, MachineID: v.MachineID}
		presenceID = v.MachineID
	case DaemonRuntimeEndedCommitted:
		msg = WakeupMessage{
			Type:            WakeupTypeDaemonRuntimeEnded,
			MachineID:       v.MachineID,
			RuntimeID:       &v.RuntimeID,
			RuntimeEndCause: v.Cause,
		}
		presenceID = v.RuntimeID
		runtimeScoped = true
	case DaemonProcessTerminationCommitted:
		msg = WakeupMessage{
			Type:       WakeupTypeDaemonProcessTerminate,
			MachineID:  v.MachineID,
			ProcessIDs: v.ProcessIDs,
		}
		presenceID = v.MachineID
	default:
		return uuid.Nil, WakeupMessage{}, false, errors.New("unsupported daemon post-commit intent")
	}
	if err := msg.Validate(); err != nil {
		return uuid.Nil, WakeupMessage{}, false, fmt.Errorf("%w: %w", errInvalidNotificationIntent, err)
	}
	var presence DaemonPresence
	var ok bool
	var err error
	if runtimeScoped {
		presence, ok, err = p.presence.GetRuntime(ctx, presenceID)
	} else {
		presence, ok, err = p.presence.Get(ctx, presenceID)
	}
	if err != nil || !ok {
		return uuid.Nil, WakeupMessage{}, false, err
	}
	if err := validateDaemonPresence(presence); err != nil {
		return uuid.Nil, WakeupMessage{}, false, err
	}
	return presence.ReplicaID, msg, true, nil
}

func (p *RoutedPublisher) record(intent PostCommitIntent, result, reason string) {
	if p == nil || p.recorder == nil {
		return
	}
	p.recorder.RecordNotification(notificationIntentLabel(intent), result, reason)
}

func notificationIntentLabel(intent PostCommitIntent) string {
	switch intent.(type) {
	case DaemonWorkCommitted:
		return "daemon_work"
	case DaemonRuntimeEndedCommitted:
		return "daemon_runtime_ended"
	case DaemonProcessTerminationCommitted:
		return "daemon_process_terminate"
	case AgentEventCommitted:
		return "agent_event"
	case ToolCallUpdatedCommitted:
		return "tool_call_update"
	case WorkerControlCommitted:
		return "worker_control"
	default:
		return "unknown"
	}
}
