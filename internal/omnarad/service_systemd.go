//go:build linux

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
	"text/template"

	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
)

const systemdServiceName = "omnarad.service"
const minimumSystemdVersion = 240
const systemdLingerWarningText = "warning: systemd user lingering is not enabled; run 'sudo loginctl enable-linger %s' " +
	"to start omnarad at boot without an interactive login"

var systemdUnitTemplate = template.Must(
	template.New(systemdServiceName).
		Funcs(template.FuncMap{
			"escapeSystemdExecValue":   escapeSystemdExecValue,
			"escapeSystemdPathValue":   escapeSystemdPathValue,
			"escapeSystemdQuotedValue": escapeSystemdQuotedValue,
		}).
		Parse(`[Unit]
Description=Omnara Daemon
After=network.target

[Service]
Type=exec
ExecStart="{{.BinaryPath | escapeSystemdExecValue}}" {{.Subcommand | escapeSystemdExecValue}}
Environment="OMNARA_HOME={{.Home | escapeSystemdQuotedValue}}"
Environment="HOME={{.UserHome | escapeSystemdQuotedValue}}"
Restart=on-failure
RestartSec={{.RestartDelaySeconds}}
KillMode=process
TimeoutStopSec=30s
StandardOutput=append:{{.LogPath | escapeSystemdPathValue}}
StandardError=append:{{.LogPath | escapeSystemdPathValue}}
WorkingDirectory={{.UserHome | escapeSystemdPathValue}}

[Install]
WantedBy=default.target
`),
)

type systemdUnitTemplateData struct {
	BinaryPath          string
	Subcommand          string
	Home                string
	UserHome            string
	RestartDelaySeconds int
	LogPath             string
}

type systemdServiceState struct {
	loaded            bool
	active            bool
	mainPID           int
	fragmentPath      string
	needsDaemonReload bool
}

type managedDaemonStatus struct {
	manager    string
	registered bool
	running    bool
	pid        int
}

func ensureDaemonService(
	ctx context.Context,
	home string,
	forceRestart bool,
	stderr io.Writer,
	log *slog.Logger,
) (bool, error) {
	systemctl, err := exec.LookPath("systemctl")
	if errors.Is(err, exec.ErrNotFound) {
		return false, ensureDaemonRuntimeUnlocked(home)
	}
	if err != nil {
		return false, fmt.Errorf("find systemctl: %w", err)
	}
	probeArgs := []string{"--user", "show", "--property=Version"}
	probe := runServiceCommand(ctx, serviceCommandTimeout, systemctl, probeArgs...)
	if probe.err != nil {
		if systemdUnavailable(probe) {
			return false, ensureDaemonRuntimeUnlocked(home)
		}
		return false, serviceCommandError(systemctl, probeArgs, probe)
	}
	var systemdVersion int
	if _, err := fmt.Sscanf(strings.TrimSpace(probe.stdout), "Version=%d", &systemdVersion); err != nil {
		return false, fmt.Errorf("parse systemd version: %w", err)
	}
	if systemdVersion < minimumSystemdVersion {
		return false, fmt.Errorf(
			"systemd %d is unsupported; omnarad requires systemd %d or newer",
			systemdVersion,
			minimumSystemdVersion,
		)
	}
	userHome, err := serviceUserHome()
	if err != nil {
		return false, err
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(configHome) {
		configHome = filepath.Join(userHome, ".config")
	}
	unitDir := filepath.Join(configHome, "systemd", "user")
	if err := createOwnedDirectoryAll(unitDir, 0o700); err != nil {
		return false, err
	}
	unitPath := filepath.Join(unitDir, systemdServiceName)
	binaryPath := filepath.Join(home, "bin", "omnarad")
	if err := ensureOwnedRegularFile(binaryPath, 0o700); err != nil {
		return false, err
	}
	logPath, err := ensureServiceLog(home)
	if err != nil {
		return false, err
	}
	unit, err := renderSystemdUnit(home, userHome, binaryPath, logPath)
	if err != nil {
		return false, err
	}
	existing, exists, err := readOwnedServiceFile(unitPath)
	if err != nil {
		return false, err
	}
	unitChanged := !exists || !bytes.Equal(existing, unit)
	state, err := inspectSystemdService(ctx, systemctl)
	if err != nil {
		return false, err
	}
	fragmentMatches := filepath.Clean(state.fragmentPath) == filepath.Clean(unitPath)
	reloadNeeded := unitChanged || !state.loaded || !fragmentMatches || state.needsDaemonReload
	restartRequired := forceRestart || reloadNeeded || (state.active && state.mainPID <= 0)
	if restartRequired && (state.active || state.mainPID > 0) {
		stopArgs := []string{"--user", "stop", systemdServiceName}
		result := runServiceCommand(ctx, serviceStopTimeout, systemctl, stopArgs...)
		if result.err != nil {
			state, err = inspectSystemdService(ctx, systemctl)
			if err != nil || state.active || state.mainPID > 0 {
				return false, serviceCommandError(systemctl, stopArgs, result)
			}
		}
		if err := waitForDaemonRuntimeUnlock(ctx, home); err != nil {
			return false, err
		}
	}
	if !state.active && state.mainPID <= 0 {
		if err := ensureDaemonRuntimeUnlocked(home); err != nil {
			return false, err
		}
	}
	if unitChanged {
		if err := localstore.WriteFileAtomicPreservingParentMode(unitPath, unit, 0o644); err != nil {
			return false, fmt.Errorf("write systemd unit: %w", err)
		}
	}
	if reloadNeeded {
		reloadArgs := []string{"--user", "daemon-reload"}
		result := runServiceCommand(ctx, serviceCommandTimeout, systemctl, reloadArgs...)
		if result.err != nil {
			state, err = inspectSystemdService(ctx, systemctl)
			if err != nil || state.needsDaemonReload ||
				filepath.Clean(state.fragmentPath) != filepath.Clean(unitPath) {
				return false, serviceCommandError(systemctl, reloadArgs, result)
			}
		}
	}
	enabled, err := systemdServiceEnabled(ctx, systemctl)
	if err != nil {
		return false, err
	}
	if !enabled {
		enableArgs := []string{"--user", "enable", systemdServiceName}
		result := runServiceCommand(ctx, serviceCommandTimeout, systemctl, enableArgs...)
		if result.err != nil {
			enabled, err = systemdServiceEnabled(ctx, systemctl)
			if err != nil || !enabled {
				return false, serviceCommandError(systemctl, enableArgs, result)
			}
		}
	}
	state, err = inspectSystemdService(ctx, systemctl)
	if err != nil {
		return false, err
	}
	if restartRequired || !state.active || state.mainPID <= 0 {
		startArgs := []string{"--user", "start", systemdServiceName}
		result := runServiceCommand(ctx, serviceCommandTimeout, systemctl, startArgs...)
		if result.err != nil {
			state, err = inspectSystemdService(ctx, systemctl)
			if err != nil || !systemdServiceReady(state, unitPath) {
				return false, serviceCommandError(systemctl, startArgs, result)
			}
		}
	}
	state, err = inspectSystemdService(ctx, systemctl)
	if err != nil {
		return false, err
	}
	if !systemdServiceReady(state, unitPath) {
		return false, fmt.Errorf(
			"systemd service is not running from %s: active=%t main_pid=%d fragment_path=%q",
			unitPath,
			state.active,
			state.mainPID,
			state.fragmentPath,
		)
	}
	ensureSystemdLinger(ctx, stderr, log)
	return true, nil
}

func inspectDaemonService(ctx context.Context) (managedDaemonStatus, error) {
	systemctl, err := exec.LookPath("systemctl")
	if errors.Is(err, exec.ErrNotFound) {
		return managedDaemonStatus{}, nil
	}
	if err != nil {
		return managedDaemonStatus{}, fmt.Errorf("find systemctl: %w", err)
	}
	probe := runServiceCommand(ctx, serviceCommandTimeout, systemctl, "--user", "show-environment")
	if probe.err != nil {
		if systemdUnavailable(probe) {
			return managedDaemonStatus{}, nil
		}
		return managedDaemonStatus{}, serviceCommandError(systemctl, []string{"--user", "show-environment"}, probe)
	}
	state, err := inspectSystemdService(ctx, systemctl)
	if err != nil {
		return managedDaemonStatus{}, err
	}
	return managedDaemonStatus{
		manager:    "systemd",
		registered: state.loaded,
		running:    state.mainPID > 0,
		pid:        state.mainPID,
	}, nil
}

func stopDaemonService(ctx context.Context) error {
	systemctl, err := exec.LookPath("systemctl")
	if errors.Is(err, exec.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find systemctl: %w", err)
	}
	probe := runServiceCommand(ctx, serviceCommandTimeout, systemctl, "--user", "show-environment")
	if probe.err != nil {
		if systemdUnavailable(probe) {
			return nil
		}
		return serviceCommandError(systemctl, []string{"--user", "show-environment"}, probe)
	}
	state, err := inspectSystemdService(ctx, systemctl)
	if err != nil {
		return err
	}
	if !state.loaded {
		return nil
	}
	args := []string{"--user", "stop", systemdServiceName}
	result := runServiceCommand(ctx, serviceStopTimeout, systemctl, args...)
	if result.err != nil {
		return serviceCommandError(systemctl, args, result)
	}
	return nil
}

func uninstallDaemonService(ctx context.Context, home string) error {
	userHome, err := serviceUserHome()
	if err != nil {
		return err
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(configHome) {
		configHome = filepath.Join(userHome, ".config")
	}
	unitPath := filepath.Join(configHome, "systemd", "user", systemdServiceName)
	body, exists, err := readOwnedServiceFile(unitPath)
	if err != nil {
		return err
	}
	if exists {
		homeLine := []byte(`Environment="OMNARA_HOME=` + escapeSystemdQuotedValue(home) + `"`)
		if !bytes.Contains(body, homeLine) {
			return fmt.Errorf("systemd service does not belong to Omnara home %s", home)
		}
	}
	status, err := inspectDaemonService(ctx)
	if err != nil {
		return err
	}
	if exists && status.manager == "" {
		return errors.New("systemd is unavailable; cannot safely unregister the Omnara service")
	}
	systemctl := ""
	if status.manager != "" {
		systemctl, err = exec.LookPath("systemctl")
		if err != nil {
			return fmt.Errorf("find systemctl: %w", err)
		}
		if !exists {
			args := []string{"--user", "daemon-reload"}
			result := runServiceCommand(ctx, serviceCommandTimeout, systemctl, args...)
			if result.err != nil {
				return serviceCommandError(systemctl, args, result)
			}
		}
		state, err := inspectSystemdService(ctx, systemctl)
		if err != nil {
			return err
		}
		if state.loaded && filepath.Clean(state.fragmentPath) != filepath.Clean(unitPath) {
			return fmt.Errorf("systemd service is loaded from an unowned definition: %s", state.fragmentPath)
		}
		enabled, err := systemdServiceEnabled(ctx, systemctl)
		if err != nil {
			return err
		}
		if !exists && (state.loaded || enabled) {
			return errors.New("systemd service is registered without an owned service definition")
		}
		if state.loaded || enabled {
			args := []string{"--user", "disable", "--now", systemdServiceName}
			result := runServiceCommand(ctx, serviceStopTimeout, systemctl, args...)
			if result.err != nil {
				return serviceCommandError(systemctl, args, result)
			}
		}
	}
	if err := removeSystemdWantsLink(unitPath); err != nil {
		return err
	}
	if exists {
		if err := os.Remove(unitPath); err != nil {
			return fmt.Errorf("remove systemd service definition: %w", err)
		}
		if err := localstore.SyncDir(filepath.Dir(unitPath)); err != nil {
			return err
		}
	}
	if status.manager == "" || !exists {
		return nil
	}
	args := []string{"--user", "daemon-reload"}
	result := runServiceCommand(ctx, serviceCommandTimeout, systemctl, args...)
	if result.err != nil {
		return serviceCommandError(systemctl, args, result)
	}
	return nil
}

func removeSystemdWantsLink(unitPath string) error {
	wantsPath := filepath.Join(filepath.Dir(unitPath), "default.target.wants", systemdServiceName)
	info, err := os.Lstat(wantsPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect systemd service enablement: %w", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	if err := ensureCurrentUserOwner(info, wantsPath); err != nil {
		return err
	}
	target, err := os.Readlink(wantsPath)
	if err != nil {
		return fmt.Errorf("read systemd service enablement: %w", err)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(wantsPath), target)
	}
	if filepath.Clean(target) != filepath.Clean(unitPath) {
		return nil
	}
	if err := os.Remove(wantsPath); err != nil {
		return fmt.Errorf("remove systemd service enablement: %w", err)
	}
	return localstore.SyncDir(filepath.Dir(wantsPath))
}

func renderSystemdUnit(home, userHome, binaryPath, logPath string) ([]byte, error) {
	values := map[string]string{
		"OMNARA_HOME": home,
		"HOME":        userHome,
		"binary path": binaryPath,
		"log path":    logPath,
	}
	for name, value := range values {
		if err := validateServiceValue(name, value); err != nil {
			return nil, err
		}
		if strings.ContainsRune(value, '\\') {
			return nil, fmt.Errorf("%s contains a backslash", name)
		}
	}
	var unit bytes.Buffer
	if err := systemdUnitTemplate.Execute(&unit, systemdUnitTemplateData{
		BinaryPath:          binaryPath,
		Subcommand:          runServiceSubcommand,
		Home:                home,
		UserHome:            userHome,
		RestartDelaySeconds: int(daemonRestartDelay.Seconds()),
		LogPath:             logPath,
	}); err != nil {
		return nil, fmt.Errorf("render systemd unit: %w", err)
	}
	return unit.Bytes(), nil
}

func escapeSystemdPathValue(value string) string {
	return strings.ReplaceAll(value, "%", "%%")
}

func escapeSystemdQuotedValue(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`).Replace(value)
}

func escapeSystemdExecValue(value string) string {
	return strings.ReplaceAll(escapeSystemdQuotedValue(value), "$", "$$")
}

func inspectSystemdService(ctx context.Context, systemctl string) (systemdServiceState, error) {
	args := []string{
		"--user",
		"show",
		systemdServiceName,
		"--property=LoadState",
		"--property=FragmentPath",
		"--property=ActiveState",
		"--property=MainPID",
		"--property=NeedDaemonReload",
	}
	result := runServiceCommand(ctx, serviceCommandTimeout, systemctl, args...)
	if result.err != nil {
		lower := strings.ToLower(result.stderr + "\n" + result.stdout)
		if strings.Contains(lower, "not found") || strings.Contains(lower, "could not be found") {
			return systemdServiceState{}, nil
		}
		return systemdServiceState{}, serviceCommandError(systemctl, args, result)
	}
	values := map[string]string{}
	for _, line := range strings.Split(result.stdout, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = value
		}
	}
	return systemdServiceState{
		loaded:            values["LoadState"] == "loaded",
		active:            values["ActiveState"] == "active",
		mainPID:           parsePositivePID(values["MainPID"]),
		fragmentPath:      values["FragmentPath"],
		needsDaemonReload: values["NeedDaemonReload"] != "no",
	}, nil
}

func systemdServiceEnabled(ctx context.Context, systemctl string) (bool, error) {
	args := []string{"--user", "is-enabled", systemdServiceName}
	result := runServiceCommand(ctx, serviceCommandTimeout, systemctl, args...)
	value := strings.TrimSpace(result.stdout)
	if result.err == nil {
		return value == "enabled" || value == "enabled-runtime", nil
	}
	if value == "disabled" || value == "static" || value == "not-found" {
		return false, nil
	}
	return false, serviceCommandError(systemctl, args, result)
}

func systemdServiceReady(state systemdServiceState, unitPath string) bool {
	return state.loaded && state.active && state.mainPID > 0 && !state.needsDaemonReload &&
		filepath.Clean(state.fragmentPath) == filepath.Clean(unitPath)
}

func systemdUnavailable(result serviceCommandResult) bool {
	text := strings.ToLower(result.stderr + "\n" + result.stdout)
	for _, fragment := range []string{
		"failed to connect to bus",
		"no medium found",
		"system has not been booted with systemd",
		"transport endpoint is not connected",
	} {
		if strings.Contains(text, fragment) {
			return true
		}
	}
	return false
}

func ensureSystemdLinger(ctx context.Context, stderr io.Writer, log *slog.Logger) {
	username := strings.TrimSpace(os.Getenv("USER"))
	if username == "" {
		username = strconv.Itoa(os.Geteuid())
	}
	warn := func() {
		_, _ = fmt.Fprintf(stderr, systemdLingerWarningText+"\n", username)
	}
	loginctl, err := exec.LookPath("loginctl")
	if err != nil {
		warn()
		return
	}
	showArgs := []string{"show-user", username, "--property=Linger", "--value"}
	result := runServiceCommand(ctx, serviceCommandTimeout, loginctl, showArgs...)
	if result.err == nil && strings.TrimSpace(result.stdout) == "yes" {
		return
	}
	if result.err == nil && strings.TrimSpace(result.stdout) == "no" {
		enableArgs := []string{"enable-linger", username}
		enableResult := runServiceCommand(ctx, serviceCommandTimeout, loginctl, enableArgs...)
		verify := runServiceCommand(ctx, serviceCommandTimeout, loginctl, showArgs...)
		if verify.err == nil && strings.TrimSpace(verify.stdout) == "yes" {
			return
		}
		if enableResult.err != nil {
			log.Warn("enable systemd user lingering failed", "error", serviceCommandError(loginctl, enableArgs, enableResult))
		}
	}
	warn()
}
