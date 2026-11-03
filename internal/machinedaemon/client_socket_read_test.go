package machinedaemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
	"github.com/omnara-ai/omnara/internal/processaction"
	"github.com/stretchr/testify/require"
)

func TestDirectReadUsesGrantCursorUnlessRequestExplicitlyUsesZero(t *testing.T) {
	tests := []struct {
		name       string
		payload    json.RawMessage
		wantOutput string
		wantCursor int64
	}{
		{
			name:       "implicit",
			payload:    json.RawMessage(`{"max_bytes":64}`),
			wantOutput: "56789",
			wantCursor: 5,
		},
		{
			name:       "explicit zero",
			payload:    json.RawMessage(`{"cursor":0,"max_bytes":64}`),
			wantOutput: "0123456789",
			wantCursor: 0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(
				context.Background(),
				5*time.Second,
			)
			defer cancel()
			client, transport, machine := newDirectReadTestTransport(t)
			defer client.closeState()
			defer transport.stopAndWait(func() {})

			path, err := machine.OutputBufferPath("prc_cursor_handoff")
			require.NoError(t, err)
			require.NoError(t, writeProcessOutputFile(
				path,
				[]byte("0123456789"),
				0,
			))
			result := runDirectReadAndAcknowledge(
				t,
				ctx,
				transport,
				pendingAction{
					processID:           "prc_cursor_handoff",
					processState:        "exited",
					defaultOutputCursor: 5,
					action: ProcessAction{
						ID:         "act_cursor_handoff",
						ActionKind: "read",
						Seq:        1,
						Payload:    test.payload,
					},
				},
			)
			var observed struct {
				Output     string `json:"output"`
				Cursor     int64  `json:"cursor"`
				NextCursor int64  `json:"next_cursor"`
			}
			require.NoError(t, json.Unmarshal(result.Result, &observed))
			if observed.Output != test.wantOutput ||
				observed.Cursor != test.wantCursor ||
				observed.NextCursor != 10 {
				t.Fatalf("read observation = %+v", observed)
			}
		})
	}
}

func TestDirectReadReturnsRetainedTerminalOutputWithoutLocalState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, transport, machine := newDirectReadTestTransport(t)
	defer client.closeState()
	defer transport.stopAndWait(func() {})

	path, err := machine.OutputBufferPath("prc_terminal_read")
	require.NoError(t, err)
	require.NoError(t, writeProcessOutputFile(path, []byte("retained"), 4))
	result := runDirectReadAndAcknowledge(
		t,
		ctx,
		transport,
		pendingAction{
			processID:           "prc_terminal_read",
			processState:        "exited",
			defaultOutputCursor: 0,
			action: ProcessAction{
				ID:         "act_terminal_read",
				ActionKind: "read",
				Seq:        1,
				Payload:    json.RawMessage(`{"max_bytes":64}`),
			},
		},
	)
	if result.Type != "process_action_applied" {
		t.Fatalf("terminal read event type = %q", result.Type)
	}
	var observed struct {
		Output     string `json:"output"`
		Cursor     int64  `json:"cursor"`
		NextCursor int64  `json:"next_cursor"`
		Truncated  bool   `json:"truncated"`
	}
	require.NoError(t, json.Unmarshal(result.Result, &observed))
	if observed.Output != "retained" ||
		observed.Cursor != 4 ||
		observed.NextCursor != 12 ||
		!observed.Truncated {
		t.Fatalf("terminal read observation = %+v", observed)
	}
}

func TestDirectReadRejectedObservationFailsOnlyTheRead(t *testing.T) {
	directReadReportRetryDelay = time.Millisecond
	t.Cleanup(func() { directReadReportRetryDelay = time.Second })
	storageUnavailable := []daemonReportAck{}
	for range directReadReportAttempts {
		storageUnavailable = append(storageUnavailable, daemonReportAck{
			status: daemonprotocol.AckStatusTransientError,
			code:   daemonprotocol.ErrorCodeStorageUnavailable,
		})
	}
	for _, test := range []struct {
		name       string
		acks       []daemonReportAck
		wantReason string
	}{
		{
			name: "permanent",
			acks: []daemonReportAck{{
				status: daemonprotocol.AckStatusPermanentReject,
				code:   daemonprotocol.ErrorCodeValidationFailed,
			}},
			wantReason: daemonprotocol.ProcessActionReasonInvalidReadObservation,
		},
		{
			name: "storage recovers",
			acks: []daemonReportAck{
				storageUnavailable[0],
				storageUnavailable[0],
				{status: daemonprotocol.AckStatusCommitted},
			},
		},
		{
			name:       "storage stays unavailable",
			acks:       storageUnavailable,
			wantReason: daemonprotocol.ProcessActionReasonStorageUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(
				context.Background(),
				5*time.Second,
			)
			defer cancel()
			client, transport, machine := newDirectReadTestTransport(t)
			defer client.closeState()
			defer transport.stopAndWait(func() {})

			path, err := machine.OutputBufferPath("prc_rejected_read")
			require.NoError(t, err)
			require.NoError(t, writeProcessOutputFile(
				path,
				[]byte("bin\x00ary\xff\xfe"),
				0,
			))
			readDone := make(chan error, 1)
			go func() {
				readDone <- transport.runAcceptedRead(ctx, pendingAction{
					processID:    "prc_rejected_read",
					processState: "exited",
					action: ProcessAction{
						ID:         "act_rejected_read",
						ActionKind: "read",
						Seq:        1,
						Payload:    json.RawMessage(`{"max_bytes":64}`),
					},
				})
			}()
			for _, ack := range test.acks {
				applied := waitForDirectReadReport(t, ctx, transport)
				var observed struct {
					Output     string `json:"output"`
					NextCursor int64  `json:"next_cursor"`
				}
				require.NoError(t, json.Unmarshal(applied.Event.Result, &observed))
				if applied.Event.Type != daemonprotocol.EventProcessActionApplied ||
					observed.Output != "bin␀ary\uFFFD" ||
					observed.NextCursor != 9 {
					t.Fatalf("applied read report = %+v %+v", applied.Event, observed)
				}
				transport.ackReport(daemonprotocol.Message{
					Type:      "report_ack",
					ReportID:  applied.ReportID,
					AckStatus: ack.status,
					ErrorCode: ack.code,
					Error:     "rejected",
				})
			}

			if test.wantReason != "" {
				failed := waitForDirectReadReport(t, ctx, transport)
				if failed.Event.Type != daemonprotocol.EventProcessActionFailed ||
					failed.Event.StateReasonCode != test.wantReason ||
					len(failed.Event.Result) != 0 {
					t.Fatalf("fallback read report = %+v", failed.Event)
				}
				ackDirectReadReport(transport, failed)
			}
			select {
			case err := <-readDone:
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}

func TestDirectReadWaitReturnsOnFirstOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, transport, machine := newDirectReadTestTransport(t)
	defer client.closeState()
	defer transport.stopAndWait(func() {})

	const (
		processID            = "prc_wait_first_output"
		actionID             = "act_wait_first_output"
		supervisorInstanceID = "supervisor-instance-wait_first_output"
	)
	path, err := machine.OutputBufferPath(processID)
	require.NoError(t, err)
	require.NoError(t, writeProcessOutputFile(path, nil, 0))
	store, err := client.stateStore(ctx)
	require.NoError(t, err)
	require.NoError(t, store.ReserveProcess(
		ctx,
		statedb.Process{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
			SupervisorToken:      "supervisor-token-wait_first_output",
		}))
	require.NoError(t, store.MarkPrepared(ctx, processID, supervisorInstanceID))
	require.NoError(t, store.MarkAccepted(ctx, processID, supervisorInstanceID))

	readDone := make(chan error, 1)
	go func() {
		readDone <- transport.runAcceptedRead(ctx, pendingAction{
			processID:    processID,
			processState: "running",
			action: ProcessAction{
				ID:         actionID,
				ActionKind: "read",
				Seq:        1,
				Payload:    json.RawMessage(`{"wait_ms":1000}`),
			},
		})
	}()
	select {
	case message := <-transport.send:
		t.Fatalf("read returned before output was written: %+v", message)
	case <-time.After(75 * time.Millisecond):
	}

	output := processOutput{
		path:         path,
		limit:        1024,
		syncBytes:    1024,
		syncInterval: time.Hour,
	}
	if _, err := output.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	defer output.Close()

	message := waitForDirectReadReport(t, ctx, transport)
	var observed struct {
		Output string `json:"output"`
	}
	require.NoError(t, json.Unmarshal(message.Event.Result, &observed))
	if observed.Output != "first" {
		t.Fatalf("first-output observation = %+v", observed)
	}
	ackDirectReadReport(transport, message)
	select {
	case err := <-readDone:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	process, found, err := store.Process(ctx, processID)
	require.NoError(t, err)
	if !found || process.ResolvedActionSeq != 1 {
		t.Fatalf("resolved process state = %+v, found=%t", process, found)
	}
}

func TestDirectReadWaitReturnsWhenProcessBecomesTerminal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, transport, machine := newDirectReadTestTransport(t)
	defer client.closeState()
	defer transport.stopAndWait(func() {})

	const (
		processID            = "prc_wait_terminal"
		actionID             = "act_wait_terminal"
		supervisorInstanceID = "supervisor-instance-wait_terminal"
	)
	path, err := machine.OutputBufferPath(processID)
	require.NoError(t, err)
	require.NoError(t, writeProcessOutputFile(path, nil, 0))
	store, err := client.stateStore(ctx)
	require.NoError(t, err)
	require.NoError(t, store.ReserveProcess(
		ctx,
		statedb.Process{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
			SupervisorToken:      "supervisor-token-wait_terminal",
		}))
	require.NoError(t, store.MarkPrepared(ctx, processID, supervisorInstanceID))
	require.NoError(t, store.MarkAccepted(ctx, processID, supervisorInstanceID))

	readStarted := time.Now()
	readDone := make(chan error, 1)
	go func() {
		readDone <- transport.runAcceptedRead(ctx, pendingAction{
			processID:    processID,
			processState: "running",
			action: ProcessAction{
				ID:         actionID,
				ActionKind: "read",
				Seq:        1,
				Payload:    json.RawMessage(`{"wait_ms":2000}`),
			},
		})
	}()
	select {
	case message := <-transport.send:
		t.Fatalf("read returned before process terminalization: %+v", message)
	case <-time.After(75 * time.Millisecond):
	}
	require.NoError(t, store.MarkServerReleased(
		ctx,
		processID,
		supervisorInstanceID,
	))
	message := waitForDirectReadReport(t, ctx, transport)
	var observed struct {
		Output string `json:"output"`
	}
	require.NoError(t, json.Unmarshal(message.Event.Result, &observed))
	if observed.Output != "" {
		t.Fatalf("terminal observation = %+v", observed)
	}
	if time.Since(readStarted) >= 1500*time.Millisecond {
		t.Fatal("terminal read waited for its deadline instead of process completion")
	}
	ackDirectReadReport(transport, message)
	select {
	case err := <-readDone:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestDirectReadWaitReturnsEmptyAtItsDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, transport, machine := newDirectReadTestTransport(t)
	defer client.closeState()
	defer transport.stopAndWait(func() {})

	const (
		processID            = "prc_wait_deadline"
		supervisorInstanceID = "supervisor-instance-wait_deadline"
	)
	path, err := machine.OutputBufferPath(processID)
	require.NoError(t, err)
	require.NoError(t, writeProcessOutputFile(path, nil, 0))
	store, err := client.stateStore(ctx)
	require.NoError(t, err)
	require.NoError(t, store.ReserveProcess(
		ctx,
		statedb.Process{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
			SupervisorToken:      "supervisor-token-wait_deadline",
		}))
	require.NoError(t, store.MarkPrepared(ctx, processID, supervisorInstanceID))
	require.NoError(t, store.MarkAccepted(ctx, processID, supervisorInstanceID))

	readDone := make(chan error, 1)
	started := time.Now()
	go func() {
		readDone <- transport.runAcceptedRead(ctx, pendingAction{
			processID:    processID,
			processState: "running",
			action: ProcessAction{
				ID:         "act_wait_deadline",
				ActionKind: "read",
				Seq:        1,
				Payload:    json.RawMessage(`{"wait_ms":150}`),
			},
		})
	}()
	select {
	case message := <-transport.send:
		t.Fatalf("deadline read returned immediately: %+v", message)
	case <-time.After(75 * time.Millisecond):
	}
	message := waitForDirectReadReport(t, ctx, transport)
	if time.Since(started) < 125*time.Millisecond {
		t.Fatal("deadline read returned before its wait window")
	}
	var observed struct {
		Output string `json:"output"`
		Done   bool   `json:"done"`
	}
	require.NoError(t, json.Unmarshal(message.Event.Result, &observed))
	if observed.Output != "" || observed.Done {
		t.Fatalf("deadline observation = %+v", observed)
	}
	ackDirectReadReport(transport, message)
	select {
	case err := <-readDone:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestDirectReadReportsMissingRetainedOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, transport, machine := newDirectReadTestTransport(t)
	defer client.closeState()
	defer transport.stopAndWait(func() {})

	processDir, err := machine.ProcessDir("prc_missing_output")
	require.NoError(t, err)
	require.NoError(t, localstore.EnsurePrivateDir(processDir))
	result := runDirectReadAndAcknowledge(
		t,
		ctx,
		transport,
		pendingAction{
			processID:    "prc_missing_output",
			processState: "exited",
			action: ProcessAction{
				ID:         "act_missing_output",
				ActionKind: "read",
				Seq:        1,
				Payload:    json.RawMessage(`{}`),
			},
		},
	)
	if result.Type != "process_action_failed" ||
		result.StateReasonCode != "output_unavailable" ||
		result.StateReasonMessage == "" {
		t.Fatalf("missing-output event = %+v", result)
	}
}

func newDirectReadTestTransport(
	t *testing.T,
) (*Client, *daemonSocketTransport, localstore.MachineStore) {
	t.Helper()
	client := New(Config{OmnaraHome: t.TempDir()}, nil, nil)
	client.bootstrap = daemonBootstrap{
		InstallationID: "ins_direct_read",
		MachineID:      "mch_direct_read",
	}
	machine, err := client.machineStore()
	require.NoError(t, err)
	return &client, newDaemonSocketTransport(
		&client,
		DaemonRuntime{},
		localStartupState{},
	), machine
}

func runDirectReadAndAcknowledge(
	t *testing.T,
	ctx context.Context,
	transport *daemonSocketTransport,
	action pendingAction,
) *daemonprotocol.ReportedEvent {
	t.Helper()
	readDone := make(chan error, 1)
	go func() {
		readDone <- transport.runAcceptedRead(ctx, action)
	}()
	message := waitForDirectReadReport(t, ctx, transport)
	ackDirectReadReport(transport, message)
	select {
	case err := <-readDone:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	return message.Event
}

func waitForDirectReadReport(
	t *testing.T,
	ctx context.Context,
	transport *daemonSocketTransport,
) daemonprotocol.Message {
	t.Helper()
	select {
	case message := <-transport.send:
		if message.Type != "report" ||
			message.ReportID == "" ||
			message.Event == nil {
			t.Fatalf("direct read message = %+v", message)
		}
		return message
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		return daemonprotocol.Message{}
	}
}

func ackDirectReadReport(
	transport *daemonSocketTransport,
	message daemonprotocol.Message,
) {
	transport.ackReport(daemonprotocol.Message{
		Type:      "report_ack",
		ReportID:  message.ReportID,
		AckStatus: daemonprotocol.AckStatusCommitted,
	})
}

func TestDirectReadWaitYieldsToNewerAcceptedAction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, transport, machine := newDirectReadTestTransport(t)
	defer client.closeState()
	defer transport.stopAndWait(cancel)

	const (
		processID            = "prc_wait_yield"
		readActionID         = "act_wait_yield_read"
		terminateActionID    = "act_wait_yield_terminate"
		supervisorInstanceID = "supervisor-instance-wait_yield"
	)
	path, err := machine.OutputBufferPath(processID)
	require.NoError(t, err)
	require.NoError(t, writeProcessOutputFile(path, nil, 0))
	store, err := client.stateStore(ctx)
	require.NoError(t, err)
	require.NoError(t, store.ReserveProcess(
		ctx,
		statedb.Process{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
			SupervisorToken:      "supervisor-token-wait_yield",
		}))
	require.NoError(t, store.MarkPrepared(ctx, processID, supervisorInstanceID))
	require.NoError(t, store.MarkAccepted(ctx, processID, supervisorInstanceID))
	runner := &recordingProcessRunner{
		applied: make(chan ProcessAction, 1),
		done:    make(chan struct{}),
	}
	client.processMu.Lock()
	client.processes[processID] = &processRuntime{processID: processID, runner: runner}
	client.processMu.Unlock()

	nextSent := func(wantType daemonprotocol.MessageType) daemonprotocol.Message {
		t.Helper()
		select {
		case message := <-transport.send:
			if message.Type != wantType {
				t.Fatalf("sent %+v, want %s", message, wantType)
			}
			return message
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return daemonprotocol.Message{}
		}
	}
	accept := func(actionID string, kind daemonprotocol.ProcessActionKind, seq int64, payload string) {
		t.Helper()
		transport.offerAction(daemonprotocol.ActionOffer{
			ProcessID:       processID,
			ProcessActionID: actionID,
			ActionKind:      kind,
			Seq:             seq,
			Payload:         json.RawMessage(payload),
		})
		nextSent(daemonprotocol.MessageActionAccept)
		transport.applyAcceptedAction(ctx, daemonprotocol.ActionGrant{
			ProcessID:       processID,
			ProcessActionID: actionID,
			ProcessState:    daemonprotocol.ProcessStateRunning,
		})
	}

	// The server canceled this read's turn and has already offered a stop,
	// which it does only once no earlier action is still accepted.
	accept(readActionID, daemonprotocol.ProcessActionRead, 1, `{"wait_ms":300000}`)
	select {
	case message := <-transport.send:
		t.Fatalf("read returned before a newer action arrived: %+v", message)
	case <-time.After(75 * time.Millisecond):
	}
	accept(terminateActionID, daemonprotocol.ProcessActionTerminate, 2, `{}`)

	message := nextSent(daemonprotocol.MessageReport)
	if message.Event == nil || message.Event.ProcessActionID != readActionID {
		t.Fatalf("yielded read report = %+v", message)
	}
	var observed struct {
		Output string `json:"output"`
	}
	require.NoError(t, json.Unmarshal(message.Event.Result, &observed))
	if observed.Output != "" {
		t.Fatalf("yielded read observation = %+v", observed)
	}
	ackDirectReadReport(transport, message)
	select {
	case applied := <-runner.applied:
		if applied.ID != terminateActionID {
			t.Fatalf("applied %+v, want the terminate", applied)
		}
	case <-ctx.Done():
		t.Fatal("terminate stayed queued behind the long read")
	}
}

func TestDirectReadWaitForExitKeepsHeadAndTailAtDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, transport, machine := newDirectReadTestTransport(t)
	defer client.closeState()
	defer transport.stopAndWait(func() {})

	const (
		processID            = "prc_wait_for_exit"
		actionID             = "act_wait_for_exit"
		supervisorInstanceID = "supervisor-instance-wait_for_exit"
	)
	path, err := machine.OutputBufferPath(processID)
	require.NoError(t, err)
	require.NoError(t, writeProcessOutputFile(path, nil, 0))
	store, err := client.stateStore(ctx)
	require.NoError(t, err)
	require.NoError(t, store.ReserveProcess(
		ctx,
		statedb.Process{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
			SupervisorToken:      "supervisor-token-wait_for_exit",
		}))
	require.NoError(t, store.MarkPrepared(ctx, processID, supervisorInstanceID))
	require.NoError(t, store.MarkAccepted(ctx, processID, supervisorInstanceID))

	output := processOutput{
		path:         path,
		limit:        1024,
		syncBytes:    1024,
		syncInterval: time.Hour,
	}
	defer output.Close()
	if _, err := output.Write([]byte("start-")); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	readDone := make(chan error, 1)
	go func() {
		readDone <- transport.runAcceptedRead(ctx, pendingAction{
			processID:    processID,
			processState: "running",
			action: ProcessAction{
				ID:         actionID,
				ActionKind: "read",
				Seq:        1,
				Payload: json.RawMessage(
					`{"wait_ms":400,"max_bytes":10,"wait_for_exit":true,"head_bytes":4}`,
				),
			},
		})
	}()
	select {
	case message := <-transport.send:
		t.Fatalf("wait_for_exit read returned on existing output: %+v", message)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := output.Write([]byte("middle-end")); err != nil {
		t.Fatal(err)
	}

	message := waitForDirectReadReport(t, ctx, transport)
	if elapsed := time.Since(started); elapsed < 350*time.Millisecond {
		t.Fatalf("wait_for_exit read returned after %s, before its deadline", elapsed)
	}
	var observed processaction.ReadObservation
	require.NoError(t, json.Unmarshal(message.Event.Result, &observed))
	require.Equal(t, processaction.ReadObservation{
		ProcessID:     processID,
		Output:        "star",
		Cursor:        0,
		NextCursor:    16,
		OutputTail:    "le-end",
		OmittedCursor: 4,
		TailCursor:    10,
	}, observed)
	ackDirectReadReport(transport, message)
	select {
	case err := <-readDone:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
