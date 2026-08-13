package notifications

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakePresenceStore struct {
	records        map[uuid.UUID]DaemonPresence
	runtimeRecords map[uuid.UUID]DaemonPresence
	err            error
}

func (s fakePresenceStore) PutIfRuntime(context.Context, uuid.UUID, DaemonPresence, time.Duration) error {
	return nil
}

func (s fakePresenceStore) PutIfMissing(context.Context, uuid.UUID, DaemonPresence, time.Duration) error {
	return nil
}

func (s fakePresenceStore) Refresh(context.Context, uuid.UUID, PresenceOwner, time.Duration) error {
	return nil
}

func (s fakePresenceStore) Get(_ context.Context, machineID uuid.UUID) (DaemonPresence, bool, error) {
	if s.err != nil {
		return DaemonPresence{}, false, s.err
	}
	presence, ok := s.records[machineID]
	return presence, ok, nil
}

func (s fakePresenceStore) PutRuntime(context.Context, uuid.UUID, DaemonPresence, time.Duration) error {
	return nil
}

func (s fakePresenceStore) PutRuntimeIfMissing(context.Context, uuid.UUID, DaemonPresence, time.Duration) error {
	return nil
}

func (s fakePresenceStore) RefreshRuntime(context.Context, uuid.UUID, PresenceOwner, time.Duration) error {
	return nil
}

func (s fakePresenceStore) GetRuntime(_ context.Context, runtimeID uuid.UUID) (DaemonPresence, bool, error) {
	if s.err != nil {
		return DaemonPresence{}, false, s.err
	}
	presence, ok := s.runtimeRecords[runtimeID]
	return presence, ok, nil
}

func (s fakePresenceStore) DeleteIfOwned(context.Context, uuid.UUID, PresenceOwner) error {
	return nil
}

func (s fakePresenceStore) DeleteRuntimeIfOwned(context.Context, uuid.UUID, PresenceOwner) error {
	return nil
}

type fakeBus struct {
	mu               sync.Mutex
	daemonPublished  []WakeupMessage
	agentPublished   []uuid.UUID
	workerPublished  []WorkerControlCommitted
	toolCallUpdates  []ToolCallUpdatedCommitted
	daemonPublishErr error
	agentPublishErr  error
	workerPublishErr error
	toolCallError    error
}

type closeAwareAgentWakeupPublisher struct {
	entered           chan struct{}
	firstCanceled     chan struct{}
	mu                sync.Mutex
	calls             int
	shutdownDeadlines []time.Time
}

type blockingWorkerControlPublisher struct {
	entered  chan struct{}
	canceled chan struct{}
}

type deadlineRecordingAgentWakeupPublisher struct {
	deadlines []time.Time
}

func (p *deadlineRecordingAgentWakeupPublisher) PublishAgentEventWakeup(
	ctx context.Context,
	_ uuid.UUID,
) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return errors.New("publish context has no deadline")
	}
	p.deadlines = append(p.deadlines, deadline)
	return nil
}

func newBlockingWorkerControlPublisher() *blockingWorkerControlPublisher {
	return &blockingWorkerControlPublisher{
		entered:  make(chan struct{}),
		canceled: make(chan struct{}),
	}
}

func (p *blockingWorkerControlPublisher) PublishWorkerControl(
	ctx context.Context,
	_ uuid.UUID,
	_ WorkerControl,
) error {
	close(p.entered)
	<-ctx.Done()
	close(p.canceled)
	return ctx.Err()
}

func newCloseAwareAgentWakeupPublisher() *closeAwareAgentWakeupPublisher {
	return &closeAwareAgentWakeupPublisher{
		entered:       make(chan struct{}),
		firstCanceled: make(chan struct{}),
	}
}

func (p *closeAwareAgentWakeupPublisher) PublishAgentEventWakeup(ctx context.Context, _ uuid.UUID) error {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	if call == 1 {
		close(p.entered)
		<-ctx.Done()
		close(p.firstCanceled)
		return ctx.Err()
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return errors.New("publish context has no deadline")
	}
	p.mu.Lock()
	p.shutdownDeadlines = append(p.shutdownDeadlines, deadline)
	p.mu.Unlock()
	return nil
}

func (p *closeAwareAgentWakeupPublisher) deadlines() []time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]time.Time(nil), p.shutdownDeadlines...)
}

func (b *fakeBus) PublishDaemonReplicaWakeup(_ context.Context, _ uuid.UUID, msg WakeupMessage) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.daemonPublishErr != nil {
		return b.daemonPublishErr
	}
	b.daemonPublished = append(b.daemonPublished, msg)
	return nil
}

func (b *fakeBus) SubscribeDaemonReplicaWakeups(
	context.Context,
	uuid.UUID,
	func(context.Context, WakeupMessage),
) (Subscription, error) {
	return nil, errors.New("not implemented")
}

func (b *fakeBus) PublishAgentEventWakeup(_ context.Context, agentID uuid.UUID) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.agentPublishErr != nil {
		return b.agentPublishErr
	}
	b.agentPublished = append(b.agentPublished, agentID)
	return nil
}

func (b *fakeBus) PublishAgentToolCallUpdate(_ context.Context, update ToolCallUpdatedCommitted) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.toolCallError != nil {
		return b.toolCallError
	}
	b.toolCallUpdates = append(b.toolCallUpdates, update)
	return nil
}

func (b *fakeBus) PublishWorkerControl(_ context.Context, workerID uuid.UUID, msg WorkerControl) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.workerPublishErr != nil {
		return b.workerPublishErr
	}
	b.workerPublished = append(b.workerPublished, WorkerControlCommitted{WorkerProcessID: workerID, Control: msg})
	return nil
}

func (b *fakeBus) SubscribeDaemonReplicaInbox(
	context.Context,
	uuid.UUID,
	func(context.Context, []byte),
) (Subscription, error) {
	return nil, errors.New("not implemented")
}

func (b *fakeBus) daemonCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.daemonPublished)
}

func (b *fakeBus) agentSnapshot() []uuid.UUID {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]uuid.UUID, len(b.agentPublished))
	copy(out, b.agentPublished)
	return out
}

func (b *fakeBus) workerSnapshot() []WorkerControlCommitted {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]WorkerControlCommitted, len(b.workerPublished))
	copy(out, b.workerPublished)
	return out
}

func (b *fakeBus) toolCallSnapshot() []ToolCallUpdatedCommitted {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]ToolCallUpdatedCommitted(nil), b.toolCallUpdates...)
}

func newTestRoutedPublisher(
	t *testing.T,
	ports RoutedPublisherPorts,
	presence DaemonPresenceStore,
	recorder Recorder,
) *RoutedPublisher {
	t.Helper()
	fallback := &fakeBus{}
	if ports.DaemonWakeups == nil {
		ports.DaemonWakeups = fallback
	}
	if ports.AgentEventWakeups == nil {
		ports.AgentEventWakeups = fallback
	}
	if ports.ToolCallUpdates == nil {
		ports.ToolCallUpdates = fallback
	}
	if ports.WorkerControls == nil {
		ports.WorkerControls = fallback
	}
	if presence == nil {
		presence = fakePresenceStore{}
	}
	publisher, err := NewRoutedPublisher(ports, presence, nil, recorder)
	if err != nil {
		t.Fatalf("create routed publisher: %v", err)
	}
	t.Cleanup(publisher.Close)
	return publisher
}

func TestNewRoutedPublisherRejectsMissingDependencies(t *testing.T) {
	bus := &fakeBus{}
	validPorts := RoutedPublisherPorts{
		DaemonWakeups:     bus,
		AgentEventWakeups: bus,
		ToolCallUpdates:   bus,
		WorkerControls:    bus,
	}
	for _, tc := range []struct {
		name     string
		ports    RoutedPublisherPorts
		presence DaemonPresenceStore
	}{
		{
			name:     "daemon wakeups",
			ports:    RoutedPublisherPorts{AgentEventWakeups: bus, ToolCallUpdates: bus, WorkerControls: bus},
			presence: fakePresenceStore{},
		},
		{
			name:     "agent event wakeups",
			ports:    RoutedPublisherPorts{DaemonWakeups: bus, ToolCallUpdates: bus, WorkerControls: bus},
			presence: fakePresenceStore{},
		},
		{
			name:     "worker controls",
			ports:    RoutedPublisherPorts{DaemonWakeups: bus, AgentEventWakeups: bus, ToolCallUpdates: bus},
			presence: fakePresenceStore{},
		},
		{
			name:     "tool call updates",
			ports:    RoutedPublisherPorts{DaemonWakeups: bus, AgentEventWakeups: bus, WorkerControls: bus},
			presence: fakePresenceStore{},
		},
		{name: "presence", ports: validPorts},
	} {
		t.Run(tc.name, func(t *testing.T) {
			publisher, err := NewRoutedPublisher(tc.ports, tc.presence, nil, nil)
			if err == nil {
				publisher.Close()
				t.Fatal("NewRoutedPublisher accepted missing dependency")
			}
		})
	}
}

func TestNotificationChannelsUseCanonicalUUIDs(t *testing.T) {
	id := uuid.MustParse("00112233-4455-6677-8899-aabbccddeeff")
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{
			name: "daemon replica",
			got:  daemonReplicaWakeupChannel(id),
			want: "omnara:wakeup:api:00112233-4455-6677-8899-aabbccddeeff",
		},
		{
			name: "agent event",
			got:  agentEventWakeupChannel(id),
			want: "omnara:agent_event_wakeups:00112233-4455-6677-8899-aabbccddeeff",
		},
		{
			name: "agent stream delta",
			got:  agentStreamDeltaChannel(id),
			want: "omnara:agent_stream_deltas:00112233-4455-6677-8899-aabbccddeeff",
		},
		{
			name: "agent tool call update",
			got:  agentToolCallUpdateChannel(id),
			want: "omnara:agent_tool_call_updates:00112233-4455-6677-8899-aabbccddeeff",
		},
		{
			name: "worker control",
			got:  workerControlChannel(id),
			want: "omnara:worker_control:00112233-4455-6677-8899-aabbccddeeff",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("channel = %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestWakeupMessageValidate(t *testing.T) {
	machineID := uuid.New()
	runtimeID := uuid.New()
	processID := uuid.New()
	for _, tc := range []struct {
		name    string
		message WakeupMessage
		wantErr bool
	}{
		{name: "daemon work", message: WakeupMessage{Type: WakeupTypeDaemonWork, MachineID: machineID}},
		{
			name: "runtime ended",
			message: WakeupMessage{
				Type:            WakeupTypeDaemonRuntimeEnded,
				MachineID:       machineID,
				RuntimeID:       &runtimeID,
				RuntimeEndCause: DaemonRuntimeEndReconnect,
			},
		},
		{
			name: "process terminate",
			message: WakeupMessage{
				Type:       WakeupTypeDaemonProcessTerminate,
				MachineID:  machineID,
				ProcessIDs: []uuid.UUID{processID},
			},
		},
		{name: "missing machine", message: WakeupMessage{Type: WakeupTypeDaemonWork}, wantErr: true},
		{name: "missing type", message: WakeupMessage{MachineID: machineID}, wantErr: true},
		{
			name:    "missing runtime",
			message: WakeupMessage{Type: WakeupTypeDaemonRuntimeEnded, MachineID: machineID},
			wantErr: true,
		},
		{
			name:    "missing processes",
			message: WakeupMessage{Type: WakeupTypeDaemonProcessTerminate, MachineID: machineID},
			wantErr: true,
		},
		{
			name: "work with runtime",
			message: WakeupMessage{
				Type:      WakeupTypeDaemonWork,
				MachineID: machineID,
				RuntimeID: &runtimeID,
			},
			wantErr: true,
		},
		{
			name: "runtime with processes",
			message: WakeupMessage{
				Type:            WakeupTypeDaemonRuntimeEnded,
				MachineID:       machineID,
				RuntimeID:       &runtimeID,
				RuntimeEndCause: DaemonRuntimeEndReconnect,
				ProcessIDs:      []uuid.UUID{processID},
			},
			wantErr: true,
		},
		{
			name: "processes with runtime",
			message: WakeupMessage{
				Type:       WakeupTypeDaemonProcessTerminate,
				MachineID:  machineID,
				RuntimeID:  &runtimeID,
				ProcessIDs: []uuid.UUID{processID},
			},
			wantErr: true,
		},
		{
			name: "nil process id",
			message: WakeupMessage{
				Type:       WakeupTypeDaemonProcessTerminate,
				MachineID:  machineID,
				ProcessIDs: []uuid.UUID{uuid.Nil},
			},
			wantErr: true,
		},
		{name: "unknown type", message: WakeupMessage{Type: "unknown", MachineID: machineID}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.message.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

type recordingRecorder struct {
	mu      sync.Mutex
	entries []recordingEntry
}

type recordingEntry struct {
	Intent string
	Result string
	Reason string
}

func (r *recordingRecorder) RecordNotification(intent, result, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, recordingEntry{Intent: intent, Result: result, Reason: reason})
}

func (r *recordingRecorder) snapshot() []recordingEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordingEntry, len(r.entries))
	copy(out, r.entries)
	return out
}

func TestRoutedPublisherCoalescesDaemonWorkIntents(t *testing.T) {
	machineA := uuid.New()
	machineB := uuid.New()
	bus := &fakeBus{}
	publisher := &RoutedPublisher{
		daemonWakeupPublisher:     bus,
		agentEventWakeupPublisher: bus,
		presence: fakePresenceStore{records: map[uuid.UUID]DaemonPresence{
			machineA: {
				PresenceOwner: PresenceOwner{ReplicaID: uuid.New(), RuntimeID: uuid.New(), ConnectionID: uuid.New()},
			},
			machineB: {
				PresenceOwner: PresenceOwner{ReplicaID: uuid.New(), RuntimeID: uuid.New(), ConnectionID: uuid.New()},
			},
		}},
		queue: make(chan PostCommitIntent, 4),
	}
	publisher.queue <- DaemonWorkCommitted{MachineID: machineA}
	publisher.queue <- DaemonWorkCommitted{MachineID: machineB}
	publisher.queue <- DaemonWorkCommitted{MachineID: machineA}

	publisher.publishCoalesced(context.Background(), DaemonWorkCommitted{MachineID: machineA})

	if got := bus.daemonCount(); got != 2 {
		t.Fatalf("published daemon work wakeups = %d, want one per machine", got)
	}
}

func TestRoutedPublisherCoalescesAgentEventIntents(t *testing.T) {
	agentA := uuid.New()
	agentB := uuid.New()
	bus := &fakeBus{}
	publisher := &RoutedPublisher{
		daemonWakeupPublisher:     bus,
		agentEventWakeupPublisher: bus,
		queue:                     make(chan PostCommitIntent, 8),
	}
	publisher.queue <- AgentEventCommitted{AgentID: agentA}
	publisher.queue <- AgentEventCommitted{AgentID: agentB}
	publisher.queue <- AgentEventCommitted{AgentID: agentA}
	publisher.queue <- AgentEventCommitted{AgentID: agentA}

	publisher.publishCoalesced(context.Background(), AgentEventCommitted{AgentID: agentA})

	got := bus.agentSnapshot()
	if len(got) != 2 {
		t.Fatalf("published agent-event wakeups = %d, want one per agent", len(got))
	}
	seen := map[uuid.UUID]struct{}{}
	for _, id := range got {
		seen[id] = struct{}{}
	}
	if _, ok := seen[agentA]; !ok {
		t.Fatalf("missing publish for agentA")
	}
	if _, ok := seen[agentB]; !ok {
		t.Fatalf("missing publish for agentB")
	}
}

func TestRoutedPublisherPublishesWorkerControlDirectly(t *testing.T) {
	workerID := uuid.New()
	agentID := uuid.New()
	runtimeID := uuid.New()
	bus := &fakeBus{}
	publisher := newTestRoutedPublisher(t, RoutedPublisherPorts{WorkerControls: bus}, nil, nil)

	publisher.PublishPostCommit(context.Background(), WorkerControlCommitted{
		WorkerProcessID: workerID,
		Control:         NewWorkerControlCancel(agentID, runtimeID),
	})

	got := bus.workerSnapshot()
	if len(got) != 1 {
		t.Fatalf("worker-control publishes = %d, want 1", len(got))
	}
	if got[0].WorkerProcessID != workerID ||
		got[0].Control.Kind != WorkerControlKindCancel ||
		got[0].Control.Cancel == nil ||
		got[0].Control.Cancel.AgentID != agentID ||
		got[0].Control.Cancel.RuntimeLockID != runtimeID {
		t.Fatalf("worker-control publish = %+v, want worker/runtime cancel", got[0])
	}
	if len(publisher.queue) != 0 {
		t.Fatalf("worker-control intent should bypass queue, queue length = %d", len(publisher.queue))
	}
}

func TestRoutedPublisherPreservesToolCallUpdates(t *testing.T) {
	agentID := uuid.New()
	toolCallID := uuid.New()
	bus := &fakeBus{}
	publisher := newTestRoutedPublisher(t, RoutedPublisherPorts{ToolCallUpdates: bus}, nil, nil)

	publisher.PublishPostCommit(context.Background(), ToolCallUpdatedCommitted{
		AgentID:    agentID,
		ToolCallID: toolCallID,
		State:      "awaiting_authorization",
	})
	publisher.PublishPostCommit(context.Background(), ToolCallUpdatedCommitted{
		AgentID:    agentID,
		ToolCallID: toolCallID,
		State:      "ready",
	})
	publisher.Close()

	want := []ToolCallUpdatedCommitted{
		{AgentID: agentID, ToolCallID: toolCallID, State: "awaiting_authorization"},
		{AgentID: agentID, ToolCallID: toolCallID, State: "ready"},
	}
	if got := bus.toolCallSnapshot(); !slices.Equal(got, want) {
		t.Fatalf("tool call updates = %+v, want %+v", got, want)
	}
}

func TestRoutedPublisherCoalescesMixedIntents(t *testing.T) {
	machineID := uuid.New()
	agentID := uuid.New()
	runtimeID := uuid.New()
	bus := &fakeBus{}
	publisher := &RoutedPublisher{
		daemonWakeupPublisher:     bus,
		agentEventWakeupPublisher: bus,
		presence: fakePresenceStore{
			records: map[uuid.UUID]DaemonPresence{
				machineID: {
					PresenceOwner: PresenceOwner{ReplicaID: uuid.New(), RuntimeID: uuid.New(), ConnectionID: uuid.New()},
				},
			},
			runtimeRecords: map[uuid.UUID]DaemonPresence{
				runtimeID: {
					PresenceOwner: PresenceOwner{ReplicaID: uuid.New(), RuntimeID: runtimeID, ConnectionID: uuid.New()},
				},
			},
		},
		queue: make(chan PostCommitIntent, 8),
	}
	publisher.queue <- AgentEventCommitted{AgentID: agentID}
	publisher.queue <- DaemonRuntimeEndedCommitted{
		MachineID: machineID,
		RuntimeID: runtimeID,
		Cause:     DaemonRuntimeEndReconnect,
	}
	publisher.queue <- DaemonWorkCommitted{MachineID: machineID}

	publisher.publishCoalesced(context.Background(), DaemonWorkCommitted{MachineID: machineID})

	if got := bus.daemonCount(); got != 2 {
		t.Fatalf("daemon publishes = %d, want 2 (one work + one runtime-ended)", got)
	}
	if got := bus.agentSnapshot(); len(got) != 1 || got[0] != agentID {
		t.Fatalf("agent publishes = %v, want [%s]", got, agentID)
	}
}

func TestRoutedPublisherEncodesRuntimeEndedIntent(t *testing.T) {
	machineID := uuid.New()
	runtimeID := uuid.New()
	wantReplicaID := uuid.New()
	publisher := &RoutedPublisher{presence: fakePresenceStore{runtimeRecords: map[uuid.UUID]DaemonPresence{
		runtimeID: {PresenceOwner: PresenceOwner{ReplicaID: wantReplicaID, RuntimeID: runtimeID, ConnectionID: uuid.New()}},
	}}}

	replicaID, msg, ok, err := publisher.encodeDaemonIntent(
		context.Background(),
		DaemonRuntimeEndedCommitted{
			MachineID: machineID,
			RuntimeID: runtimeID,
			Cause:     DaemonRuntimeEndReconnect,
		},
	)
	if err != nil {
		t.Fatalf("encode intent: %v", err)
	}
	if !ok {
		t.Fatal("runtime-ended intent was not routed")
	}
	if replicaID != wantReplicaID {
		t.Fatalf("replica id = %s, want %s", replicaID, wantReplicaID)
	}
	if msg.Type != WakeupTypeDaemonRuntimeEnded || msg.MachineID != machineID || msg.RuntimeID == nil ||
		*msg.RuntimeID != runtimeID ||
		msg.RuntimeEndCause != DaemonRuntimeEndReconnect {
		t.Fatalf("wakeup = %+v, want runtime-ended machine/runtime wakeup", msg)
	}
}

func TestRoutedPublisherEncodesProcessTerminationIntent(t *testing.T) {
	machineID := uuid.New()
	processID := uuid.New()
	wantReplicaID := uuid.New()
	publisher := &RoutedPublisher{presence: fakePresenceStore{records: map[uuid.UUID]DaemonPresence{
		machineID: {PresenceOwner: PresenceOwner{ReplicaID: wantReplicaID, RuntimeID: uuid.New(), ConnectionID: uuid.New()}},
	}}}

	replicaID, msg, ok, err := publisher.encodeDaemonIntent(
		context.Background(),
		DaemonProcessTerminationCommitted{MachineID: machineID, ProcessIDs: []uuid.UUID{processID}},
	)
	if err != nil {
		t.Fatalf("encode intent: %v", err)
	}
	if !ok {
		t.Fatal("process termination intent was not routed")
	}
	if replicaID != wantReplicaID {
		t.Fatalf("replica id = %s, want %s", replicaID, wantReplicaID)
	}
	if msg.Type != WakeupTypeDaemonProcessTerminate || msg.MachineID != machineID ||
		len(msg.ProcessIDs) != 1 || msg.ProcessIDs[0] != processID {
		t.Fatalf("wakeup = %+v, want process termination wakeup", msg)
	}
}

func TestRoutedPublisherSkipsAbsentPresence(t *testing.T) {
	publisher := &RoutedPublisher{presence: fakePresenceStore{records: map[uuid.UUID]DaemonPresence{}}}

	replicaID, msg, ok, err := publisher.encodeDaemonIntent(
		context.Background(),
		DaemonWorkCommitted{MachineID: uuid.New()},
	)
	if err != nil {
		t.Fatalf("encode absent presence: %v", err)
	}
	if ok || replicaID != uuid.Nil || msg.Type != "" {
		t.Fatalf("encode absent presence replica_id=%q ok=%v msg=%+v, want skipped", replicaID, ok, msg)
	}
}

func TestRoutedPublisherReturnsPresenceError(t *testing.T) {
	presenceErr := errors.New("presence unavailable")
	publisher := &RoutedPublisher{presence: fakePresenceStore{err: presenceErr}}

	_, _, _, err := publisher.encodeDaemonIntent(context.Background(), DaemonWorkCommitted{MachineID: uuid.New()})
	if !errors.Is(err, presenceErr) {
		t.Fatalf("encode error = %v, want presence error", err)
	}
}

func TestRoutedPublisherSkipsInvalidPresence(t *testing.T) {
	machineID := uuid.New()
	bus := &fakeBus{}
	recorder := &recordingRecorder{}
	publisher := &RoutedPublisher{
		daemonWakeupPublisher: bus,
		presence: fakePresenceStore{records: map[uuid.UUID]DaemonPresence{
			machineID: {},
		}},
		recorder: recorder,
	}

	publisher.publishDaemon(context.Background(), DaemonWorkCommitted{MachineID: machineID})

	if got := bus.daemonCount(); got != 0 {
		t.Fatalf("daemon publishes for invalid presence = %d, want 0", got)
	}
	entries := recorder.snapshot()
	if len(entries) != 1 {
		t.Fatalf("recorder entries = %d, want 1", len(entries))
	}
	want := recordingEntry{Intent: "daemon_work", Result: "skipped", Reason: "invalid_presence"}
	if entries[0] != want {
		t.Fatalf("recorder entry = %+v, want %+v", entries[0], want)
	}
}

func TestRoutedPublisherSkipsInvalidDaemonIntent(t *testing.T) {
	machineID := uuid.New()
	bus := &fakeBus{}
	recorder := &recordingRecorder{}
	publisher := &RoutedPublisher{
		daemonWakeupPublisher: bus,
		presence: fakePresenceStore{records: map[uuid.UUID]DaemonPresence{
			machineID: {
				PresenceOwner: PresenceOwner{
					RuntimeID:    uuid.New(),
					ReplicaID:    uuid.New(),
					ConnectionID: uuid.New(),
				},
			},
		}},
		recorder: recorder,
	}

	publisher.publishDaemon(
		context.Background(),
		DaemonProcessTerminationCommitted{MachineID: machineID},
	)

	if got := bus.daemonCount(); got != 0 {
		t.Fatalf("daemon publishes for invalid intent = %d, want 0", got)
	}
	entries := recorder.snapshot()
	want := []recordingEntry{{
		Intent: "daemon_process_terminate",
		Result: "skipped",
		Reason: "invalid_intent",
	}}
	if !slices.Equal(entries, want) {
		t.Fatalf("recorder entries = %+v, want %+v", entries, want)
	}
}

func TestRoutedPublisherClassifiesMissingDaemonIDAsInvalidIntent(t *testing.T) {
	bus := &fakeBus{}
	recorder := &recordingRecorder{}
	publisher := &RoutedPublisher{
		daemonWakeupPublisher: bus,
		presence:              fakePresenceStore{},
		recorder:              recorder,
	}

	publisher.publishDaemon(context.Background(), DaemonWorkCommitted{})

	if got := bus.daemonCount(); got != 0 {
		t.Fatalf("daemon publishes for invalid intent = %d, want 0", got)
	}
	want := []recordingEntry{{Intent: "daemon_work", Result: "skipped", Reason: "invalid_intent"}}
	if entries := recorder.snapshot(); !slices.Equal(entries, want) {
		t.Fatalf("recorder entries = %+v, want %+v", entries, want)
	}
}

func TestRoutedPublisherClassifiesMissingAgentIDAsInvalidIntent(t *testing.T) {
	bus := &fakeBus{}
	recorder := &recordingRecorder{}
	publisher := &RoutedPublisher{agentEventWakeupPublisher: bus, recorder: recorder}

	publisher.publishAgentEvent(context.Background(), AgentEventCommitted{})

	if got := bus.agentSnapshot(); len(got) != 0 {
		t.Fatalf("agent publishes for invalid intent = %v, want none", got)
	}
	want := []recordingEntry{{Intent: "agent_event", Result: "skipped", Reason: "invalid_intent"}}
	if entries := recorder.snapshot(); !slices.Equal(entries, want) {
		t.Fatalf("recorder entries = %+v, want %+v", entries, want)
	}
}

func TestRoutedPublisherCloseDrainsQueuedIntents(t *testing.T) {
	machineID := uuid.New()
	agentID := uuid.New()
	bus := &fakeBus{}
	publisher := newTestRoutedPublisher(t, RoutedPublisherPorts{
		DaemonWakeups:     bus,
		AgentEventWakeups: bus,
	}, fakePresenceStore{records: map[uuid.UUID]DaemonPresence{
		machineID: {
			PresenceOwner: PresenceOwner{ReplicaID: uuid.New(), RuntimeID: uuid.New(), ConnectionID: uuid.New()},
		},
	}}, nil)
	publisher.PublishPostCommit(context.Background(), DaemonWorkCommitted{MachineID: machineID})
	publisher.PublishPostCommit(context.Background(), AgentEventCommitted{AgentID: agentID})

	publisher.Close()

	if got := bus.daemonCount(); got != 1 {
		t.Fatalf("daemon publishes after close = %d, want 1", got)
	}
	if got := bus.agentSnapshot(); len(got) != 1 || got[0] != agentID {
		t.Fatalf("agent publishes after close = %v, want [%s]", got, agentID)
	}
}

func TestRoutedPublisherCloseCancelsInFlightPublishAndSharesDrainDeadline(t *testing.T) {
	publishPort := newCloseAwareAgentWakeupPublisher()
	publisher := newTestRoutedPublisher(
		t,
		RoutedPublisherPorts{AgentEventWakeups: publishPort},
		nil,
		nil,
	)
	publisher.PublishPostCommit(context.Background(), AgentEventCommitted{AgentID: uuid.New()})
	select {
	case <-publishPort.entered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first publish to start")
	}
	publisher.PublishPostCommit(context.Background(), AgentEventCommitted{AgentID: uuid.New()})
	publisher.PublishPostCommit(context.Background(), AgentEventCommitted{AgentID: uuid.New()})

	closed := make(chan struct{})
	go func() {
		publisher.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Close did not cancel the in-flight publish promptly")
	}
	select {
	case <-publishPort.firstCanceled:
	default:
		t.Fatal("in-flight publish context was not canceled")
	}

	deadlines := publishPort.deadlines()
	if len(deadlines) != 2 {
		t.Fatalf("close drain publishes = %d, want 2", len(deadlines))
	}
	if !deadlines[0].Equal(deadlines[1]) {
		t.Fatalf(
			"close drain deadlines = %v, want one shared shutdown budget",
			deadlines,
		)
	}
}

func TestRoutedPublisherCloseWaitsOnlyForWorkerControlPublishTimeout(t *testing.T) {
	publishPort := newBlockingWorkerControlPublisher()
	publisher := newTestRoutedPublisher(
		t,
		RoutedPublisherPorts{WorkerControls: publishPort},
		nil,
		nil,
	)
	callerCtx, cancelCaller := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelCaller()
	publishDone := make(chan struct{})
	go func() {
		publisher.PublishPostCommit(callerCtx, WorkerControlCommitted{
			WorkerProcessID: uuid.New(),
			Control:         NewWorkerControlCancel(uuid.New(), uuid.New()),
		})
		close(publishDone)
	}()
	select {
	case <-publishPort.entered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for worker-control publish to start")
	}

	closed := make(chan struct{})
	go func() {
		publisher.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close remained blocked by a worker-control publish past its timeout")
	}
	select {
	case <-publishPort.canceled:
	default:
		t.Fatal("worker-control publish context was not canceled")
	}
	select {
	case <-publishDone:
	case <-time.After(time.Second):
		t.Fatal("worker-control publish did not return after its context was canceled")
	}
}

func TestRoutedPublisherCoalescedShutdownHonorsHardDeadline(t *testing.T) {
	publishPort := &deadlineRecordingAgentWakeupPublisher{}
	publisher := &RoutedPublisher{agentEventWakeupPublisher: publishPort}
	runCtx, cancelRun := context.WithCancel(context.Background())
	cancelRun()

	shutdownBy := time.Now().Add(time.Second)
	publisher.shutdownBy = shutdownBy
	publisher.flushCoalesced(
		runCtx,
		nil,
		map[uuid.UUID]struct{}{uuid.New(): {}, uuid.New(): {}},
		nil,
	)
	if len(publishPort.deadlines) != 2 {
		t.Fatalf("coalesced shutdown publishes = %d, want 2", len(publishPort.deadlines))
	}
	for i, deadline := range publishPort.deadlines {
		if !deadline.Equal(shutdownBy) {
			t.Fatalf("coalesced shutdown deadline %d = %v, want %v", i, deadline, shutdownBy)
		}
	}

	publisher.shutdownBy = time.Now().Add(-time.Second)
	publisher.flushCoalesced(
		runCtx,
		nil,
		map[uuid.UUID]struct{}{uuid.New(): {}},
		nil,
	)
	if len(publishPort.deadlines) != 2 {
		t.Fatalf("publish attempted after shutdown deadline, total publishes = %d", len(publishPort.deadlines))
	}
}

func TestRoutedPublisherDropsPostCommitIntentAfterClose(t *testing.T) {
	bus := &fakeBus{}
	recorder := &recordingRecorder{}
	publisher := newTestRoutedPublisher(t, RoutedPublisherPorts{
		AgentEventWakeups: bus,
		WorkerControls:    bus,
	}, nil, recorder)

	publisher.Close()
	publisher.PublishPostCommit(context.Background(), AgentEventCommitted{AgentID: uuid.New()})
	publisher.PublishPostCommit(context.Background(), WorkerControlCommitted{
		WorkerProcessID: uuid.New(),
		Control:         NewWorkerControlCancel(uuid.New(), uuid.New()),
	})

	if got := len(publisher.queue); got != 0 {
		t.Fatalf("queue length after publish on closed publisher = %d, want 0", got)
	}
	if got := bus.agentSnapshot(); len(got) != 0 {
		t.Fatalf("agent publishes after closed publish = %v, want none", got)
	}
	if got := bus.workerSnapshot(); len(got) != 0 {
		t.Fatalf("worker-control publishes after closed publish = %v, want none", got)
	}
	entries := recorder.snapshot()
	if len(entries) != 2 {
		t.Fatalf("recorder entries = %d, want 2", len(entries))
	}
	want := []recordingEntry{
		{Intent: "agent_event", Result: "dropped", Reason: "closed"},
		{Intent: "worker_control", Result: "dropped", Reason: "closed"},
	}
	for i := range want {
		if entries[i] != want[i] {
			t.Fatalf("recorder entry %d = %+v, want %+v", i, entries[i], want[i])
		}
	}
}

func TestRoutedPublisherDropsUnknownIntent(t *testing.T) {
	bus := &fakeBus{}
	publisher := newTestRoutedPublisher(t, RoutedPublisherPorts{
		DaemonWakeups:     bus,
		AgentEventWakeups: bus,
		WorkerControls:    bus,
	}, fakePresenceStore{}, nil)

	publisher.PublishPostCommit(context.Background(), unknownIntent{})

	if got := bus.daemonCount(); got != 0 {
		t.Fatalf("daemon publishes for unknown intent = %d, want 0", got)
	}
	if got := bus.agentSnapshot(); len(got) != 0 {
		t.Fatalf("agent publishes for unknown intent = %v, want []", got)
	}
}

func TestRoutedPublisherRecordsAgentPublishFailure(t *testing.T) {
	bus := &fakeBus{agentPublishErr: errors.New("redis down")}
	recorder := &recordingRecorder{}
	publisher := &RoutedPublisher{
		agentEventWakeupPublisher: bus,
		recorder:                  recorder,
	}

	publisher.publishAgentEvent(context.Background(), AgentEventCommitted{AgentID: uuid.New()})

	entries := recorder.snapshot()
	if len(entries) != 1 {
		t.Fatalf("recorder entries = %d, want 1", len(entries))
	}
	want := recordingEntry{Intent: "agent_event", Result: "error", Reason: "publish_failed"}
	if entries[0] != want {
		t.Fatalf("recorder entry = %+v, want %+v", entries[0], want)
	}
}

func TestRoutedPublisherRecordsIntentLabelPerType(t *testing.T) {
	bus := &fakeBus{}
	recorder := &recordingRecorder{}
	machineID := uuid.New()
	agentID := uuid.New()
	publisher := newTestRoutedPublisher(t, RoutedPublisherPorts{
		DaemonWakeups:     bus,
		AgentEventWakeups: bus,
	}, fakePresenceStore{records: map[uuid.UUID]DaemonPresence{
		machineID: {
			PresenceOwner: PresenceOwner{ReplicaID: uuid.New(), RuntimeID: uuid.New(), ConnectionID: uuid.New()},
		},
	}}, recorder)

	publisher.PublishPostCommit(context.Background(), DaemonWorkCommitted{MachineID: machineID})
	publisher.PublishPostCommit(context.Background(), AgentEventCommitted{AgentID: agentID})
	publisher.Close()

	seen := map[string]int{}
	for _, entry := range recorder.snapshot() {
		seen[entry.Intent]++
	}
	if seen["daemon_work"] == 0 {
		t.Fatal("recorder missing daemon_work entries")
	}
	if seen["agent_event"] == 0 {
		t.Fatal("recorder missing agent_event entries")
	}
}

func TestNotificationIntentLabels(t *testing.T) {
	cases := []struct {
		intent PostCommitIntent
		want   string
	}{
		{DaemonWorkCommitted{}, "daemon_work"},
		{DaemonRuntimeEndedCommitted{}, "daemon_runtime_ended"},
		{DaemonProcessTerminationCommitted{}, "daemon_process_terminate"},
		{AgentEventCommitted{}, "agent_event"},
		{ToolCallUpdatedCommitted{}, "tool_call_update"},
		{WorkerControlCommitted{}, "worker_control"},
	}
	for _, tc := range cases {
		if got := notificationIntentLabel(tc.intent); got != tc.want {
			t.Errorf("notificationIntentLabel(%T) = %q, want %q", tc.intent, got, tc.want)
		}
	}
}

func TestSubscribeAgentFanoutWaitsForClosedFanoutRemoval(t *testing.T) {
	bus := &RedisBus{}
	closedFanout := newAgentFanout(
		bus,
		func(context.Context, []byte) (struct{}, bool) { return struct{}{}, true },
		nil,
	)
	close(closedFanout.ready)
	closedFanout.mu.Lock()
	closedFanout.closed = true
	closedFanout.mu.Unlock()

	liveFanout := newAgentFanout(
		bus,
		func(context.Context, []byte) (struct{}, bool) { return struct{}{}, true },
		nil,
	)
	close(liveFanout.ready)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	attempts := make(chan int, 4)
	done := make(chan error, 1)
	var sub Subscription
	go func() {
		attempt := 0
		var err error
		sub, err = subscribeAgentFanout(
			ctx,
			bus,
			"unused",
			func(context.Context, struct{}) {},
			func() (*agentFanout[struct{}], bool) {
				attempt++
				attempts <- attempt
				if attempt == 1 {
					return closedFanout, false
				}
				return liveFanout, false
			},
		)
		done <- err
	}()

	if got := <-attempts; got != 1 {
		t.Fatalf("first fanout lookup attempt = %d, want 1", got)
	}
	select {
	case got := <-attempts:
		t.Fatalf("subscribe retried closed fanout before removal, attempt=%d", got)
	case err := <-done:
		t.Fatalf("subscribe returned before closed fanout removal: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	closedFanout.markRemoved()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("subscribe after closed fanout removal: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for subscribe after closed fanout removal: %v", ctx.Err())
	}
	if sub == nil {
		t.Fatal("subscribe returned nil subscription")
	}
	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("unsubscribe live fanout: %v", err)
	}
}

func TestAgentFanoutContainsSubscriberPanic(t *testing.T) {
	fanout := newAgentFanout(
		&RedisBus{},
		func(context.Context, []byte) (struct{}, bool) { return struct{}{}, true },
		nil,
	)
	fanout.subscribers[1] = func(context.Context, struct{}) {
		panic("subscriber failure")
	}
	called := false
	fanout.subscribers[2] = func(context.Context, struct{}) {
		called = true
	}

	fanout.dispatch(context.Background(), nil)

	if !called {
		t.Fatal("healthy subscriber was skipped after sibling panic")
	}
}

func TestSubscribeAgentFanoutReturnsCancellationDuringSetup(t *testing.T) {
	bus := &RedisBus{}
	fanout := newAgentFanout(
		bus,
		func(context.Context, []byte) (struct{}, bool) { return struct{}{}, true },
		nil,
	)
	close(fanout.ready)
	ctx, cancel := context.WithCancel(context.Background())

	sub, err := subscribeAgentFanout(
		ctx,
		bus,
		"unused",
		func(context.Context, struct{}) {},
		func() (*agentFanout[struct{}], bool) {
			cancel()
			return fanout, false
		},
	)
	if !errors.Is(err, context.Canceled) || sub != nil {
		t.Fatalf("subscribe result = (%v, %v), want nil, context.Canceled", sub, err)
	}
	fanout.mu.Lock()
	defer fanout.mu.Unlock()
	if len(fanout.subscribers) != 0 {
		t.Fatalf("subscribers after canceled setup = %d, want 0", len(fanout.subscribers))
	}
}

func TestTxNotificationsFlushIncludesAgentEvents(t *testing.T) {
	tx := NewTxNotifications()
	machineID := uuid.New()
	runtimeID := uuid.New()
	processID := uuid.New()
	agentA := uuid.New()
	agentB := uuid.New()
	workerID := uuid.New()
	workerAgentID := uuid.New()
	workerRuntimeID := uuid.New()
	toolCallID := uuid.New()
	tx.AddDaemonWork(machineID)
	tx.AddDaemonRuntimeEnded(
		runtimeID,
		machineID,
		DaemonRuntimeEndReconnect,
	)
	tx.AddDaemonProcessTermination(machineID, processID)
	tx.AddDaemonProcessTermination(machineID, processID)
	tx.AddAgentEvent(agentA)
	tx.AddAgentEvent(agentA)
	tx.AddAgentEvent(agentB)
	tx.AddToolCallUpdate(agentA, toolCallID, "awaiting_authorization")
	tx.AddToolCallUpdate(agentA, toolCallID, "ready")
	tx.AddWorkerControlCancel(workerID, workerAgentID, workerRuntimeID)

	publisher := &capturingPublisher{}
	tx.Flush(context.Background(), publisher)

	var sawDaemonWork, sawRuntimeEnded, sawProcessTermination bool
	var sawWorkerControl bool
	agentSeen := map[uuid.UUID]int{}
	toolCallStates := []string{}
	for _, intent := range publisher.intents {
		switch v := intent.(type) {
		case DaemonWorkCommitted:
			sawDaemonWork = true
		case DaemonRuntimeEndedCommitted:
			sawRuntimeEnded = true
		case DaemonProcessTerminationCommitted:
			sawProcessTermination = true
			if v.MachineID != machineID || len(v.ProcessIDs) != 1 || v.ProcessIDs[0] != processID {
				t.Fatalf("process termination intent = %+v, want machine/process", v)
			}
		case AgentEventCommitted:
			agentSeen[v.AgentID]++
		case ToolCallUpdatedCommitted:
			if v.AgentID != agentA || v.ToolCallID != toolCallID {
				t.Fatalf("tool call update = %+v, want agent/tool call", v)
			}
			toolCallStates = append(toolCallStates, v.State)
		case WorkerControlCommitted:
			sawWorkerControl = true
			if v.WorkerProcessID != workerID ||
				v.Control.Kind != WorkerControlKindCancel ||
				v.Control.Cancel == nil ||
				v.Control.Cancel.AgentID != workerAgentID ||
				v.Control.Cancel.RuntimeLockID != workerRuntimeID {
				t.Fatalf("worker control intent = %+v, want worker/runtime cancel", v)
			}
		}
	}
	if !sawDaemonWork || !sawRuntimeEnded || !sawProcessTermination || !sawWorkerControl {
		t.Fatalf(
			"flush missing intents: work=%v runtime=%v process_termination=%v worker_control=%v",
			sawDaemonWork,
			sawRuntimeEnded,
			sawProcessTermination,
			sawWorkerControl,
		)
	}
	if agentSeen[agentA] != 1 || agentSeen[agentB] != 1 {
		t.Fatalf("agent dedup failed: %+v", agentSeen)
	}
	if !slices.Equal(toolCallStates, []string{"awaiting_authorization", "ready"}) {
		t.Fatalf("tool call states = %v, want both transitions in order", toolCallStates)
	}
}

type unknownIntent struct{}

func (unknownIntent) postCommitIntent() {}

type capturingPublisher struct {
	mu      sync.Mutex
	intents []PostCommitIntent
}

func (p *capturingPublisher) PublishPostCommit(_ context.Context, intent PostCommitIntent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.intents = append(p.intents, intent)
}
