package omnarad

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
)

const (
	serviceCommandTimeout  = 10 * time.Second
	serviceStopTimeout     = 35 * time.Second
	serviceRuntimeLockWait = 30 * time.Second
	serviceRuntimeLockPoll = 100 * time.Millisecond
	serviceLogRelativePath = "logs/daemon/daemon.log"
)

type serviceCommandResult struct {
	stdout string
	stderr string
	err    error
}

func runServiceCommand(ctx context.Context, timeout time.Duration, name string, args ...string) serviceCommandResult {
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := exec.CommandContext(commandCtx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
		err = context.DeadlineExceeded
	}
	return serviceCommandResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func serviceCommandError(name string, args []string, result serviceCommandResult) error {
	detail := strings.TrimSpace(result.stderr)
	if detail == "" {
		detail = strings.TrimSpace(result.stdout)
	}
	if len(detail) > 4096 {
		detail = detail[:4096]
	}
	command := strings.Join(append([]string{name}, args...), " ")
	if errors.Is(result.err, context.DeadlineExceeded) {
		return fmt.Errorf("%s timed out", command)
	}
	if detail != "" {
		return fmt.Errorf("%s failed: %w: %s", command, result.err, detail)
	}
	return fmt.Errorf("%s failed: %w", command, result.err)
}

func validateServiceValue(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", name)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s contains control characters", name)
		}
	}
	return nil
}

func waitForDaemonRuntimeUnlock(ctx context.Context, home string) error {
	lock, err := waitForDaemonRuntimeLock(ctx, home)
	if err != nil {
		return err
	}
	return lock.Release()
}

func waitForDaemonRuntimeLock(ctx context.Context, home string) (*localstore.Lock, error) {
	store, err := localstore.New(home)
	if err != nil {
		return nil, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, serviceRuntimeLockWait)
	defer cancel()
	ticker := time.NewTicker(serviceRuntimeLockPoll)
	defer ticker.Stop()
	for {
		lock, err := localstore.TryAcquireLock(store.DaemonLockPath())
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, localstore.ErrLockHeld) {
			return nil, fmt.Errorf("inspect daemon runtime lock: %w", err)
		}
		select {
		case <-waitCtx.Done():
			return nil, fmt.Errorf("wait for daemon runtime lock release: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func ensureDaemonRuntimeUnlocked(home string) error {
	store, err := localstore.New(home)
	if err != nil {
		return err
	}
	lock, err := localstore.TryAcquireLock(store.DaemonLockPath())
	if errors.Is(err, localstore.ErrLockHeld) {
		return errors.New("another daemon is already running; stop it before running 'omnarad start'")
	}
	if err != nil {
		return fmt.Errorf("inspect daemon runtime lock: %w", err)
	}
	if err := lock.Release(); err != nil {
		return fmt.Errorf("release daemon runtime lock: %w", err)
	}
	return nil
}

func inspectDaemonRuntimeLock(home string) (int, bool, error) {
	store, err := localstore.New(home)
	if err != nil {
		return 0, false, err
	}
	return localstore.InspectLock(store.DaemonLockPath())
}

func stopDaemon(ctx context.Context, home string) (resultErr error) {
	lock, err := acquireInstallLock(ctx, home)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, lock.Release())
	}()
	if err := stopDaemonService(ctx); err != nil {
		return err
	}
	return stopDaemonRuntime(ctx, home)
}

func stopDaemonRuntime(ctx context.Context, home string) error {
	lock, err := stopDaemonRuntimeLocked(ctx, home)
	if err != nil {
		return err
	}
	return lock.Release()
}

func stopDaemonRuntimeLocked(ctx context.Context, home string) (*localstore.Lock, error) {
	pid, held, err := inspectDaemonRuntimeLock(home)
	if err != nil {
		return nil, fmt.Errorf("inspect daemon runtime lock: %w", err)
	}
	if held {
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			return nil, fmt.Errorf("stop daemon process %d: %w", pid, err)
		}
	}
	return waitForDaemonRuntimeLock(ctx, home)
}

func prepareTemporaryDaemonRun(ctx context.Context, home string) (resultErr error) {
	lock, err := acquireInstallLock(ctx, home)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, lock.Release())
	}()
	status, err := inspectDaemonService(ctx)
	if err != nil {
		return err
	}
	if status.manager != "" {
		return fmt.Errorf(
			"omnarad uses %s on this machine; environment overrides cannot be applied through the service manager. "+
				"Update %s, then run 'omnarad restart'",
			status.manager,
			filepath.Join(home, daemonConfigFileName),
		)
	}
	return stopDaemonRuntime(ctx, home)
}

func dispatchDaemonRestart(
	ctx context.Context,
	home string,
	stderr io.Writer,
	log *slog.Logger,
) (shouldStartForeground bool, restartInProgress bool, resultErr error) {
	lock, err := acquireInstallLock(ctx, home)
	if err != nil {
		return false, false, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, lock.Release())
	}()
	status, err := inspectDaemonService(ctx)
	if err != nil {
		return false, false, err
	}
	if status.running {
		managed, err := ensureDaemonService(ctx, home, true, stderr, log)
		return !managed, false, err
	}
	pid, held, err := inspectDaemonRuntimeLock(home)
	if err != nil {
		return false, false, fmt.Errorf("inspect daemon runtime lock: %w", err)
	}
	if held {
		if err := syscall.Kill(pid, daemonRestartSignal); err != nil {
			return false, false, fmt.Errorf("restart daemon process %d: %w", pid, err)
		}
		return false, true, nil
	}
	managed, err := ensureDaemonService(ctx, home, false, stderr, log)
	return !managed, false, err
}

func parsePositivePID(raw string) int {
	pid, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}
