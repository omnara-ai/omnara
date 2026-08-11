package machinedaemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
)

func (c *Client) shutdownAfterAuthorityLoss(
	ctx context.Context,
	reason string,
) {
	if c.cfg.OmnaraHome == "" {
		return
	}
	machine, err := c.machineStore()
	if err != nil {
		return
	}
	if _, err := os.Lstat(machine.StateDBPath()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		c.log.Warn(
			"inspect local process state before authority-loss shutdown failed",
			"reason",
			reason,
			"error",
			err,
		)
		return
	}

	shutdownCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		30*time.Second,
	)
	defer cancel()
	terminateErr := c.terminateLocalSupervisors(
		shutdownCtx,
		reason,
	)
	deleteErr := c.deleteStoppedLocalMachine(shutdownCtx, machine)
	if err := errors.Join(terminateErr, deleteErr); err != nil {
		c.log.Warn(
			"best-effort local process shutdown was incomplete",
			"reason",
			reason,
			"error",
			err,
		)
	}
}

func StopLocalMachine(
	ctx context.Context,
	home string,
	installationID string,
	machineID string,
	reason string,
) (resultErr error) {
	client := New(Config{
		OmnaraHome:             home,
		ExpectedInstallationID: installationID,
		ExpectedMachineID:      machineID,
	}, nil, nil)
	machine, err := client.machineStore()
	if err != nil {
		return err
	}
	machineInfo, err := os.Lstat(machine.MachineDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect local machine state: %w", err)
	}
	if machineInfo.Mode()&os.ModeSymlink != 0 || !machineInfo.IsDir() {
		return errors.New("local machine state must be a directory and not a symlink")
	}
	if _, err := os.Lstat(machine.StateDBPath()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("local process state is missing")
		}
		return fmt.Errorf("inspect local process state: %w", err)
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	defer func() {
		resultErr = errors.Join(resultErr, client.closeState())
	}()
	terminateErr := client.terminateLocalSupervisors(shutdownCtx, reason)
	if verifyErr := client.verifyStoppedLocalMachine(shutdownCtx, machine); verifyErr != nil {
		return errors.Join(terminateErr, verifyErr)
	}
	return nil
}

func (c *Client) terminateLocalSupervisors(
	ctx context.Context,
	reason string,
) error {
	stateStore, err := c.stateStore(ctx)
	if err != nil {
		return err
	}
	processes, err := stateStore.Processes(ctx)
	if err != nil {
		return err
	}
	machine, err := c.machineStore()
	if err != nil {
		return err
	}

	var result error
	for _, process := range processes {
		if ctx.Err() != nil {
			return errors.Join(result, ctx.Err())
		}
		lockPath, pathErr := machine.LifetimeLockPath(process.ProcessID)
		if pathErr != nil {
			result = errors.Join(result, pathErr)
			continue
		}
		lock, lockErr := localstore.TryAcquireExistingLock(lockPath)
		switch {
		case lockErr == nil || errors.Is(lockErr, os.ErrNotExist):
			if lock != nil {
				result = errors.Join(result, lock.Release())
			}
			continue
		case !errors.Is(lockErr, localstore.ErrLockHeld):
			result = errors.Join(
				result,
				fmt.Errorf(
					"inspect process %s supervisor: %w",
					process.ProcessID,
					lockErr,
				),
			)
			continue
		}

		endpoint, pathErr := machine.ControlEndpointPath(process.ProcessID)
		if pathErr != nil {
			result = errors.Join(result, pathErr)
			continue
		}
		runner := &ipcProcessRunner{
			endpoint:             endpoint,
			supervisorToken:      process.SupervisorToken,
			supervisorInstanceID: process.SupervisorInstanceID,
			done:                 make(chan struct{}),
		}
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		switch process.Phase {
		case statedb.ProcessPreparing, statedb.ProcessPrepared:
			err = runner.CloseUngranted(callCtx)
		case statedb.ProcessAccepted, statedb.ProcessTerminal:
			err = runner.Terminate(callCtx, reason)
		default:
			err = fmt.Errorf(
				"unsupported local process phase %q",
				process.Phase,
			)
		}
		cancel()
		if err != nil {
			result = errors.Join(
				result,
				fmt.Errorf(
					"stop process %s supervisor: %w",
					process.ProcessID,
					err,
				),
			)
		}
	}
	return result
}

func (c *Client) deleteStoppedLocalMachine(
	ctx context.Context,
	machine localstore.MachineStore,
) error {
	if err := waitForLocalSupervisors(ctx, machine); err != nil {
		return errors.Join(err, c.closeState())
	}
	verifyErr := c.inspectStoppedLocalMachine(ctx)
	if closeErr := c.closeState(); closeErr != nil {
		return errors.Join(verifyErr, closeErr)
	}
	machineDir := machine.MachineDir()
	removeErr := os.RemoveAll(machineDir)
	if removeErr != nil {
		removeErr = fmt.Errorf("remove decommissioned machine state: %w", removeErr)
	}
	syncErr := localstore.SyncDir(filepath.Dir(machineDir))
	if syncErr != nil {
		syncErr = fmt.Errorf("sync decommissioned machine state: %w", syncErr)
	}
	return errors.Join(verifyErr, removeErr, syncErr)
}

func (c *Client) verifyStoppedLocalMachine(
	ctx context.Context,
	machine localstore.MachineStore,
) error {
	if err := waitForLocalSupervisors(ctx, machine); err != nil {
		return err
	}
	return c.inspectStoppedLocalMachine(ctx)
}

func waitForLocalSupervisors(ctx context.Context, machine localstore.MachineStore) error {
	for {
		live, err := machineHasLiveSupervisorLocks(machine)
		if err != nil {
			return err
		}
		if !live {
			break
		}
		if err := sleepContext(ctx, 25*time.Millisecond); err != nil {
			return fmt.Errorf(
				"wait for local supervisors to stop: %w",
				err,
			)
		}
	}
	return nil
}

func (c *Client) inspectStoppedLocalMachine(ctx context.Context) error {
	stateStore, err := c.stateStore(ctx)
	if err != nil {
		return fmt.Errorf("inspect stopped process state: %w", err)
	}
	processes, err := stateStore.Processes(ctx)
	if err != nil {
		return fmt.Errorf("inspect stopped processes: %w", err)
	}
	var result error
	for _, process := range processes {
		if process.ExecCommitted && !process.ContainmentEmpty {
			result = errors.Join(
				result,
				fmt.Errorf(
					"could not confirm agent process %s stopped; it may still be running and may need to be stopped manually",
					process.ProcessID,
				),
			)
		}
	}
	return result
}

func machineHasLiveSupervisorLocks(
	machine localstore.MachineStore,
) (bool, error) {
	entries, err := os.ReadDir(machine.ProcessesDir())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("list process lifetime locks: %w", err)
	}
	suffix := "." + localstore.LifetimeLockFileName
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), suffix) {
			continue
		}
		path := filepath.Join(machine.ProcessesDir(), entry.Name())
		lock, lockErr := localstore.TryAcquireExistingLock(path)
		switch {
		case lockErr == nil:
			if err := lock.Release(); err != nil {
				return false, err
			}
		case errors.Is(lockErr, os.ErrNotExist):
		case errors.Is(lockErr, localstore.ErrLockHeld):
			return true, nil
		default:
			return false, fmt.Errorf(
				"inspect supervisor lifetime lock %s: %w",
				entry.Name(),
				lockErr,
			)
		}
	}
	return false, nil
}
