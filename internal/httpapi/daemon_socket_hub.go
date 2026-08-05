package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/metrics"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
)

const (
	daemonSocketQueueSize                    = 64
	daemonSocketReadLimitBytes               = daemonprotocol.MaxMessageBytes
	daemonProcessOfferLimit                  = 4
	daemonActionOfferLimit                   = 16
	defaultDaemonSocketFallbackDrainInterval = 30 * time.Second
	defaultDaemonSocketFallbackDrainJitter   = 10 * time.Second

	// Redis Pub/Sub wakeups are best-effort. The per-socket periodic drain is
	// the bounded recovery floor for missed publishes, subscriber reconnects,
	// and dropped messages while Postgres remains the source of truth.
	daemonRuntimeHeartbeatInterval = 10 * time.Second
)

type daemonSocketHub struct {
	log                   *slog.Logger
	wakeupSubscriber      notifications.DaemonWakeupSubscriber
	replyPublisher        replyChannelPublisher
	presence              notifications.DaemonPresenceStore
	replicaID             uuid.UUID
	recorder              *metrics.DaemonRecorder
	fallbackDrainInterval time.Duration
	fallbackDrainJitter   time.Duration
	done                  <-chan struct{}
	cancel                context.CancelFunc
	mu                    sync.RWMutex
	byMachine             map[storage.ID]*daemonSocket
	byRuntime             map[storage.ID]*daemonSocket
	subs                  []notifications.Subscription

	pendingSkillMu      sync.Mutex
	pendingSkillReports map[skillReportKey]skillReportPending
}

type replyChannelPublisher interface {
	PublishChannel(ctx context.Context, channel string, payload []byte) error
}

type skillReportKey struct {
	machineID storage.ID
	requestID string
}

type skillReportPending struct {
	replyChannel string
	expiresAt    time.Time
}

const pendingSkillReportTTL = 2 * time.Minute

func newDaemonSocketHub(
	wakeupSubscriber notifications.DaemonWakeupSubscriber,
	replyPublisher replyChannelPublisher,
	presence notifications.DaemonPresenceStore,
	replicaID uuid.UUID,
	log *slog.Logger,
	recorder *metrics.DaemonRecorder,
	fallbackDrainInterval time.Duration,
	fallbackDrainJitter time.Duration,
) (*daemonSocketHub, error) {
	if wakeupSubscriber == nil {
		return nil, errors.New("daemon wakeup subscriber is required")
	}
	if presence == nil {
		return nil, errors.New("daemon presence store is required")
	}
	if replicaID == uuid.Nil {
		return nil, errors.New("replica id is required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &daemonSocketHub{
		log:                   log,
		wakeupSubscriber:      wakeupSubscriber,
		replyPublisher:        replyPublisher,
		presence:              presence,
		replicaID:             replicaID,
		recorder:              recorder,
		fallbackDrainInterval: fallbackDrainInterval,
		fallbackDrainJitter:   fallbackDrainJitter,
		done:                  ctx.Done(),
		cancel:                cancel,
		byMachine:             map[storage.ID]*daemonSocket{},
		byRuntime:             map[storage.ID]*daemonSocket{},
		pendingSkillReports:   map[skillReportKey]skillReportPending{},
	}
	wakeupSub, err := wakeupSubscriber.SubscribeDaemonReplicaWakeups(ctx, replicaID, h.handleWakeup)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("subscribe daemon wakeup notifications: %w", err)
	}
	inboxSub, err := wakeupSubscriber.SubscribeDaemonReplicaInbox(ctx, replicaID, h.handleInboxPayload)
	if err != nil {
		_ = wakeupSub.Unsubscribe()
		cancel()
		return nil, fmt.Errorf("subscribe daemon inbox: %w", err)
	}
	h.subs = []notifications.Subscription{wakeupSub, inboxSub}
	go h.sweepPendingSkillReports(ctx)
	return h, nil
}

func (h *daemonSocketHub) recordPendingSkillReply(machineID storage.ID, requestID, replyChannel string) {
	if h == nil || requestID == "" || replyChannel == "" {
		return
	}
	h.pendingSkillMu.Lock()
	h.pendingSkillReports[skillReportKey{machineID: machineID, requestID: requestID}] = skillReportPending{
		replyChannel: replyChannel,
		expiresAt:    time.Now().Add(pendingSkillReportTTL),
	}
	h.pendingSkillMu.Unlock()
}

func (h *daemonSocketHub) takePendingSkillReply(machineID storage.ID, requestID string) (string, bool) {
	if h == nil || requestID == "" {
		return "", false
	}
	key := skillReportKey{machineID: machineID, requestID: requestID}
	h.pendingSkillMu.Lock()
	entry, ok := h.pendingSkillReports[key]
	if ok {
		delete(h.pendingSkillReports, key)
	}
	h.pendingSkillMu.Unlock()
	if !ok {
		return "", false
	}
	return entry.replyChannel, true
}

func (h *daemonSocketHub) sweepPendingSkillReports(ctx context.Context) {
	ticker := time.NewTicker(pendingSkillReportTTL / 2)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		now := time.Now()
		h.pendingSkillMu.Lock()
		for key, entry := range h.pendingSkillReports {
			if !entry.expiresAt.After(now) {
				delete(h.pendingSkillReports, key)
			}
		}
		h.pendingSkillMu.Unlock()
	}
}

func (h *daemonSocketHub) Close() {
	if h == nil {
		return
	}
	h.cancel()
	for _, sub := range h.subs {
		_ = sub.Unsubscribe()
	}
	h.mu.RLock()
	sockets := make([]*daemonSocket, 0, len(h.byRuntime))
	for _, socket := range h.byRuntime {
		sockets = append(sockets, socket)
	}
	h.mu.RUnlock()
	for _, socket := range sockets {
		socket.close(websocket.StatusNormalClosure, "api shutting down")
	}
}

func (h *daemonSocketHub) register(socket *daemonSocket) {
	var replaced []*daemonSocket
	h.mu.Lock()
	if prior := h.byMachine[socket.machineID]; prior != nil && prior != socket {
		replaced = append(replaced, prior)
	}
	if prior := h.byRuntime[socket.runtimeID]; prior != nil && prior != socket {
		duplicate := false
		for _, existing := range replaced {
			if existing == prior {
				duplicate = true
				break
			}
		}
		if !duplicate {
			replaced = append(replaced, prior)
		}
	}
	h.byMachine[socket.machineID] = socket
	h.byRuntime[socket.runtimeID] = socket
	h.mu.Unlock()
	for _, prior := range replaced {
		prior.close(websocket.StatusNormalClosure, "replaced")
	}
}

func (h *daemonSocketHub) unregister(socket *daemonSocket) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.byMachine[socket.machineID] == socket {
		delete(h.byMachine, socket.machineID)
	}
	if h.byRuntime[socket.runtimeID] == socket {
		delete(h.byRuntime, socket.runtimeID)
	}
}

func (h *daemonSocketHub) handleWakeup(ctx context.Context, body notifications.WakeupMessage) {
	if err := ctx.Err(); err != nil {
		return
	}
	switch body.Type {
	case notifications.WakeupTypeDaemonWork:
		h.handleDaemonWork(ctx, body)
	case notifications.WakeupTypeDaemonRuntimeEnded:
		h.handleRuntimeEnded(ctx, body)
	case notifications.WakeupTypeDaemonProcessTerminate:
		h.handleProcessTerminate(body)
	default:
		if h.log != nil {
			h.log.Warn("unknown daemon wakeup type", "type", body.Type)
		}
	}
}

func (h *daemonSocketHub) handleDaemonWork(ctx context.Context, body notifications.WakeupMessage) {
	socket := h.socketByMachine(body.MachineID)
	if socket != nil {
		if socket.enqueueDrain(ctx) {
			socket.recordSocketEvent("work_drain", "queued", "redis_wakeup")
		}
	} else {
		h.recordSocketEvent("daemon_work_notification", "miss", "local_socket_missing")
		if h.log != nil {
			h.log.Warn("daemon work notification had no local socket", "machine_id", body.MachineID)
		}
	}
}

func (h *daemonSocketHub) handleProcessTerminate(body notifications.WakeupMessage) {
	socket := h.socketByMachine(body.MachineID)
	if socket == nil {
		h.recordSocketEvent("daemon_process_terminate_notification", "miss", "local_socket_missing")
		if h.log != nil {
			h.log.Warn("daemon process terminate notification had no local socket", "machine_id", body.MachineID)
		}
		return
	}
	for _, processID := range body.ProcessIDs {
		processPublicID, err := publicID(publicid.KindProcess, processID)
		if err != nil {
			h.recordSocketEvent("daemon_process_terminate_notification", "dropped", "invalid_process_id")
			if h.log != nil {
				h.log.Warn("encode daemon process terminate process id failed", "process_id", processID, "error", err)
			}
			continue
		}
		if !socket.enqueue(daemonprotocol.Message{
			Type:      daemonprotocol.MessageProcessTerminate,
			ProcessID: processPublicID,
		}) {
			socket.recordSocketEvent("send_queue", "dropped", "queue_full")
		}
	}
}

func (h *daemonSocketHub) handleRuntimeEnded(
	ctx context.Context,
	body notifications.WakeupMessage,
) {
	if body.RuntimeID == nil {
		h.recordSocketEvent("runtime_ended_notification", "miss", "runtime_id_missing")
		if h.log != nil {
			h.log.Warn(
				"daemon runtime-ended notification missing runtime id",
				"machine_id",
				body.MachineID,
			)
		}
		return
	}
	socket := h.socketByRuntime(*body.RuntimeID)
	if socket != nil {
		errorCode := daemonprotocol.ErrorCodeInvalidRuntime
		switch body.RuntimeEndCause {
		case notifications.DaemonRuntimeEndAuthorizationRevoked:
			errorCode = daemonprotocol.ErrorCodeAuthorizationRevoked
		case notifications.DaemonRuntimeEndMachineDecommissioned:
			errorCode = daemonprotocol.ErrorCodeMachineDecommissioned
		case notifications.DaemonRuntimeEndReconnect:
		}
		if !socket.enqueueThenClose(
			daemonprotocol.Message{
				Type:      daemonprotocol.MessageRuntimeEnded,
				ErrorCode: errorCode,
			},
			websocket.StatusNormalClosure,
			"runtime ended",
		) {
			socket.close(websocket.StatusNormalClosure, "runtime ended")
		}
	} else {
		h.recordSocketEvent("runtime_ended_notification", "miss", "local_socket_missing")
		if h.log != nil {
			h.log.Warn(
				"daemon runtime-ended notification had no local socket",
				"runtime_id",
				*body.RuntimeID,
			)
		}
	}
}

func (h *daemonSocketHub) handleInboxPayload(ctx context.Context, payload []byte) {
	if err := ctx.Err(); err != nil {
		return
	}
	var envelope notifications.DaemonInboxMessage
	if err := json.Unmarshal(payload, &envelope); err != nil {
		h.recordSocketEvent("daemon_inbox", "error", "decode")
		if h.log != nil {
			h.log.Warn("decode daemon inbox envelope", "error", err)
		}
		return
	}
	if envelope.MachineID == uuid.Nil || envelope.Kind == "" {
		h.recordSocketEvent("daemon_inbox", "error", "envelope")
		if h.log != nil {
			h.log.Warn("daemon inbox envelope missing machine_id or kind")
		}
		return
	}
	var msg daemonprotocol.Message
	if err := json.Unmarshal(envelope.Payload, &msg); err != nil {
		h.recordSocketEvent("daemon_inbox", "error", "payload_decode")
		if h.log != nil {
			h.log.Warn("decode daemon inbox payload", "error", err, "kind", envelope.Kind)
		}
		return
	}
	if msg.Type == "" {
		msg.Type = envelope.Kind
	}
	if msg.Type != envelope.Kind {
		h.recordSocketEvent("daemon_inbox", "error", "kind_mismatch")
		if h.log != nil {
			h.log.Warn("daemon inbox kind/payload mismatch", "envelope_kind", envelope.Kind, "payload_type", msg.Type)
		}
		return
	}
	socket := h.socketByMachine(envelope.MachineID)
	if socket == nil {
		h.recordSocketEvent("daemon_inbox", "miss", "local_socket_missing")
		return
	}
	if _, err := json.Marshal(msg); err != nil {
		h.recordSocketEvent("daemon_inbox", "dropped", "payload_encode")
		if h.log != nil {
			h.log.Warn("drop unencodable daemon inbox payload", "error", err, "kind", envelope.Kind)
		}
		return
	}
	if envelope.ReplyChannel != "" {
		if requestID, ok := requestIDForInboxMessage(envelope.Kind, msg); ok {
			h.recordPendingSkillReply(envelope.MachineID, requestID, envelope.ReplyChannel)
		}
	}
	if !socket.enqueue(msg) {
		if envelope.ReplyChannel != "" {
			if requestID, ok := requestIDForInboxMessage(envelope.Kind, msg); ok {
				_, _ = h.takePendingSkillReply(envelope.MachineID, requestID)
			}
		}
		h.recordSocketEvent("daemon_inbox", "dropped", "queue_full")
	} else {
		h.recordSocketEvent(
			"daemon_inbox",
			"delivered",
			string(envelope.Kind),
		)
	}
}

func requestIDForInboxMessage(
	kind daemonprotocol.MessageType,
	msg daemonprotocol.Message,
) (string, bool) {
	switch kind {
	case daemonprotocol.MessageSkillOffer:
		if msg.SkillOffer == nil {
			return "", false
		}
		return msg.SkillOffer.RequestID, msg.SkillOffer.RequestID != ""
	default:
		return "", false
	}
}

func (h *daemonSocketHub) recordSocketEvent(event, result, reason string) {
	if h == nil || h.recorder == nil {
		return
	}
	h.recorder.RecordSocketEvent(event, result, reason)
}

func (h *daemonSocketHub) socketByMachine(id uuid.UUID) *daemonSocket {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.byMachine[id]
}

func (h *daemonSocketHub) socketByRuntime(id uuid.UUID) *daemonSocket {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.byRuntime[id]
}

func (h *daemonSocketHub) fallbackDrainDelay(connectionID uuid.UUID) time.Duration {
	base := h.fallbackDrainInterval
	jitter := h.fallbackDrainJitter
	if base <= 0 {
		base = defaultDaemonSocketFallbackDrainInterval
	}
	if jitter <= 0 {
		return base
	}
	if jitter > base {
		jitter = base
	}
	lowerBound := base - jitter
	if lowerBound <= 0 {
		lowerBound = time.Millisecond
	}
	return lowerBound + stableJitterDuration(connectionID.String(), 2*jitter)
}
