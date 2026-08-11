//go:build darwin

package omnarad

import (
	"bytes"
	"context"
	"encoding/xml"
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
	"text/template"

	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
)

const launchdServiceLabel = "com.omnara.omnarad"
const daemonServiceManager = "launchd"

var launchdPlistTemplate = template.Must(
	template.New(launchdServiceLabel + ".plist").
		Funcs(template.FuncMap{"escapeXMLValue": escapeXMLValue}).
		Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>{{.Label | escapeXMLValue}}</string>
  <key>ProgramArguments</key>
  <array>
    <string>{{.BinaryPath | escapeXMLValue}}</string>
    <string>{{.Subcommand | escapeXMLValue}}</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>OMNARA_HOME</key>
    <string>{{.Home | escapeXMLValue}}</string>
    <key>HOME</key>
    <string>{{.UserHome | escapeXMLValue}}</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
    <key>Crashed</key>
    <true/>
  </dict>
  <key>ThrottleInterval</key>
  <integer>{{.RestartDelaySeconds}}</integer>
  <key>ExitTimeOut</key>
  <integer>30</integer>
  <key>StandardOutPath</key>
  <string>{{.LogPath | escapeXMLValue}}</string>
  <key>StandardErrorPath</key>
  <string>{{.LogPath | escapeXMLValue}}</string>
  <key>WorkingDirectory</key>
  <string>{{.UserHome | escapeXMLValue}}</string>
</dict>
</plist>
`),
)

type launchdPlistTemplateData struct {
	Label               string
	BinaryPath          string
	Subcommand          string
	Home                string
	UserHome            string
	RestartDelaySeconds int
	LogPath             string
}

type launchdServiceState struct {
	definitionPath string
	registered     bool
	running        bool
	pid            int
}

type launchdManagedDaemonStatus struct {
	managerAvailable bool
	definitionPath   string
	registered       bool
	running          bool
	pid              int
}

func ensureDaemonService(
	ctx context.Context,
	home string,
	forceRestart bool,
	_ io.Writer,
	_ *slog.Logger,
) (bool, error) {
	launchctl, err := exec.LookPath("launchctl")
	if errors.Is(err, exec.ErrNotFound) {
		return false, ensureDaemonRuntimeUnlocked(home)
	}
	if err != nil {
		return false, fmt.Errorf("find launchctl: %w", err)
	}
	domain := "gui/" + strconv.Itoa(os.Geteuid())
	domainProbe := runServiceCommand(ctx, serviceCommandTimeout, launchctl, "print", domain)
	if domainProbe.err != nil {
		if launchdUnavailable(domainProbe) {
			return false, ensureDaemonRuntimeUnlocked(home)
		}
		return false, serviceCommandError(launchctl, []string{"print", domain}, domainProbe)
	}
	userHome, err := serviceUserHome()
	if err != nil {
		return false, err
	}
	plistDir := filepath.Join(userHome, "Library", "LaunchAgents")
	if err := createOwnedDirectoryAll(plistDir, 0o700); err != nil {
		return false, err
	}
	plistPath := filepath.Join(plistDir, launchdServiceLabel+".plist")
	binaryPath := filepath.Join(home, "bin", "omnarad")
	if err := ensureOwnedRegularFile(binaryPath, 0o700); err != nil {
		return false, err
	}
	logPath, err := ensureServiceLog(home)
	if err != nil {
		return false, err
	}
	plist, err := renderLaunchdPlist(home, userHome, binaryPath, logPath)
	if err != nil {
		return false, err
	}
	existing, exists, err := readOwnedServiceFile(plistPath)
	if err != nil {
		return false, err
	}
	plistChanged := !exists || !bytes.Equal(existing, plist)
	if plistChanged {
		if err := validateLaunchdPlist(ctx, plistDir, plist); err != nil {
			return false, err
		}
	}
	serviceTarget := domain + "/" + launchdServiceLabel
	state, err := inspectLaunchdService(ctx, launchctl, serviceTarget)
	if err != nil {
		return false, err
	}
	restartRequired := forceRestart || plistChanged
	if restartRequired && state.registered {
		bootoutArgs := []string{"bootout", serviceTarget}
		result := runServiceCommand(ctx, serviceStopTimeout, launchctl, bootoutArgs...)
		if result.err != nil {
			state, err = inspectLaunchdService(ctx, launchctl, serviceTarget)
			if err != nil || state.registered {
				return false, serviceCommandError(launchctl, bootoutArgs, result)
			}
		}
		if err := waitForDaemonRuntimeUnlock(ctx, home); err != nil {
			return false, err
		}
		state = launchdServiceState{}
	}
	if !state.running && state.pid <= 0 {
		if err := ensureDaemonRuntimeUnlocked(home); err != nil {
			return false, err
		}
	}
	if plistChanged {
		if err := localstore.WriteFileAtomicPreservingParentMode(plistPath, plist, 0o644); err != nil {
			return false, fmt.Errorf("write launchd plist: %w", err)
		}
	}
	if !state.registered {
		bootstrapArgs := []string{"bootstrap", domain, plistPath}
		result := runServiceCommand(ctx, serviceCommandTimeout, launchctl, bootstrapArgs...)
		if result.err != nil {
			state, err = inspectLaunchdService(ctx, launchctl, serviceTarget)
			if err != nil || !state.registered {
				return false, serviceCommandError(launchctl, bootstrapArgs, result)
			}
		}
	}
	state, err = inspectLaunchdService(ctx, launchctl, serviceTarget)
	if err != nil {
		return false, err
	}
	if restartRequired || !state.running || state.pid <= 0 {
		kickstartArgs := []string{"kickstart", "-p", serviceTarget}
		result := runServiceCommand(ctx, serviceCommandTimeout, launchctl, kickstartArgs...)
		if result.err != nil {
			state, err = inspectLaunchdService(ctx, launchctl, serviceTarget)
			if err != nil || !launchdServiceReady(state) {
				return false, serviceCommandError(launchctl, kickstartArgs, result)
			}
		}
	}
	state, err = inspectLaunchdService(ctx, launchctl, serviceTarget)
	if err != nil {
		return false, err
	}
	if !launchdServiceReady(state) {
		return false, fmt.Errorf(
			"launchd service is not running: registered=%t running=%t pid=%d",
			state.registered,
			state.running,
			state.pid,
		)
	}
	return true, nil
}

func inspectDaemonService(ctx context.Context) (launchdManagedDaemonStatus, error) {
	launchctl, err := exec.LookPath("launchctl")
	if errors.Is(err, exec.ErrNotFound) {
		return launchdManagedDaemonStatus{}, nil
	}
	if err != nil {
		return launchdManagedDaemonStatus{}, fmt.Errorf("find launchctl: %w", err)
	}
	domain := "gui/" + strconv.Itoa(os.Geteuid())
	probe := runServiceCommand(ctx, serviceCommandTimeout, launchctl, "print", domain)
	if probe.err != nil {
		if launchdUnavailable(probe) {
			return launchdManagedDaemonStatus{}, nil
		}
		return launchdManagedDaemonStatus{}, serviceCommandError(launchctl, []string{"print", domain}, probe)
	}
	state, err := inspectLaunchdService(ctx, launchctl, domain+"/"+launchdServiceLabel)
	if err != nil {
		return launchdManagedDaemonStatus{}, err
	}
	return launchdManagedDaemonStatus{
		managerAvailable: true,
		definitionPath:   state.definitionPath,
		registered:       state.registered,
		running:          launchdServiceReady(state),
		pid:              state.pid,
	}, nil
}

func stopDaemonService(ctx context.Context) error {
	launchctl, err := exec.LookPath("launchctl")
	if errors.Is(err, exec.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find launchctl: %w", err)
	}
	domain := "gui/" + strconv.Itoa(os.Geteuid())
	probe := runServiceCommand(ctx, serviceCommandTimeout, launchctl, "print", domain)
	if probe.err != nil {
		if launchdUnavailable(probe) {
			return nil
		}
		return serviceCommandError(launchctl, []string{"print", domain}, probe)
	}
	target := domain + "/" + launchdServiceLabel
	state, err := inspectLaunchdService(ctx, launchctl, target)
	if err != nil {
		return err
	}
	if !state.registered {
		return nil
	}
	args := []string{"bootout", target}
	result := runServiceCommand(ctx, serviceStopTimeout, launchctl, args...)
	if result.err != nil {
		current, inspectErr := inspectLaunchdService(ctx, launchctl, target)
		if inspectErr != nil || current.registered {
			return serviceCommandError(launchctl, args, result)
		}
	}
	return nil
}

func uninstallDaemonService(ctx context.Context, home string) error {
	userHome, err := serviceUserHome()
	if err != nil {
		return err
	}
	plistPath := filepath.Join(userHome, "Library", "LaunchAgents", launchdServiceLabel+".plist")
	body, exists, err := readOwnedServiceFile(plistPath)
	if err != nil {
		return err
	}
	if exists {
		homeEntry := []byte(
			"<key>OMNARA_HOME</key>\n    <string>" + escapeXMLValue(home) + "</string>",
		)
		if !bytes.Contains(body, homeEntry) {
			return fmt.Errorf("launchd service does not belong to Omnara home %s", home)
		}
	}
	status, err := inspectDaemonService(ctx)
	if err != nil {
		return err
	}
	if !exists && status.registered {
		return errors.New("launchd service is registered without an owned service definition")
	}
	if status.registered && filepath.Clean(status.definitionPath) != filepath.Clean(plistPath) {
		return fmt.Errorf("launchd service is registered from an unowned definition: %q", status.definitionPath)
	}
	if exists && !status.managerAvailable {
		return errors.New("launchd is unavailable; cannot safely unregister the Omnara service")
	}
	if exists && status.managerAvailable {
		if err := stopDaemonService(ctx); err != nil {
			return err
		}
	}
	if !exists {
		return nil
	}
	if err := os.Remove(plistPath); err != nil {
		return fmt.Errorf("remove launchd service definition: %w", err)
	}
	return localstore.SyncDir(filepath.Dir(plistPath))
}

func renderLaunchdPlist(home, userHome, binaryPath, logPath string) ([]byte, error) {
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
	}
	var plist bytes.Buffer
	if err := launchdPlistTemplate.Execute(&plist, launchdPlistTemplateData{
		Label:               launchdServiceLabel,
		BinaryPath:          binaryPath,
		Subcommand:          runServiceSubcommand,
		Home:                home,
		UserHome:            userHome,
		RestartDelaySeconds: int(daemonRestartDelay.Seconds()),
		LogPath:             logPath,
	}); err != nil {
		return nil, fmt.Errorf("render launchd plist: %w", err)
	}
	return plist.Bytes(), nil
}

func escapeXMLValue(value string) string {
	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, []byte(value))
	return escaped.String()
}

func validateLaunchdPlist(ctx context.Context, dir string, body []byte) error {
	plutil, err := exec.LookPath("plutil")
	if err != nil {
		return fmt.Errorf("plutil is required to validate the launchd service definition: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+launchdServiceLabel+".plist.validate-*")
	if err != nil {
		return fmt.Errorf("create temporary launchd plist: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temporary launchd plist: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary launchd plist: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary launchd plist: %w", err)
	}
	args := []string{"-lint", tmpPath}
	result := runServiceCommand(ctx, serviceCommandTimeout, plutil, args...)
	if result.err != nil {
		return serviceCommandError(plutil, args, result)
	}
	return nil
}

func inspectLaunchdService(ctx context.Context, launchctl, serviceTarget string) (launchdServiceState, error) {
	args := []string{"print", serviceTarget}
	result := runServiceCommand(ctx, serviceCommandTimeout, launchctl, args...)
	if result.err != nil {
		if launchdServiceMissing(result) {
			return launchdServiceState{}, nil
		}
		return launchdServiceState{}, serviceCommandError(launchctl, args, result)
	}
	state := launchdServiceState{registered: true}
	for _, line := range strings.Split(result.stdout, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), " = ")
		if !ok {
			continue
		}
		switch key {
		case "path":
			state.definitionPath = value
		case "state":
			state.running = value == "running"
		case "pid":
			state.pid = parsePositivePID(value)
		}
	}
	return state, nil
}

func launchdServiceReady(state launchdServiceState) bool {
	if !state.registered || state.pid <= 0 {
		return false
	}
	err := syscall.Kill(state.pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func launchdUnavailable(result serviceCommandResult) bool {
	text := strings.ToLower(result.stderr + "\n" + result.stdout)
	return strings.Contains(text, "could not find domain") ||
		strings.Contains(text, "domain does not exist") ||
		strings.Contains(text, "no such process")
}

func launchdServiceMissing(result serviceCommandResult) bool {
	text := strings.ToLower(result.stderr + "\n" + result.stdout)
	return strings.Contains(text, "could not find service") ||
		strings.Contains(text, "service not found") ||
		strings.Contains(text, "no such process")
}
