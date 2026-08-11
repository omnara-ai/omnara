package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type daemonSocket struct {
	server            *Server
	wire              *daemonprotocol.BackendSocket
	connectionID      uuid.UUID
	orgID             storage.ID
	machineID         storage.ID
	runtimeID         storage.ID
	tokenID           storage.ID
	send              chan daemonSocketOutbound
	workMu            sync.Mutex
	drainQueued       bool
	drainRunning      bool
	acceptedProcesses map[storage.ID]struct{}
	acceptedActions   map[storage.ID]struct{}
	leaseRenewAfter   time.Time
	observedPlatform  string
	drainAfterRenewal bool
	done              chan struct{}
	closeOnce         sync.Once
}

type daemonSocketOutbound struct {
	msg         daemonprotocol.Message
	closeCode   websocket.StatusCode
	closeReason string
}

type daemonPresenceRefreshOutcome int

const (
	daemonPresenceRefreshUnknown daemonPresenceRefreshOutcome = iota
	daemonPresenceRefreshed
	daemonPresenceRehydrated
	daemonPresenceRuntimeGone
	daemonPresenceReplaced
)

func newDaemonSocket(
	server *Server,
	wire *daemonprotocol.BackendSocket,
	connectionID uuid.UUID,
	orgID, machineID, runtimeID, tokenID storage.ID,
	drainAfterRenewal bool,
) *daemonSocket {
	return &daemonSocket{
		server:            server,
		wire:              wire,
		connectionID:      connectionID,
		orgID:             orgID,
		machineID:         machineID,
		runtimeID:         runtimeID,
		tokenID:           tokenID,
		send:              make(chan daemonSocketOutbound, daemonSocketQueueSize),
		done:              make(chan struct{}),
		acceptedProcesses: map[storage.ID]struct{}{},
		acceptedActions:   map[storage.ID]struct{}{},
		drainAfterRenewal: drainAfterRenewal,
	}
}

func (s *daemonSocket) recordSocketEvent(event, result, reason string) {
	if s == nil || s.server == nil || s.server.daemonRecorder == nil {
		return
	}
	s.server.daemonRecorder.RecordSocketEvent(event, result, reason)
}

func (s *daemonSocket) presenceOwner() notifications.PresenceOwner {
	return notifications.PresenceOwner{
		RuntimeID:    s.runtimeID,
		ReplicaID:    s.server.daemonHub.replicaID,
		ConnectionID: s.connectionID,
	}
}

func (s *daemonSocket) authority() executionstore.DaemonRuntimeAuthority {
	return executionstore.DaemonRuntimeAuthority{
		OrgID:           s.orgID,
		MachineID:       s.machineID,
		DaemonRuntimeID: s.runtimeID,
		DaemonTokenID:   s.tokenID,
	}
}

func (s *daemonSocket) enqueueDrain(ctx context.Context) bool {
	select {
	case <-s.done:
		return false
	default:
	}
	s.workMu.Lock()
	if s.drainRunning {
		if s.drainQueued {
			s.workMu.Unlock()
			return false
		}
		s.drainQueued = true
		s.workMu.Unlock()
		return true
	}
	if s.drainQueued {
		s.workMu.Unlock()
		return false
	}
	s.drainQueued = true
	s.workMu.Unlock()
	go s.drainLoop(ctx)
	return true
}

func (s *daemonSocket) enqueue(msg daemonprotocol.Message) bool {
	select {
	case s.send <- daemonSocketOutbound{msg: msg}:
		return true
	default:
		return false
	}
}

func (s *daemonSocket) enqueueThenClose(
	msg daemonprotocol.Message,
	code websocket.StatusCode,
	reason string,
) bool {
	select {
	case s.send <- daemonSocketOutbound{msg: msg, closeCode: code, closeReason: reason}:
		return true
	default:
		return false
	}
}

func (s *daemonSocket) close(code websocket.StatusCode, reason string) {
	s.closeOnce.Do(func() {
		close(s.done)
		if s.wire != nil {
			_ = s.wire.Close(code, reason)
		}
	})
}

func (s *daemonSocket) run(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() {
		s.server.daemonHub.unregister(s)
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cleanupCancel()
		owner := s.presenceOwner()
		_ = s.server.daemonHub.presence.DeleteIfOwned(cleanupCtx, s.machineID, owner)
		_ = s.server.daemonHub.presence.DeleteRuntimeIfOwned(cleanupCtx, s.runtimeID, owner)
	}()
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		defer cancel()
		s.writeLoop(runCtx)
	}()
	s.enqueueDrain(runCtx)
	fallbackDrainTimer := time.NewTimer(s.server.daemonHub.fallbackDrainDelay(s.connectionID))
	defer fallbackDrainTimer.Stop()
	go func() {
		for {
			select {
			case <-runCtx.Done():
				return
			case <-fallbackDrainTimer.C:
				if s.enqueueDrain(runCtx) {
					s.recordSocketEvent("work_drain", "queued", "fallback_timer")
				}
				fallbackDrainTimer.Reset(s.server.daemonHub.fallbackDrainDelay(s.connectionID))
			}
		}
	}()
	s.readLoop(runCtx)
	cancel()
	s.close(websocket.StatusNormalClosure, "closing")
	<-writerDone
}

func stableJitterDuration(value string, maxDuration time.Duration) time.Duration {
	if maxDuration <= 0 {
		return 0
	}
	return time.Duration(stableHashUint64(value) % uint64(maxDuration))
}

func stableHashUint64(value string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(value))
	return h.Sum64()
}

func (s *daemonSocket) writeLoop(ctx context.Context) {
	pingTicker := time.NewTicker(20 * time.Second)
	defer pingTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-pingTicker.C:
			pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := s.wire.Ping(pingCtx)
			cancel()
			if err != nil {
				return
			}
		case outbound := <-s.send:
			writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := s.wire.Write(writeCtx, outbound.msg)
			cancel()
			if err != nil {
				return
			}
			if outbound.closeCode != 0 {
				s.close(outbound.closeCode, outbound.closeReason)
				return
			}
		}
	}
}

func (s *daemonSocket) readLoop(ctx context.Context) {
	for {
		readCtx, cancel := context.WithTimeout(ctx, 70*time.Second)
		msg, err := s.wire.Read(readCtx)
		cancel()
		if err != nil {
			return
		}
		if err := s.handleMessage(ctx, msg); err != nil {
			s.enqueueOrClose(errorResponseForMessage(msg, err))
		}
	}
}

func (s *daemonSocket) handleMessage(ctx context.Context, msg daemonprotocol.Message) error {
	switch msg.Type {
	case daemonprotocol.MessageHeartbeat:
		return s.handleHeartbeat(ctx, msg)
	case daemonprotocol.MessageProcessAccept:
		return s.handleProcessAccept(ctx, msg)
	case daemonprotocol.MessageActionAccept:
		return s.handleActionAccept(ctx, msg)
	case daemonprotocol.MessageReport:
		return s.handleReport(ctx, msg)
	case daemonprotocol.MessageSkillReport:
		return s.handleSkillReport(ctx, msg)
	default:
		return errUnsupportedDaemonMessage
	}
}

func (s *daemonSocket) handleSkillReport(ctx context.Context, msg daemonprotocol.Message) error {
	if msg.SkillReport == nil {
		return errors.New("skill_report payload is required")
	}
	report := *msg.SkillReport
	s.recordSocketEvent("skill_report", string(report.State), report.ErrorCode)
	if s.server == nil || s.server.daemonHub == nil {
		return nil
	}
	if s.server.daemonHub.replyPublisher == nil {
		return nil
	}
	channel, ok := s.server.daemonHub.takePendingSkillReply(s.machineID, report.RequestID)
	if !ok {
		return nil
	}
	payload, err := encodeSkillReportReply(s.machineID, report)
	if err != nil {
		s.recordSocketEvent("skill_report", "encode_error", "")
		return nil //nolint:nilerr // intentional drop on impossible encoding failure
	}
	if err := s.server.daemonHub.replyPublisher.PublishChannel(ctx, channel, payload); err != nil {
		s.recordSocketEvent("skill_report", "publish_error", "")
	}
	return nil
}

func encodeSkillReportReply(machineID storage.ID, report daemonprotocol.SkillReport) ([]byte, error) {
	machinePublicID, err := publicID(publicid.KindMachine, machineID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(skillReportReply{
		MachineID: machinePublicID,
		RequestID: report.RequestID,
		SkillID:   report.SkillID,
		State:     report.State,
		ErrorCode: report.ErrorCode,
		Error:     report.Error,
	})
}

type skillReportReply struct {
	MachineID string                    `json:"machine_id"`
	RequestID string                    `json:"request_id"`
	SkillID   string                    `json:"skill_id"`
	State     daemonprotocol.SkillState `json:"state"`
	ErrorCode string                    `json:"error_code,omitempty"`
	Error     string                    `json:"error,omitempty"`
}

func (s *daemonSocket) handleHeartbeat(ctx context.Context, msg daemonprotocol.Message) error {
	if msg.DaemonInstanceID == uuid.Nil {
		return errors.New("daemon_instance_id is required")
	}
	now := s.server.timer.Now()
	observedPlatform := compactDaemonRawJSON(msg.ObservedPlatform)
	renewedLease := false
	if s.shouldRenewDaemonRuntimeLease(now, observedPlatform) {
		_, err := s.server.store.Execution().HeartbeatDaemonRuntime(
			ctx,
			executionstore.DaemonRuntimeLeaseInput{
				Authority:        s.authority(),
				DaemonInstanceID: msg.DaemonInstanceID,
				ObservedPlatform: msg.ObservedPlatform,
				LeaseTimeout:     s.server.daemonRuntimeLeaseDuration,
			},
		)
		if err != nil {
			if errors.Is(err, storeerr.ErrDaemonRuntimeUnregistered) {
				if !s.enqueueThenClose(
					daemonprotocol.Message{
						Type:      daemonprotocol.MessageRuntimeEnded,
						ErrorCode: daemonprotocol.ErrorCodeInvalidRuntime,
						Error:     err.Error(),
					},
					websocket.StatusNormalClosure,
					"runtime ended",
				) {
					s.close(websocket.StatusNormalClosure, "runtime ended")
				}
				return nil
			}
			return err
		}
		renewAfter := s.server.daemonRuntimeLeaseDuration / 2
		if renewAfter > 0 {
			s.leaseRenewAfter = now.Add(renewAfter)
		} else {
			s.leaseRenewAfter = time.Time{}
		}
		s.observedPlatform = observedPlatform
		renewedLease = true
	}
	presenceOutcome, err := s.refreshDaemonPresence(ctx)
	if err != nil {
		return err
	}
	switch presenceOutcome {
	case daemonPresenceRefreshed, daemonPresenceRehydrated:
	case daemonPresenceRefreshUnknown:
		return errors.New("daemon presence refresh returned unknown outcome")
	case daemonPresenceRuntimeGone:
		if !s.enqueueThenClose(
			daemonprotocol.Message{
				Type:      daemonprotocol.MessageRuntimeEnded,
				ErrorCode: daemonprotocol.ErrorCodeInvalidRuntime,
				Error:     storeerr.ErrDaemonRuntimeUnregistered.Error(),
			},
			websocket.StatusNormalClosure,
			"runtime ended",
		) {
			s.close(websocket.StatusNormalClosure, "runtime ended")
		}
		return nil
	case daemonPresenceReplaced:
		if !s.enqueueThenClose(
			daemonprotocol.Message{
				Type:      daemonprotocol.MessageRuntimeEnded,
				ErrorCode: daemonprotocol.ErrorCodePresenceReplaced,
				Error:     "daemon websocket presence was replaced",
			},
			websocket.StatusNormalClosure,
			"presence replaced",
		) {
			s.close(websocket.StatusNormalClosure, "presence replaced")
		}
		return nil
	}
	if renewedLease && s.drainAfterRenewal {
		s.drainAfterRenewal = false
		s.enqueueDrain(ctx)
	}
	s.enqueueOrClose(
		daemonprotocol.Message{
			Type:                 daemonprotocol.MessageHeartbeatAck,
			NextHeartbeatAfterMS: int(s.server.daemonRuntimeHeartbeatAfter() / time.Millisecond),
		},
	)
	return nil
}

func (s *daemonSocket) refreshDaemonPresence(
	ctx context.Context,
) (daemonPresenceRefreshOutcome, error) {
	owner := s.presenceOwner()
	err := s.server.daemonHub.presence.Refresh(
		ctx,
		s.machineID,
		owner,
		s.server.daemonRuntimePresenceTTL(),
	)
	if err == nil {
		if err := s.refreshDaemonRuntimePresence(ctx, owner); err != nil {
			return daemonPresenceRefreshUnknown, err
		}
		s.recordSocketEvent("presence_refresh", "success", "none")
		return daemonPresenceRefreshed, nil
	}
	if !errors.Is(err, notifications.ErrPresenceNotOwned) {
		s.recordSocketEvent("presence_refresh", "error", "store_error")
		return daemonPresenceRefreshUnknown, err
	}
	rehydrated, runtimeGone, err := s.rehydrateMissingPresence(ctx)
	if err != nil {
		s.recordSocketEvent("presence_refresh", "error", "rehydrate")
		return daemonPresenceRefreshUnknown, err
	}
	if rehydrated {
		s.recordSocketEvent("presence_refresh", "success", "rehydrated")
		return daemonPresenceRehydrated, nil
	}
	if runtimeGone {
		s.recordSocketEvent("presence_refresh", "rejected", "runtime_gone")
		return daemonPresenceRuntimeGone, nil
	}
	s.recordSocketEvent("presence_refresh", "rejected", "not_owned")
	return daemonPresenceReplaced, nil
}

func (s *daemonSocket) refreshDaemonRuntimePresence(
	ctx context.Context,
	owner notifications.PresenceOwner,
) error {
	ttl := s.server.daemonRuntimePresenceTTL()
	err := s.server.daemonHub.presence.RefreshRuntime(ctx, s.runtimeID, owner, ttl)
	if err == nil {
		return nil
	}
	if !errors.Is(err, notifications.ErrPresenceNotOwned) {
		s.recordSocketEvent("presence_refresh", "error", "runtime_store_error")
		return err
	}
	presence := notifications.DaemonPresence{PresenceOwner: owner}
	if err := s.server.daemonHub.presence.PutRuntimeIfMissing(
		ctx,
		s.runtimeID,
		presence,
		ttl,
	); err != nil {
		s.recordSocketEvent("presence_refresh", "error", "runtime_rehydrate")
		return err
	}
	s.recordSocketEvent("presence_refresh", "success", "runtime_rehydrated")
	return nil
}

func (s *daemonSocket) rehydrateMissingPresence(
	ctx context.Context,
) (rehydrated bool, runtimeGone bool, err error) {
	_, ok, err := s.server.daemonHub.presence.Get(ctx, s.machineID)
	if err != nil || ok {
		return false, false, err
	}
	registered, err := s.server.store.Execution().RegisteredDaemonRuntimeExists(
		ctx,
		s.authority(),
	)
	if err != nil || !registered {
		return false, !registered, err
	}
	presence := notifications.DaemonPresence{PresenceOwner: s.presenceOwner()}
	if err := s.server.daemonHub.presence.PutIfMissing(
		ctx,
		s.machineID,
		presence,
		s.server.daemonRuntimePresenceTTL(),
	); err != nil {
		if errors.Is(err, notifications.ErrPresenceNotOwned) {
			return false, false, nil
		}
		return false, false, err
	}
	owner := s.presenceOwner()
	if err := s.refreshDaemonRuntimePresence(ctx, owner); err != nil {
		_ = s.server.daemonHub.presence.DeleteIfOwned(
			context.WithoutCancel(ctx),
			s.machineID,
			owner,
		)
		if errors.Is(err, notifications.ErrPresenceNotOwned) {
			return false, false, nil
		}
		return false, false, err
	}
	return true, false, nil
}

func (s *daemonSocket) shouldRenewDaemonRuntimeLease(now time.Time, observedPlatform string) bool {
	if s.leaseRenewAfter.IsZero() {
		return true
	}
	if s.observedPlatform != observedPlatform {
		return true
	}
	return !now.Before(s.leaseRenewAfter)
}

func compactDaemonRawJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

func (s *daemonSocket) enqueueOrClose(msg daemonprotocol.Message) bool {
	if s.enqueue(msg) {
		return true
	}
	s.recordSocketEvent("send_queue", "dropped", "queue_full")
	s.close(websocket.StatusPolicyViolation, "daemon socket send queue full")
	return false
}
