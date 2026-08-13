package machinedaemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
)

const (
	runnerSubcommand                  = "__omnara_process_runner"
	supervisorIdentityBootstrapPrefix = "supervisor-"
	omnaraEnvironmentPrefix           = "OMNARA_"
	omnaraHomeEnvironmentName         = "OMNARA_HOME"
)

type supervisorIdentityBootstrap struct {
	OmnaraHome           string `json:"omnara_home"`
	InstallationID       string `json:"installation_id"`
	MachineID            string `json:"machine_id"`
	ProcessID            string `json:"process_id"`
	SupervisorInstanceID string `json:"supervisor_instance_id"`
	SupervisorToken      string `json:"supervisor_token"`
}

type subprocessRunnerLauncher struct{}

func (subprocessRunnerLauncher) Prepare(
	ctx context.Context,
	c *Client,
	assignment ProcessAssignment,
) (*processRuntime, error) {
	if assignment.PreparationError == "" {
		canonicalCwd, err := filepath.Abs(assignment.Process.Cwd)
		if err != nil {
			return nil, err
		}
		assignment.Process.Cwd = canonicalCwd
	}
	machine, err := c.machineStore()
	if err != nil {
		return nil, err
	}
	if err := localstore.EnsurePrivateDir(machine.RunDir()); err != nil {
		return nil, err
	}
	if err := localstore.EnsurePrivateDir(machine.ProcessesDir()); err != nil {
		return nil, err
	}
	endpoint, err := machine.ControlEndpointPath(assignment.ID)
	if err != nil {
		return nil, err
	}
	supervisorInstanceUUID, err := uuid.NewRandom()
	if err != nil {
		return nil, err
	}
	supervisorInstanceID := supervisorInstanceUUID.String()
	supervisorToken, err := newSupervisorToken()
	if err != nil {
		return nil, err
	}
	stateStore, err := c.stateStore(ctx)
	if err != nil {
		return nil, err
	}
	if err := stateStore.ReserveProcess(
		ctx,
		statedb.Process{
			ProcessID:            assignment.ID,
			SupervisorInstanceID: supervisorInstanceID,
			SupervisorToken:      supervisorToken,
		},
	); err != nil {
		return nil, err
	}

	runner := &ipcProcessRunner{
		endpoint:             endpoint,
		supervisorToken:      supervisorToken,
		supervisorInstanceID: supervisorInstanceID,
		done:                 make(chan struct{}),
	}
	runtime := &processRuntime{
		processID:            assignment.ID,
		supervisorInstanceID: supervisorInstanceID,
		runner:               runner,
	}
	var (
		lifetimeLock      *localstore.Lock
		command           *exec.Cmd
		supervisorStarted bool
		supervisorDone    chan struct{}
	)
	fail := func(cause error) error {
		if !supervisorStarted {
			runner.markDone()
		}
		cleanupCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			5*time.Second,
		)
		defer cancel()

		var stopErr error
		stopped := !supervisorStarted
		if supervisorStarted {
			closeCtx, closeCancel := context.WithTimeout(
				cleanupCtx,
				time.Second,
			)
			closeErr := runner.CloseUngranted(closeCtx)
			closeCancel()
			select {
			case <-supervisorDone:
				stopped = true
			default:
			}
			if !stopped {
				killErr := command.Process.Kill()
				if errors.Is(killErr, os.ErrProcessDone) {
					killErr = nil
				}
				select {
				case <-supervisorDone:
					stopped = true
				case <-cleanupCtx.Done():
					stopErr = errors.Join(
						closeErr,
						killErr,
						cleanupCtx.Err(),
					)
				}
			}
		}
		if !stopped {
			return errors.Join(
				errUnresolvedProcessPreparation,
				cause,
				fmt.Errorf(
					"rollback ungranted supervisor %s: %w",
					assignment.ID,
					stopErr,
				),
			)
		}

		heldLock := lifetimeLock
		if supervisorStarted {
			heldLock = nil
		}
		cleanupPending, cleanupErr := c.closeRejectedPreparation(
			cleanupCtx,
			assignment.ID,
			supervisorInstanceID,
			nil,
			heldLock,
		)
		if cleanupErr != nil {
			runtime.cleanupOnly = cleanupPending
			if cleanupPending {
				return errors.Join(
					cause,
					fmt.Errorf(
						"rollback ungranted process %s cleanup remains pending: %w",
						assignment.ID,
						cleanupErr,
					),
				)
			}
			return errors.Join(
				errUnresolvedProcessPreparation,
				cause,
				fmt.Errorf(
					"rollback ungranted process %s: %w",
					assignment.ID,
					cleanupErr,
				),
			)
		}
		return cause
	}

	lockPath, err := machine.LifetimeLockPath(assignment.ID)
	if err != nil {
		return runtime, fail(err)
	}
	lifetimeLock, err = localstore.TryAcquireLock(lockPath)
	if err != nil {
		return runtime, fail(
			fmt.Errorf("acquire supervisor lifetime lock: %w", err),
		)
	}
	defer func() { _ = lifetimeLock.Release() }()

	bootstrap := supervisorIdentityBootstrap{
		OmnaraHome:           c.cfg.OmnaraHome,
		InstallationID:       c.bootstrap.InstallationID,
		MachineID:            c.bootstrap.MachineID,
		ProcessID:            assignment.ID,
		SupervisorInstanceID: supervisorInstanceID,
		SupervisorToken:      supervisorToken,
	}
	bootstrapPath, err := writeSupervisorIdentityBootstrap(
		bootstrap,
		machine.RunDir(),
	)
	if err != nil {
		return runtime, fail(err)
	}
	defer func() {
		if err := removeSupervisorIdentityBootstrap(
			machine,
			assignment.ID,
			supervisorInstanceID,
		); err != nil {
			c.log.Warn(
				"remove supervisor bootstrap failed",
				"process_id",
				assignment.ID,
				"error",
				err,
			)
		}
	}()

	executable, err := os.Executable()
	if err != nil {
		return runtime, fail(err)
	}
	command = exec.Command( //nolint:noctx // The supervisor intentionally survives daemon cancellation.
		executable,
		runnerSubcommand,
		bootstrapPath,
	)
	command.Env = processRunnerEnv(
		os.Environ(),
		c.cfg.RunnerPath,
		map[string]string{omnaraHomeEnvironmentName: c.cfg.OmnaraHome},
	)
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return runtime, fail(err)
	}
	defer func() { _ = devNull.Close() }()
	command.Stdin = devNull
	command.Stdout = devNull
	command.Stderr = devNull
	if err := detachRunnerCommand(command); err != nil {
		return runtime, fail(err)
	}
	inheritedLockFD, restoreInheritance, err :=
		lifetimeLock.PrepareChildInheritance(command)
	if err != nil {
		return runtime, fail(err)
	}
	command.Args = append(command.Args, strconv.Itoa(inheritedLockFD))
	startErr := func() error {
		defer restoreInheritance()
		return command.Start()
	}()
	if startErr != nil {
		return runtime, fail(startErr)
	}
	supervisorStarted = true
	runtime.supervisorPID = command.Process.Pid
	supervisorDone = make(chan struct{})
	go func() {
		waitErr := command.Wait()
		c.processMu.Lock()
		runner.markDone()
		wasCurrent := false
		if current := c.processes[assignment.ID]; current != nil &&
			current.supervisorInstanceID == supervisorInstanceID &&
			!current.cleanupOnly {
			delete(c.processes, assignment.ID)
			wasCurrent = true
		}
		c.processMu.Unlock()
		close(supervisorDone)
		if wasCurrent && waitErr != nil {
			c.log.Warn(
				"process supervisor exited with an error",
				"process_id",
				assignment.ID,
				"error",
				waitErr,
			)
		}
	}()
	if err := lifetimeLock.RelinquishInherited(); err != nil {
		_ = command.Process.Kill()
		return runtime, fail(err)
	}
	if err := runner.waitReady(ctx); err != nil {
		return runtime, fail(err)
	}
	if err := runner.prepare(ctx, assignment); err != nil {
		return runtime, fail(err)
	}
	if err := stateStore.MarkPrepared(
		ctx,
		assignment.ID,
		supervisorInstanceID,
	); err != nil {
		return runtime, fail(err)
	}
	return runtime, nil
}

func (r *ipcProcessRunner) prepare(
	ctx context.Context,
	assignment ProcessAssignment,
) error {
	body, err := json.Marshal(assignment)
	if err != nil {
		return err
	}
	_, err = r.call(
		ctx,
		runnerRequest{Method: runnerMethodPrepare, Payload: body},
	)
	return err
}

func writeSupervisorIdentityBootstrap(
	bootstrap supervisorIdentityBootstrap,
	dir string,
) (string, error) {
	if dir == "" {
		return "", errors.New("supervisor bootstrap dir is required")
	}
	body, err := json.Marshal(bootstrap)
	if err != nil {
		return "", err
	}
	name, err := supervisorIdentityBootstrapFileName(
		bootstrap.ProcessID,
		bootstrap.SupervisorInstanceID,
	)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if err := localstore.WriteFileAtomic(path, body, 0o600); err != nil {
		return path, err
	}
	return path, nil
}

func removeSupervisorIdentityBootstrap(
	machine localstore.MachineStore,
	processID, supervisorInstanceID string,
) error {
	name, err := supervisorIdentityBootstrapFileName(
		processID,
		supervisorInstanceID,
	)
	if err != nil {
		return err
	}
	path := filepath.Join(machine.RunDir(), name)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return localstore.SyncDir(machine.RunDir())
}

func supervisorIdentityBootstrapFileName(
	processID, supervisorInstanceID string,
) (string, error) {
	if processID == "" || supervisorInstanceID == "" {
		return "", errors.New("supervisor bootstrap identity is required")
	}
	for _, value := range []string{processID, supervisorInstanceID} {
		if value == "." || value == ".." ||
			strings.ContainsAny(value, `/\`) {
			return "", errors.New(
				"supervisor bootstrap identity is not path-safe",
			)
		}
	}
	return supervisorIdentityBootstrapPrefix +
		processID + "-" + supervisorInstanceID + ".json", nil
}

func sanitizedRunnerEnv(
	env []string,
	runnerPath string,
) []string {
	allowed := map[string]struct{}{
		"HOME": {}, "USER": {}, "LOGNAME": {}, "SHELL": {},
		"LANG": {}, "LC_ALL": {}, "LC_CTYPE": {}, "TERM": {},
		"TMPDIR": {}, "TEMP": {}, "TMP": {},
		"SYSTEMROOT": {}, "WINDIR": {}, "COMSPEC": {}, "PATHEXT": {},
		omnaraHomeEnvironmentName: {},
	}
	out := make([]string, 0, len(env))
	for _, entry := range env {
		key := entry
		if idx := strings.IndexByte(entry, '='); idx >= 0 {
			key = entry[:idx]
		}
		upperKey := strings.ToUpper(key)
		if upperKey == "PATH" {
			continue
		}
		if _, ok := allowed[upperKey]; ok {
			out = append(out, entry)
		}
	}
	if runnerPath != "" {
		out = append(out, "PATH="+runnerPath)
	}
	return out
}

func processRunnerEnv(
	base []string,
	runnerPath string,
	overrides map[string]string,
) []string {
	byName := make(map[string]string, len(base)+len(overrides))
	for _, entry := range sanitizedRunnerEnv(base, runnerPath) {
		name, _, _ := strings.Cut(entry, "=")
		foldedName := strings.ToUpper(name)
		if _, ok := byName[foldedName]; !ok || name == foldedName {
			byName[foldedName] = entry
		}
	}
	for _, name := range slices.Sorted(maps.Keys(overrides)) {
		value := overrides[name]
		foldedName := strings.ToUpper(name)
		if existing, ok := byName[foldedName]; ok {
			name, _, _ = strings.Cut(existing, "=")
		}
		byName[foldedName] = name + "=" + value
	}
	return slices.Sorted(maps.Values(byName))
}

func workloadProcessEnv(
	base []string,
	runnerPath string,
	overrides map[string]string,
) []string {
	filteredBase := make([]string, 0, len(base))
	for _, entry := range base {
		name, value, _ := strings.Cut(entry, "=")
		upperName := strings.ToUpper(name)
		if upperName == omnaraHomeEnvironmentName {
			filteredBase = append(filteredBase, omnaraHomeEnvironmentName+"="+value)
			continue
		}
		if strings.HasPrefix(upperName, omnaraEnvironmentPrefix) {
			continue
		}
		filteredBase = append(filteredBase, entry)
	}
	// OMNARA_HOME is the sole workload-safe Omnara variable. Skill paths use
	// the daemon's canonical value from base; assignments cannot override it
	// or inject any other control-plane variable.
	filteredOverrides := make(map[string]string, len(overrides))
	for name, value := range overrides {
		if strings.HasPrefix(strings.ToUpper(name), omnaraEnvironmentPrefix) {
			continue
		}
		filteredOverrides[name] = value
	}
	return processRunnerEnv(filteredBase, runnerPath, filteredOverrides)
}
