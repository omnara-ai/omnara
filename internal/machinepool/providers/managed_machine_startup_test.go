package providers

import (
	"bufio"
	"bytes"
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

func TestBuildManagedBootEnvironment(t *testing.T) {
	startupScript := "echo ready\n"
	bootEnvironment, err := BuildManagedBootEnvironment(
		"  https://app.omnara.test///  ",
		"machine-token",
		startupScript,
		map[string]string{"APP_ENV": "production", "GITHUB_TOKEN": "resolved-secret"},
	)
	if err != nil {
		t.Fatalf("build managed boot environment: %v", err)
	}
	env := bootEnvironment.CombinedEnv()
	if len(env) != 5 || env["OMNARA_API_URL"] != "https://app.omnara.test" ||
		env["OMNARA_MACHINE_TOKEN"] != "machine-token" ||
		env[startupScriptEnvVar] != base64.StdEncoding.EncodeToString([]byte(startupScript)) ||
		env["APP_ENV"] != "production" ||
		env["GITHUB_TOKEN"] != "resolved-secret" {
		t.Fatalf("managed machine env = %+v", env)
	}
	if len(bootEnvironment.StartupEnv) != 2 || bootEnvironment.StartupEnv["APP_ENV"] != "production" ||
		bootEnvironment.StartupEnv["GITHUB_TOKEN"] != "resolved-secret" ||
		bootEnvironment.DaemonEnv["APP_ENV"] != "" || bootEnvironment.DaemonEnv["GITHUB_TOKEN"] != "" {
		t.Fatalf("managed boot environment scopes = %+v", bootEnvironment)
	}
}

func TestBuildManagedBootEnvironmentWithoutMachineEnv(t *testing.T) {
	bootEnvironment, err := BuildManagedBootEnvironment("https://app.omnara.test", "machine-token", "", nil)
	if err != nil {
		t.Fatalf("build managed boot environment: %v", err)
	}
	env := bootEnvironment.CombinedEnv()
	if len(env) != 2 || env["OMNARA_API_URL"] != "https://app.omnara.test" ||
		env["OMNARA_MACHINE_TOKEN"] != "machine-token" {
		t.Fatalf("managed machine env = %+v", env)
	}
}

func TestBuildManagedBootEnvironmentRejectsReservedMachineEnv(t *testing.T) {
	_, err := BuildManagedBootEnvironment(
		"https://app.omnara.test",
		"machine-token",
		"",
		map[string]string{"omnara_api_url": "spoofed"},
	)
	if err == nil || !strings.Contains(err.Error(), "reserved OMNARA_ key") {
		t.Fatalf("build managed machine env error = %v, want reserved key rejection", err)
	}
}

func TestBuildManagedBootEnvironmentRejectsInvalidMachineEnv(t *testing.T) {
	for _, name := range []string{"1APP", "APP-NAME", "APP.NAME", "APP NAME", "APP\nNAME"} {
		t.Run(name, func(t *testing.T) {
			_, err := BuildManagedBootEnvironment(
				"https://app.omnara.test",
				"machine-token",
				"",
				map[string]string{name: "value"},
			)
			if err == nil || !strings.Contains(err.Error(), "must match") {
				t.Fatalf("build managed boot environment error = %v, want invalid name", err)
			}
		})
	}
}

func TestRenderManagedStartupEnvironmentPreservesValues(t *testing.T) {
	env := map[string]string{
		"DOLLAR":    "$HOME `touch should-not-run` $(touch also-not-run)",
		"EMPTY":     "",
		"MULTILINE": "first\nsecond\n",
		"QUOTE":     "single'and\"double",
		"lower_1":   "lowercase",
	}
	script, err := RenderManagedStartupEnvironment(env)
	if err != nil {
		t.Fatalf("render startup environment: %v", err)
	}
	wantScript := "export DOLLAR='$HOME `touch should-not-run` $(touch also-not-run)'\n" +
		"export EMPTY=''\n" +
		"export MULTILINE='first\nsecond\n'\n" +
		"export QUOTE='single'\"'\"'and\"double'\n" +
		"export lower_1='lowercase'\n"
	if script != wantScript {
		t.Fatalf("startup environment script = %q, want %q", script, wantScript)
	}
	path := filepath.Join(t.TempDir(), "startup-env.sh")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatalf("write startup environment: %v", err)
	}
	cmd := exec.Command(
		"/bin/sh",
		"-c",
		`. "$1"; printf '%s\000' "$DOLLAR" "$EMPTY" "$MULTILINE" "$QUOTE" "$lower_1"`,
		"sh",
		path,
	)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("source startup environment: %v", err)
	}
	got := bytes.Split(output, []byte{0})
	want := []string{env["DOLLAR"], env["EMPTY"], env["MULTILINE"], env["QUOTE"], env["lower_1"]}
	if len(got) != len(want)+1 {
		t.Fatalf("sourced values = %q", output)
	}
	for i := range want {
		if string(got[i]) != want[i] {
			t.Fatalf("sourced value %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRenderManagedStartupEnvironmentRejectsUnsafeInput(t *testing.T) {
	for _, test := range []struct {
		name string
		env  map[string]string
	}{
		{name: "invalid name", env: map[string]string{"BAD-NAME": "value"}},
		{name: "reserved name", env: map[string]string{"omnara_machine_token": "value"}},
		{name: "NUL value", env: map[string]string{"VALUE": "bad\x00value"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := RenderManagedStartupEnvironment(test.env); err == nil {
				t.Fatal("render startup environment succeeded, want error")
			}
		})
	}
}

func TestManagedBootEnvironmentBuildsCompleteDaemonKeyManifest(t *testing.T) {
	bootEnvironment, err := BuildManagedBootEnvironment(
		"https://app.omnara.test",
		"machine-token",
		"echo ready",
		map[string]string{"CUSTOMER": "value"},
	)
	if err != nil {
		t.Fatalf("build managed boot environment: %v", err)
	}
	env, err := bootEnvironment.ScopedDaemonEnv("/tmp/startup-env")
	if err != nil {
		t.Fatalf("build scoped daemon env: %v", err)
	}
	wantKeys := strings.Join([]string{
		"OMNARA_API_URL",
		"OMNARA_MACHINE_TOKEN",
		"OMNARA_STARTUP_SCRIPT_PAYLOAD",
	}, " ")
	if env[daemonEnvKeysEnvVar] != wantKeys || env[startupEnvFileEnvVar] != "/tmp/startup-env" ||
		env["CUSTOMER"] != "" {
		t.Fatalf("scoped daemon env = %+v, want manifest %q", env, wantKeys)
	}
}

func TestManagedScopedStartupSeparatesCustomerAndDaemonEnvironment(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is required")
	}
	dir := t.TempDir()
	startupEnvironmentDir := filepath.Join(dir, "bootstrap-attempt")
	if err := os.Mkdir(startupEnvironmentDir, 0o700); err != nil {
		t.Fatalf("create startup environment directory: %v", err)
	}
	startupEnvironmentPath := filepath.Join(startupEnvironmentDir, "startup-env.sh")
	valuePaths := map[string]string{
		"DOLLAR":    filepath.Join(dir, "startup-dollar"),
		"EMPTY":     filepath.Join(dir, "startup-empty"),
		"MULTILINE": filepath.Join(dir, "startup-multiline"),
		"QUOTE":     filepath.Join(dir, "startup-quote"),
		"m":         filepath.Join(dir, "startup-wrapper-name-collision"),
	}
	startupScript := "set -eu\n" +
		`if [ "${OMNARA_MACHINE_TOKEN+x}" = x ];then echo token-leaked >&2;exit 23;fi` + "\n" +
		fmt.Sprintf(
			"if [ -e %s ] || [ -e %s ];then echo startup-env-not-unlinked >&2;exit 24;fi\n",
			strconv.Quote(startupEnvironmentPath),
			strconv.Quote(startupEnvironmentDir),
		)
	for _, key := range []string{"DOLLAR", "EMPTY", "MULTILINE", "QUOTE", "m"} {
		startupScript += fmt.Sprintf("printf '%%s' \"$%s\" > %s\n", key, strconv.Quote(valuePaths[key]))
	}
	server, requests := managedLauncherTestServer(t, `#!/bin/sh
env | sort > "$HOME/daemon-scoped-env"
`)
	bootEnvironment, err := BuildManagedBootEnvironment(
		server.URL,
		"machine-token",
		startupScript,
		map[string]string{
			"DOLLAR":    "$HOME `literal` $(literal)",
			"EMPTY":     "",
			"MULTILINE": "first\nsecond\n",
			"QUOTE":     "single'and\"double",
			"m":         "not-a-valid-umask",
		},
	)
	if err != nil {
		t.Fatalf("build managed boot environment: %v", err)
	}
	startupEnvironment, err := RenderManagedStartupEnvironment(bootEnvironment.StartupEnv)
	if err != nil {
		t.Fatalf("render startup environment: %v", err)
	}
	if err := os.WriteFile(startupEnvironmentPath, []byte(startupEnvironment), 0o600); err != nil {
		t.Fatalf("write startup environment: %v", err)
	}
	daemonEnv, err := bootEnvironment.ScopedDaemonEnv(startupEnvironmentPath)
	if err != nil {
		t.Fatalf("build scoped daemon env: %v", err)
	}
	fakeBin := managedStartupTestBin(
		t,
		dir,
		daemonEnv[startupScriptEnvVar],
		startupScript,
		ManagedScopedBootScript(""),
	)
	args := ManagedDaemonLauncherArgs()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = []string{
		"HOME=" + dir,
		"PATH=" + fakeBin + ":" + os.Getenv("PATH"),
		managedBootstrapScriptTestEnv(ManagedScopedBootScript("")),
		"NO_PROXY=127.0.0.1",
		"no_proxy=127.0.0.1",
	}
	for key, value := range daemonEnv {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scoped launcher failed: %v\n%s", err, output)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("installer requests = %d, want 1", got)
	}
	for key, path := range valuePaths {
		if got := string(mustReadFile(t, path)); got != bootEnvironment.StartupEnv[key] {
			t.Fatalf("startup %s = %q, want %q", key, got, bootEnvironment.StartupEnv[key])
		}
	}
	if _, err := os.Stat(startupEnvironmentPath); !os.IsNotExist(err) {
		t.Fatalf("startup environment file stat = %v, want removed", err)
	}
	if _, err := os.Stat(startupEnvironmentDir); !os.IsNotExist(err) {
		t.Fatalf("startup environment directory stat = %v, want removed", err)
	}
	for _, path := range []string{
		filepath.Join(dir, "installer-env"),
		filepath.Join(dir, "daemon-scoped-env"),
	} {
		environment := string(mustReadFile(t, path))
		for _, key := range []string{
			"DOLLAR=", "EMPTY=", "MULTILINE=", "QUOTE=", "m=",
			startupScriptEnvVar + "=", startupEnvFileEnvVar + "=", daemonEnvKeysEnvVar + "=",
		} {
			if strings.Contains(environment, key) {
				t.Fatalf("%s contains isolated variable %q:\n%s", path, key, environment)
			}
		}
		if !strings.Contains(environment, "OMNARA_MACHINE_TOKEN=machine-token\n") {
			t.Fatalf("%s is missing daemon credential:\n%s", path, environment)
		}
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
	server, reports := managedFailureTestServer(t)
	wantTail := strings.Repeat("t", 4*1024)
	startupScript := "printf '%s' " + strconv.Quote("discarded"+wantTail) + "\nexit 7\n"
	startupScriptPayload := base64.StdEncoding.EncodeToString([]byte(startupScript))
	fakeBin := managedStartupTestBin(t, dir, startupScriptPayload, startupScript)

	args := ManagedDaemonLauncherArgs()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(),
		"HOME="+dir,
		managedBootstrapScriptTestEnv(ManagedBootScript()),
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

func TestManagedScopedStartupFailureReportsExitAndOutputTail(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is required")
	}
	dir := t.TempDir()
	server, reports := managedFailureTestServer(t)
	wantTail := strings.Repeat("s", 4*1024)
	startupScript := "printf '%s' " + strconv.Quote("discarded"+wantTail) + "\nexit 7\n"
	bootEnvironment, err := BuildManagedBootEnvironment(
		server.URL,
		"machine-token",
		startupScript,
		map[string]string{"STARTUP_VALUE": "resolved"},
	)
	if err != nil {
		t.Fatalf("build managed boot environment: %v", err)
	}
	startupEnvironmentDir := filepath.Join(dir, "bootstrap-attempt")
	if err := os.Mkdir(startupEnvironmentDir, 0o700); err != nil {
		t.Fatalf("create startup environment directory: %v", err)
	}
	startupEnvironmentPath := filepath.Join(startupEnvironmentDir, "startup-env")
	startupEnvironment, err := RenderManagedStartupEnvironment(bootEnvironment.StartupEnv)
	if err != nil {
		t.Fatalf("render startup environment: %v", err)
	}
	if err := os.WriteFile(startupEnvironmentPath, []byte(startupEnvironment), 0o600); err != nil {
		t.Fatalf("write startup environment: %v", err)
	}
	daemonEnv, err := bootEnvironment.ScopedDaemonEnv(startupEnvironmentPath)
	if err != nil {
		t.Fatalf("build scoped daemon env: %v", err)
	}
	bootstrapScript := ManagedScopedBootScript("")
	fakeBin := managedStartupTestBin(
		t,
		dir,
		daemonEnv[startupScriptEnvVar],
		startupScript,
		bootstrapScript,
	)
	args := ManagedDaemonLauncherArgs()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = []string{
		"HOME=" + dir,
		"PATH=" + fakeBin + ":" + os.Getenv("PATH"),
		managedBootstrapScriptTestEnv(bootstrapScript),
		"NO_PROXY=127.0.0.1",
		"no_proxy=127.0.0.1",
	}
	for key, value := range daemonEnv {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
		t.Fatalf("scoped launcher error = %v output = %q, want exit status 7", err, out)
	}
	if _, err := os.Stat(startupEnvironmentPath); !os.IsNotExist(err) {
		t.Fatalf("startup environment stat = %v, want removed", err)
	}
	if _, err := os.Stat(startupEnvironmentDir); !os.IsNotExist(err) {
		t.Fatalf("startup environment directory stat = %v, want removed", err)
	}
	select {
	case report := <-reports:
		if report.Stage != "startup_script" || report.ExitStatus != 7 ||
			report.CaptureStatus != 0 || string(report.OutputTail) != "d"+wantTail {
			t.Fatalf(
				"scoped startup failure status=%d capture_status=%d tail_bytes=%d",
				report.ExitStatus,
				report.CaptureStatus,
				len(report.OutputTail),
			)
		}
	default:
		t.Fatal("scoped startup failure report was not sent")
	}
}

func TestManagedProviderSetupFailurePreventsStartupAndDaemon(t *testing.T) {
	dir := t.TempDir()
	server, reports := managedFailureTestServer(t)
	startupMarkerPath := filepath.Join(dir, "startup-ran")
	startupScript := "echo ran > " + strconv.Quote(startupMarkerPath) + "\n"
	startupPayload := base64.StdEncoding.EncodeToString([]byte(startupScript))
	startupEnvironmentDir := filepath.Join(dir, "bootstrap-attempt")
	if err := os.Mkdir(startupEnvironmentDir, 0o700); err != nil {
		t.Fatalf("create startup environment directory: %v", err)
	}
	startupEnvironmentPath := filepath.Join(startupEnvironmentDir, "startup-env")
	if err := os.WriteFile(startupEnvironmentPath, []byte("export SECRET='resolved'\n"), 0o600); err != nil {
		t.Fatalf("write startup environment: %v", err)
	}
	bootstrapScript := ManagedScopedBootScript(
		`r provider_setup /bin/sh -c 'echo awake-setup-failed; exit 8'`,
	)
	fakeBin := managedStartupTestBin(t, dir, startupPayload, startupScript, bootstrapScript)

	args := ManagedDaemonLauncherArgs()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(),
		"HOME="+dir,
		managedBootstrapScriptTestEnv(bootstrapScript),
		"OMNARA_API_URL="+server.URL,
		"OMNARA_MACHINE_TOKEN=machine-token",
		startupScriptEnvVar+"="+startupPayload,
		startupEnvFileEnvVar+"="+startupEnvironmentPath,
		daemonEnvKeysEnvVar+"=OMNARA_API_URL OMNARA_MACHINE_TOKEN "+startupScriptEnvVar,
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"NO_PROXY=127.0.0.1",
		"no_proxy=127.0.0.1",
	)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 8 {
		t.Fatalf("launcher error = %v output = %q, want provider setup exit status 8", err, out)
	}
	if _, err := os.Stat(startupEnvironmentPath); !os.IsNotExist(err) {
		t.Fatalf("startup environment stat = %v, want provider failure cleanup", err)
	}
	if _, err := os.Stat(startupEnvironmentDir); !os.IsNotExist(err) {
		t.Fatalf("startup environment directory stat = %v, want provider failure cleanup", err)
	}
	if _, err := os.Stat(startupMarkerPath); !os.IsNotExist(err) {
		t.Fatalf("startup marker stat = %v, want startup blocked by provider setup", err)
	}
	select {
	case report := <-reports:
		if report.Stage != "provider_setup" || report.ExitStatus != 8 ||
			report.CaptureStatus != 0 || string(report.OutputTail) != "awake-setup-failed\n" {
			t.Fatalf("provider setup failure report = %+v", report)
		}
	default:
		t.Fatal("provider setup failure report was not sent")
	}
}

func TestManagedScopedStartupFailsClosedWithoutTemporaryDirectory(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is required")
	}
	realMktemp, err := exec.LookPath("mktemp")
	if err != nil {
		t.Fatalf("find mktemp: %v", err)
	}
	for _, test := range []struct {
		name     string
		behavior string
	}{
		{name: "mktemp fails", behavior: "exit 9"},
		{name: "mktemp returns empty path", behavior: "exit 0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			server, reports := managedFailureTestServer(t)
			startupMarkerPath := filepath.Join(dir, "startup-ran")
			startupScript := "echo ran > " + strconv.Quote(startupMarkerPath) + "\n"
			bootEnvironment, err := BuildManagedBootEnvironment(
				server.URL,
				"machine-token",
				startupScript,
				map[string]string{"STARTUP_VALUE": "resolved"},
			)
			if err != nil {
				t.Fatalf("build managed boot environment: %v", err)
			}
			startupEnvironmentDir := filepath.Join(dir, "bootstrap-attempt")
			if err := os.Mkdir(startupEnvironmentDir, 0o700); err != nil {
				t.Fatalf("create startup environment directory: %v", err)
			}
			startupEnvironmentPath := filepath.Join(startupEnvironmentDir, "startup-env")
			startupEnvironment, err := RenderManagedStartupEnvironment(bootEnvironment.StartupEnv)
			if err != nil {
				t.Fatalf("render startup environment: %v", err)
			}
			if err := os.WriteFile(startupEnvironmentPath, []byte(startupEnvironment), 0o600); err != nil {
				t.Fatalf("write startup environment: %v", err)
			}
			daemonEnv, err := bootEnvironment.ScopedDaemonEnv(startupEnvironmentPath)
			if err != nil {
				t.Fatalf("build scoped daemon env: %v", err)
			}
			bootstrapScript := ManagedScopedBootScript("")
			fakeBin := managedStartupTestBin(
				t,
				dir,
				daemonEnv[startupScriptEnvVar],
				startupScript,
				bootstrapScript,
			)
			mktempCallsPath := filepath.Join(dir, "mktemp-calls")
			writeExecutable(t, filepath.Join(fakeBin, "mktemp"), fmt.Sprintf(`#!/bin/sh
count=0
if [ -f %s ];then count=$(cat %s);fi
count=$((count+1))
printf '%%s\n' "$count" > %s
if [ "$count" -eq 1 ];then exec %s "$@";fi
if [ "$count" -eq 2 ];then %s;fi
exec %s "$@"
`,
				strconv.Quote(mktempCallsPath),
				strconv.Quote(mktempCallsPath),
				strconv.Quote(mktempCallsPath),
				strconv.Quote(realMktemp),
				test.behavior,
				strconv.Quote(realMktemp),
			))

			args := ManagedDaemonLauncherArgs()
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Env = []string{
				"HOME=" + dir,
				"PATH=" + fakeBin + ":" + os.Getenv("PATH"),
				managedBootstrapScriptTestEnv(bootstrapScript),
				"NO_PROXY=127.0.0.1",
				"no_proxy=127.0.0.1",
			}
			for key, value := range daemonEnv {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
			out, err := cmd.CombinedOutput()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 70 {
				t.Fatalf("launcher error = %v output = %q, want status 70", err, out)
			}
			if got := strings.TrimSpace(string(mustReadFile(t, mktempCallsPath))); got != "2" {
				t.Fatalf("mktemp calls = %q, want inner call 2 to be faulted", got)
			}
			if _, err := os.Stat(startupMarkerPath); !os.IsNotExist(err) {
				t.Fatalf("startup marker stat = %v, want startup blocked", err)
			}
			if _, err := os.Stat(startupEnvironmentPath); !os.IsNotExist(err) {
				t.Fatalf("startup environment stat = %v, want removed", err)
			}
			if _, err := os.Stat(startupEnvironmentDir); !os.IsNotExist(err) {
				t.Fatalf("startup environment directory stat = %v, want removed", err)
			}
			select {
			case report := <-reports:
				if report.Stage != "startup_script" || report.ExitStatus != 70 ||
					report.CaptureStatus != 0 || len(report.OutputTail) != 0 {
					t.Fatalf("temporary directory failure report = %+v", report)
				}
			default:
				t.Fatal("temporary directory failure report was not sent")
			}
		})
	}
}

func TestManagedScopedStartupRejectsPartialDecode(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is required")
	}
	dir := t.TempDir()
	startupMarkerPath := filepath.Join(dir, "partial-startup-ran")
	startupScript := "echo ran > " + strconv.Quote(startupMarkerPath) + "\n"
	server, reports := managedFailureTestServer(t)
	bootEnvironment, err := BuildManagedBootEnvironment(
		server.URL,
		"machine-token",
		startupScript,
		nil,
	)
	if err != nil {
		t.Fatalf("build managed boot environment: %v", err)
	}
	startupEnvironmentDir := filepath.Join(dir, "bootstrap-attempt")
	if err := os.Mkdir(startupEnvironmentDir, 0o700); err != nil {
		t.Fatalf("create startup environment directory: %v", err)
	}
	startupEnvironmentPath := filepath.Join(startupEnvironmentDir, "startup-env")
	if err := os.WriteFile(startupEnvironmentPath, nil, 0o600); err != nil {
		t.Fatalf("write startup environment: %v", err)
	}
	daemonEnv, err := bootEnvironment.ScopedDaemonEnv(startupEnvironmentPath)
	if err != nil {
		t.Fatalf("build scoped daemon env: %v", err)
	}
	bootstrapScript := ManagedScopedBootScript("")
	fakeBin := managedStartupTestBin(t, dir, daemonEnv[startupScriptEnvVar], startupScript, bootstrapScript)
	realBase64, err := exec.LookPath("base64")
	if err != nil {
		t.Fatalf("find base64: %v", err)
	}
	bootstrapPayload := base64.StdEncoding.EncodeToString([]byte(bootstrapScript))
	writeExecutable(t, filepath.Join(fakeBin, "base64"), fmt.Sprintf(`#!/bin/sh
payload=$(cat)
if [ "$payload" = %s ];then
  printf '%%s' "$payload"|%s -d
  exit $?
fi
if [ "$payload" = %s ];then
  printf 'echo partial startup'
  exit 9
fi
exit 2
`, strconv.Quote(bootstrapPayload), strconv.Quote(realBase64), strconv.Quote(daemonEnv[startupScriptEnvVar])))

	args := ManagedDaemonLauncherArgs()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = []string{
		"HOME=" + dir,
		"PATH=" + fakeBin + ":" + os.Getenv("PATH"),
		managedBootstrapScriptTestEnv(bootstrapScript),
		"NO_PROXY=127.0.0.1",
		"no_proxy=127.0.0.1",
	}
	for key, value := range daemonEnv {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 70 {
		t.Fatalf("launcher error = %v output = %q, want decode failure status 70", err, out)
	}
	if _, err := os.Stat(startupMarkerPath); !os.IsNotExist(err) {
		t.Fatalf("partial startup marker stat = %v, want script not executed", err)
	}
	if _, err := os.Stat(startupEnvironmentDir); !os.IsNotExist(err) {
		t.Fatalf("startup environment directory stat = %v, want removed", err)
	}
	select {
	case report := <-reports:
		if report.Stage != "startup_script" || report.ExitStatus != 70 || report.CaptureStatus != 0 {
			t.Fatalf("partial decode failure report = %+v", report)
		}
	default:
		t.Fatal("partial decode failure report was not sent")
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
		managedBootstrapScriptTestEnv(ManagedBootScript()),
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
		server, reports := managedFailureTestServer(t)
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

func TestManagedStartupScriptIncludesUnscopedOrchestration(t *testing.T) {
	script := managedStartupScript()
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

type managedFailureReport struct {
	Stage         string
	ExitStatus    int
	CaptureStatus int
	OutputTail    []byte
}

func managedFailureTestServer(
	t *testing.T,
) (*httptest.Server, <-chan managedFailureReport) {
	t.Helper()
	reports := make(chan managedFailureReport, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/daemon/failures" ||
			r.Header.Get("Authorization") != "Bearer machine-token" ||
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
		reports <- managedFailureReport{
			Stage:         r.URL.Query().Get("stage"),
			ExitStatus:    exitStatus,
			CaptureStatus: captureStatus,
			OutputTail:    outputTail,
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	return server, reports
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
		"OMNARA_API_URL="+serverURL,
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

func managedStartupTestBin(
	t *testing.T,
	dir, expectedPayload, startupScript string,
	bootstrapScripts ...string,
) string {
	t.Helper()
	realBase64, err := exec.LookPath("base64")
	if err != nil {
		t.Fatalf("find base64: %v", err)
	}
	bootstrapScript := ManagedBootScript()
	if len(bootstrapScripts) > 0 {
		bootstrapScript = bootstrapScripts[0]
	}
	bootstrapPayload := base64.StdEncoding.EncodeToString([]byte(bootstrapScript))
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
