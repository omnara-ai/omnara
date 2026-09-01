package providers

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
		ManagedMachineEndpoints{
			APIURL:       "  https://api.omnara.test/v1///  ",
			InstallerURL: " https://app.omnara.test/install/omnarad.sh ",
		},
		"machine-token",
		startupScript,
		map[string]string{"APP_ENV": "production", "GITHUB_TOKEN": "resolved-secret"},
	)
	if err != nil {
		t.Fatalf("build managed machine env: %v", err)
	}
	if len(env) != 6 || env["OMNARA_API_URL"] != "https://api.omnara.test/v1" ||
		env["OMNARA_INSTALLER_URL"] != "https://app.omnara.test/install/omnarad.sh" ||
		env["OMNARA_MACHINE_TOKEN"] != "machine-token" ||
		env[startupScriptEnvVar] != base64.StdEncoding.EncodeToString([]byte(startupScript)) ||
		env["APP_ENV"] != "production" ||
		env["GITHUB_TOKEN"] != "resolved-secret" {
		t.Fatalf("managed machine env = %+v", env)
	}
}

func TestBuildManagedMachineEnvWithoutMachineEnv(t *testing.T) {
	env, err := BuildManagedMachineEnv(ManagedMachineEndpoints{
		APIURL:       "https://api.omnara.test/v1",
		InstallerURL: "https://app.omnara.test/install/omnarad.sh",
	}, "machine-token", "", nil)
	if err != nil {
		t.Fatalf("build managed machine env: %v", err)
	}
	if len(env) != 3 || env["OMNARA_API_URL"] != "https://api.omnara.test/v1" ||
		env["OMNARA_INSTALLER_URL"] != "https://app.omnara.test/install/omnarad.sh" ||
		env["OMNARA_MACHINE_TOKEN"] != "machine-token" {
		t.Fatalf("managed machine env = %+v", env)
	}
}

func TestBuildManagedMachineEnvRejectsReservedMachineEnv(t *testing.T) {
	_, err := BuildManagedMachineEnv(
		ManagedMachineEndpoints{
			APIURL:       "https://api.omnara.test/v1",
			InstallerURL: "https://app.omnara.test/install/omnarad.sh",
		},
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
	server, requests := managedLauncherTestServer(
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
		managedBootstrapScriptTestEnv(ManagedBootScript()),
		"OMNARA_API_URL="+server.URL+"/api/v1",
		"OMNARA_INSTALLER_URL="+server.URL+"/install/omnarad.sh",
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
	if got := requests.Load(); got != 1 {
		t.Fatalf("launcher requests = %d, want installer only", got)
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
		managedBootstrapScriptTestEnv(ManagedBootScript()),
		"OMNARA_API_URL="+server.URL+"/api/v1",
		"OMNARA_INSTALLER_URL="+server.URL+"/install/omnarad.sh",
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
	type failureReport struct {
		Stage         string
		ExitStatus    int
		CaptureStatus int
		OutputTail    []byte
	}
	reports := make(chan failureReport, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/daemon/failures" || r.Header.Get("Authorization") != "Bearer machine-token" ||
			r.Header.Get("Content-Type") != "text/plain" {
			http.NotFound(w, r)
			return
		}
		exitStatus, err := strconv.Atoi(r.URL.Query().Get("exit_status"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		captureStatus, err := strconv.Atoi(r.URL.Query().Get("capture_status"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		outputTail, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		reports <- failureReport{
			Stage:         r.URL.Query().Get("stage"),
			ExitStatus:    exitStatus,
			CaptureStatus: captureStatus,
			OutputTail:    outputTail,
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	wantTail := strings.Repeat("t", 4*1024)
	startupScript := "printf '%s' " + strconv.Quote("discarded"+wantTail) + "\nexit 7\n"
	startupScriptPayload := base64.StdEncoding.EncodeToString([]byte(startupScript))
	fakeBin := managedStartupTestBin(t, dir, startupScriptPayload, startupScript)

	args := ManagedDaemonLauncherArgs()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(),
		"HOME="+dir,
		managedBootstrapScriptTestEnv(ManagedBootScript()),
		"OMNARA_API_URL="+server.URL+"/api/v1",
		"OMNARA_INSTALLER_URL="+server.URL+"/install/omnarad.sh",
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
	select {
	case report := <-reports:
		if report.Stage != "startup_script" || report.ExitStatus != 7 || report.CaptureStatus != 0 ||
			string(report.OutputTail) != "d"+wantTail {
			t.Fatalf(
				"failure report status=%d capture_status=%d tail_bytes=%d",
				report.ExitStatus,
				report.CaptureStatus,
				len(report.OutputTail),
			)
		}
	default:
		t.Fatal("startup failure report was not sent")
	}
}

func TestManagedMachineStartupBlocksDaemonUntilStartupScriptFinishes(t *testing.T) {
	dir := t.TempDir()
	server, _ := managedLauncherTestServer(t, "#!/bin/sh\necho daemon-started\n")
	startupScript := "echo startup-running\nread release\necho startup-done\n"
	startupScriptPayload := base64.StdEncoding.EncodeToString([]byte(startupScript))
	fakeBin := managedStartupTestBin(t, dir, startupScriptPayload, startupScript)

	args := ManagedDaemonLauncherArgs()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(),
		"HOME="+dir,
		managedBootstrapScriptTestEnv(ManagedBootScript()),
		"OMNARA_API_URL="+server.URL+"/api/v1",
		"OMNARA_INSTALLER_URL="+server.URL+"/install/omnarad.sh",
		"OMNARA_MACHINE_TOKEN=machine-token",
		startupScriptEnvVar+"="+startupScriptPayload,
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"NO_PROXY=127.0.0.1",
		"no_proxy=127.0.0.1",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
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
		_ = stdin.Close()
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
	stderrLines := make(chan string, 16)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			stderrLines <- scanner.Text()
		}
		close(stderrLines)
	}()
	waitForStderrLine := func(want string) {
		t.Helper()
		for {
			select {
			case line, ok := <-stderrLines:
				if !ok {
					t.Fatalf("launcher stderr closed before %q", want)
				}
				if line == want {
					return
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("launcher stderr did not print %q", want)
			}
		}
	}
	waitForStderrLine("startup-running")
	select {
	case line := <-outputLine:
		t.Fatalf("launcher output line = %q, want daemon blocked while startup runs", line)
	default:
	}
	if _, err := io.WriteString(stdin, "release\n"); err != nil {
		t.Fatalf("release startup script: %v", err)
	}
	waitForStderrLine("startup-done")
	select {
	case line := <-outputLine:
		if line != "daemon-started" {
			t.Fatalf("launcher output line = %q, want daemon-started", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("daemon did not start after startup script finished")
	}
}

func TestManagedMachineStartupDoesNotWaitForBackgroundOutput(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		dir := t.TempDir()
		backgroundPIDPath := filepath.Join(dir, "background.pid")
		t.Cleanup(func() { killProcessFromPIDFile(backgroundPIDPath) })
		daemonMarkerPath := filepath.Join(dir, "daemon-started")
		server, requests := managedLauncherTestServer(
			t,
			fmt.Sprintf("#!/bin/sh\necho started > %q\n", daemonMarkerPath),
		)
		startupScript := fmt.Sprintf("sleep 30 &\necho $! > %q\nexit 0\n", backgroundPIDPath)

		out, duration, err := runManagedStartupWithFileOutput(t, dir, server.URL, startupScript)
		if err != nil {
			t.Fatalf("launcher failed: %v\n%s", err, out)
		}
		if duration >= 3*time.Second {
			t.Fatalf("launcher duration = %s, want background output not to block daemon", duration)
		}
		if got := strings.TrimSpace(string(mustReadFile(t, daemonMarkerPath))); got != "started" {
			t.Fatalf("daemon marker = %q, want started", got)
		}
		if got := requests.Load(); got != 1 {
			t.Fatalf("launcher requests = %d, want installer only", got)
		}
	})

	t.Run("failure", func(t *testing.T) {
		dir := t.TempDir()
		backgroundPIDPath := filepath.Join(dir, "background.pid")
		t.Cleanup(func() { killProcessFromPIDFile(backgroundPIDPath) })
		type failureReport struct {
			Stage         string
			ExitStatus    int
			CaptureStatus int
			OutputTail    []byte
		}
		reports := make(chan failureReport, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/daemon/failures" || r.Header.Get("Authorization") != "Bearer machine-token" ||
				r.Header.Get("Content-Type") != "text/plain" {
				http.NotFound(w, r)
				return
			}
			exitStatus, err := strconv.Atoi(r.URL.Query().Get("exit_status"))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			captureStatus, err := strconv.Atoi(r.URL.Query().Get("capture_status"))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			outputTail, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			reports <- failureReport{
				Stage:         r.URL.Query().Get("stage"),
				ExitStatus:    exitStatus,
				CaptureStatus: captureStatus,
				OutputTail:    outputTail,
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		t.Cleanup(server.Close)
		startupScript := fmt.Sprintf(
			"echo background-failure\nsleep 30 &\necho $! > %q\nexit 7\n",
			backgroundPIDPath,
		)

		out, duration, err := runManagedStartupWithFileOutput(t, dir, server.URL, startupScript)
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
			t.Fatalf("launcher error = %v output = %q, want exit status 7", err, out)
		}
		if duration >= 3*time.Second {
			t.Fatalf("launcher duration = %s, want background output not to block failure report", duration)
		}
		select {
		case report := <-reports:
			if report.Stage != "startup_script" || report.ExitStatus != 7 ||
				report.CaptureStatus == 0 || len(report.OutputTail) != 0 {
				t.Fatalf(
					"failure report status=%d capture_status=%d tail_bytes=%d",
					report.ExitStatus,
					report.CaptureStatus,
					len(report.OutputTail),
				)
			}
		default:
			t.Fatal("startup failure report was not sent")
		}
	})
}

func TestManagedBootstrapPreludeIncludesStartupOrchestration(t *testing.T) {
	script := managedBootstrapPreludeScript()
	if !strings.Contains(script, startupScriptEnvVar) ||
		!strings.Contains(script, `/bin/sh -c "$s"`) {
		t.Fatal("startup script does not include startup orchestration")
	}
}

func TestManagedBootScriptWithoutStartupScript(t *testing.T) {
	script := ManagedBootScript()
	if !strings.Contains(script, "r daemon_install") || !strings.Contains(script, "r startup_script") {
		t.Fatal("managed boot script without startup payload must retain failure reporting")
	}
}

func runManagedStartupWithFileOutput(
	t *testing.T,
	dir string,
	serverURL string,
	startupScript string,
) ([]byte, time.Duration, error) {
	t.Helper()
	startupScriptPayload := base64.StdEncoding.EncodeToString([]byte(startupScript))
	fakeBin := managedStartupTestBin(t, dir, startupScriptPayload, startupScript)
	outputPath := filepath.Join(dir, "launcher-output")
	outputFile, err := os.Create(outputPath)
	if err != nil {
		t.Fatalf("create launcher output: %v", err)
	}
	args := ManagedDaemonLauncherArgs()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(),
		"HOME="+dir,
		managedBootstrapScriptTestEnv(ManagedBootScript()),
		"OMNARA_API_URL="+serverURL+"/api/v1",
		"OMNARA_INSTALLER_URL="+serverURL+"/install/omnarad.sh",
		"OMNARA_MACHINE_TOKEN=machine-token",
		startupScriptEnvVar+"="+startupScriptPayload,
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"NO_PROXY=127.0.0.1",
		"no_proxy=127.0.0.1",
	)
	cmd.Stdout = outputFile
	cmd.Stderr = outputFile
	startedAt := time.Now()
	runErr := cmd.Run()
	duration := time.Since(startedAt)
	if err := outputFile.Close(); err != nil {
		t.Fatalf("close launcher output: %v", err)
	}
	return mustReadFile(t, outputPath), duration, runErr
}

func killProcessFromPIDFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return
	}
	process, err := os.FindProcess(pid)
	if err == nil {
		_ = process.Kill()
	}
}

func managedStartupTestBin(t *testing.T, dir, expectedPayload, startupScript string) string {
	t.Helper()
	realBase64, err := exec.LookPath("base64")
	if err != nil {
		t.Fatalf("find base64: %v", err)
	}
	bootstrapPayload := ManagedBootScriptPayload()
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
if [ "${1:-}" != "-d" ]; then
  exec %s "$@"
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
`, strconv.Quote(realBase64), strconv.Quote(bootstrapPayload), strconv.Quote(realBase64), strconv.Quote(expectedPayload), delimiter, startupScript, delimiter))
	return fakeBin
}
