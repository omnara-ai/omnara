package machinedaemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
	"github.com/omnara-ai/omnara/internal/processcmd"
)

type Config struct {
	APIURL                 string
	MachineToken           string
	DaemonVersion          string
	OmnaraHome             string
	ExpectedInstallationID string
	ExpectedMachineID      string
	RunnerPath             string
	RetryInterval          time.Duration
	SleepAfter             time.Duration
	WakeListenAddr         string
	SleepPlatform          string
}

type BootstrapIdentity struct {
	InstallationID string
	MachineID      string
}

type DaemonRuntime struct {
	ID                   string `json:"id"`
	NextHeartbeatAfterMS int    `json:"next_heartbeat_after_ms"`
}

type Client struct {
	cfg            Config
	instanceID     uuid.UUID
	http           *http.Client
	log            *slog.Logger
	bootstrap      daemonBootstrap
	runnerLauncher processRunnerLauncher

	stateMu sync.Mutex
	state   *statedb.Store

	transportMu   sync.RWMutex
	transport     daemonReportTransport
	runtimeMu     sync.RWMutex
	activeRuntime DaemonRuntime

	processMu sync.RWMutex
	processes map[string]*processRuntime

	wakeSignals   chan struct{}
	sleepPlatform sleepPlatform
	sleepDisabled atomic.Bool
}

type registerResponse struct {
	Runtime        DaemonRuntime               `json:"runtime"`
	Reconciliation DaemonRuntimeReconciliation `json:"reconciliation"`
}

type DaemonRuntimeReconciliation struct {
	Processes []ProcessReconciliationDirective `json:"processes"`
}

type ProcessReconciliationClaim struct {
	ProcessID             string                             `json:"process_id"`
	SupervisorInstanceID  string                             `json:"supervisor_instance_id"`
	Phase                 daemonprotocol.ProcessPhase        `json:"phase"`
	SupervisorLive        bool                               `json:"supervisor_live"`
	ExecutionCommitted    bool                               `json:"execution_committed"`
	ActionAdmissionClosed bool                               `json:"action_admission_closed"`
	ResolvedActionSeq     int64                              `json:"resolved_action_seq"`
	Actions               []ProcessActionReconciliationClaim `json:"actions"`
}

type ProcessActionReconciliationClaim struct {
	ProcessActionID string                           `json:"process_action_id"`
	ActionKind      daemonprotocol.ProcessActionKind `json:"action_kind"`
	Seq             int64                            `json:"seq"`
	Position        daemonprotocol.ActionPosition    `json:"position"`
}

type ProcessReconciliationDirective struct {
	ProcessID            string                                 `json:"process_id"`
	SupervisorInstanceID string                                 `json:"supervisor_instance_id"`
	Disposition          daemonprotocol.ProcessDisposition      `json:"disposition"`
	Actions              []ProcessActionReconciliationDirective `json:"actions"`
}

type ProcessActionReconciliationDirective struct {
	ProcessActionID string                           `json:"process_action_id"`
	ActionKind      daemonprotocol.ProcessActionKind `json:"action_kind"`
	Seq             int64                            `json:"seq"`
	Payload         json.RawMessage                  `json:"payload,omitempty"`
	Disposition     daemonprotocol.ActionDisposition `json:"disposition"`
}

type daemonBootstrap struct {
	InstallationID string `json:"installation_id"`
	MachineID      string `json:"machine_id"`
}

type daemonReportedEvent = daemonprotocol.ReportedEvent

type ProcessAssignment struct {
	Process          Process           `json:"process"`
	ID               string            `json:"process_id"`
	Env              map[string]string `json:"env,omitempty"`
	PreparationError string            `json:"preparation_error,omitempty"`
	WaitMs           int               `json:"wait_ms,omitempty"`
	TimeoutSeconds   int               `json:"timeout_seconds"`
}

type Process struct {
	Command       string                   `json:"command"`
	ShellSelector processcmd.ShellSelector `json:"shell_selector"`
	Cwd           string                   `json:"cwd"`
	IOMode        processcmd.IOMode        `json:"io_mode"`
}

type ProcessAction struct {
	ActionKind daemonprotocol.ProcessActionKind `json:"action_kind"`
	Seq        int64                            `json:"seq"`
	Payload    json.RawMessage                  `json:"payload"`
	ID         string                           `json:"process_action_id"`
}

type processRunner interface {
	Status(context.Context) error
	StartOnce(context.Context) error
	ApplyOnce(context.Context, ProcessAction) error
	CloseUngranted(context.Context) error
	Terminate(context.Context, string) error
	Done() <-chan struct{}
	IsDone() bool
}

type processRunnerLauncher interface {
	Prepare(
		context.Context,
		*Client,
		ProcessAssignment,
	) (*processRuntime, error)
}

var errUnresolvedProcessPreparation = errors.New(
	"process preparation retained unresolved local state",
)

type processStartOutcome string

const (
	processStarted    processStartOutcome = "started"
	processNotStarted processStartOutcome = "not_started"
	processAmbiguous  processStartOutcome = "ambiguous"
)

type processActionResult struct {
	EventType          daemonprotocol.ReportedEventType
	Result             json.RawMessage
	StateReasonCode    string
	StateReasonMessage string
}

type processRunnerExit struct {
	State              daemonprotocol.ProcessState
	ExitCode           *int
	ExitSignal         string
	StateReasonCode    string
	StateReasonMessage string
	WaitErr            error
	EndedAt            time.Time
}

type processClosureResult struct {
	WaitErr        error
	ContainmentErr error
}

type processRuntime struct {
	processID            string
	supervisorInstanceID string
	supervisorPID        int
	runner               processRunner
	cleanupOnly          bool
}

type localProcessRunner struct {
	cmd                 *exec.Cmd
	startErr            error
	containment         atomic.Pointer[processContainment]
	startedAt           time.Time
	stdin               *os.File
	stdinMu             sync.Mutex
	stdinOK             bool
	ptyMode             bool
	output              processOutput
	outputDone          chan struct{}
	outputErr           error
	terminalResultReady chan struct{}
	terminalResultOnce  sync.Once

	errMu sync.Mutex
	exit  processRunnerExit

	terminalMu sync.Mutex
	terminal   processRunnerExit
}

type processInputWriteResult struct {
	written int
	err     error
}

const (
	defaultProcessOutputBufferBytes = 100 * 1024 * 1024
	processInputWriteTimeout        = 10 * time.Second
)

var errPTYUnsupported = errors.New(
	"pty-backed processes are not supported on this platform",
)

var errInterruptUnsupported = errors.New(
	"interrupt-mode stop is not supported on this platform",
)

func (r *localProcessRunner) publishTerminalResult(exit processRunnerExit) {
	r.errMu.Lock()
	r.exit = exit
	r.errMu.Unlock()
	r.terminalResultOnce.Do(func() { close(r.terminalResultReady) })
}

func (r *localProcessRunner) hasTerminalResult() bool {
	select {
	case <-r.terminalResultReady:
		return true
	default:
		return false
	}
}

func (r *localProcessRunner) terminalResultSignal() <-chan struct{} {
	return r.terminalResultReady
}

func (r *localProcessRunner) terminalResult() processRunnerExit {
	r.errMu.Lock()
	defer r.errMu.Unlock()
	return r.exit
}

func (r *localProcessRunner) sourceTimeNow() time.Time {
	if r == nil || r.startedAt.IsZero() {
		return time.Time{}
	}
	return r.startedAt.Add(time.Since(r.startedAt)).UTC()
}

func (r *localProcessRunner) WriteInput(
	ctx context.Context,
	data string,
) error {
	r.stdinMu.Lock()
	defer r.stdinMu.Unlock()
	if !r.stdinOK {
		return errors.New("stdin is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return errors.New("stdin write requires a bounded deadline")
	}
	deadlineErr := r.stdin.SetWriteDeadline(deadline)
	if deadlineErr != nil && !errors.Is(deadlineErr, os.ErrNoDeadline) {
		r.stdinOK = false
		return errors.Join(
			fmt.Errorf("bound stdin write: %w", deadlineErr),
			r.stdin.Close(),
		)
	}
	// A PTY may accept SetWriteDeadline yet still block in Write.
	result := make(chan processInputWriteResult, 1)
	go func() {
		n, err := r.stdin.WriteString(data)
		result <- processInputWriteResult{written: n, err: err}
	}()
	var written int
	var writeErr error
	var timeoutErr error
	select {
	case completed := <-result:
		written, writeErr = completed.written, completed.err
	case <-ctx.Done():
		select {
		case completed := <-result:
			written, writeErr = completed.written, completed.err
		default:
			timeoutErr = ctx.Err()
		}
	}
	if errors.Is(writeErr, os.ErrDeadlineExceeded) {
		timeoutErr = context.DeadlineExceeded
		writeErr = nil
	}
	if timeoutErr != nil {
		r.stdinOK = false
		closeErr := r.stdin.Close()
		var terminateErr error
		if r.ptyMode {
			r.setTerminalOverride(processRunnerExit{
				State:              daemonprotocol.ProcessStateUnknown,
				StateReasonCode:    "stdin_write_timeout",
				StateReasonMessage: "a blocked stdin write forced process termination",
			})
			terminateCtx, cancel := context.WithTimeout(
				context.WithoutCancel(ctx),
				processInputWriteTimeout,
			)
			terminateErr = requestTerminateProcessCommand(terminateCtx, r)
			cancel()
		}

		select {
		case completed := <-result:
			written = completed.written
			if !errors.Is(completed.err, os.ErrClosed) &&
				!errors.Is(completed.err, os.ErrDeadlineExceeded) {
				writeErr = completed.err
			}
		default:
		}
		return errors.Join(
			fmt.Errorf(
				"stdin write stopped after %d of %d bytes: %w",
				written,
				len(data),
				timeoutErr,
			),
			writeErr,
			closeErr,
			terminateErr,
		)
	}
	if writeErr == nil && written == len(data) {
		if deadlineErr == nil {
			deadlineErr = r.stdin.SetWriteDeadline(time.Time{})
		}
		if deadlineErr != nil &&
			!errors.Is(deadlineErr, os.ErrNoDeadline) {
			r.stdinOK = false
			_ = r.stdin.Close()
		}
		return nil
	}
	if writeErr == nil {
		writeErr = io.ErrShortWrite
	}
	r.stdinOK = false
	return errors.Join(
		fmt.Errorf(
			"stdin write stopped after %d of %d bytes: %w",
			written,
			len(data),
			writeErr,
		),
		r.stdin.Close(),
	)
}

func (r *localProcessRunner) CloseInput() error {
	if r.ptyMode {
		return errors.New(
			"close_stdin is not supported for pty-backed processes",
		)
	}
	r.stdinMu.Lock()
	defer r.stdinMu.Unlock()
	if !r.stdinOK {
		return nil
	}
	r.stdinOK = false
	return r.stdin.Close()
}

func (r *localProcessRunner) Interrupt() error {
	return interruptProcessCommand(r)
}

func (r *localProcessRunner) terminate(ctx context.Context) error {
	return requestTerminateProcessCommand(ctx, r)
}

func (r *localProcessRunner) terminateForAction(
	ctx context.Context,
) error {
	r.setTerminalOverride(processRunnerExit{
		State:              daemonprotocol.ProcessStateKilled,
		StateReasonCode:    "terminate_requested",
		StateReasonMessage: "terminate-mode stop requested",
	})
	return requestTerminateProcessCommand(ctx, r)
}

func (r *localProcessRunner) Slice(
	cursor *int64,
	maxBytes int,
) (string, int64, int64, bool, error) {
	var useCursor int64
	if cursor != nil {
		useCursor = *cursor
	}
	return r.output.Slice(useCursor, maxBytes)
}

func (r *localProcessRunner) waitOutputDrain() error {
	if r.outputDone == nil {
		return r.output.Close()
	}
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-r.outputDone:
		return errors.Join(r.outputErr, r.output.Close())
	case <-timer.C:
		return errors.New("timed out waiting for process output to finish")
	}
}

func (r *localProcessRunner) setTerminalOverride(exit processRunnerExit) {
	r.terminalMu.Lock()
	defer r.terminalMu.Unlock()
	if r.terminal.State == "" {
		r.terminal = exit
	}
}

func (r *localProcessRunner) exitFromWait(
	waitErr error,
	reason string,
) processRunnerExit {
	exit := runnerExitFromWait(waitErr, reason)
	exit.EndedAt = r.sourceTimeNow()
	r.terminalMu.Lock()
	override := r.terminal
	r.terminalMu.Unlock()
	if override.State == "" {
		return exit
	}
	if override.EndedAt.IsZero() {
		override.EndedAt = exit.EndedAt
	}
	override.WaitErr = waitErr
	override.ExitSignal = exit.ExitSignal
	return override
}
