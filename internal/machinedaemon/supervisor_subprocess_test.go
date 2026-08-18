package machinedaemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
	"github.com/omnara-ai/omnara/internal/processaction"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == runnerSubcommand {
		lockFD, err := strconv.Atoi(os.Args[3])
		if err == nil {
			err = RunCommandSupervisorFromBootstrap(
				context.Background(),
				os.Args[2],
				lockFD,
			)
		}
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type detachedSupervisorTestFixture struct {
	client   *Client
	runtime  *processRuntime
	store    *statedb.Store
	accepted bool
}

func newDetachedSupervisorTestFixture(
	t *testing.T,
	ctx context.Context,
	assignment ProcessAssignment,
) *detachedSupervisorTestFixture {
	t.Helper()
	return newDetachedSupervisorTestFixtureWithConfig(
		t,
		ctx,
		Config{},
		assignment,
	)
}

func newDetachedSupervisorTestFixtureWithConfig(
	t *testing.T,
	ctx context.Context,
	cfg Config,
	assignment ProcessAssignment,
) *detachedSupervisorTestFixture {
	t.Helper()
	if cfg.OmnaraHome == "" {
		cfg.OmnaraHome = t.TempDir()
	}
	if cfg.RunnerPath == "" {
		cfg.RunnerPath = os.Getenv("PATH")
	}
	client := New(cfg, nil, nil)
	client.bootstrap = daemonBootstrap{
		InstallationID: "ins_" + assignment.ID,
		MachineID:      "mch_" + assignment.ID,
	}
	prepared, err := client.runnerLauncher.Prepare(
		ctx,
		&client,
		assignment,
	)
	if err != nil {
		t.Fatalf("prepare detached supervisor: %v", err)
	}
	store, err := client.stateStore(ctx)
	if err != nil {
		t.Fatalf("open detached supervisor state: %v", err)
	}
	fixture := &detachedSupervisorTestFixture{
		client:  &client,
		runtime: prepared,
		store:   store,
	}
	t.Cleanup(func() {
		if !fixture.runtime.runner.IsDone() {
			cleanupCtx, cancel := context.WithTimeout(
				context.Background(),
				5*time.Second,
			)
			if fixture.accepted {
				_ = fixture.runtime.runner.StartOnce(cleanupCtx)
				_ = fixture.runtime.runner.Terminate(
					cleanupCtx,
					"test_cleanup",
				)
			} else {
				_ = fixture.runtime.runner.CloseUngranted(cleanupCtx)
			}
			cancel()
			select {
			case <-fixture.runtime.runner.Done():
			case <-time.After(5 * time.Second):
				t.Errorf(
					"detached supervisor %s did not stop during cleanup",
					assignment.ID,
				)
			}
		}
		fixture.client.closeState()
	})
	return fixture
}

func (f *detachedSupervisorTestFixture) acceptAndStart(
	t *testing.T,
	ctx context.Context,
) {
	t.Helper()
	if err := f.store.MarkAccepted(
		ctx,
		f.runtime.processID,
		f.runtime.supervisorInstanceID,
	); err != nil {
		t.Fatalf("mark detached supervisor accepted: %v", err)
	}
	f.accepted = true
	if err := f.runtime.runner.StartOnce(ctx); err != nil {
		t.Fatalf("start detached supervisor: %v", err)
	}
	f.client.addProcess(f.runtime)
}

func (f *detachedSupervisorTestFixture) waitClosed(
	t *testing.T,
	timeout time.Duration,
) statedb.Process {
	t.Helper()
	f.waitDone(t, timeout)
	process, found, err := f.store.Process(
		context.Background(),
		f.runtime.processID,
	)
	if err != nil || !found {
		t.Fatalf("read closed process state: found=%t err=%v", found, err)
	}
	if !process.LocalClosed {
		t.Fatalf("supervisor exited before local closure: %+v", process)
	}
	return process
}

func (f *detachedSupervisorTestFixture) waitDone(
	t *testing.T,
	timeout time.Duration,
) {
	t.Helper()
	select {
	case <-f.runtime.runner.Done():
	case <-time.After(timeout):
		t.Fatalf(
			"detached supervisor %s did not close",
			f.runtime.processID,
		)
	}
	if _, found := f.client.localProcess(f.runtime.processID); found {
		t.Fatalf(
			"detached supervisor %s left a stale in-memory process",
			f.runtime.processID,
		)
	}
}

func (f *detachedSupervisorTestFixture) waitForOutput(
	t *testing.T,
	ctx context.Context,
	want string,
) {
	t.Helper()
	machine, err := f.client.machineStore()
	if err != nil {
		t.Fatal(err)
	}
	path, err := machine.OutputBufferPath(f.runtime.processID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var last string
	for {
		output, _, readErr := readTestProcessOutputFile(path)
		if readErr == nil && strings.Contains(string(output), want) {
			return
		}
		if readErr != nil {
			last = readErr.Error()
		} else {
			last = string(output)
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"process output did not contain %q: %s",
				want,
				last,
			)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for process output %q: %v", want, ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (f *detachedSupervisorTestFixture) terminalEvent(
	t *testing.T,
) (statedb.Report, daemonReportedEvent) {
	t.Helper()
	report, found, err := f.store.ReportBySlot(
		context.Background(),
		statedb.ReportProcessTerminal,
		f.runtime.processID,
		"",
	)
	if err != nil || !found {
		t.Fatalf("read terminal report: found=%t err=%v", found, err)
	}
	var event daemonReportedEvent
	if err := json.Unmarshal(report.Body, &event); err != nil {
		t.Fatalf("decode terminal report: %v", err)
	}
	return report, event
}

func TestRestartReconciliationStartsSupervisorAndAppliesActionOnce(
	t *testing.T,
) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const (
		installationID = "ins_real_supervisor"
		machineID      = "mch_real_supervisor"
		processID      = "prc_real_supervisor"
	)
	root := t.TempDir()
	commandDir := t.TempDir()
	markerPath := commandDir + string(os.PathSeparator) + "side-effect.txt"
	actionMarkerPath := commandDir +
		string(os.PathSeparator) +
		"action-side-effect.txt"
	command := `printf x >> "$MARKER"; IFS= read -r line; printf %s "$line" >> "$ACTION_MARKER"; printf supervisor-output`
	if runtime.GOOS == "windows" {
		command = `set /p =x<nul >> "%MARKER%" & set /p line= & ` +
			`set /p =%line%<nul >> "%ACTION_MARKER%" & ` +
			`set /p =supervisor-output<nul`
	}

	firstClient := New(
		Config{
			OmnaraHome: root,
			RunnerPath: os.Getenv("PATH"),
		},
		nil,
		nil,
	)
	firstClient.bootstrap = daemonBootstrap{
		InstallationID: installationID,
		MachineID:      machineID,
	}
	prepared, err := firstClient.runnerLauncher.Prepare(
		ctx,
		&firstClient,
		ProcessAssignment{
			ID: processID,
			Process: Process{
				Command:       command,
				ShellSelector: "default",
				Cwd:           commandDir,
				IOMode:        "pipe",
			},
			Env: map[string]string{
				"MARKER":        markerPath,
				"ACTION_MARKER": actionMarkerPath,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	accepted := false
	t.Cleanup(func() {
		if prepared.runner.IsDone() {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cleanupCancel()
		if accepted {
			_ = prepared.runner.Terminate(cleanupCtx, "test_cleanup")
		} else {
			_ = prepared.runner.CloseUngranted(cleanupCtx)
		}
	})
	firstClient.closeState()

	secondClient := New(
		Config{
			OmnaraHome: root,
			RunnerPath: os.Getenv("PATH"),
		},
		nil,
		nil,
	)
	secondClient.bootstrap = firstClient.bootstrap
	startup, err := secondClient.scanLocalProcessesForRegistration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(startup.Claims) != 1 ||
		startup.Claims[0].ProcessID != processID ||
		startup.Claims[0].Phase != statedb.ProcessPrepared ||
		!startup.Claims[0].SupervisorLive {
		startup.releaseResources()
		t.Fatalf("prepared restart claim = %+v", startup.Claims)
	}
	err = secondClient.applyRegistrationReconciliation(
		ctx,
		&startup,
		DaemonRuntimeReconciliation{
			Processes: []ProcessReconciliationDirective{{
				ProcessID:            processID,
				SupervisorInstanceID: prepared.supervisorInstanceID,
				Disposition:          "start",
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	accepted = true
	secondClient.closeState()

	thirdClient := New(
		Config{
			OmnaraHome: root,
			RunnerPath: os.Getenv("PATH"),
		},
		nil,
		nil,
	)
	thirdClient.bootstrap = firstClient.bootstrap
	actionStartup, err := thirdClient.scanLocalProcessesForRegistration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const actionID = "act_real_supervisor_write"
	actionPayload := json.RawMessage(`{"data":"y\n"}`)
	err = thirdClient.applyRegistrationReconciliation(
		ctx,
		&actionStartup,
		DaemonRuntimeReconciliation{
			Processes: []ProcessReconciliationDirective{{
				ProcessID:            processID,
				SupervisorInstanceID: prepared.supervisorInstanceID,
				Disposition:          "retain",
				Actions: []ProcessActionReconciliationDirective{{
					ProcessActionID: actionID,
					ActionKind:      "write",
					Seq:             1,
					Payload:         actionPayload,
					Disposition:     "apply",
				}},
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	actionCtx, cancelActions := context.WithCancel(ctx)
	transport := newDaemonSocketTransport(
		&thirdClient,
		DaemonRuntime{},
		actionStartup,
	)
	transport.resumeStartupActions(actionCtx)
	defer func() {
		cancelActions()
		transport.stopAndWait(func() {})
	}()
	defer thirdClient.closeState()

	store, err := thirdClient.stateStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-prepared.runner.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor retained itself while reports were unacknowledged")
	}
	terminal, found, err := store.Process(ctx, processID)
	if err != nil || !found {
		t.Fatalf("read closed process state: found=%t err=%v", found, err)
	}
	if terminal.Phase != statedb.ProcessTerminal || !terminal.LocalClosed {
		t.Fatalf("supervisor did not durably close: %+v", terminal)
	}
	actionReport, found, err := store.ReportBySlot(
		ctx,
		statedb.ReportActionTerminal,
		processID,
		actionID,
	)
	if err != nil || !found {
		t.Fatalf("read frozen action report: found=%t err=%v", found, err)
	}
	if !terminal.ExecCommitted || terminal.ContainmentKind == "" ||
		!terminal.ContainmentEmpty {
		t.Fatalf("terminal process state = %+v", terminal)
	}
	var actionEvent daemonReportedEvent
	if err := json.Unmarshal(actionReport.Body, &actionEvent); err != nil {
		t.Fatal(err)
	}
	if actionEvent.Type != "process_action_applied" {
		t.Fatalf("frozen action report = %+v", actionReport)
	}

	marker, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(marker) != "x" {
		t.Fatalf("external side effect = %q, want exactly one x", marker)
	}
	actionMarker, err := os.ReadFile(actionMarkerPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(actionMarker) != "y" {
		t.Fatalf(
			"action external side effect = %q, want exactly one y",
			actionMarker,
		)
	}

	reports, err := store.ReportsForProcess(ctx, processID)
	if err != nil {
		t.Fatal(err)
	}
	var sawStarted, sawTerminal bool
	for _, report := range reports {
		switch report.Kind {
		case statedb.ReportProcessStarted:
			sawStarted = true
		case statedb.ReportProcessTerminal:
			sawTerminal = true
			var event daemonReportedEvent
			if err := json.Unmarshal(report.Body, &event); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(event.Result), "supervisor-output") {
				t.Fatalf("terminal result lost process output: %s", event.Result)
			}
		case statedb.ReportActionTerminal:
		}
	}
	if !sawStarted || !sawTerminal {
		t.Fatalf("frozen process reports = %+v", reports)
	}
}

func TestRestartReconciliationClosesPreparedSupervisor(
	t *testing.T,
) {
	if runtime.GOOS == "windows" {
		t.Skip("detached supervisor execution is not complete on Windows")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	const (
		installationID = "ins_close_prepared_restart"
		machineID      = "mch_close_prepared_restart"
		processID      = "prc_close_prepared_restart"
	)
	root := t.TempDir()
	first := New(
		Config{
			OmnaraHome: root,
			RunnerPath: os.Getenv("PATH"),
		},
		nil,
		nil,
	)
	first.bootstrap = daemonBootstrap{
		InstallationID: installationID,
		MachineID:      machineID,
	}
	prepared, err := first.runnerLauncher.Prepare(
		ctx,
		&first,
		ProcessAssignment{
			ID: processID,
			Process: Process{
				Command:       "printf should-not-run",
				ShellSelector: "default",
				Cwd:           t.TempDir(),
				IOMode:        "pipe",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if !prepared.runner.IsDone() {
			cleanupCtx, cleanupCancel := context.WithTimeout(
				context.Background(),
				5*time.Second,
			)
			_ = prepared.runner.CloseUngranted(cleanupCtx)
			cleanupCancel()
		}
	}()
	first.closeState()

	restarted := New(
		Config{
			OmnaraHome: root,
			RunnerPath: os.Getenv("PATH"),
		},
		nil,
		nil,
	)
	restarted.bootstrap = first.bootstrap
	defer restarted.closeState()
	startup, err := restarted.scanLocalProcessesForRegistration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(startup.Claims) != 1 ||
		startup.Claims[0].Phase != statedb.ProcessPrepared {
		startup.releaseResources()
		t.Fatalf("prepared restart claims = %+v", startup.Claims)
	}
	if err := restarted.applyRegistrationReconciliation(
		ctx,
		&startup,
		DaemonRuntimeReconciliation{
			Processes: []ProcessReconciliationDirective{{
				ProcessID:            processID,
				SupervisorInstanceID: prepared.supervisorInstanceID,
				Disposition:          "close_preparation",
			}},
		},
	); err != nil {
		t.Fatal(err)
	}
	select {
	case <-prepared.runner.Done():
	case <-ctx.Done():
		t.Fatal("reconstructed supervisor did not close")
	}
	store, err := restarted.stateStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Process(ctx, processID); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("closed prepared supervisor retained process state")
	}
}

func TestAuthenticationRejectionStopsDetachedAcceptedProcess(
	t *testing.T,
) {
	if runtime.GOOS == "windows" {
		t.Skip("detached process containment is not complete on Windows")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const (
		installationID = "ins_auth_rejected_teardown"
		machineID      = "mch_auth_rejected_teardown"
		processID      = "prc_auth_rejected_teardown"
	)
	root := t.TempDir()
	commandDir := t.TempDir()
	markerPath := filepath.Join(commandDir, "started")
	first := New(
		Config{
			OmnaraHome: root,
			RunnerPath: os.Getenv("PATH"),
		},
		nil,
		nil,
	)
	first.bootstrap = daemonBootstrap{
		InstallationID: installationID,
		MachineID:      machineID,
	}
	prepared, err := first.runnerLauncher.Prepare(
		ctx,
		&first,
		ProcessAssignment{
			ID: processID,
			Process: Process{
				Command:       `printf started > "$MARKER"; sleep 30`,
				ShellSelector: "default",
				Cwd:           commandDir,
				IOMode:        "pipe",
			},
			Env: map[string]string{"MARKER": markerPath},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if !prepared.runner.IsDone() {
			cleanupCtx, cleanupCancel := context.WithTimeout(
				context.Background(),
				5*time.Second,
			)
			_ = prepared.runner.Terminate(cleanupCtx, "test_cleanup")
			cleanupCancel()
		}
	}()
	store, err := first.stateStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAccepted(
		ctx,
		processID,
		prepared.supervisorInstanceID,
	); err != nil {
		t.Fatal(err)
	}
	if err := prepared.runner.StartOnce(ctx); err != nil {
		t.Fatalf("start accepted process: %v", err)
	}
	first.addProcess(prepared)
	for {
		if _, err := os.Stat(markerPath); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if err := sleepContext(ctx, 10*time.Millisecond); err != nil {
			t.Fatal("accepted process did not start")
		}
	}
	first.closeState()

	rejectingServer := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "machine token rejected", http.StatusUnauthorized)
		},
	))
	defer rejectingServer.Close()
	var logs bytes.Buffer
	restarted := New(
		Config{
			APIURL:                 rejectingServer.URL,
			MachineToken:           "rejected",
			OmnaraHome:             root,
			ExpectedInstallationID: installationID,
			ExpectedMachineID:      machineID,
			RunnerPath:             os.Getenv("PATH"),
		},
		nil,
		slog.New(slog.NewTextHandler(&logs, nil)),
	)
	if err := restarted.Run(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-prepared.runner.Done():
	case <-ctx.Done():
		t.Fatal("authentication rejection left the process supervisor alive")
	}
	machine, err := restarted.machineStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(machine.MachineDir()); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf(
			"decommissioned local machine state still exists: %v",
			err,
		)
	}
	if got := logs.String(); strings.Contains(
		got,
		"best-effort local process shutdown was incomplete",
	) {
		t.Fatalf("clean authority-loss shutdown warning = %q", got)
	}
}

func TestDetachedSupervisorArtifactsExcludeSecretsAndActionPayloads(
	t *testing.T,
) {
	if runtime.GOOS == "windows" {
		t.Skip("detached process containment is not complete on Windows")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	const (
		machineCredential    = "machine-credential-must-not-persist-7f51"
		launchSecret         = "launch-secret-must-not-persist-3a92"
		reservedLaunchSecret = "reserved-launch-secret-must-not-persist-1da4"
		actionPayload        = "action-payload-must-not-persist-8c14"
	)
	root := t.TempDir()
	workloadEnvMarker := filepath.Join(t.TempDir(), "workload-env-filtered")
	fixture := newDetachedSupervisorTestFixtureWithConfig(
		t,
		ctx,
		Config{
			MachineToken: machineCredential,
			OmnaraHome:   root,
		},
		ProcessAssignment{
			ID: "prc_no_persisted_secrets",
			Process: Process{
				Command: `test -n "$OMNARA_HOME" && ` +
					`test -n "$LAUNCH_TEST_SECRET" && ` +
					`test -z "${OMNARA_TEST_SECRET+x}" && ` +
					`printf ok > "$WORKLOAD_ENV_MARKER" && sleep 30`,
				ShellSelector: "default",
				Cwd:           t.TempDir(),
				IOMode:        "pipe",
			},
			Env: map[string]string{
				"LAUNCH_TEST_SECRET":  launchSecret,
				"OMNARA_TEST_SECRET":  reservedLaunchSecret,
				"WORKLOAD_ENV_MARKER": workloadEnvMarker,
			},
		},
	)
	fixture.acceptAndStart(t, ctx)
	for {
		if _, err := os.Stat(workloadEnvMarker); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if err := sleepContext(ctx, 10*time.Millisecond); err != nil {
			t.Fatal("workload did not confirm its filtered environment")
		}
	}
	payload, err := json.Marshal(map[string]string{"data": actionPayload})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.runtime.runner.ApplyOnce(ctx, ProcessAction{
		ID:         "act_no_persisted_payload",
		ActionKind: "write",
		Seq:        1,
		Payload:    payload,
	}); err != nil {
		t.Fatalf("apply secret-bearing action: %v", err)
	}
	if err := fixture.runtime.runner.Terminate(
		ctx,
		"test_artifact_scan",
	); err != nil {
		t.Fatalf("terminate process before artifact scan: %v", err)
	}
	fixture.waitDone(t, 10*time.Second)
	if err := fixture.client.closeState(); err != nil {
		t.Fatalf("close process state before artifact scan: %v", err)
	}

	sentinels := []string{machineCredential, launchSecret, reservedLaunchSecret, actionPayload}
	if err := filepath.WalkDir(
		root,
		func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !entry.Type().IsRegular() {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, sentinel := range sentinels {
				if bytes.Contains(body, []byte(sentinel)) {
					return fmt.Errorf(
						"daemon artifact %s contains %q",
						path,
						sentinel,
					)
				}
			}
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptedProcessCanTerminateBeforeSpawn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("detached process containment is not complete on Windows")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	markerPath := filepath.Join(t.TempDir(), "should-not-exist")
	fixture := newDetachedSupervisorTestFixture(
		t,
		ctx,
		ProcessAssignment{
			ID: "prc_accepted_terminate_before_spawn",
			Process: Process{
				Command:       `printf ran > "$MARKER"`,
				ShellSelector: "default",
				Cwd:           t.TempDir(),
				IOMode:        "pipe",
			},
			Env: map[string]string{"MARKER": markerPath},
		},
	)
	if err := fixture.store.MarkAccepted(
		ctx,
		fixture.runtime.processID,
		fixture.runtime.supervisorInstanceID,
	); err != nil {
		t.Fatal(err)
	}
	fixture.accepted = true
	if err := fixture.runtime.runner.Terminate(
		ctx,
		"server_requested",
	); err != nil {
		t.Fatal(err)
	}
	process := fixture.waitClosed(t, 10*time.Second)
	if process.ExecCommitted {
		t.Fatalf(
			"pre-spawn termination crossed execution boundary: %+v",
			process,
		)
	}
	if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-spawn terminated command ran: %v", err)
	}
	_, event := fixture.terminalEvent(t)
	if event.State != "unknown" ||
		event.StateReasonCode != "server_requested" {
		t.Fatalf("pre-spawn terminal evidence = %+v", event)
	}
}

func TestDetachedSupervisorPreparationErrorNeedsNoCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	fixture := newDetachedSupervisorTestFixture(
		t,
		ctx,
		ProcessAssignment{
			ID:               "prc_known_start_failure",
			PreparationError: "launch package could not be resolved",
		},
	)
	fixture.acceptAndStart(t, ctx)
	process := fixture.waitClosed(t, 10*time.Second)
	if process.ExecCommitted ||
		process.ContainmentKind != "" ||
		!process.ContainmentEmpty ||
		process.Phase != statedb.ProcessTerminal {
		t.Fatalf("known start failure state = %+v", process)
	}
	machine, err := fixture.client.machineStore()
	if err != nil {
		t.Fatal(err)
	}
	outputPath, err := machine.OutputBufferPath(process.ProcessID)
	if err != nil {
		t.Fatal(err)
	}
	output, baseOffset, err := readTestProcessOutputFile(outputPath)
	if err != nil || len(output) != 0 || baseOffset != 0 {
		t.Fatalf(
			"retained failed-start output = %q at %d, err=%v",
			output,
			baseOffset,
			err,
		)
	}
	reports, err := fixture.store.ReportsForProcess(ctx, process.ProcessID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 ||
		reports[0].Kind != statedb.ReportProcessTerminal {
		t.Fatalf("known start failure reports = %+v", reports)
	}
	var terminal daemonReportedEvent
	if err := json.Unmarshal(reports[0].Body, &terminal); err != nil {
		t.Fatal(err)
	}
	if terminal.State != "failed" ||
		terminal.StateReasonCode != "start_failed" ||
		!terminal.StartedAt.IsZero() ||
		!terminal.EndedAt.IsZero() {
		t.Fatalf("known start failure evidence = %+v", terminal)
	}
}

func TestDetachedSupervisorFastExitFreezesOnlyTerminalReport(t *testing.T) {
	tests := []struct {
		name      string
		exitCode  int
		wantState daemonprotocol.ProcessState
	}{
		{name: "success", exitCode: 0, wantState: "exited"},
		{name: "failure", exitCode: 7, wantState: "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(
				context.Background(),
				15*time.Second,
			)
			defer cancel()

			command := fmt.Sprintf("exit %d", tt.exitCode)
			if runtime.GOOS == "windows" {
				command = fmt.Sprintf("exit /b %d", tt.exitCode)
			}
			fixture := newDetachedSupervisorTestFixture(
				t,
				ctx,
				ProcessAssignment{
					ID: "prc_fast_terminal_only_" + tt.name,
					Process: Process{
						Command:       command,
						ShellSelector: "default",
						Cwd:           t.TempDir(),
						IOMode:        "pipe",
					},
					WaitMs: 1000,
				},
			)
			fixture.acceptAndStart(t, ctx)
			process := fixture.waitClosed(t, 10*time.Second)
			reports, err := fixture.store.ReportsForProcess(
				ctx,
				process.ProcessID,
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(reports) != 1 ||
				reports[0].Kind != statedb.ReportProcessTerminal {
				t.Fatalf(
					"fast process reports = %+v, want one terminal report",
					reports,
				)
			}
			var event daemonReportedEvent
			if err := json.Unmarshal(reports[0].Body, &event); err != nil {
				t.Fatal(err)
			}
			if event.StartedAt.IsZero() ||
				event.EndedAt.Before(event.StartedAt) ||
				event.State != tt.wantState ||
				event.ExitCode == nil ||
				*event.ExitCode != tt.exitCode {
				t.Fatalf(
					"fast terminal evidence = %+v result=%s",
					event,
					event.Result,
				)
			}
		})
	}
}

func TestDetachedSupervisorOutputReadFailureStillFreezesStartedReport(
	t *testing.T,
) {
	if runtime.GOOS == "windows" {
		t.Skip("detached process containment is not complete on Windows")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	fixture := newDetachedSupervisorTestFixture(
		t,
		ctx,
		ProcessAssignment{
			ID: "prc_started_without_initial_output",
			Process: Process{
				Command:       "sleep 30",
				ShellSelector: "default",
				Cwd:           t.TempDir(),
				IOMode:        "pipe",
			},
			WaitMs: 10_000,
		},
	)
	machine, err := fixture.client.machineStore()
	if err != nil {
		t.Fatal(err)
	}
	outputPath, err := machine.OutputBufferPath(fixture.runtime.processID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(outputPath); err != nil {
		t.Fatal(err)
	}

	fixture.acceptAndStart(t, ctx)
	var report statedb.Report
	for {
		var found bool
		report, found, err = fixture.store.ReportBySlot(
			ctx,
			statedb.ReportProcessStarted,
			fixture.runtime.processID,
			"",
		)
		if err != nil {
			t.Fatal(err)
		}
		if found {
			break
		}
		if err := sleepContext(ctx, 10*time.Millisecond); err != nil {
			t.Fatal("started process did not freeze its start report")
		}
	}
	if fixture.runtime.runner.IsDone() {
		t.Fatal("output read failure stopped the running process")
	}
	var event daemonReportedEvent
	if err := json.Unmarshal(report.Body, &event); err != nil {
		t.Fatal(err)
	}
	var result struct {
		State      string `json:"state"`
		Error      string `json:"error"`
		Done       bool   `json:"done"`
		Truncated  bool   `json:"truncated"`
		Cursor     int64  `json:"cursor"`
		NextCursor int64  `json:"next_cursor"`
	}
	if err := json.Unmarshal(event.Result, &result); err != nil {
		t.Fatal(err)
	}
	if event.Type != "process_started" || result.State != "running" ||
		result.Done || !result.Truncated || result.Cursor != 0 ||
		result.NextCursor != 0 ||
		!strings.Contains(result.Error, "initial output could not be read") {
		t.Fatalf("degraded started report = %+v result=%s", event, event.Result)
	}

	if err := fixture.runtime.runner.Terminate(ctx, "test_finished"); err != nil {
		t.Fatal(err)
	}
	fixture.waitClosed(t, 10*time.Second)
}

func TestDetachedSupervisorRejectsWrongIPCIdentityBeforeStart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	fixture := newDetachedSupervisorTestFixture(
		t,
		ctx,
		ProcessAssignment{
			ID: "prc_ipc_identity",
			Process: Process{
				Command:       "echo should-not-run",
				ShellSelector: "default",
				Cwd:           t.TempDir(),
				IOMode:        "pipe",
			},
		},
	)
	correct, ok := fixture.runtime.runner.(*ipcProcessRunner)
	if !ok {
		t.Fatalf("detached runner type = %T", fixture.runtime.runner)
	}
	for _, test := range []struct {
		name                 string
		supervisorToken      string
		supervisorInstanceID string
	}{
		{
			name:                 "supervisor_token",
			supervisorToken:      "wrong-supervisor-token",
			supervisorInstanceID: correct.supervisorInstanceID,
		},
		{
			name:                 "supervisor_instance_id",
			supervisorToken:      correct.supervisorToken,
			supervisorInstanceID: "wrong-supervisor-instance",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			impostor := &ipcProcessRunner{
				endpoint:             correct.endpoint,
				supervisorToken:      test.supervisorToken,
				supervisorInstanceID: test.supervisorInstanceID,
				done:                 make(chan struct{}),
			}
			if err := impostor.StartOnce(ctx); !errors.Is(
				err,
				errRunnerIdentityMismatch,
			) {
				t.Fatalf("impostor start error = %v", err)
			}
		})
	}
	process, found, err := fixture.store.Process(
		ctx,
		fixture.runtime.processID,
	)
	if err != nil || !found {
		t.Fatalf("read prepared process state: found=%t err=%v", found, err)
	}
	if process.ExecCommitted ||
		process.Phase != statedb.ProcessPrepared ||
		process.ContainmentKind != "" {
		t.Fatalf("IPC impostor mutated process state: %+v", process)
	}
	if err := fixture.runtime.runner.CloseUngranted(ctx); err != nil {
		t.Fatal(err)
	}
	fixture.waitDone(t, 5*time.Second)
}

func TestDetachedSupervisorCloseUngrantedRetainsLifetimeLock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	fixture := newDetachedSupervisorTestFixture(
		t,
		ctx,
		ProcessAssignment{
			ID: "prc_close_ungranted_lock",
			Process: Process{
				Command:       "echo should-not-run",
				ShellSelector: "default",
				Cwd:           t.TempDir(),
				IOMode:        "pipe",
			},
		},
	)
	machine, err := fixture.client.machineStore()
	if err != nil {
		t.Fatal(err)
	}
	lockPath, err := machine.LifetimeLockPath(fixture.runtime.processID)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.runtime.runner.CloseUngranted(ctx); err != nil {
		t.Fatal(err)
	}
	fixture.waitDone(t, 5*time.Second)
	lock, err := localstore.TryAcquireExistingLock(lockPath)
	if err != nil {
		t.Fatalf("acquire retained lifetime lock: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestDetachedSupervisorTimeoutClosesWholeProcessTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("complete Windows process-tree containment is separate work")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	fixture := newDetachedSupervisorTestFixture(
		t,
		ctx,
		ProcessAssignment{
			ID: "prc_detached_timeout",
			Process: Process{
				Command:       "sleep 30 & wait",
				ShellSelector: "default",
				Cwd:           t.TempDir(),
				IOMode:        "pipe",
			},
			TimeoutSeconds: 1,
		},
	)
	fixture.acceptAndStart(t, ctx)
	process := fixture.waitClosed(t, 15*time.Second)
	if !process.ExecCommitted ||
		process.ContainmentKind == "" ||
		!process.ContainmentEmpty ||
		process.Phase != statedb.ProcessTerminal {
		t.Fatalf("timed-out process state = %+v", process)
	}
	_, event := fixture.terminalEvent(t)
	if event.State != "failed" ||
		event.StateReasonCode != "timeout" {
		t.Fatalf("timeout terminal event = %+v", event)
	}
}

func TestDetachedPTYRoundTripAndWriteCloseRejection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PTY execution is not yet supported on Windows")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	fixture := newDetachedSupervisorTestFixture(
		t,
		ctx,
		ProcessAssignment{
			ID: "prc_detached_pty",
			Process: Process{
				Command: `printf '\033[31mready\033[0m\n'; ` +
					`IFS= read -r line; printf 'got:%s\n' "$line"; sleep 30`,
				ShellSelector: "default",
				Cwd:           t.TempDir(),
				IOMode:        "pty",
			},
		},
	)
	fixture.acceptAndStart(t, ctx)
	write := ProcessAction{
		ID:         "act_pty_write",
		ActionKind: "write",
		Seq:        1,
		Payload:    json.RawMessage(`{"data":"hello\n"}`),
	}
	if err := fixture.runtime.runner.ApplyOnce(ctx, write); err != nil {
		t.Fatalf("write PTY input: %v", err)
	}
	writeReport, found, err := fixture.store.ReportBySlot(
		ctx,
		statedb.ReportActionTerminal,
		fixture.runtime.processID,
		write.ID,
	)
	if err != nil || !found {
		t.Fatalf("read PTY write report: found=%t err=%v", found, err)
	}
	var writeEvent daemonReportedEvent
	if err := json.Unmarshal(writeReport.Body, &writeEvent); err != nil {
		t.Fatal(err)
	}
	if writeEvent.Type != "process_action_applied" {
		t.Fatalf("PTY write report = %+v", writeEvent)
	}
	if err := fixture.store.AcknowledgeReport(
		ctx,
		writeReport.ID,
	); err != nil {
		t.Fatal(err)
	}

	closeInput := ProcessAction{
		ID:         "act_pty_close_stdin",
		ActionKind: "write",
		Seq:        2,
		Payload:    json.RawMessage(`{"close_stdin":true}`),
	}
	if err := fixture.runtime.runner.ApplyOnce(
		ctx,
		closeInput,
	); err != nil {
		t.Fatalf("record PTY close-input rejection: %v", err)
	}
	closeReport, found, err := fixture.store.ReportBySlot(
		ctx,
		statedb.ReportActionTerminal,
		fixture.runtime.processID,
		closeInput.ID,
	)
	if err != nil || !found {
		t.Fatalf("read PTY close-input report: found=%t err=%v", found, err)
	}
	var closeEvent daemonReportedEvent
	if err := json.Unmarshal(closeReport.Body, &closeEvent); err != nil {
		t.Fatal(err)
	}
	if closeEvent.Type != "process_action_failed" ||
		closeEvent.StateReasonCode != "not_supported" {
		t.Fatalf("PTY close-input report = %+v", closeEvent)
	}
	closeMarker, found, err := fixture.store.Action(ctx, closeInput.ID)
	if err != nil || !found {
		t.Fatalf(
			"read PTY close-input boundary: found=%t err=%v",
			found,
			err,
		)
	}
	if closeMarker.EffectCommitted {
		t.Fatalf(
			"PTY close-input rejection crossed its effect boundary: %+v",
			closeMarker,
		)
	}
	if err := fixture.store.AcknowledgeReport(
		ctx,
		closeReport.ID,
	); err != nil {
		t.Fatal(err)
	}
	fixture.waitForOutput(t, ctx, "got:hello")
	if err := fixture.runtime.runner.Terminate(ctx, "test_finished"); err != nil {
		t.Fatal(err)
	}
	fixture.waitClosed(t, 10*time.Second)
	_, event := fixture.terminalEvent(t)
	if !strings.Contains(string(event.Result), "ready") ||
		!strings.Contains(string(event.Result), "got:hello") {
		t.Fatalf("PTY terminal output = %s", event.Result)
	}
}

func TestDetachedSupervisorRejectsUnsafeActionPayloads(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("detached process containment is not complete on Windows")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	fixture := newDetachedSupervisorTestFixture(
		t,
		ctx,
		ProcessAssignment{
			ID: "prc_unsafe_action_payload",
			Process: Process{
				Command:       "sleep 30",
				ShellSelector: "default",
				Cwd:           t.TempDir(),
				IOMode:        "pipe",
			},
		},
	)
	fixture.acceptAndStart(t, ctx)

	for index, test := range []struct {
		name       string
		actionKind daemonprotocol.ProcessActionKind
		payload    json.RawMessage
	}{
		{
			name:       "oversized_write",
			actionKind: "write",
			payload: mustOutboxJSON(t, map[string]any{
				"data": strings.Repeat(
					"x",
					processaction.MaxWriteBytes+1,
				),
			}),
		},
		{
			name:       "unknown_write_field",
			actionKind: "write",
			payload:    json.RawMessage(`{"future":true}`),
		},
		{
			name:       "interrupt_payload",
			actionKind: "interrupt",
			payload:    json.RawMessage(`{"future":true}`),
		},
		{
			name:       "terminate_payload",
			actionKind: "terminate",
			payload:    json.RawMessage(`{"future":true}`),
		},
	} {
		action := ProcessAction{
			ID:         "act_unsafe_" + test.name,
			ActionKind: test.actionKind,
			Seq:        int64(index + 1),
			Payload:    test.payload,
		}
		startedAt := time.Now()
		if err := fixture.runtime.runner.ApplyOnce(ctx, action); err != nil {
			t.Fatalf("%s apply: %v", test.name, err)
		}
		if elapsed := time.Since(startedAt); elapsed > time.Second {
			t.Fatalf("%s validation took %s", test.name, elapsed)
		}
		report, found, err := fixture.store.ReportBySlot(
			ctx,
			statedb.ReportActionTerminal,
			fixture.runtime.processID,
			action.ID,
		)
		if err != nil || !found {
			t.Fatalf(
				"%s report: found=%t err=%v",
				test.name,
				found,
				err,
			)
		}
		var event daemonReportedEvent
		if err := json.Unmarshal(report.Body, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type != "process_action_failed" ||
			event.StateReasonCode != "invalid_payload" {
			t.Fatalf("%s event = %+v", test.name, event)
		}
		marker, found, err := fixture.store.Action(ctx, action.ID)
		if err != nil || !found {
			t.Fatalf(
				"%s boundary: found=%t err=%v",
				test.name,
				found,
				err,
			)
		}
		if marker.EffectCommitted {
			t.Fatalf(
				"%s crossed its effect boundary: %+v",
				test.name,
				marker,
			)
		}
		if err := fixture.store.AcknowledgeReport(
			ctx,
			report.ID,
		); err != nil {
			t.Fatal(err)
		}
	}

	if err := fixture.runtime.runner.Terminate(ctx, "test_finished"); err != nil {
		t.Fatal(err)
	}
	fixture.waitClosed(t, 10*time.Second)
}

func TestDetachedStalledWriteClosesStdinAndReleasesReconciliation(
	t *testing.T,
) {
	if runtime.GOOS == "windows" {
		t.Skip("detached process containment is not complete on Windows")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fixture := newDetachedSupervisorTestFixture(
		t,
		ctx,
		ProcessAssignment{
			ID: "prc_stalled_stdin",
			Process: Process{
				Command:       "sleep 30",
				ShellSelector: "default",
				Cwd:           t.TempDir(),
				IOMode:        "pipe",
			},
		},
	)
	fixture.acceptAndStart(t, ctx)
	action := ProcessAction{
		ID:         "act_stalled_stdin",
		ActionKind: "write",
		Seq:        1,
		Payload: mustOutboxJSON(t, map[string]any{
			"data": strings.Repeat(
				"x",
				processaction.MaxWriteBytes,
			),
		}),
	}
	startedAt := time.Now()
	if err := fixture.runtime.runner.ApplyOnce(ctx, action); err != nil {
		t.Fatalf("bounded stalled write: %v", err)
	}
	elapsed := time.Since(startedAt)
	// Race instrumentation may delay scheduling past the I/O deadline.
	if elapsed < processInputWriteTimeout-2*time.Second ||
		elapsed > 2*processInputWriteTimeout {
		t.Fatalf("stalled write remained blocked for %s", elapsed)
	}
	report, found, err := fixture.store.ReportBySlot(
		ctx,
		statedb.ReportActionTerminal,
		fixture.runtime.processID,
		action.ID,
	)
	if err != nil || !found {
		t.Fatalf("read stalled write report: found=%t err=%v", found, err)
	}
	var event daemonReportedEvent
	if err := json.Unmarshal(report.Body, &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "process_action_unknown" ||
		event.StateReasonCode != "stdin_write_unknown" {
		t.Fatalf("stalled write report = %+v", event)
	}
	select {
	case <-fixture.runtime.runner.Done():
		t.Fatal("stalled pipe write terminated the process")
	case <-time.After(500 * time.Millisecond):
	}
	process, found, err := fixture.store.Process(
		ctx,
		fixture.runtime.processID,
	)
	if err != nil || !found {
		t.Fatalf("read live pipe process state: found=%t err=%v", found, err)
	}
	if process.Phase != statedb.ProcessAccepted ||
		process.ContainmentEmpty ||
		process.LocalClosed {
		t.Fatalf("stalled pipe write closed process state: %+v", process)
	}
	if _, found, err := fixture.store.ReportBySlot(
		ctx,
		statedb.ReportProcessTerminal,
		fixture.runtime.processID,
		"",
	); err != nil || found {
		t.Fatalf(
			"stalled pipe write froze terminal evidence: found=%t err=%v",
			found,
			err,
		)
	}

	runner, ok := fixture.runtime.runner.(*ipcProcessRunner)
	if !ok {
		t.Fatalf("runner type = %T, want *ipcProcessRunner", fixture.runtime.runner)
	}
	reconcileCtx, cancelReconcile := context.WithTimeout(
		ctx,
		2*time.Second,
	)
	if err := runner.BeginReconciliation(reconcileCtx); err != nil {
		cancelReconcile()
		t.Fatalf("stalled write retained reconciliation fence: %v", err)
	}
	if err := runner.EndReconciliation(); err != nil {
		cancelReconcile()
		t.Fatal(err)
	}
	cancelReconcile()
	if err := fixture.runtime.runner.Terminate(ctx, "test_finished"); err != nil {
		t.Fatal(err)
	}
	fixture.waitClosed(t, 10*time.Second)
}

func TestDetachedStalledPTYWriteTerminatesAndReconciles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PTY execution is not yet supported on Windows")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()

	fixture := newDetachedSupervisorTestFixture(
		t,
		ctx,
		ProcessAssignment{
			ID: "prc_stalled_pty_stdin",
			Process: Process{
				Command:       "stty raw -echo; printf ready; sleep 30",
				ShellSelector: "default",
				Cwd:           t.TempDir(),
				IOMode:        "pty",
			},
		},
	)
	fixture.acceptAndStart(t, ctx)
	fixture.waitForOutput(t, ctx, "ready")
	action := ProcessAction{
		ID:         "act_stalled_pty_stdin",
		ActionKind: "write",
		Seq:        1,
		Payload: mustOutboxJSON(t, map[string]any{
			"data": strings.Repeat(
				"x",
				processaction.MaxWriteBytes,
			),
		}),
	}
	startedAt := time.Now()
	if err := fixture.runtime.runner.ApplyOnce(ctx, action); err != nil {
		t.Fatalf("bounded stalled PTY write: %v", err)
	}
	elapsed := time.Since(startedAt)
	if elapsed < processInputWriteTimeout-2*time.Second ||
		elapsed > 2*processInputWriteTimeout+5*time.Second {
		t.Fatalf("stalled PTY write completed after %s", elapsed)
	}

	report, found, err := fixture.store.ReportBySlot(
		ctx,
		statedb.ReportActionTerminal,
		fixture.runtime.processID,
		action.ID,
	)
	if err != nil || !found {
		t.Fatalf("read stalled PTY write report: found=%t err=%v", found, err)
	}
	var event daemonReportedEvent
	if err := json.Unmarshal(report.Body, &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "process_action_unknown" ||
		event.StateReasonCode != "stdin_write_unknown" {
		t.Fatalf("stalled PTY write report = %+v", event)
	}

	process := fixture.waitClosed(t, 10*time.Second)
	if !process.ContainmentEmpty ||
		process.Phase != statedb.ProcessTerminal {
		t.Fatalf("stalled PTY process state = %+v", process)
	}
	_, terminal := fixture.terminalEvent(t)
	if terminal.State != "unknown" ||
		terminal.StateReasonCode != "stdin_write_timeout" {
		t.Fatalf("stalled PTY terminal event = %+v", terminal)
	}

	startup, err := fixture.client.scanLocalProcessesForRegistration(ctx)
	if err != nil {
		t.Fatalf("scan after stalled PTY write: %v", err)
	}
	defer startup.releaseResources()
	if len(startup.Claims) != 1 ||
		startup.Claims[0].ProcessID != fixture.runtime.processID ||
		startup.Claims[0].Phase != statedb.ProcessTerminal ||
		startup.Claims[0].SupervisorLive {
		t.Fatalf("stalled PTY registration claim = %+v", startup.Claims)
	}
}
