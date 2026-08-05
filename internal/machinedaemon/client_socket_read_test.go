package machinedaemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
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
			if err != nil {
				t.Fatal(err)
			}
			if err := writeProcessOutputFile(
				path,
				[]byte("0123456789"),
				0,
			); err != nil {
				t.Fatal(err)
			}
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
			if err := json.Unmarshal(result.Result, &observed); err != nil {
				t.Fatal(err)
			}
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
	if err != nil {
		t.Fatal(err)
	}
	if err := writeProcessOutputFile(path, []byte("retained"), 4); err != nil {
		t.Fatal(err)
	}
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
	if err := json.Unmarshal(result.Result, &observed); err != nil {
		t.Fatal(err)
	}
	if observed.Output != "retained" ||
		observed.Cursor != 4 ||
		observed.NextCursor != 12 ||
		!observed.Truncated {
		t.Fatalf("terminal read observation = %+v", observed)
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
	if err != nil {
		t.Fatal(err)
	}
	if err := writeProcessOutputFile(path, nil, 0); err != nil {
		t.Fatal(err)
	}
	store, err := client.stateStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveProcess(
		ctx,
		statedb.Process{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
			SupervisorToken:      "supervisor-token-wait_first_output",
		}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPrepared(ctx, processID, supervisorInstanceID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAccepted(ctx, processID, supervisorInstanceID); err != nil {
		t.Fatal(err)
	}

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
	if err := json.Unmarshal(message.Event.Result, &observed); err != nil {
		t.Fatal(err)
	}
	if observed.Output != "first" {
		t.Fatalf("first-output observation = %+v", observed)
	}
	ackDirectReadReport(transport, message)
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	process, found, err := store.Process(ctx, processID)
	if err != nil {
		t.Fatal(err)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	if err := writeProcessOutputFile(path, nil, 0); err != nil {
		t.Fatal(err)
	}
	store, err := client.stateStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveProcess(
		ctx,
		statedb.Process{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
			SupervisorToken:      "supervisor-token-wait_terminal",
		}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPrepared(ctx, processID, supervisorInstanceID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAccepted(ctx, processID, supervisorInstanceID); err != nil {
		t.Fatal(err)
	}

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
	if err := store.MarkServerReleased(
		ctx,
		processID,
		supervisorInstanceID,
	); err != nil {
		t.Fatal(err)
	}
	message := waitForDirectReadReport(t, ctx, transport)
	var observed struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(message.Event.Result, &observed); err != nil {
		t.Fatal(err)
	}
	if observed.Output != "" {
		t.Fatalf("terminal observation = %+v", observed)
	}
	if time.Since(readStarted) >= 1500*time.Millisecond {
		t.Fatal("terminal read waited for its deadline instead of process completion")
	}
	ackDirectReadReport(transport, message)
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatal(err)
		}
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
	if err != nil {
		t.Fatal(err)
	}
	if err := writeProcessOutputFile(path, nil, 0); err != nil {
		t.Fatal(err)
	}
	store, err := client.stateStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReserveProcess(
		ctx,
		statedb.Process{
			ProcessID:            processID,
			SupervisorInstanceID: supervisorInstanceID,
			SupervisorToken:      "supervisor-token-wait_deadline",
		}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPrepared(ctx, processID, supervisorInstanceID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAccepted(ctx, processID, supervisorInstanceID); err != nil {
		t.Fatal(err)
	}

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
	if err := json.Unmarshal(message.Event.Result, &observed); err != nil {
		t.Fatal(err)
	}
	if observed.Output != "" || observed.Done {
		t.Fatalf("deadline observation = %+v", observed)
	}
	ackDirectReadReport(transport, message)
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatal(err)
		}
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
	if err != nil {
		t.Fatal(err)
	}
	if err := localstore.EnsurePrivateDir(processDir); err != nil {
		t.Fatal(err)
	}
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
	if err != nil {
		t.Fatal(err)
	}
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
		if err != nil {
			t.Fatal(err)
		}
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
