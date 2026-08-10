//go:build darwin

package omnarad

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLaunchdStartReconcilesService(t *testing.T) {
	home := filepath.Join(t.TempDir(), "daemon home")
	userHome := filepath.Join(t.TempDir(), "user home")
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o700); err != nil {
		t.Fatalf("create daemon bin: %v", err)
	}
	writeTestExecutable(t, filepath.Join(home, "bin", "omnarad"), "#!/bin/sh\nexit 0\n")
	if err := os.MkdirAll(userHome, 0o700); err != nil {
		t.Fatalf("create user home: %v", err)
	}
	commands := filepath.Join(t.TempDir(), "launchctl.commands")
	state := filepath.Join(t.TempDir(), "launchctl.state")
	commandDir := t.TempDir()
	domain := "gui/" + strconv.Itoa(os.Geteuid())
	serviceTarget := domain + "/" + launchdServiceLabel
	launchctl := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %s
if [ "$1" = print ] && [ "$2" = %s ]; then
  [ "${LAUNCHD_NO_DOMAIN:-0}" = 0 ] || { printf 'Could not find domain\n' >&2; exit 3; }
  exit 0
fi
if [ "$1" = print ] && [ "$2" = %s ]; then
  [ -f %s ] || { printf 'Could not find service\n' >&2; exit 3; }
  if [ "${LAUNCHD_STATE_STOPPED:-0}" = 1 ]; then
    printf 'state = waiting\n'
  else
    printf 'state = running\npid = %d\n'
  fi
  exit 0
fi
if [ "$1" = bootout ]; then
  rm -f %s
  exit 0
fi
if [ "$1" = bootstrap ]; then
  : > %s
  exit 0
fi
if [ "$1" = kickstart ] && [ "$2" = -p ]; then
  [ "${LAUNCHD_KICKSTART_FAIL:-0}" = 0 ] || exit 5
  : > %s
  printf '%%d\n' %d
  exit 0
fi
exit 4
`, shellTestQuote(commands), shellTestQuote(domain), shellTestQuote(serviceTarget), shellTestQuote(state), os.Getpid(),
		shellTestQuote(state), shellTestQuote(state), shellTestQuote(state), os.Getpid())
	writeTestExecutable(t, filepath.Join(commandDir, "launchctl"), launchctl)
	writeTestExecutable(t, filepath.Join(commandDir, "plutil"), "#!/bin/sh\nexit 0\n")
	server := bootstrapServer(t, "token-a", "inst-a", "mch-a")
	defer server.Close()
	setConfiguredDaemonEnvironment(t, home, server.URL, "/stored/bin")
	t.Setenv("HOME", userHome)
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
	if code := Run(context.Background(), []string{"status"}, nil, &stdout, &stderr, discardLogger()); code != 0 {
		t.Fatalf("status exit code = %d, stderr = %q", code, stderr.String())
	}
	if stdout.String() != fmt.Sprintf("omnarad is running (launchd, pid %d)\n", os.Getpid()) {
		t.Fatalf("status stdout = %q", stdout.String())
	}
	plistPath := filepath.Join(userHome, "Library", "LaunchAgents", launchdServiceLabel+".plist")
	plist, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatalf("read launchd plist: %v", err)
	}
	for _, value := range []string{
		"<string>" + filepath.Join(home, "bin", "omnarad") + "</string>",
		"<string>run-service</string>",
		"<key>SuccessfulExit</key>\n    <false/>",
		"<key>Crashed</key>\n    <true/>",
		"<key>ThrottleInterval</key>\n  <integer>3</integer>",
		"<integer>30</integer>",
	} {
		if !strings.Contains(string(plist), value) {
			t.Fatalf("launchd plist missing %q", value)
		}
	}
	if strings.Contains(string(plist), "<key>Umask</key>") {
		t.Fatalf("launchd plist overrides user workload umask: %s", plist)
	}
	firstCommands := readTestFile(t, commands)
	if !strings.Contains(firstCommands, "bootstrap "+domain+" "+plistPath) ||
		!strings.Contains(firstCommands, "kickstart -p "+serviceTarget) ||
		strings.Contains(firstCommands, "bootout ") {
		t.Fatalf("first launchctl commands = %q", firstCommands)
	}
	before, err := os.Stat(plistPath)
	if err != nil {
		t.Fatalf("stat launchd plist: %v", err)
	}
	if err := os.WriteFile(commands, nil, 0o600); err != nil {
		t.Fatalf("clear launchctl commands: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{"start"}, nil, &stdout, &stderr, discardLogger()); code != 0 {
		t.Fatalf("unchanged start exit code = %d, stderr = %q", code, stderr.String())
	}
	after, err := os.Stat(plistPath)
	if err != nil {
		t.Fatalf("stat unchanged launchd plist: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("unchanged start replaced launchd plist")
	}
	unchangedCommands := readTestFile(t, commands)
	for _, command := range []string{"bootout ", "bootstrap ", "kickstart "} {
		if strings.Contains(unchangedCommands, command) {
			t.Fatalf("unchanged launchctl commands = %q", unchangedCommands)
		}
	}
	if err := os.WriteFile(commands, nil, 0o600); err != nil {
		t.Fatalf("clear launchctl commands: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{"restart"}, nil, &stdout, &stderr, discardLogger()); code != 0 {
		t.Fatalf("restart exit code = %d, stderr = %q", code, stderr.String())
	}
	repairCommands := readTestFile(t, commands)
	for _, command := range []string{"bootout " + serviceTarget, "bootstrap " + domain, "kickstart -p " + serviceTarget} {
		if !strings.Contains(repairCommands, command) {
			t.Fatalf("repair launchctl commands = %q, missing %q", repairCommands, command)
		}
	}
	if err := os.WriteFile(commands, nil, 0o600); err != nil {
		t.Fatalf("clear launchctl commands: %v", err)
	}
	t.Setenv("OMNARA_RUNNER_PATH", "/updated/bin")
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{"start"}, nil, &stdout, &stderr, discardLogger()); code != 1 {
		t.Fatalf("changed start exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "omnarad uses launchd on this machine") ||
		!strings.Contains(stderr.String(), filepath.Join(home, daemonConfigFileName)) {
		t.Fatalf("changed start stderr = %q", stderr.String())
	}
	changedCommands := readTestFile(t, commands)
	for _, command := range []string{"bootout " + serviceTarget, "bootstrap " + domain, "kickstart -p " + serviceTarget} {
		if strings.Contains(changedCommands, command) {
			t.Fatalf("config update launchctl commands = %q", changedCommands)
		}
	}
	blockedConfig, err := loadDaemonConfig(home)
	if err != nil {
		t.Fatalf("load daemon config after override: %v", err)
	}
	if blockedConfig.RunnerPath != "/stored/bin" {
		t.Fatalf("runner path = %q, want /stored/bin", blockedConfig.RunnerPath)
	}
	if err := os.Remove(state); err != nil {
		t.Fatalf("mark launchd service inactive: %v", err)
	}
	if err := os.WriteFile(commands, nil, 0o600); err != nil {
		t.Fatalf("clear launchctl commands: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{"start"}, nil, &stdout, &stderr, discardLogger()); code != 1 {
		t.Fatalf("stopped managed start exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "omnarad uses launchd on this machine") {
		t.Fatalf("stopped managed start stderr = %q", stderr.String())
	}
	stoppedCommands := readTestFile(t, commands)
	for _, command := range []string{"bootout ", "bootstrap ", "kickstart "} {
		if strings.Contains(stoppedCommands, command) {
			t.Fatalf("stopped managed start launchctl commands = %q", stoppedCommands)
		}
	}
	if err := os.WriteFile(commands, nil, 0o600); err != nil {
		t.Fatalf("clear launchctl commands: %v", err)
	}
	if err := os.WriteFile(state, nil, 0o600); err != nil {
		t.Fatalf("mark launchd service running: %v", err)
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
	if !strings.Contains(noServiceCommands, "bootout "+serviceTarget) ||
		strings.Contains(noServiceCommands, "bootstrap ") ||
		strings.Contains(noServiceCommands, "kickstart ") {
		t.Fatalf("explicit no-service launchctl commands = %q", noServiceCommands)
	}
	if err := os.WriteFile(commands, nil, 0o600); err != nil {
		t.Fatalf("clear launchctl commands: %v", err)
	}
	t.Setenv("OMNARA_RUNNER_PATH", "")
	t.Setenv("LAUNCHD_STATE_STOPPED", "1")
	t.Setenv("LAUNCHD_KICKSTART_FAIL", "1")
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{"start"}, nil, &stdout, &stderr, discardLogger()); code != 1 {
		t.Fatalf("failed kickstart exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "kickstart") {
		t.Fatalf("failed kickstart stderr = %q", stderr.String())
	}
}

func TestLaunchdUnavailable(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	userHome := filepath.Join(t.TempDir(), "user-home")
	if err := os.MkdirAll(userHome, 0o700); err != nil {
		t.Fatalf("create user home: %v", err)
	}
	commandDir := t.TempDir()
	writeTestExecutable(t, filepath.Join(commandDir, "launchctl"), `#!/bin/sh
printf 'Could not find domain\n' >&2
exit 3
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
		t.Fatalf("unavailable launchd exit code = %d", code)
	}
	if stdout.Len() != 0 || stderr.String() != "warning: no launchd/systemd user service manager is available; "+
		"running omnarad with foreground restart supervision\n" {
		t.Fatalf("unavailable launchd stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(userHome, "Library", "LaunchAgents")); !os.IsNotExist(err) {
		t.Fatalf("launchd directory exists after unavailable probe: %v", err)
	}
}

func TestLaunchdStop(t *testing.T) {
	home := t.TempDir()
	process, ready, _ := startLockOwnerHelper(t, home)
	waitForFileSize(t, ready, 0)
	commands := filepath.Join(t.TempDir(), "commands")
	domain := "gui/" + strconv.Itoa(os.Geteuid())
	target := domain + "/" + launchdServiceLabel
	commandDir := t.TempDir()
	writeTestExecutable(t, filepath.Join(commandDir, "launchctl"), fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %s
if [ "$1" = print ] && [ "$2" = %s ]; then exit 0; fi
if [ "$1" = print ] && [ "$2" = %s ]; then
  printf 'state = running\npid = %d\n'
  exit 0
fi
if [ "$1" = bootout ]; then exit 0; fi
exit 4
`, shellTestQuote(commands), shellTestQuote(domain), shellTestQuote(target), os.Getpid()))
	t.Setenv("OMNARA_HOME", home)
	t.Setenv("PATH", commandDir)
	var stdout strings.Builder
	var stderr strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if code := Run(ctx, []string{"stop"}, nil, &stdout, &stderr, discardLogger()); code != 0 {
		t.Fatalf("stop exit code = %d stderr = %q", code, stderr.String())
	}
	if !strings.Contains(readTestFile(t, commands), "bootout "+target) {
		t.Fatalf("launchctl commands = %q", readTestFile(t, commands))
	}
	if err := process.Wait(); err != nil {
		t.Fatalf("wait for lock owner: %v", err)
	}
}

func TestLaunchdUninstallRemovesMatchingService(t *testing.T) {
	home := filepath.Join(t.TempDir(), "daemon-home")
	userHome := filepath.Join(t.TempDir(), "user-home")
	plistDir := filepath.Join(userHome, "Library", "LaunchAgents")
	if err := os.MkdirAll(plistDir, 0o700); err != nil {
		t.Fatalf("create launchd directory: %v", err)
	}
	plist, err := renderLaunchdPlist(home, userHome, "/opt/omnarad", filepath.Join(home, "daemon.log"))
	if err != nil {
		t.Fatalf("render launchd plist: %v", err)
	}
	plistPath := filepath.Join(plistDir, launchdServiceLabel+".plist")
	if err := os.WriteFile(plistPath, plist, 0o644); err != nil {
		t.Fatalf("write launchd plist: %v", err)
	}
	commands := filepath.Join(t.TempDir(), "commands")
	domain := "gui/" + strconv.Itoa(os.Geteuid())
	target := domain + "/" + launchdServiceLabel
	commandDir := t.TempDir()
	writeTestExecutable(t, filepath.Join(commandDir, "launchctl"), fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %s
if [ "$1" = print ] && [ "$2" = %s ]; then exit 0; fi
if [ "$1" = print ] && [ "$2" = %s ]; then printf 'path = %%s\nstate = running\npid = %d\n' %s; exit 0; fi
if [ "$1" = bootout ]; then exit 0; fi
exit 4
`, shellTestQuote(commands), shellTestQuote(domain), shellTestQuote(target), os.Getpid(), shellTestQuote(plistPath)))
	t.Setenv("HOME", userHome)
	t.Setenv("PATH", commandDir)
	if err := uninstallDaemonService(context.Background(), home); err != nil {
		t.Fatalf("uninstall launchd service: %v", err)
	}
	if _, err := os.Lstat(plistPath); !os.IsNotExist(err) {
		t.Fatalf("launchd plist still exists: %v", err)
	}
	if !strings.Contains(readTestFile(t, commands), "bootout "+target) {
		t.Fatalf("launchctl commands = %q", readTestFile(t, commands))
	}
}

func TestLaunchdUninstallRejectsRegisteredForeignDefinition(t *testing.T) {
	home := filepath.Join(t.TempDir(), "daemon-home")
	userHome := filepath.Join(t.TempDir(), "user-home")
	plistDir := filepath.Join(userHome, "Library", "LaunchAgents")
	if err := os.MkdirAll(plistDir, 0o700); err != nil {
		t.Fatal(err)
	}
	plist, err := renderLaunchdPlist(home, userHome, "/opt/omnarad", filepath.Join(home, "daemon.log"))
	if err != nil {
		t.Fatal(err)
	}
	plistPath := filepath.Join(plistDir, launchdServiceLabel+".plist")
	if err := os.WriteFile(plistPath, plist, 0o600); err != nil {
		t.Fatal(err)
	}
	commands := filepath.Join(t.TempDir(), "commands")
	domain := "gui/" + strconv.Itoa(os.Geteuid())
	target := domain + "/" + launchdServiceLabel
	commandDir := t.TempDir()
	writeTestExecutable(t, filepath.Join(commandDir, "launchctl"), fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %s
if [ "$1" = print ] && [ "$2" = %s ]; then exit 0; fi
if [ "$1" = print ] && [ "$2" = %s ]; then
  printf 'path = /Library/LaunchAgents/com.omnara.omnarad.plist\nstate = running\npid = %d\n'
  exit 0
fi
if [ "$1" = bootout ]; then exit 0; fi
exit 4
`, shellTestQuote(commands), shellTestQuote(domain), shellTestQuote(target), os.Getpid()))
	t.Setenv("HOME", userHome)
	t.Setenv("PATH", commandDir)
	if err := uninstallDaemonService(context.Background(), home); err == nil ||
		!strings.Contains(err.Error(), "registered from an unowned definition") {
		t.Fatalf("uninstall error = %v", err)
	}
	if _, err := os.Stat(plistPath); err != nil {
		t.Fatalf("owned plist was removed: %v", err)
	}
	if got := readTestFile(t, commands); strings.Contains(got, "bootout "+target) {
		t.Fatalf("launchctl commands = %q", got)
	}
}

func TestLaunchdUninstallRejectsRegisteredServiceWithoutDefinition(t *testing.T) {
	home := filepath.Join(t.TempDir(), "daemon-home")
	userHome := filepath.Join(t.TempDir(), "user-home")
	commands := filepath.Join(t.TempDir(), "commands")
	domain := "gui/" + strconv.Itoa(os.Geteuid())
	target := domain + "/" + launchdServiceLabel
	commandDir := t.TempDir()
	writeTestExecutable(t, filepath.Join(commandDir, "launchctl"), fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %s
if [ "$1" = print ] && [ "$2" = %s ]; then exit 0; fi
if [ "$1" = print ] && [ "$2" = %s ]; then printf 'state = waiting\n'; exit 0; fi
if [ "$1" = bootout ]; then exit 0; fi
exit 4
`, shellTestQuote(commands), shellTestQuote(domain), shellTestQuote(target)))
	t.Setenv("HOME", userHome)
	t.Setenv("PATH", commandDir)
	if err := uninstallDaemonService(context.Background(), home); err == nil ||
		!strings.Contains(err.Error(), "registered without an owned service definition") {
		t.Fatalf("uninstall error = %v", err)
	}
	if got := readTestFile(t, commands); strings.Contains(got, "bootout "+target) {
		t.Fatalf("launchctl commands = %q", got)
	}
}

func TestLaunchdUninstallRejectsUnavailableManager(t *testing.T) {
	home := filepath.Join(t.TempDir(), "daemon-home")
	userHome := filepath.Join(t.TempDir(), "user-home")
	plistDir := filepath.Join(userHome, "Library", "LaunchAgents")
	if err := os.MkdirAll(plistDir, 0o700); err != nil {
		t.Fatal(err)
	}
	plist, err := renderLaunchdPlist(home, userHome, "/opt/omnarad", filepath.Join(home, "daemon.log"))
	if err != nil {
		t.Fatal(err)
	}
	plistPath := filepath.Join(plistDir, launchdServiceLabel+".plist")
	if err := os.WriteFile(plistPath, plist, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", userHome)
	t.Setenv("PATH", t.TempDir())
	if err := uninstallDaemonService(context.Background(), home); err == nil ||
		!strings.Contains(err.Error(), "launchd is unavailable") {
		t.Fatalf("uninstall error = %v", err)
	}
	if _, err := os.Stat(plistPath); err != nil {
		t.Fatalf("launchd plist was removed: %v", err)
	}
}

func TestLaunchdUninstallRejectsDifferentHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "daemon-home")
	userHome := filepath.Join(t.TempDir(), "user-home")
	plistDir := filepath.Join(userHome, "Library", "LaunchAgents")
	if err := os.MkdirAll(plistDir, 0o700); err != nil {
		t.Fatal(err)
	}
	plist, err := renderLaunchdPlist("/other/home", userHome, "/opt/omnarad", "/tmp/daemon.log")
	if err != nil {
		t.Fatal(err)
	}
	plistPath := filepath.Join(plistDir, launchdServiceLabel+".plist")
	if err := os.WriteFile(plistPath, plist, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", userHome)
	t.Setenv("PATH", t.TempDir())
	if err := uninstallDaemonService(context.Background(), home); err == nil ||
		!strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("uninstall error = %v", err)
	}
	if _, err := os.Stat(plistPath); err != nil {
		t.Fatalf("launchd plist was removed: %v", err)
	}
}

func TestLaunchdServiceReadyRequiresLivePID(t *testing.T) {
	if launchdServiceReady(launchdServiceState{registered: true, running: true}) {
		t.Fatal("launchd service without a PID reported ready")
	}
	if !launchdServiceReady(launchdServiceState{registered: true, running: true, pid: os.Getpid()}) {
		t.Fatal("launchd service with a live PID reported unready")
	}
}

func TestRenderLaunchdPlistEscapesValues(t *testing.T) {
	body, err := renderLaunchdPlist("/tmp/a&b", "/tmp/<home>", `/tmp/"bin"`, "/tmp/a>b.log")
	if err != nil {
		t.Fatalf("render launchd plist: %v", err)
	}
	for _, value := range []string{
		"<key>ProgramArguments</key>\n  <array>\n    <string>/tmp/&#34;bin&#34;</string>\n    <string>run-service</string>",
		"<key>OMNARA_HOME</key>\n    <string>/tmp/a&amp;b</string>",
		"<key>HOME</key>\n    <string>/tmp/&lt;home&gt;</string>",
		"<key>StandardOutPath</key>\n  <string>/tmp/a&gt;b.log</string>",
		"<key>StandardErrorPath</key>\n  <string>/tmp/a&gt;b.log</string>",
		"<key>WorkingDirectory</key>\n  <string>/tmp/&lt;home&gt;</string>",
	} {
		if !strings.Contains(string(body), value) {
			t.Fatalf("launchd plist missing expected content %q: %s", value, body)
		}
	}
	if _, err := renderLaunchdPlist("/tmp/bad\npath", "/tmp/home", "/tmp/bin", "/tmp/log"); err == nil {
		t.Fatal("launchd plist accepted control character")
	}
}
