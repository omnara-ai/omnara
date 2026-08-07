package providers

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestManagedDaemonLauncherPreservesImageEnvironment(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is required")
	}
	home := t.TempDir()
	server, requests := managedLauncherTestServer(t, `#!/bin/sh
printf '%s\n' "$@" > "$HOME/daemon-args"
env | sort > "$HOME/daemon-env"
umask > "$HOME/daemon-umask"
`)

	args := ManagedDaemonLauncherArgs()
	args[2] = `umask${IFS}027;` + args[2]
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = []string{
		"HOME=" + home,
		"PATH=" + os.Getenv("PATH"),
		managedBootstrapScriptTestEnv(ManagedBootScript()),
		"OMNARA_API_URL=" + server.URL,
		"OMNARA_MACHINE_TOKEN=machine-secret",
		"OMNARA_NO_UPDATE=1",
		"OMNARA_RUNNER_PATH=/runner/bin",
		"OMNARA_DAEMON_SEED_PATH=/image/omnarad",
		"OMNARA_DAEMON_RELEASE_URL=https://releases.example/omnarad/latest",
		"USER_SECRET=user-secret",
		"NO_PROXY=127.0.0.1",
		"no_proxy=127.0.0.1",
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("launcher failed: %v\n%s", err, out)
	}

	if got := requests.Load(); got != 1 {
		t.Fatalf("installer requests = %d, want 1", got)
	}
	if got := strings.TrimSpace(string(mustReadFile(t, filepath.Join(home, "installer-args")))); got != "--install-only" {
		t.Fatalf("installer args = %q, want --install-only", got)
	}
	installerEnv := string(mustReadFile(t, filepath.Join(home, "installer-env")))
	for _, value := range []string{
		"OMNARA_API_URL=" + server.URL,
		"OMNARA_MACHINE_TOKEN=machine-secret",
		"OMNARA_NO_UPDATE=1",
		"OMNARA_RUNNER_PATH=/runner/bin",
		"OMNARA_DAEMON_SEED_PATH=/image/omnarad",
		"OMNARA_DAEMON_RELEASE_URL=https://releases.example/omnarad/latest",
		"USER_SECRET=user-secret",
	} {
		if !strings.Contains(installerEnv, value+"\n") {
			t.Fatalf("installer environment does not contain %q:\n%s", value, installerEnv)
		}
	}
	for _, key := range []string{
		ManagedBootstrapScriptEnvVar + "=",
	} {
		if strings.Contains(installerEnv, key) {
			t.Fatalf("installer environment contains %q:\n%s", key, installerEnv)
		}
	}

	daemonArgs := mustReadFile(t, filepath.Join(home, "daemon-args"))
	if got := strings.Fields(string(daemonArgs)); len(got) != 2 || got[0] != "start" || got[1] != "--no-service" {
		t.Fatalf("daemon args = %#v, want start --no-service", got)
	}
	daemonEnv := string(mustReadFile(t, filepath.Join(home, "daemon-env")))
	for _, value := range []string{
		"OMNARA_API_URL=" + server.URL,
		"OMNARA_MACHINE_TOKEN=machine-secret",
		"OMNARA_NO_UPDATE=1",
		"OMNARA_RUNNER_PATH=/runner/bin",
		"OMNARA_DAEMON_SEED_PATH=/image/omnarad",
		"OMNARA_DAEMON_RELEASE_URL=https://releases.example/omnarad/latest",
		"USER_SECRET=user-secret",
	} {
		if !strings.Contains(daemonEnv, value+"\n") {
			t.Fatalf("daemon environment does not contain %q:\n%s", value, daemonEnv)
		}
	}
	for _, key := range []string{
		ManagedBootstrapScriptEnvVar + "=",
	} {
		if strings.Contains(daemonEnv, key) {
			t.Fatalf("daemon environment contains %q:\n%s", key, daemonEnv)
		}
	}
	wantPath := filepath.Join(home, ".omnarad", "bin") + string(os.PathListSeparator) + os.Getenv("PATH")
	if !strings.Contains(daemonEnv, "PATH="+wantPath+"\n") {
		t.Fatalf("daemon environment does not contain managed PATH %q:\n%s", wantPath, daemonEnv)
	}
	if got := strings.TrimSpace(string(mustReadFile(t, filepath.Join(home, "daemon-umask")))); got != "0027" {
		t.Fatalf("daemon umask = %q, want 0027", got)
	}
}

func TestManagedDaemonLauncherReportsInstallFailures(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is required")
	}
	tests := []struct {
		name       string
		statusCode int
		installer  string
		wantStatus int
	}{
		{name: "download", statusCode: http.StatusServiceUnavailable, wantStatus: 22},
		{
			name:       "installer",
			statusCode: http.StatusOK,
			installer:  "#!/bin/sh\necho install-failed >&2\nexit 13\n",
			wantStatus: 13,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			type failureReport struct {
				Stage         string
				ExitStatus    int
				CaptureStatus int
				OutputTail    string
			}
			var installerRequests atomic.Int32
			reports := make(chan failureReport, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/install/omnarad.sh":
					installerRequests.Add(1)
					w.WriteHeader(tc.statusCode)
					_, _ = w.Write([]byte(tc.installer))
				case "/api/v1/daemon/failures":
					if r.Header.Get("Authorization") != "Bearer machine-token" {
						http.Error(w, "unauthorized", http.StatusUnauthorized)
						return
					}
					exitStatus, _ := strconv.Atoi(r.URL.Query().Get("exit_status"))
					captureStatus, _ := strconv.Atoi(r.URL.Query().Get("capture_status"))
					output, _ := io.ReadAll(r.Body)
					reports <- failureReport{
						Stage:         r.URL.Query().Get("stage"),
						ExitStatus:    exitStatus,
						CaptureStatus: captureStatus,
						OutputTail:    string(output),
					}
					w.WriteHeader(http.StatusNoContent)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)

			args := ManagedDaemonLauncherArgs()
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Env = []string{
				"HOME=" + t.TempDir(),
				"PATH=" + os.Getenv("PATH"),
				managedBootstrapScriptTestEnv(ManagedBootScript()),
				"OMNARA_API_URL=" + server.URL,
				"OMNARA_MACHINE_TOKEN=machine-token",
				"NO_PROXY=127.0.0.1",
				"no_proxy=127.0.0.1",
			}
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("launcher succeeded after install failure:\n%s", out)
			}
			if got := installerRequests.Load(); got != 1 {
				t.Fatalf("installer requests = %d, want 1", got)
			}
			select {
			case report := <-reports:
				if report.Stage != "daemon_install" || report.ExitStatus != tc.wantStatus ||
					report.CaptureStatus != 0 || report.OutputTail == "" {
					t.Fatalf("install failure report = %+v", report)
				}
			default:
				t.Fatal("install failure report was not sent")
			}
		})
	}
}

func TestManagedDaemonLauncherRejectsInsecureInstallerRedirect(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is required")
	}
	var redirectedRequests atomic.Int32
	redirected := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedRequests.Add(1)
	}))
	t.Cleanup(redirected.Close)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirected.URL, http.StatusFound)
	}))
	t.Cleanup(server.Close)

	args := ManagedDaemonLauncherArgs()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = []string{
		"HOME=" + t.TempDir(),
		"PATH=" + os.Getenv("PATH"),
		managedBootstrapScriptTestEnv(ManagedBootScript()),
		"OMNARA_API_URL=" + server.URL,
		"NO_PROXY=127.0.0.1",
		"no_proxy=127.0.0.1",
	}
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("launcher followed insecure installer redirect:\n%s", out)
	}
	if got := redirectedRequests.Load(); got != 0 {
		t.Fatalf("redirected installer requests = %d, want 0", got)
	}
}

func TestManagedDaemonLauncherShape(t *testing.T) {
	args := ManagedDaemonLauncherArgs()
	wantCommand := `m=$(umask);umask${IFS}077;b=/tmp/omnarad-bootstrap;printf${IFS}%s${IFS}${OMNARA_BOOTSTRAP_SCRIPT:?}` +
		`|base64${IFS}-d>$b&&unset${IFS}OMNARA_BOOTSTRAP_SCRIPT&&umask${IFS}"$m"&&exec${IFS}/bin/sh${IFS}$b`
	if len(args) != 3 || args[0] != "/bin/sh" || args[1] != "-c" ||
		args[2] != wantCommand {
		t.Fatalf("launcher args = %#v", args)
	}
	if strings.ContainsAny(args[2], " \t\r\n") {
		t.Fatalf("launcher command contains literal whitespace: %q", args[2])
	}
	if len(strings.Join(args, " ")) >= 1024 {
		t.Fatalf("launcher args exceed conservative Unikraft command limit: %#v", args)
	}
	if size := len(ManagedBootScriptPayload()); size > 1936 {
		t.Fatalf("bootstrap payload is %d bytes, exceeds pre-failure-reporting size 1936", size)
	}
	script := managedDaemonLauncherScript()
	for _, value := range []string{
		"/install/omnarad.sh",
		"--install-only",
		"b=/usr/local/bin/omnarad",
		`OMNARA_DAEMON_SEED_PATH="${OMNARA_DAEMON_SEED_PATH:-$b}"`,
		"n=.omnarad",
		`h=${OMNARA_HOME:-"${h%/}/$n"}`,
		`export PATH="$h/bin${PATH:+:$PATH}"`,
		`exec "$h/bin/omnarad" start --no-service`,
	} {
		if !strings.Contains(script, value) {
			t.Fatalf("launcher script missing %q", value)
		}
	}
	for _, value := range []string{
		"OMNARA_DAEMON_PATH",
		"run_sanitized",
		"env -i",
		"retry_delay",
	} {
		if strings.Contains(script, value) {
			t.Fatalf("launcher script unexpectedly contains %q", value)
		}
	}
}

func TestManagedDaemonLauncherRejectsMalformedBootstrapPayload(t *testing.T) {
	args := ManagedDaemonLauncherArgs()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		ManagedBootstrapScriptEnvVar + "=" + base64.StdEncoding.EncodeToString([]byte("exit 0\n")) + "!",
	}
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("launcher executed malformed bootstrap payload:\n%s", out)
	}
}

func TestManagedDaemonLauncherAllowsExplicitHomeWithoutHomeEnvironment(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is required")
	}
	omnaraHome := t.TempDir()
	server, _ := managedLauncherTestServer(t, `#!/bin/sh
printf 'started\n' > "$OMNARA_HOME/daemon-started"
`)

	args := ManagedDaemonLauncherArgs()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		managedBootstrapScriptTestEnv(ManagedBootScript()),
		"OMNARA_API_URL=" + server.URL,
		"OMNARA_HOME=" + omnaraHome,
		"NO_PROXY=127.0.0.1",
		"no_proxy=127.0.0.1",
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("launcher failed: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(mustReadFile(t, filepath.Join(omnaraHome, "daemon-started")))); got != "started" {
		t.Fatalf("daemon marker = %q, want started", got)
	}
}

func managedBootstrapScriptTestEnv(script string) string {
	return ManagedBootstrapScriptEnvVar + "=" + base64.StdEncoding.EncodeToString([]byte(script))
}

func managedLauncherTestServer(t *testing.T, daemonScript string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	delimiter := "__OMNARAD_TEST_DAEMON__"
	for strings.Contains(daemonScript, delimiter) {
		delimiter += "_"
	}
	var requests atomic.Int32
	installer := fmt.Sprintf(`#!/bin/sh
set -eu
test_dir=${HOME:-$OMNARA_HOME}
printf '%%s\n' "$@" > "$test_dir/installer-args"
env | sort > "$test_dir/installer-env"
daemon_home=${OMNARA_HOME:-"$HOME/.omnarad"}
mkdir -p "$daemon_home/bin"
cat > "$daemon_home/bin/omnarad" <<'%s'
%s%s
chmod 0700 "$daemon_home/bin/omnarad"
`, delimiter, daemonScript, delimiter)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/install/omnarad.sh" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(installer))
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
