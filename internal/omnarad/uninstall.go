package omnarad

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/machinedaemon"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
	"golang.org/x/term"
)

func runUninstallCommand(
	ctx context.Context,
	yes bool,
	stdin *os.File,
	stdout io.Writer,
	stderr io.Writer,
	log *slog.Logger,
) int {
	home, err := localstore.ResolveHome()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if _, err := inspectUninstallHome(home); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if !yes {
		if stdin == nil || !term.IsTerminal(int(stdin.Fd())) {
			_, _ = fmt.Fprintln(stderr, "uninstall requires an interactive terminal; rerun with --yes")
			return 1
		}
		_, _ = fmt.Fprintf(
			stderr,
			"Uninstall omnarad?\nWARNING: This will stop the daemon and agent processes, remove its user service, and permanently delete this entire directory and everything beneath it:\n  %s\nThis includes files you added and files stored on any filesystem mounted inside this directory. This cannot be undone.\nType \"uninstall\" to continue: ",
			home,
		)
		answer, readErr := bufio.NewReader(stdin).ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			_, _ = fmt.Fprintln(stderr, readErr)
			return 1
		}
		if strings.TrimSpace(answer) != "uninstall" {
			_, _ = fmt.Fprintln(stderr, "uninstall canceled")
			return 1
		}
	}
	if err := uninstallDaemon(ctx, home, log); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "omnarad uninstalled from "+home)
	return 0
}

func inspectUninstallHome(home string) (*daemonConfig, error) {
	if err := validateServiceValue("OMNARA_HOME", home); err != nil {
		return nil, err
	}
	info, err := os.Lstat(home)
	if err != nil {
		return nil, fmt.Errorf("inspect Omnara home %s: %w", home, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("omnara home must be a directory and not a symlink: %s", home)
	}
	if err := ensureCurrentUserOwner(info, home); err != nil {
		return nil, err
	}
	resolvedHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return nil, fmt.Errorf("resolve Omnara home: %w", err)
	}
	account, err := user.Current()
	if err != nil || account.HomeDir == "" {
		return nil, errors.New("resolve operating-system user home")
	}
	serviceHome, err := serviceUserHome()
	if err != nil {
		return nil, err
	}
	for _, protectedHome := range []string{account.HomeDir, serviceHome} {
		resolvedProtectedHome, err := filepath.EvalSymlinks(protectedHome)
		if err != nil {
			return nil, fmt.Errorf("resolve user home %s: %w", protectedHome, err)
		}
		relative, err := filepath.Rel(resolvedHome, resolvedProtectedHome)
		if err != nil {
			return nil, fmt.Errorf("compare Omnara home with user home: %w", err)
		}
		if filepath.IsLocal(relative) {
			return nil, fmt.Errorf(
				"refusing to uninstall the user home directory or one of its ancestors: %s",
				home,
			)
		}
	}
	config, configErr := loadDaemonConfig(home)
	_, receiptErr := loadInstallReceipt(home)
	if configErr != nil && receiptErr != nil {
		return nil, fmt.Errorf(
			"%s does not contain a valid daemon config or install receipt: %w",
			home,
			errors.Join(configErr, receiptErr),
		)
	}
	if err := validateUninstallState(home, config); err != nil {
		return nil, err
	}
	return config, nil
}

func validateUninstallState(home string, config *daemonConfig) error {
	installations, err := readUninstallDirectory(filepath.Join(home, localstore.InstallationsDirName))
	if err != nil {
		return err
	}
	if len(installations) == 0 {
		return nil
	}
	if config == nil {
		return errors.New("cannot safely remove local machine state without a valid daemon config")
	}
	if len(installations) != 1 || installations[0].Name() != config.InstallationID ||
		installations[0].Type()&os.ModeSymlink != 0 || !installations[0].IsDir() {
		return fmt.Errorf(
			"local state must contain only configured installation %s",
			config.InstallationID,
		)
	}
	machinesPath := filepath.Join(
		home,
		localstore.InstallationsDirName,
		config.InstallationID,
		localstore.MachinesDirName,
	)
	machines, err := readUninstallDirectory(machinesPath)
	if err != nil {
		return err
	}
	if len(machines) == 0 {
		return nil
	}
	if len(machines) != 1 || machines[0].Name() != config.MachineID ||
		machines[0].Type()&os.ModeSymlink != 0 || !machines[0].IsDir() {
		return fmt.Errorf("local state must contain only configured machine %s", config.MachineID)
	}
	return nil
}

func readUninstallDirectory(path string) ([]os.DirEntry, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect uninstall state %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("uninstall state must be a directory and not a symlink: %s", path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read uninstall state %s: %w", path, err)
	}
	return entries, nil
}

func uninstallDaemon(ctx context.Context, home string, log *slog.Logger) (resultErr error) {
	var config *daemonConfig
	homeRetired := false
	defer func() {
		reportErr := resultErr
		if homeRetired {
			reportErr = nil
		}
		reportUninstall(ctx, config, reportErr, log)
	}()
	installLock, err := acquireInstallLock(ctx, home)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, installLock.Release())
	}()
	config, err = inspectUninstallHome(home)
	if err != nil {
		return err
	}
	if err := uninstallDaemonService(ctx, home); err != nil {
		return err
	}
	daemonLock, err := stopDaemonRuntimeLocked(ctx, home)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, daemonLock.Release())
	}()
	if config != nil {
		if err := machinedaemon.StopLocalMachine(
			ctx,
			home,
			config.InstallationID,
			config.MachineID,
			"daemon_uninstalled",
		); err != nil {
			return fmt.Errorf("stop local agent processes: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	parent := filepath.Dir(home)
	tombstone, err := os.MkdirTemp(parent, "."+filepath.Base(home)+".uninstall-")
	if err != nil {
		return fmt.Errorf("create uninstall staging directory: %w", err)
	}
	if err := removeManagedPathSymlink(home); err != nil {
		return errors.Join(err, os.Remove(tombstone))
	}
	movedHome := filepath.Join(tombstone, filepath.Base(home))
	if err := os.Rename(home, movedHome); err != nil {
		return errors.Join(
			fmt.Errorf("retire Omnara home: %w", err),
			os.Remove(tombstone),
		)
	}
	homeRetired = true
	resultErr = errors.Join(resultErr, localstore.SyncDir(parent), daemonLock.Release(), installLock.Release())
	daemonLock = nil
	installLock = nil
	if err := os.RemoveAll(tombstone); err != nil {
		return errors.Join(resultErr, fmt.Errorf("remove retired Omnara home %s: %w", tombstone, err))
	}
	return errors.Join(resultErr, localstore.SyncDir(parent))
}

func reportUninstall(ctx context.Context, config *daemonConfig, uninstallErr error, log *slog.Logger) {
	if config == nil {
		return
	}
	detail := ""
	if uninstallErr != nil {
		detail = uninstallErr.Error()
	}
	reportCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	client := machinedaemon.New(machinedaemon.Config{
		APIURL:       config.APIURL,
		MachineToken: config.MachineToken,
	}, nil, log)
	if err := client.ReportUninstall(reportCtx, detail); err != nil {
		log.Warn("report daemon uninstall failed", "error", err)
	}
}

func removeManagedPathSymlink(home string) error {
	userHome, err := serviceUserHome()
	if err != nil {
		return err
	}
	link := filepath.Join(userHome, ".local", "bin", "omnarad")
	info, err := os.Lstat(link)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect omnarad PATH symlink: %w", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	if err := ensureCurrentUserOwner(info, link); err != nil {
		return err
	}
	target, err := os.Readlink(link)
	if err != nil {
		return fmt.Errorf("read omnarad PATH symlink: %w", err)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(link), target)
	}
	if filepath.Clean(target) != canonicalDaemonPath(home) {
		return nil
	}
	if err := os.Remove(link); err != nil {
		return fmt.Errorf("remove omnarad PATH symlink: %w", err)
	}
	return localstore.SyncDir(filepath.Dir(link))
}
