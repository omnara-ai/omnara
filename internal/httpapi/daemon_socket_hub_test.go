package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestDaemonProcessOfferMessageReplacesOversizedOffer(t *testing.T) {
	message := daemonProcessOfferMessage("prc_test", executionstore.DaemonProcessOffer{
		Env: map[string]string{"VALUE": strings.Repeat("x", daemonSocketReadLimitBytes)},
	})
	if message.ProcessOffer == nil ||
		message.ProcessOffer.ProcessID != "prc_test" ||
		message.ProcessOffer.PreparationError != "process offer exceeds daemon message size limit" ||
		message.ProcessOffer.Env != nil {
		t.Fatalf("oversized process offer message = %+v", message)
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("marshal fallback process offer: %v", err)
	}
	if len(encoded) > daemonSocketReadLimitBytes {
		t.Fatalf("fallback process offer size = %d", len(encoded))
	}
}

func TestDaemonProcessOfferMessageCarriesInitialWait(t *testing.T) {
	message := daemonProcessOfferMessage("prc_test", executionstore.DaemonProcessOffer{
		Process: executionstore.ProcessRecord{
			IOMode:         "pipe",
			Command:        "echo ok",
			ShellSelector:  "default",
			InitialWaitMS:  750,
			TimeoutSeconds: 30,
		},
	})
	if message.ProcessOffer == nil || message.ProcessOffer.WaitMs != 750 {
		t.Fatalf("process offer = %+v, want initial wait 750ms", message.ProcessOffer)
	}
}

func newTestDaemonSocket(t *testing.T, socket *daemonSocket) *daemonSocket {
	t.Helper()
	if socket.done == nil {
		socket.done = make(chan struct{})
	}
	if socket.send == nil {
		socket.send = make(chan daemonSocketOutbound, 1)
	}
	if socket.acceptedProcesses == nil {
		socket.acceptedProcesses = map[storage.ID]struct{}{}
	}
	if socket.acceptedActions == nil {
		socket.acceptedActions = map[storage.ID]struct{}{}
	}
	return socket
}

func TestDaemonSocketHubRegisterReplacesByMachineAndRuntime(t *testing.T) {
	hub := &daemonSocketHub{
		byMachine: map[storage.ID]*daemonSocket{},
		byRuntime: map[storage.ID]*daemonSocket{},
	}
	machineID := uuid.New()
	runtimeA := uuid.New()
	runtimeB := uuid.New()
	first := newTestDaemonSocket(
		t,
		&daemonSocket{machineID: machineID, runtimeID: runtimeA},
	)
	second := newTestDaemonSocket(
		t,
		&daemonSocket{machineID: machineID, runtimeID: runtimeB},
	)

	hub.register(first)
	hub.register(second)
	hub.unregister(first)

	if got := hub.byMachine[machineID]; got != second {
		t.Fatal("machine index did not retain replacement socket")
	}
	if got := hub.byRuntime[runtimeB]; got != second {
		t.Fatal("runtime index did not retain replacement socket")
	}
	if got := hub.byRuntime[runtimeA]; got != nil {
		t.Fatal("old runtime index was not replaced")
	}
}

func TestDaemonSocketHubRuntimeEndedRoutesToOwningSocket(t *testing.T) {
	runtimeID := uuid.New()
	socket := newTestDaemonSocket(t, &daemonSocket{runtimeID: runtimeID})
	hub := &daemonSocketHub{
		byMachine: map[storage.ID]*daemonSocket{},
		byRuntime: map[storage.ID]*daemonSocket{runtimeID: socket},
	}

	hub.handleRuntimeEnded(
		context.Background(),
		notifications.WakeupMessage{
			Type:            notifications.WakeupTypeDaemonRuntimeEnded,
			RuntimeID:       &runtimeID,
			RuntimeEndCause: notifications.DaemonRuntimeEndReconnect,
			MachineID:       uuid.New(),
		},
	)

	select {
	case outbound := <-socket.send:
		if outbound.msg.Type != "runtime_ended" {
			t.Fatalf("message type = %q, want runtime_ended", outbound.msg.Type)
		}
		if outbound.closeCode == 0 {
			t.Fatal("runtime_ended message should close the socket after send")
		}
	default:
		t.Fatal("runtime_ended message was not routed to socket")
	}
}

func TestDaemonSocketHubProcessTerminateRoutesToOwningSocket(t *testing.T) {
	machineID := uuid.New()
	processID := uuid.New()
	socket := newTestDaemonSocket(t, &daemonSocket{machineID: machineID})
	hub := &daemonSocketHub{
		byMachine: map[storage.ID]*daemonSocket{machineID: socket},
		byRuntime: map[storage.ID]*daemonSocket{},
	}
	processPublicID, err := publicID(publicid.KindProcess, processID)
	if err != nil {
		t.Fatalf("process public id: %v", err)
	}

	hub.handleProcessTerminate(
		notifications.WakeupMessage{
			Type:       notifications.WakeupTypeDaemonProcessTerminate,
			MachineID:  machineID,
			ProcessIDs: []uuid.UUID{processID},
		},
	)

	select {
	case outbound := <-socket.send:
		if outbound.msg.Type != "process_terminate" {
			t.Fatalf("message type = %q, want process_terminate", outbound.msg.Type)
		}
		if outbound.msg.ProcessID != processPublicID {
			t.Fatalf("process id = %q, want %q", outbound.msg.ProcessID, processPublicID)
		}
		if outbound.closeCode != 0 {
			t.Fatal("process_terminate message should not close the socket")
		}
	default:
		t.Fatal("process_terminate message was not routed to socket")
	}
}

func TestDaemonSocketHubDropsUnencodableInboxMessage(t *testing.T) {
	machineID := uuid.New()
	socket := newTestDaemonSocket(t, &daemonSocket{machineID: machineID})
	hub := &daemonSocketHub{
		byMachine: map[storage.ID]*daemonSocket{machineID: socket},
		byRuntime: map[storage.ID]*daemonSocket{},
	}
	payload, err := json.Marshal(notifications.DaemonInboxMessage{
		MachineID: machineID,
		Kind:      "future_message",
		Payload:   json.RawMessage(`{"type":"future_message","payload":{"value":true}}`),
	})
	if err != nil {
		t.Fatalf("marshal inbox message: %v", err)
	}

	hub.handleInboxPayload(context.Background(), payload)

	select {
	case outbound := <-socket.send:
		t.Fatalf("unencodable inbox message was enqueued: %+v", outbound.msg)
	default:
	}
}

func TestDaemonSocketIgnoresDrainAfterClose(t *testing.T) {
	socket := newTestDaemonSocket(t, &daemonSocket{})
	socket.close(websocket.StatusNormalClosure, "test")

	socket.enqueueDrain(context.Background())

	socket.workMu.Lock()
	defer socket.workMu.Unlock()
	if socket.drainQueued || socket.drainRunning {
		t.Fatalf(
			"closed socket queued drain: queued=%v running=%v",
			socket.drainQueued,
			socket.drainRunning,
		)
	}
}

func TestDaemonSocketFallbackDrainDelayUsesPlusMinusJitter(t *testing.T) {
	hub := &daemonSocketHub{
		fallbackDrainInterval: 30 * time.Second,
		fallbackDrainJitter:   10 * time.Second,
	}
	for _, connectionID := range []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New()} {
		delay := hub.fallbackDrainDelay(connectionID)
		if delay < 20*time.Second || delay >= 40*time.Second {
			t.Fatalf(
				"fallback delay for %q = %s, want [20s, 40s)",
				connectionID,
				delay,
			)
		}
	}
}

func TestDaemonSocketRejectsUnknownMessageType(t *testing.T) {
	socket := newTestDaemonSocket(t, &daemonSocket{})
	err := socket.handleMessage(
		context.Background(),
		daemonprotocol.Message{Type: "future_message"},
	)
	if got := daemonErrorCode(err); got != daemonprotocol.ErrorCodeValidationFailed {
		t.Fatalf("unknown message error code = %q, want validation_failed", got)
	}
}

func TestDaemonAcceptRejectionsHaveExplicitErrorCodes(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		err  error
		code string
	}{
		{
			name: "process",
			err:  errDaemonProcessOfferUnavailable,
			code: daemonprotocol.ErrorCodeProcessOfferUnavailable,
		},
		{
			name: "action",
			err:  errDaemonActionOfferUnavailable,
			code: daemonprotocol.ErrorCodeActionOfferUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := daemonErrorCode(test.err); got != test.code {
				t.Fatalf(
					"daemon error code = %q, want %q",
					got,
					test.code,
				)
			}
		})
	}
}

func TestDaemonReportValidationErrorsArePermanentRejects(t *testing.T) {
	now := time.Now().UTC()
	err := validateDaemonReportedEvent(
		daemonReportedEvent{
			Type:       "process_started",
			ProcessID:  "prc_1",
			ObservedAt: now,
		},
	)
	if !errors.Is(err, errDaemonReportValidation) {
		t.Fatalf(
			"missing physical start validation error = %v, want errDaemonReportValidation",
			err,
		)
	}

	err = validateDaemonReportedEvent(
		daemonReportedEvent{
			Type:            "process_action_failed",
			ProcessID:       "prc_1",
			ProcessActionID: "act_1",
		},
	)
	if !errors.Is(err, errDaemonReportValidation) {
		t.Fatalf("validation error = %v, want errDaemonReportValidation", err)
	}
	if err := validateDaemonReportedEvent(
		daemonReportedEvent{
			Type:            daemonprotocol.EventProcessActionApplied,
			ProcessID:       "prc_1",
			ProcessActionID: "act_1",
			ObservedAt:      now,
		},
	); !errors.Is(err, errDaemonReportValidation) {
		t.Fatalf(
			"action observed_at validation error = %v, want errDaemonReportValidation",
			err,
		)
	}
	ack := errorResponseForMessage(
		daemonprotocol.Message{
			Type: "report",
			Event: &daemonprotocol.ReportedEvent{
				Type:            "process_action_failed",
				ProcessID:       "prc_1",
				ProcessActionID: "act_1",
			},
		},
		err,
	)
	if ack.Type != "report_ack" ||
		ack.AckStatus != daemonprotocol.AckStatusPermanentReject {
		t.Fatalf("validation ack = %+v, want permanent report ack", ack)
	}

	err = validateDaemonReportedEvent(
		daemonReportedEvent{
			Type:      "process_finished",
			ProcessID: "prc_1",
			State:     daemonprotocol.ProcessStateFailed,
			EndedAt:   time.Now().UTC(),
		},
	)
	if !errors.Is(err, errDaemonReportValidation) {
		t.Fatalf(
			"process validation error = %v, want errDaemonReportValidation",
			err,
		)
	}
	if err := validateDaemonReportedEvent(
		daemonReportedEvent{
			Type:            daemonprotocol.EventProcessFinished,
			ProcessID:       "prc_1",
			State:           daemonprotocol.ProcessStateFailed,
			StateReasonCode: "start_failed",
		},
	); err != nil {
		t.Fatalf("pre-start failure without physical end was rejected: %v", err)
	}
	if err := validateDaemonReportedEvent(
		daemonReportedEvent{
			Type:            daemonprotocol.EventProcessFinished,
			ProcessID:       "prc_1",
			State:           daemonprotocol.ProcessStateFailed,
			StateReasonCode: "start_failed",
			ObservedAt:      now,
		},
	); !errors.Is(err, errDaemonReportValidation) {
		t.Fatalf(
			"terminal observed_at validation error = %v, want errDaemonReportValidation",
			err,
		)
	}
	if err := validateDaemonReportedEvent(
		daemonReportedEvent{
			Type:      daemonprotocol.EventProcessFinished,
			ProcessID: "prc_1",
			State:     daemonprotocol.ProcessStateExited,
		},
	); !errors.Is(err, errDaemonReportValidation) {
		t.Fatalf("exit without physical end error = %v, want errDaemonReportValidation", err)
	}
	err = validateDaemonReportedEvent(
		daemonReportedEvent{
			Type:      "process_finished",
			ProcessID: "prc_1",
			State:     daemonprotocol.ProcessStateExited,
			StartedAt: now.Add(time.Second),
			EndedAt:   now,
		},
	)
	if !errors.Is(err, errDaemonReportValidation) {
		t.Fatalf(
			"reversed process source times error = %v, want errDaemonReportValidation",
			err,
		)
	}

	err = (&daemonSocket{}).handleReport(
		context.Background(),
		daemonprotocol.Message{Type: "report"},
	)
	if !errors.Is(err, errDaemonReportValidation) {
		t.Fatalf(
			"nil event report error = %v, want errDaemonReportValidation",
			err,
		)
	}
	ack = errorResponseForMessage(daemonprotocol.Message{Type: "report"}, err)
	if ack.AckStatus != daemonprotocol.AckStatusPermanentReject {
		t.Fatalf("nil event ack = %+v, want permanent reject", ack)
	}

	_, err = (&Server{}).applyDaemonReportedEventForMachineWithContext(
		context.Background(),
		executionstore.DaemonRuntimeAuthority{},
		daemonReportedEvent{
			Type:       "process_started",
			ProcessID:  "not-a-process-id",
			ObservedAt: time.Now().UTC(),
		},
		errors.New("missing"),
	)
	if !errors.Is(err, errDaemonReportValidation) {
		t.Fatalf(
			"bad process id report error = %v, want errDaemonReportValidation",
			err,
		)
	}
}

func TestDaemonProcessReportRejectsMissingExecutionGrant(t *testing.T) {
	t.Parallel()
	cleanupOnly, err := (&Server{}).applyDaemonReportedEventForProcess(
		context.Background(),
		executionstore.DaemonRuntimeAuthority{
			OrgID:           testHTTPID(1),
			MachineID:       testHTTPID(2),
			DaemonRuntimeID: testHTTPID(3),
			DaemonTokenID:   testHTTPID(4),
		},
		executionstore.ProcessRecord{State: executionstore.ProcessStateFailed},
		daemonReportedEvent{
			Type:      "process_started",
			ProcessID: "prc_never_granted",
			StartedAt: time.Now().UTC(),
		},
		errors.New("missing process"),
	)
	if cleanupOnly || !errors.Is(err, errDaemonReportValidation) ||
		!strings.Contains(err.Error(), "process was not accepted") {
		t.Fatalf(
			"never-granted process report cleanup_only=%t error=%v, want permanent rejection",
			cleanupOnly,
			err,
		)
	}
}

func TestDaemonReportConstraintErrorsArePermanentRejects(t *testing.T) {
	t.Parallel()

	if got := daemonAckStatus(storeerr.ErrProcessExecutionNotGranted); got != daemonprotocol.AckStatusPermanentReject {
		t.Fatalf(
			"ungranted process report ack status = %q, want %q",
			got,
			daemonprotocol.AckStatusPermanentReject,
		)
	}

	for _, code := range []string{"23502", "23514"} {
		t.Run(code, func(t *testing.T) {
			t.Parallel()
			err := fmt.Errorf(
				"store daemon report: %w",
				&pgconn.PgError{Code: code},
			)
			if got := daemonAckStatus(err); got != daemonprotocol.AckStatusPermanentReject {
				t.Fatalf(
					"daemon report ack status = %q, want %q",
					got,
					daemonprotocol.AckStatusPermanentReject,
				)
			}
		})
	}

	transient := &pgconn.PgError{Code: "40001"}
	if got := daemonAckStatus(transient); got != daemonprotocol.AckStatusTransientError {
		t.Fatalf(
			"transaction retry ack status = %q, want %q",
			got,
			daemonprotocol.AckStatusTransientError,
		)
	}

	runtimeLoss := errors.Join(
		fmt.Errorf(
			"store daemon report: %w",
			storeerr.ErrDaemonRuntimeUnregistered,
		),
		&pgconn.PgError{Code: "23514"},
	)
	if got := daemonAckStatus(runtimeLoss); got != daemonprotocol.AckStatusTransientError {
		t.Fatalf(
			"runtime loss ack status = %q, want %q",
			got,
			daemonprotocol.AckStatusTransientError,
		)
	}
}

func TestDaemonSocketLeaseRenewalDecision(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	socket := newTestDaemonSocket(
		t,
		&daemonSocket{
			server: &Server{daemonRuntimeLeaseDuration: executionstore.DaemonRuntimeLeaseDuration},
		},
	)
	if !socket.shouldRenewDaemonRuntimeLease(now, "{}") {
		t.Fatal("unknown lease should renew")
	}

	socket.leaseRenewAfter = now.Add(time.Minute)
	socket.observedPlatform = "{}"
	if socket.shouldRenewDaemonRuntimeLease(now.Add(30*time.Second), "{}") {
		t.Fatal("fresh unchanged lease should not renew")
	}
	if !socket.shouldRenewDaemonRuntimeLease(
		now.Add(30*time.Second),
		`{"os":"darwin"}`,
	) {
		t.Fatal("observed platform change should renew")
	}
	if !socket.shouldRenewDaemonRuntimeLease(now.Add(time.Minute), "{}") {
		t.Fatal("half-spent lease should renew")
	}
}
