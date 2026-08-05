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
	terminateErr := c.terminateSupervisorsAfterAuthorityLoss(
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

func (c *Client) terminateSupervisorsAfterAuthorityLoss(
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
	var result error
	stateStore, err := c.stateStore(ctx)
	if err != nil {
		result = errors.Join(
			result,
			fmt.Errorf("inspect stopped process state: %w", err),
		)
	} else {
		processes, readErr := stateStore.Processes(ctx)
		if readErr != nil {
			result = errors.Join(
				result,
				fmt.Errorf("inspect stopped processes: %w", readErr),
			)
		} else {
			for _, process := range processes {
				if process.ExecCommitted && !process.ContainmentEmpty {
					result = errors.Join(
						result,
						fmt.Errorf(
							"process %s supervisor stopped before containment closure was proved",
							process.ProcessID,
						),
					)
				}
			}
		}
	}
	if err := c.closeState(); err != nil {
		return errors.Join(result, err)
	}
	machineDir := machine.MachineDir()
	if err := os.RemoveAll(machineDir); err != nil {
		return errors.Join(
			result,
			fmt.Errorf("remove decommissioned machine state: %w", err),
		)
	}
	if err := localstore.SyncDir(filepath.Dir(machineDir)); err != nil {
		return errors.Join(
			result,
			fmt.Errorf("sync decommissioned machine state: %w", err),
		)
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
