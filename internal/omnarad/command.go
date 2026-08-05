package omnarad

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/alexflint/go-arg"
	"github.com/omnara-ai/omnara/internal/daemonversion"
	"github.com/omnara-ai/omnara/internal/machinedaemon"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
)

var version = daemonversion.Development

const (
	versionFlag          = "--version"
	restartSubcommand    = "restart"
	runServiceSubcommand = "run-service"
	noServiceWarning     = "warning: no launchd/systemd user service manager is available; " +
		"running omnarad with foreground restart supervision"
)

type daemonCommand struct {
	Version bool `arg:"--version" help:"display version and exit"`
	Install *struct {
		ReleaseManifestURL string `arg:"--release-manifest-url"`
		NoStart            bool   `arg:"--no-start"`
	} `arg:"subcommand:install"`
	Start *struct {
		NoService bool `arg:"--no-service"`
	} `arg:"subcommand:start"`
	Stop          *struct{} `arg:"subcommand:stop"`
	Restart       *struct{} `arg:"subcommand:restart"`
	Status        *struct{} `arg:"subcommand:status"`
	ProcessRunner *struct {
		BootstrapPath string `arg:"positional,required"`
		LockFD        int    `arg:"positional,required"`
	} `arg:"subcommand:__omnara_process_runner,hidden"`
	RunService *struct {
		Supervised bool `arg:"--supervised"`
	} `arg:"subcommand:run-service,hidden"`
}

func (daemonCommand) Description() string {
	return "Omnara machine daemon for running agent processes."
}

func ensureDaemonStarted(
	ctx context.Context,
	home string,
	stderr io.Writer,
	log *slog.Logger,
) (managed bool, resultErr error) {
	lock, err := acquireInstallLock(ctx, home)
	if err != nil {
		return false, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, lock.Release())
	}()
	return ensureDaemonService(ctx, home, false, stderr, log)
}

func runStartCommand(
	ctx context.Context,
	noService bool,
	stdout io.Writer,
	stderr io.Writer,
	log *slog.Logger,
) int {
	home, temporaryOverrides, err := validateRuntimeConfig(ctx, log)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if noService {
		if err := stopDaemon(ctx, home); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		return runDaemonInForeground(ctx, home, log)
	}
	if temporaryOverrides {
		if err := prepareTemporaryDaemonRun(ctx, home); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		_, _ = fmt.Fprintln(stderr, noServiceWarning)
		return runDaemonInForeground(ctx, home, log)
	}
	managed, err := ensureDaemonStarted(ctx, home, stderr, log)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if !managed {
		_, _ = fmt.Fprintln(stderr, noServiceWarning)
		return runDaemonInForeground(ctx, home, log)
	}
	_, _ = fmt.Fprintln(stdout, "omnarad daemon is running")
	return 0
}

func runDaemonInForeground(ctx context.Context, home string, log *slog.Logger) int {
	if err := runForegroundSupervisor(ctx, home, log); err != nil {
		log.Error("daemon supervisor failed", "error", err)
		return 1
	}
	return 0
}

func runStatusCommand(ctx context.Context, stdout, stderr io.Writer) int {
	home, err := localstore.ResolveHome()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	status, err := inspectDaemonService(ctx)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if status.running {
		_, _ = fmt.Fprintf(stdout, "omnarad is running (%s, pid %d)\n", status.manager, status.pid)
		return 0
	}
	pid, held, err := inspectDaemonRuntimeLock(home)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if held {
		_, _ = fmt.Fprintf(stdout, "omnarad is running (no-service, pid %d)\n", pid)
		return 0
	}
	_, _ = fmt.Fprintln(stdout, "omnarad is stopped")
	return 1
}

func runRestartCommand(
	ctx context.Context,
	stdout io.Writer,
	stderr io.Writer,
	log *slog.Logger,
) int {
	home, temporaryOverrides, err := validateRuntimeConfig(ctx, log)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if temporaryOverrides {
		if err := prepareTemporaryDaemonRun(ctx, home); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		_, _ = fmt.Fprintln(stderr, noServiceWarning)
		return runDaemonInForeground(ctx, home, log)
	}
	shouldStartForeground, restartInProgress, err := dispatchDaemonRestart(ctx, home, stderr, log)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if shouldStartForeground {
		_, _ = fmt.Fprintln(stderr, noServiceWarning)
		return runDaemonInForeground(ctx, home, log)
	}
	if restartInProgress {
		_, _ = fmt.Fprintln(stdout, "omnarad is restarting (no-service)")
		return 0
	}
	_, _ = fmt.Fprintln(stdout, "omnarad restarted")
	return 0
}

func Run(
	ctx context.Context,
	args []string,
	stdin *os.File,
	stdout io.Writer,
	stderr io.Writer,
	log *slog.Logger,
) int {
	var command daemonCommand
	parser, err := arg.NewParser(arg.Config{Program: "omnarad"}, &command)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if err := parser.Parse(args); err != nil {
		if errors.Is(err, arg.ErrHelp) {
			_ = parser.WriteHelpForSubcommand(stdout, parser.SubcommandNames()...)
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		_ = parser.WriteUsageForSubcommand(stderr, parser.SubcommandNames()...)
		return 1
	}

	switch {
	case command.Version:
		_, _ = fmt.Fprintln(stdout, version)
		return 0
	case command.ProcessRunner != nil:
		if err := machinedaemon.RunCommandSupervisorFromBootstrap(
			ctx,
			command.ProcessRunner.BootstrapPath,
			command.ProcessRunner.LockFD,
		); err != nil {
			log.Error("process runner failed", "error", err)
			return 1
		}
		return 0
	case command.RunService != nil && command.RunService.Supervised:
		if err := runSupervisorChild(ctx, log); err != nil {
			log.Error("supervisor child failed", "error", err)
			return 1
		}
		return 0
	case command.RunService != nil:
		if err := runService(ctx, log, false); err != nil {
			log.Error("daemon failed", "error", err)
			return 1
		}
		return 0
	case command.Install != nil:
		return runInstallCommand(
			ctx,
			command.Install.ReleaseManifestURL,
			command.Install.NoStart,
			stdin,
			stdout,
			stderr,
			log,
		)
	case parser.Subcommand() == nil || command.Start != nil:
		return runStartCommand(ctx, command.Start != nil && command.Start.NoService, stdout, stderr, log)
	case command.Stop != nil:
		home, err := localstore.ResolveHome()
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		if err := stopDaemon(ctx, home); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		_, _ = fmt.Fprintln(stdout, "omnarad is stopped")
		return 0
	case command.Restart != nil:
		return runRestartCommand(ctx, stdout, stderr, log)
	case command.Status != nil:
		return runStatusCommand(ctx, stdout, stderr)
	}
	return 1
}
