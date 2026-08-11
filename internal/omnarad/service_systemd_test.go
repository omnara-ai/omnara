//go:build linux

package omnarad

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSystemdStartReconcilesService(t *testing.T) {
	home := filepath.Join(t.TempDir(), "daemon home")
	userHome := filepath.Join(t.TempDir(), "user home")
	configHome := filepath.Join(t.TempDir(), "config home")
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o700); err != nil {
		t.Fatalf("create daemon bin: %v", err)
	}
	writeTestExecutable(t, filepath.Join(home, "bin", "omnarad"), "#!/bin/sh\nexit 0\n")
	if err := os.MkdirAll(userHome, 0o700); err != nil {
		t.Fatalf("create user home: %v", err)
	}
	unitPath := filepath.Join(configHome, "systemd", "user", systemdServiceName)
	commands := filepath.Join(t.TempDir(), "systemctl.commands")
	active := filepath.Join(t.TempDir(), "systemctl.active")
	enabled := filepath.Join(t.TempDir(), "systemctl.enabled")
	linger := filepath.Join(t.TempDir(), "loginctl.linger")
	commandDir := t.TempDir()
	systemctl := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %s
[ "$1" = --user ] || exit 10
case "$2" in
  show-environment) exit 0 ;;
  show)
    if [ "$3" = --property=Version ]; then
      printf 'Version=240\n'
      exit 0
    fi
    if [ -f %s ]; then
	      printf 'LoadState=loaded\nFragmentPath=%%s\nNeedDaemonReload=no\n' %s
	    else
	      printf 'LoadState=not-found\nFragmentPath=\nNeedDaemonReload=no\n'
    fi
    if [ -f %s ]; then
      printf 'ActiveState=%%s\nMainPID=%d\n' "${SYSTEMD_ACTIVE_STATE:-active}"
    else
      printf 'ActiveState=inactive\nMainPID=0\n'
    fi
    ;;
  is-enabled)
    if [ -f %s ]; then printf 'enabled\n'; exit 0; fi
    printf 'disabled\n'
    exit 1
    ;;
  daemon-reload) exit 0 ;;
  enable) : > %s ;;
  start) : > %s ;;
	  stop) [ "${SYSTEMD_STOP_FAIL:-0}" = 0 ] || exit 13; rm -f %s ;;
  *) exit 11 ;;
esac
`, shellTestQuote(commands), shellTestQuote(unitPath), shellTestQuote(unitPath), shellTestQuote(active), os.Getpid(),
		shellTestQuote(enabled), shellTestQuote(enabled), shellTestQuote(active), shellTestQuote(active))
	writeTestExecutable(t, filepath.Join(commandDir, "systemctl"), systemctl)
	loginctl := fmt.Sprintf(`#!/bin/sh
if [ "$1" = show-user ]; then
  if [ -f %s ]; then printf 'yes\n'; else printf 'no\n'; fi
  exit 0
fi
if [ "$1" = enable-linger ]; then
  : > %s
  exit 0
fi
exit 12
`, shellTestQuote(linger), shellTestQuote(linger))
	writeTestExecutable(t, filepath.Join(commandDir, "loginctl"), loginctl)
	server := bootstrapServer(t, "token-a", "inst-a", "mch-a")
	defer server.Close()
	setConfiguredDaemonEnvironment(t, home, server.URL, "/stored/bin")
	t.Setenv("HOME", userHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("USER", "test-user")
	t.Setenv("PATH", commandDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout strings.Builder
	var stderr strings.Builder
	if code := Run(context.Background(), []string{"start"}, nil, &stdout, &stderr, discardLogger()); code != 0 {
		t.Fatalf("start exit code = %d, stderr = %q", code, stderr.String())
	}
	if stdout.String() != "omnarad daemon is running\n" || stderr.Len() != 0 {
		t.Fatalf("start stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	stdout.Reset()
	t.Setenv("SYSTEMD_ACTIVE_STATE", "reloading")
	if code := Run(context.Background(), []string{"status"}, nil, &stdout, &stderr, discardLogger()); code != 0 {
		t.Fatalf("status exit code = %d, stderr = %q", code, stderr.String())
	}
	if stdout.String() != fmt.Sprintf("omnarad is running (systemd, pid %d)\n", os.Getpid()) {
		t.Fatalf("status stdout = %q", stdout.String())
	}
	t.Setenv("SYSTEMD_ACTIVE_STATE", "active")
	unit, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read systemd unit: %v", err)
	}
	for _, value := range []string{
		`Type=exec`,
		`ExecStart="` + filepath.Join(home, "bin", "omnarad") + `" run-service`,
		`Environment="OMNARA_HOME=` + home + `"`,
		`Restart=on-failure`,
		`RestartSec=3`,
		`KillMode=process`,
		`TimeoutStopSec=30s`,
	} {
		if !strings.Contains(string(unit), value) {
			t.Fatalf("systemd unit missing %q", value)
		}
	}
	if strings.Contains(string(unit), "UMask=") {
		t.Fatalf("systemd unit overrides user workload umask: %s", unit)
	}
	firstCommands := readTestFile(t, commands)
	for _, command := range []string{"daemon-reload", "enable " + systemdServiceName, "start " + systemdServiceName} {
		if !strings.Contains(firstCommands, command) {
			t.Fatalf("first systemctl commands = %q, missing %q", firstCommands, command)
		}
	}
	if strings.Contains(firstCommands, "stop ") {
		t.Fatalf("first systemctl commands unexpectedly stopped service: %q", firstCommands)
	}
	before, err := os.Stat(unitPath)
	if err != nil {
		t.Fatalf("stat systemd unit: %v", err)
	}
	if err := os.WriteFile(commands, nil, 0o600); err != nil {
		t.Fatalf("clear systemctl commands: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{"start"}, nil, &stdout, &stderr, discardLogger()); code != 0 {
		t.Fatalf("unchanged start exit code = %d, stderr = %q", code, stderr.String())
	}
	after, err := os.Stat(unitPath)
	if err != nil {
		t.Fatalf("stat unchanged systemd unit: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("unchanged start replaced systemd unit")
	}
	unchangedCommands := readTestFile(t, commands)
	for _, command := range []string{"daemon-reload", "enable ", "start ", "stop "} {
		if strings.Contains(unchangedCommands, command) {
			t.Fatalf("unchanged systemctl commands = %q", unchangedCommands)
		}
	}
	if err := os.WriteFile(commands, nil, 0o600); err != nil {
		t.Fatalf("clear systemctl commands: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{"restart"}, nil, &stdout, &stderr, discardLogger()); code != 0 {
		t.Fatalf("restart exit code = %d, stderr = %q", code, stderr.String())
	}
	repairCommands := readTestFile(t, commands)
	for _, command := range []string{"stop " + systemdServiceName, "start " + systemdServiceName} {
		if !strings.Contains(repairCommands, command) {
			t.Fatalf("repair systemctl commands = %q, missing %q", repairCommands, command)
		}
	}
	if strings.Contains(repairCommands, "daemon-reload") {
		t.Fatalf("repair reloaded unchanged unit: %q", repairCommands)
	}
	if err := os.WriteFile(commands, nil, 0o600); err != nil {
		t.Fatalf("clear systemctl commands: %v", err)
	}
	t.Setenv("OMNARA_RUNNER_PATH", "/updated/bin")
	t.Setenv("SYSTEMD_STOP_FAIL", "1")
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{"start"}, nil, &stdout, &stderr, discardLogger()); code != 1 {
		t.Fatalf("config update exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "omnarad uses systemd on this machine") ||
		!strings.Contains(stderr.String(), filepath.Join(home, daemonConfigFileName)) {
		t.Fatalf("config update stderr = %q", stderr.String())
	}
	updatedConfig, err := loadDaemonConfig(home)
	if err != nil {
		t.Fatalf("load updated daemon config: %v", err)
	}
	if updatedConfig.RunnerPath != "/stored/bin" {
		t.Fatalf("runner path = %q, want /stored/bin", updatedConfig.RunnerPath)
	}
	configCommands := readTestFile(t, commands)
	for _, command := range []string{"daemon-reload", "start ", "stop "} {
		if strings.Contains(configCommands, command) {
			t.Fatalf("config update systemctl commands = %q", configCommands)
		}
	}
	t.Setenv("SYSTEMD_STOP_FAIL", "0")
	if err := os.Remove(active); err != nil {
		t.Fatalf("mark systemd service inactive: %v", err)
	}
	if err := os.WriteFile(commands, nil, 0o600); err != nil {
		t.Fatalf("clear systemctl commands: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{"start"}, nil, &stdout, &stderr, discardLogger()); code != 1 {
		t.Fatalf("stopped managed start exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "omnarad uses systemd on this machine") {
		t.Fatalf("stopped managed start stderr = %q", stderr.String())
	}
	stoppedCommands := readTestFile(t, commands)
	if strings.Contains(stoppedCommands, "start ") || strings.Contains(stoppedCommands, "stop ") {
		t.Fatalf("stopped managed start systemctl commands = %q", stoppedCommands)
	}
	if err := os.WriteFile(commands, nil, 0o600); err != nil {
		t.Fatalf("clear systemctl commands: %v", err)
	}
	if err := os.WriteFile(active, nil, 0o600); err != nil {
		t.Fatalf("mark systemd service active: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(
		context.Background(), []string{"start", "--no-service"}, nil, &stdout, &stderr, discardLogger(),
	); code != 0 {
		t.Fatalf("explicit no-service exit code = %d, stderr = %q", code, stderr.String())
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("explicit no-service stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	noServiceCommands := readTestFile(t, commands)
	if !strings.Contains(noServiceCommands, "stop "+systemdServiceName) ||
		strings.Contains(noServiceCommands, "start ") {
		t.Fatalf("explicit no-service systemctl commands = %q", noServiceCommands)
	}
}

func TestSystemdRejectsUnsupportedVersion(t *testing.T) {
	userHome := t.TempDir()
	commandDir := t.TempDir()
	writeTestExecutable(t, filepath.Join(commandDir, "systemctl"), `#!/bin/sh
if [ "$1" = --user ] && [ "$2" = show ] && [ "$3" = --property=Version ]; then
  printf 'Version=239\n'
  exit 0
fi
exit 1
`)
	t.Setenv("HOME", userHome)
	t.Setenv("PATH", commandDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stderr strings.Builder
	managed, err := ensureDaemonService(context.Background(), t.TempDir(), false, &stderr, discardLogger())
	if managed || err == nil || !strings.Contains(err.Error(), "requires systemd 240 or newer") {
		t.Fatalf("ensure daemon service = managed %t, error %v", managed, err)
	}
	if _, err := os.Stat(filepath.Join(userHome, ".config", "systemd")); !os.IsNotExist(err) {
		t.Fatalf("systemd directory exists after unsupported version: %v", err)
	}
}

func TestSystemdUnavailable(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	userHome := filepath.Join(t.TempDir(), "user-home")
	if err := os.MkdirAll(userHome, 0o700); err != nil {
		t.Fatalf("create user home: %v", err)
	}
	commandDir := t.TempDir()
	writeTestExecutable(t, filepath.Join(commandDir, "systemctl"), `#!/bin/sh
printf 'Failed to connect to bus: No medium found\n' >&2
exit 1
`)
	server := bootstrapServer(t, "token-a", "inst-a", "mch-a")
	defer server.Close()
	setConfiguredDaemonEnvironment(t, home, server.URL, "/bin")
	t.Setenv("HOME", userHome)
	t.Setenv("PATH", commandDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout strings.Builder
	ctx, cancel := context.WithCancel(context.Background())
	stderr := cancelWriter{cancel: cancel}
	if code := Run(ctx, []string{"start"}, nil, &stdout, &stderr, discardLogger()); code != 0 {
		t.Fatalf("unavailable systemd exit code = %d", code)
	}
	if stdout.Len() != 0 || stderr.String() != "warning: no launchd/systemd user service manager is available; "+
		"running omnarad with foreground restart supervision\n" {
		t.Fatalf("unavailable systemd stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(userHome, ".config", "systemd")); !os.IsNotExist(err) {
		t.Fatalf("systemd directory exists after unavailable probe: %v", err)
	}
}

func TestSystemdStop(t *testing.T) {
	home := t.TempDir()
	process, _ := startLockOwnerHelper(t, home)
	commands := filepath.Join(t.TempDir(), "commands")
	commandDir := t.TempDir()
	writeTestExecutable(t, filepath.Join(commandDir, "systemctl"), fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %s
[ "$1" = --user ] || exit 4
if [ "$2" = show-environment ]; then exit 0; fi
if [ "$2" = show ]; then
  printf 'LoadState=loaded\nFragmentPath=/tmp/omnarad.service\nNeedDaemonReload=no\nActiveState=activating\nMainPID=0\n'
  exit 0
fi
if [ "$2" = stop ]; then
  [ "${SYSTEMD_STOP_FAIL:-0}" = 0 ] || exit 13
  exit 0
fi
exit 5
`, shellTestQuote(commands)))
	t.Setenv("OMNARA_HOME", home)
	t.Setenv("PATH", commandDir)
	var stdout strings.Builder
	var stderr strings.Builder
	t.Setenv("SYSTEMD_STOP_FAIL", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if code := Run(ctx, []string{"stop"}, nil, &stdout, &stderr, discardLogger()); code != 1 {
		t.Fatalf("failed stop exit code = %d stderr = %q", code, stderr.String())
	}
	cancel()

	t.Setenv("SYSTEMD_STOP_FAIL", "0")
	stdout.Reset()
	stderr.Reset()
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	if code := Run(ctx, []string{"stop"}, nil, &stdout, &stderr, discardLogger()); code != 0 {
		t.Fatalf("stop exit code = %d stderr = %q", code, stderr.String())
	}
	cancel()
	if !strings.Contains(readTestFile(t, commands), "stop "+systemdServiceName) {
		t.Fatalf("systemctl commands = %q", readTestFile(t, commands))
	}
	if err := process.Wait(); err != nil {
		t.Fatalf("wait for lock owner: %v", err)
	}
}

func TestRenderSystemdUnitEscapesValues(t *testing.T) {
	body, err := renderSystemdUnit(
		`/tmp/a%b$home`,
		`/tmp/user home's`,
		`/tmp/bin"$omnarad`,
		`/tmp/daemon logs%output`,
	)
	if err != nil {
		t.Fatalf("render systemd unit: %v", err)
	}
	for _, value := range []string{
		`Environment="OMNARA_HOME=/tmp/a%%b$home"`,
		`Environment="HOME=/tmp/user home's"`,
		`ExecStart="/tmp/bin\"$$omnarad" run-service`,
		`StandardOutput=append:/tmp/daemon logs%%output`,
		`StandardError=append:/tmp/daemon logs%%output`,
		`WorkingDirectory=/tmp/user home's`,
	} {
		if !strings.Contains(string(body), value) {
			t.Fatalf("systemd unit missing escaped value %q: %s", value, body)
		}
	}
	if _, err := renderSystemdUnit("/tmp/bad\npath", "/tmp/home", "/tmp/bin", "/tmp/log"); err == nil {
		t.Fatal("systemd unit accepted control character")
	}
	if _, err := renderSystemdUnit(`/tmp/bad\path`, "/tmp/home", "/tmp/bin", "/tmp/log"); err == nil {
		t.Fatal("systemd unit accepted backslash")
	}
}

func TestRenderSystemdUnitPassesSystemdAnalyze(t *testing.T) {
	systemdAnalyze, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze is not available")
	}
	root := t.TempDir()
	home := filepath.Join(root, "daemon home %")
	userHome := filepath.Join(root, "user home %")
	binaryPath := filepath.Join(home, "bin", "omnarad")
	logPath := filepath.Join(home, "logs", "daemon", "daemon.log")
	for _, path := range []string{filepath.Dir(binaryPath), filepath.Dir(logPath), userHome} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("create service directory: %v", err)
		}
	}
	writeTestExecutable(t, binaryPath, "#!/bin/sh\nexit 0\n")
	body, err := renderSystemdUnit(home, userHome, binaryPath, logPath)
	if err != nil {
		t.Fatalf("render systemd unit: %v", err)
	}
	unitPath := filepath.Join(t.TempDir(), systemdServiceName)
	if err := os.WriteFile(unitPath, body, 0o600); err != nil {
		t.Fatalf("write systemd unit: %v", err)
	}
	if output, err := exec.Command(systemdAnalyze, "verify", unitPath).CombinedOutput(); err != nil {
		t.Fatalf("systemd-analyze verify: %v\n%s", err, output)
	}
}
