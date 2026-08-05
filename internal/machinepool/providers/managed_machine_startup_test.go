package providers

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBuildManagedMachineEnv(t *testing.T) {
	startupScript := "echo ready\n"
	env, err := BuildManagedMachineEnv(
		"  https://app.omnara.test///  ",
		"machine-token",
		startupScript,
		map[string]string{"APP_ENV": "production", "GITHUB_TOKEN": "resolved-secret"},
	)
	if err != nil {
		t.Fatalf("build managed machine env: %v", err)
	}
	if len(env) != 5 || env["OMNARA_API_URL"] != "https://app.omnara.test" ||
		env["OMNARA_MACHINE_TOKEN"] != "machine-token" ||
		env[startupScriptEnvVar] != base64.StdEncoding.EncodeToString([]byte(startupScript)) ||
		env["APP_ENV"] != "production" ||
		env["GITHUB_TOKEN"] != "resolved-secret" {
		t.Fatalf("managed machine env = %+v", env)
	}
}

func TestBuildManagedMachineEnvWithoutMachineEnv(t *testing.T) {
	env, err := BuildManagedMachineEnv("https://app.omnara.test", "machine-token", "", nil)
	if err != nil {
		t.Fatalf("build managed machine env: %v", err)
	}
	if len(env) != 2 || env["OMNARA_API_URL"] != "https://app.omnara.test" ||
		env["OMNARA_MACHINE_TOKEN"] != "machine-token" {
		t.Fatalf("managed machine env = %+v", env)
	}
}

func TestBuildManagedMachineEnvRejectsReservedMachineEnv(t *testing.T) {
	_, err := BuildManagedMachineEnv(
		"https://app.omnara.test",
		"machine-token",
		"",
		map[string]string{"omnara_api_url": "spoofed"},
	)
	if err == nil || !strings.Contains(err.Error(), "reserved OMNARA_ key") {
		t.Fatalf("build managed machine env error = %v, want reserved key rejection", err)
	}
}

func TestManagedMachineStartupRunsStartupScriptBeforeDaemon(t *testing.T) {
	dir := t.TempDir()
	markerPath := filepath.Join(dir, "startup-marker")
	server, _ := managedLauncherTestServer(
		t,
		fmt.Sprintf(
			"#!/bin/sh\nif [ ! -f %q ]; then echo startup-marker-missing; exit 9; fi\necho daemon-started\n",
			markerPath,
		),
	)
	startupScript := fmt.Sprintf("cat >/dev/null\numask > %q\necho startup-ran > %q\n", markerPath+".umask", markerPath)
	startupScriptPayload := base64.StdEncoding.EncodeToString([]byte(startupScript))
	fakeBin := managedStartupTestBin(t, dir, startupScriptPayload, startupScript)

	args := ManagedDaemonLauncherArgs()
	args[2] = `umask${IFS}027;` + args[2]
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(),
		"HOME="+dir,
		managedBootstrapScriptTestEnv(ManagedBootScript(startupScript)),
		"OMNARA_API_URL="+server.URL,
		"OMNARA_MACHINE_TOKEN=machine-token",
		startupScriptEnvVar+"="+startupScriptPayload,
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"NO_PROXY=127.0.0.1",
		"no_proxy=127.0.0.1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("launcher failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "daemon-started") {
		t.Fatalf("launcher output = %q, want daemon-started", out)
	}
	if got := strings.TrimSpace(string(mustReadFile(t, markerPath))); got != "startup-ran" {
		t.Fatalf("startup marker = %q, want startup-ran", got)
	}
	if got := strings.TrimSpace(string(mustReadFile(t, markerPath+".umask"))); got != "0027" {
		t.Fatalf("startup script umask = %q, want 0027", got)
	}
}

func TestManagedMachineStartupDoesNotExposeStartupPayloadToDaemon(t *testing.T) {
	dir := t.TempDir()
	server, _ := managedLauncherTestServer(
		t,
		"#!/bin/sh\n"+
			"if env | grep -q '^OMNARA_STARTUP_SCRIPT_PAYLOAD='; then "+
			"echo payload-leaked; else echo payload-cleared; fi\n",
	)
	startupScript := "exit 0\n"
	startupScriptPayload := base64.StdEncoding.EncodeToString([]byte(startupScript))
	fakeBin := managedStartupTestBin(t, dir, startupScriptPayload, startupScript)

	args := ManagedDaemonLauncherArgs()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(),
		"HOME="+dir,
		managedBootstrapScriptTestEnv(ManagedBootScript(startupScript)),
		"OMNARA_API_URL="+server.URL,
		"OMNARA_MACHINE_TOKEN=machine-token",
		startupScriptEnvVar+"="+startupScriptPayload,
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"NO_PROXY=127.0.0.1",
		"no_proxy=127.0.0.1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("launcher failed: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "payload-leaked") || !strings.Contains(string(out), "payload-cleared") {
		t.Fatalf("launcher output = %q, want payload cleared before daemon exec", out)
	}
}

func TestManagedMachineStartupFailurePreventsDaemonStart(t *testing.T) {
	dir := t.TempDir()
	server, requests := managedLauncherTestServer(t, "#!/bin/sh\necho daemon-started\n")
	startupScript := "exit 7\n"
	startupScriptPayload := base64.StdEncoding.EncodeToString([]byte(startupScript))
	fakeBin := managedStartupTestBin(t, dir, startupScriptPayload, startupScript)

	args := ManagedDaemonLauncherArgs()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(),
		"HOME="+dir,
		managedBootstrapScriptTestEnv(ManagedBootScript(startupScript)),
		"OMNARA_API_URL="+server.URL,
		"OMNARA_MACHINE_TOKEN=machine-token",
		startupScriptEnvVar+"="+startupScriptPayload,
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"NO_PROXY=127.0.0.1",
		"no_proxy=127.0.0.1",
	)
	out, err := cmd.CombinedOutput()
	output := string(out)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
		t.Fatalf("launcher error = %v output = %q, want exit status 7", err, output)
	}
	if strings.Contains(output, "daemon-started") {
		t.Fatalf("launcher output = %q, want daemon not to start", output)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("installer requests = %d, want 0", got)
	}
	if !strings.Contains(output, "omnara startup script failed with exit status 7") {
		t.Fatalf("launcher output = %q, want startup failure log", output)
	}
}

func TestManagedMachineStartupBlocksDaemonUntilStartupScriptFinishes(t *testing.T) {
	dir := t.TempDir()
	markerPath := filepath.Join(dir, "startup-running")
	stopPath := filepath.Join(dir, "startup-stop")
	donePath := filepath.Join(dir, "startup-done")
	server, _ := managedLauncherTestServer(t, "#!/bin/sh\necho daemon-started\n")
	startupScript := fmt.Sprintf(
		"echo running > %q\nwhile [ ! -f %q ]; do sleep 0.05; done\necho done > %q\n",
		markerPath,
		stopPath,
		donePath,
	)
	startupScriptPayload := base64.StdEncoding.EncodeToString([]byte(startupScript))
	fakeBin := managedStartupTestBin(t, dir, startupScriptPayload, startupScript)

	args := ManagedDaemonLauncherArgs()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(),
		"HOME="+dir,
		managedBootstrapScriptTestEnv(ManagedBootScript(startupScript)),
		"OMNARA_API_URL="+server.URL,
		"OMNARA_MACHINE_TOKEN=machine-token",
		startupScriptEnvVar+"="+startupScriptPayload,
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"NO_PROXY=127.0.0.1",
		"no_proxy=127.0.0.1",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start launcher: %v", err)
	}
	t.Cleanup(func() {
		if err := os.WriteFile(stopPath, []byte("stop\n"), 0o644); err == nil {
			waitForFileContent(t, donePath, "done")
		}
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		go func() {
			_, _ = io.Copy(io.Discard, stdout)
		}()
		go func() {
			_, _ = io.Copy(io.Discard, stderr)
		}()
		waitDone := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(waitDone)
		}()
		select {
		case <-waitDone:
		case <-time.After(500 * time.Millisecond):
		}
	})
	outputLine := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			outputLine <- scanner.Text()
			return
		}
		outputLine <- ""
	}()
	waitForFileContent(t, markerPath, "running")
	select {
	case line := <-outputLine:
		t.Fatalf("launcher output line = %q, want daemon blocked while startup runs", line)
	case <-time.After(200 * time.Millisecond):
	}
	if _, err := os.Stat(donePath); !os.IsNotExist(err) {
		t.Fatalf("startup done stat = %v, want startup script still running when daemon exits", err)
	}
	if err := os.WriteFile(stopPath, []byte("stop\n"), 0o644); err != nil {
		t.Fatalf("release startup script: %v", err)
	}
	waitForFileContent(t, donePath, "done")
	select {
	case line := <-outputLine:
		if line != "daemon-started" {
			t.Fatalf("launcher output line = %q, want daemon-started", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("daemon did not start after startup script finished")
	}
}

func TestManagedMachineStartupScriptIncludesStartupOrchestration(t *testing.T) {
	script := managedMachineStartupScript()
	if !strings.Contains(script, startupScriptEnvVar) ||
		!strings.Contains(script, `/bin/sh -c "$startup_script"`) {
		t.Fatal("startup script does not include startup orchestration")
	}
}

func TestManagedBootScriptWithoutStartupScript(t *testing.T) {
	if got, want := ManagedBootScript(""), managedDaemonLauncherScript(); got != want {
		t.Fatal("managed boot script without startup script does not equal daemon launcher")
	}
}

func managedStartupTestBin(t *testing.T, dir, expectedPayload, startupScript string) string {
	t.Helper()
	realBase64, err := exec.LookPath("base64")
	if err != nil {
		t.Fatalf("find base64: %v", err)
	}
	bootstrapPayload := base64.StdEncoding.EncodeToString([]byte(ManagedBootScript(startupScript)))
	fakeBin := filepath.Join(dir, "bin")
	if err := os.Mkdir(fakeBin, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	delimiter := "__OMNARA_STARTUP_SCRIPT_EOF__"
	for strings.Contains(startupScript, delimiter) {
		delimiter += "_"
	}
	if !strings.HasSuffix(startupScript, "\n") {
		startupScript += "\n"
	}
	writeExecutable(t, filepath.Join(fakeBin, "base64"), fmt.Sprintf(`#!/bin/sh
if [ "$1" != "-d" ]; then
  exit 2
fi
payload=$(cat)
if [ "$payload" = %s ]; then
  printf '%%s' "$payload" | %s -d
  exit $?
fi
if [ "$payload" != %s ]; then
  echo "unexpected startup script payload" >&2
  exit 2
fi
cat <<'%s'
%s%s
`, strconv.Quote(bootstrapPayload), strconv.Quote(realBase64), strconv.Quote(expectedPayload), delimiter, startupScript, delimiter))
	return fakeBin
}

func waitForFileContent(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if data, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(data)) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("file %s did not contain %q before deadline", path, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
