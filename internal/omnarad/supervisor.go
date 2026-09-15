package omnarad

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/machinedaemon"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
)

const supervisedServiceFlag = "--supervised"
const daemonRestartDelay = 3 * time.Second
const daemonRestartMaxDelay = 3 * time.Minute
const daemonRestartResetAfter = 5 * time.Minute
const daemonRestartSignal = syscall.SIGUSR1
const supervisorChildShutdownTimeout = 20 * time.Second
const supervisorFailureReportTimeout = 2 * time.Second

type supervisorRestartPolicy struct {
	initialDelay time.Duration
	maxDelay     time.Duration
	resetAfter   time.Duration
}

func runForegroundSupervisor(ctx context.Context, home string, log *slog.Logger) (resultErr error) {
	store, err := localstore.New(home)
	if err != nil {
		return err
	}
	installLock, acquired, err := tryAcquireInstallLock(home)
	if err != nil {
		return err
	}
	if !acquired {
		return errors.New("daemon installation is being modified")
	}
	defer func() {
		resultErr = errors.Join(resultErr, installLock.Release())
	}()
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
	if err := installLock.Release(); err != nil {
		return err
	}
	logPath, err := ensureServiceLog(home)
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open daemon service log: %w", err)
	}
	defer func() {
		if err := logFile.Close(); err != nil {
			log.Warn("close daemon service log failed", "error", err)
		}
	}()
	childStdout, childStderr := supervisorChildWriters(os.Stdout, os.Stderr, logFile)
	log = slog.New(logpkg.NewJSONHandler(childStdout, nil))
	return runSupervisorLoop(ctx, home, supervisorRestartPolicy{
		initialDelay: daemonRestartDelay,
		maxDelay:     daemonRestartMaxDelay,
		resetAfter:   daemonRestartResetAfter,
	}, restart, childStdout, childStderr, log)
}

func supervisorChildWriters(stdout, stderr, logFile io.Writer) (io.Writer, io.Writer) {
	serviceLog := bestEffortWriter{w: logFile}
	return io.MultiWriter(bestEffortWriter{w: stdout}, serviceLog),
		io.MultiWriter(bestEffortWriter{w: stderr}, serviceLog)
}

type bestEffortWriter struct {
	w io.Writer
}

func (b bestEffortWriter) Write(p []byte) (int, error) {
	_, _ = b.w.Write(p)
	return len(p), nil
}

func runSupervisorLoop(
	ctx context.Context,
	home string,
	policy supervisorRestartPolicy,
	restart <-chan os.Signal,
	childStdout io.Writer,
	childStderr io.Writer,
	log *slog.Logger,
) error {
	binary := canonicalDaemonPath(home)
	restartDelay := policy.initialDelay
	var reports sync.WaitGroup
	defer reports.Wait()
	for ctx.Err() == nil {
		config, configErr := loadDaemonConfig(home)
		if configErr == nil {
			_, configErr = applyDaemonEnvironment(config)
		}
		output := &supervisorOutputTail{}
		cmd := exec.CommandContext(context.WithoutCancel(ctx), binary, runServiceSubcommand, supervisedServiceFlag)
		cmd.Stdout = io.MultiWriter(supervisorOutputWriter{tail: output}, childStdout)
		cmd.Stderr = io.MultiWriter(supervisorOutputWriter{tail: output, stderr: true}, childStderr)
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start supervised daemon: %w", err)
		}
		startedAt := time.Now()
		var err error
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err = <-done:
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
			restartDelay = policy.initialDelay
			continue
		}
		if err == nil || ctx.Err() != nil {
			return nil
		}
		elapsed := time.Since(startedAt)
		if elapsed >= policy.resetAfter {
			restartDelay = policy.initialDelay
		}
		log.Error("supervised daemon exited", "error", err, "restart_after", restartDelay)
		if configErr != nil {
			log.Warn("load daemon failure reporting configuration failed", "error", configErr)
		} else {
			reportClient := machinedaemon.New(machinedaemon.Config{
				APIURL:       config.APIURL,
				MachineToken: config.MachineToken,
			}, nil, log)
			detail := fmt.Sprintf("supervised daemon exited after %s: %v", elapsed.Round(time.Millisecond), err)
			if cmd.ProcessState != nil {
				if usage, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok && usage != nil && usage.Maxrss > 0 {
					peakMiB := float64(usage.Maxrss) / 1024
					if runtime.GOOS == "darwin" {
						peakMiB /= 1024
					}
					detail += fmt.Sprintf("; wait_max_rss_mib=%.1f", peakMiB)
				}
			}
			tail, truncated := output.snapshot(machinedaemon.MaxFailureDetailBytes - len(detail) - 1)
			if tail != "" {
				detail = tail + "\n" + detail
			}
			reportCtx, cancelReport := context.WithTimeout(ctx, supervisorFailureReportTimeout)
			reports.Go(func() {
				defer cancelReport()
				if err := reportClient.ReportRuntimeFailure(reportCtx, detail, truncated); err != nil && ctx.Err() == nil {
					log.Warn("report daemon runtime failure failed", "error", err)
				}
			})
		}
		manualRestart := waitForDaemonRestart(ctx, restartDelay, restart)
		if manualRestart {
			restartDelay = policy.initialDelay
		} else {
			restartDelay = min(restartDelay*2, policy.maxDelay)
		}
	}
	return nil
}

func waitForDaemonRestart(ctx context.Context, delay time.Duration, restart <-chan os.Signal) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-restart:
		clearDaemonEnvironmentOverrides()
		return true
	case <-timer.C:
	}
	return false
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
