package omnarad

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/omnara-ai/omnara/internal/machinedaemon"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
)

const supervisedServiceFlag = "--supervised"
const daemonRestartDelay = 3 * time.Second
const daemonRestartSignal = syscall.SIGUSR1
const supervisorChildShutdownTimeout = 20 * time.Second

func runForegroundSupervisor(ctx context.Context, home string, log *slog.Logger) error {
	store, err := localstore.New(home)
	if err != nil {
		return err
	}
	lock, err := localstore.TryAcquireLock(store.DaemonLockPath())
	if errors.Is(err, localstore.ErrLockHeld) {
		return errors.New("another daemon is already running in OMNARA_HOME")
	}
	if err != nil {
		return err
	}
	defer func() {
		if err := lock.Release(); err != nil {
			log.Warn("release daemon local lock failed", "error", err)
		}
	}()
	restart := make(chan os.Signal, 1)
	signal.Notify(restart, daemonRestartSignal)
	defer signal.Stop(restart)
	if err := lock.WritePID(os.Getpid()); err != nil {
		return err
	}
	return runSupervisorLoop(ctx, home, daemonRestartDelay, restart, log)
}

func runSupervisorLoop(
	ctx context.Context,
	home string,
	restartDelay time.Duration,
	restart <-chan os.Signal,
	log *slog.Logger,
) error {
	binary := canonicalDaemonPath(home)
	for ctx.Err() == nil {
		cmd := exec.CommandContext(context.WithoutCancel(ctx), binary, runServiceSubcommand, supervisedServiceFlag)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start supervised daemon: %w", err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err == nil {
				return nil
			}
			if ctx.Err() != nil {
				continue
			}
			log.Error("supervised daemon exited", "error", err, "restart_after", restartDelay)
			waitForDaemonRestart(ctx, restartDelay, restart)
		case <-ctx.Done():
			if err := terminateSupervisorChild(
				cmd, done, syscall.SIGTERM, supervisorChildShutdownTimeout, log,
			); err != nil {
				return err
			}
			return nil
		case <-restart:
			clearDaemonEnvironmentOverrides()
			if err := terminateSupervisorChild(
				cmd, done, daemonRestartSignal, supervisorChildShutdownTimeout, log,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func waitForDaemonRestart(ctx context.Context, delay time.Duration, restart <-chan os.Signal) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-restart:
		clearDaemonEnvironmentOverrides()
	case <-timer.C:
	}
}

func clearDaemonEnvironmentOverrides() {
	_ = os.Unsetenv("OMNARA_API_URL")
	_ = os.Unsetenv("OMNARA_MACHINE_TOKEN")
	_ = os.Unsetenv("OMNARA_NO_UPDATE")
	_ = os.Unsetenv("OMNARA_RUNNER_PATH")
}

func terminateSupervisorChild(
	cmd *exec.Cmd,
	done <-chan error,
	shutdownSignal os.Signal,
	timeout time.Duration,
	log *slog.Logger,
) error {
	if err := cmd.Process.Signal(shutdownSignal); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("stop supervisor child: %w", err)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var err error
	select {
	case err = <-done:
	case <-timer.C:
		log.Warn("supervisor child did not stop; killing", "timeout", timeout)
		if killErr := cmd.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			return fmt.Errorf("kill supervisor child: %w", killErr)
		}
		err = <-done
	}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return fmt.Errorf("wait for supervisor child: %w", err)
		}
	}
	return nil
}

func runSupervisorChild(ctx context.Context, log *slog.Logger) error {
	home, err := localstore.ResolveHome()
	if err != nil {
		return err
	}
	pid, held, err := inspectDaemonRuntimeLock(home)
	if err != nil {
		return err
	}
	if !held || pid != os.Getppid() {
		return errors.New("supervised daemon parent does not own daemon.lock")
	}
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	restart := make(chan os.Signal, 1)
	signal.Notify(restart, daemonRestartSignal)
	defer signal.Stop(restart)
	go func() {
		select {
		case <-runCtx.Done():
		case <-restart:
			cancel(machinedaemon.ErrDaemonUpdate)
		}
	}()
	return runService(runCtx, log, true)
}
