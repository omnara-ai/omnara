package httpapi

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOmnaradInstallRoute(t *testing.T) {
	releaseURL := "https://releases.omnara.test/omnarad/latest"
	apiURL := "https://app.omnara.test"
	server := mustNewUnitServer(t, WithDaemonReleaseURL(releaseURL), WithPublicURL(apiURL))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, apiURL+omnaradInstallPath, nil),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("installer status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("installer Cache-Control = %q, want no-store", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/x-shellscript; charset=utf-8" {
		t.Fatalf("installer Content-Type = %q", got)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("installer X-Content-Type-Options = %q, want nosniff", got)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "default_release_url='"+releaseURL+"'") {
		t.Fatal("installer does not contain the configured release URL")
	}
	if !strings.Contains(body, "default_api_url='"+apiURL+"'") {
		t.Fatal("installer does not contain the configured API URL")
	}
	if !strings.Contains(body, `install --release-manifest-url "$release_manifest_url"`) {
		t.Fatal("installer does not delegate installation to omnarad")
	}
	for _, removed := range []string{"install.json", "install.lock", "OMNARA_INSTALL_REPAIR", "canonical_binary"} {
		if strings.Contains(body, removed) {
			t.Fatalf("installer still contains Go-owned lifecycle state %q", removed)
		}
	}
	assertShellSyntax(t, recorder.Body.Bytes())
}

func TestOmnaradInstallRouteRequiresReleaseURL(t *testing.T) {
	server := mustNewUnitServer(t)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, omnaradInstallPath, nil),
	)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("installer status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
}

func TestOmnaradInstallRouteUsesRequestOrigin(t *testing.T) {
	server := mustNewUnitServer(t, WithDaemonReleaseURL("https://releases.omnara.test/omnarad/latest"))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "http://localhost:5173"+omnaradInstallPath, nil),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("installer status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if !strings.Contains(recorder.Body.String(), "default_api_url='http://localhost:5173'") {
		t.Fatal("installer does not contain the request origin")
	}
}

func TestOmnaradInstallerDelegatesWithoutTouchingHome(t *testing.T) {
	requireInstallerShell(t)
	script, err := renderOmnaradInstallScript(
		"https://releases.omnara.test/omnarad/",
		"https://app.omnara.test",
	)
	if err != nil {
		t.Fatalf("render installer: %v", err)
	}
	assertShellSyntax(t, script)
	home := filepath.Join(t.TempDir(), "home")
	marker := filepath.Join(t.TempDir(), "install-args")
	seed := filepath.Join(t.TempDir(), "seed-omnarad")
	writeDelegatingSeed(t, seed, marker)
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Fatalf("find sh: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-s", "--", "--install-only")
	cmd.Stdin = strings.NewReader("umask 027\n" + string(script))
	cmd.Env = []string{
		"HOME=" + filepath.Dir(home),
		"SHELL=/bin/zsh",
		"OMNARA_HOME=" + home,
		"OMNARA_DAEMON_SEED_PATH=" + seed,
		"OMNARA_MACHINE_TOKEN=secret-token",
		"PATH=" + os.Getenv("PATH"),
		"TMPDIR=" + t.TempDir(),
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			t.Fatalf("streamed installer timed out: %v", ctx.Err())
		}
		t.Fatalf("run streamed installer: %v\n%s", err, output)
	}
	if strings.Contains(string(output), "malloc:") {
		t.Fatalf("streamed installer reported allocator failure:\n%s", output)
	}
	assertInstallInvocation(t, marker, true)
	if got := strings.TrimSpace(readTestFile(t, marker+".umask")); got != "0027" {
		t.Fatalf("installer delegated with umask %q, want 0027", got)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("shell modified OMNARA_HOME before Go install: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(home), ".zshrc")); !os.IsNotExist(err) {
		t.Fatalf("install-only modified shell config: %v", err)
	}

	marker = filepath.Join(t.TempDir(), "normal-install-args")
	writeDelegatingSeed(t, seed, marker)
	_, stderr, err := runInstaller(t, writeInstaller(t, "https://releases.omnara.test/omnarad"), home, nil,
		"OMNARA_DAEMON_SEED_PATH="+seed,
		"OMNARA_MACHINE_TOKEN=secret-token",
		"OMNARA_TEST_CREATE_CANONICAL=1",
	)
	if err != nil {
		t.Fatalf("run normal installer: %v\nstderr: %s", err, stderr)
	}
	assertInstallInvocation(t, marker, true)
	if got := strings.TrimSpace(readTestFile(t, marker+".restart")); got != "restart" {
		t.Fatalf("restart invocation = %q", got)
	}
}

func TestOmnaradInstallerConfiguresPath(t *testing.T) {
	releaseURL := "https://releases.omnara.test/omnarad"
	bashPathLine := func(daemonHome string) string {
		return `export PATH="` + filepath.Join(daemonHome, "bin") + `:$PATH"`
	}
	tests := []struct {
		name       string
		shell      string
		platformOS string
		configPath func(string) string
		prepare    func(*testing.T, string)
		wantLine   func(string) string
	}{
		{
			name:  "zsh",
			shell: "/bin/zsh",
			configPath: func(home string) string {
				return filepath.Join(home, ".zshrc")
			},
			wantLine: func(daemonHome string) string {
				return `path=("` + filepath.Join(daemonHome, "bin") + `" $path)`
			},
		},
		{
			name:       "bash_linux",
			shell:      "/bin/bash",
			platformOS: "Linux",
			configPath: func(home string) string {
				return filepath.Join(home, ".bashrc")
			},
			wantLine: bashPathLine,
		},
		{
			name:       "bash_darwin",
			shell:      "/bin/bash",
			platformOS: "Darwin",
			configPath: func(home string) string {
				return filepath.Join(home, ".bash_profile")
			},
			wantLine: bashPathLine,
		},
		{
			name:       "bash_darwin_existing_profile",
			shell:      "/bin/bash",
			platformOS: "Darwin",
			configPath: func(home string) string {
				return filepath.Join(home, ".profile")
			},
			prepare: func(t *testing.T, home string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(home, ".profile"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantLine: bashPathLine,
		},
		{
			name:  "fish",
			shell: "/usr/local/bin/fish",
			configPath: func(home string) string {
				return filepath.Join(home, ".config", "fish", "config.fish")
			},
			wantLine: func(daemonHome string) string {
				return `fish_add_path "` + filepath.Join(daemonHome, "bin") + `"`
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			userHome := t.TempDir()
			if testCase.prepare != nil {
				testCase.prepare(t, userHome)
			}
			daemonHome := filepath.Join(userHome, ".omnarad")
			marker := filepath.Join(t.TempDir(), "args")
			seed := filepath.Join(t.TempDir(), "omnarad")
			writeDelegatingSeed(t, seed, marker)
			script := writeInstaller(t, releaseURL)
			extraEnv := []string{
				"OMNARA_DAEMON_SEED_PATH=" + seed,
				"OMNARA_MACHINE_TOKEN=secret-token",
				"OMNARA_TEST_CREATE_CANONICAL=1",
				"SHELL=" + testCase.shell,
			}
			if testCase.platformOS != "" {
				extraEnv = append(extraEnv, "PATH="+fakePlatformPath(t, testCase.platformOS))
			}
			daemonHomes := []string{daemonHome, daemonHome, filepath.Join(userHome, ".omnarad-relocated")}
			for attempt, daemonHome := range daemonHomes {
				stdout, stderr, err := runInstaller(t, script, daemonHome, nil, extraEnv...)
				if err != nil {
					t.Fatalf("run installer attempt %d: %v\nstdout: %s\nstderr: %s", attempt+1, err, stdout, stderr)
				}
			}
			body := readTestFile(t, testCase.configPath(userHome))
			if strings.Count(body, "# managed by omnarad") != 2 ||
				!strings.Contains(body, testCase.wantLine(daemonHomes[0])) ||
				!strings.Contains(body, testCase.wantLine(daemonHomes[2])) {
				t.Fatalf("shell config = %q", body)
			}
		})
	}
}

func fakePlatformPath(t *testing.T, platformOS string) string {
	t.Helper()
	platformArch := "amd64"
	if platformOS == "Darwin" {
		platformArch = "arm64"
	}
	dir := t.TempDir()
	writeExecutable(t, filepath.Join(dir, "uname"), `#!/bin/sh
case "${1:-}" in
  -s) printf '%s\n' `+shellQuote(platformOS)+` ;;
  -m) printf '%s\n' `+shellQuote(platformArch)+` ;;
  *) exit 1 ;;
esac
`)
	return dir + string(os.PathListSeparator) + os.Getenv("PATH")
}

func TestOmnaradInstallerUsesLocalBinAndManualPathFallback(t *testing.T) {
	userHome := t.TempDir()
	daemonHome := filepath.Join(userHome, ".omnarad")
	localBin := filepath.Join(userHome, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "args")
	seed := filepath.Join(t.TempDir(), "omnarad")
	writeDelegatingSeed(t, seed, marker)
	stdout, stderr, err := runInstaller(t, writeInstaller(t, "https://releases.omnara.test/omnarad"), daemonHome, nil,
		"OMNARA_DAEMON_SEED_PATH="+seed,
		"OMNARA_MACHINE_TOKEN=secret-token",
		"OMNARA_TEST_CREATE_CANONICAL=1",
		"SHELL=/bin/zsh",
		"PATH="+localBin+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	if err != nil {
		t.Fatalf("run local-bin installer: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	target, err := os.Readlink(filepath.Join(localBin, "omnarad"))
	if err != nil {
		t.Fatalf("read omnarad symlink: %v", err)
	}
	if target != filepath.Join(daemonHome, "bin", "omnarad") {
		t.Fatalf("omnarad symlink target = %q", target)
	}
	if _, err := os.Stat(filepath.Join(userHome, ".zshrc")); !os.IsNotExist(err) {
		t.Fatalf("fast path modified zsh config: %v", err)
	}

	manualHome := t.TempDir()
	manualDaemonHome := filepath.Join(manualHome, ".omnarad")
	manualMarker := filepath.Join(t.TempDir(), "args")
	manualSeed := filepath.Join(t.TempDir(), "omnarad")
	writeDelegatingSeed(t, manualSeed, manualMarker)
	stdout, stderr, err = runInstaller(t, writeInstaller(t, "https://releases.omnara.test/omnarad"), manualDaemonHome, nil,
		"OMNARA_DAEMON_SEED_PATH="+manualSeed,
		"OMNARA_MACHINE_TOKEN=secret-token",
		"OMNARA_TEST_CREATE_CANONICAL=1",
		"SHELL=/bin/unknown-shell",
	)
	if err != nil {
		t.Fatalf("run manual installer: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	want := `export PATH="` + filepath.Join(manualDaemonHome, "bin") + `:$PATH"`
	if !strings.Contains(stdout, want) {
		t.Fatalf("manual PATH output = %q, want %q", stdout, want)
	}

	unsafeHome := filepath.Join(t.TempDir(), `home$(printf hacked)"`)
	stdout, stderr, err = runInstaller(t, writeInstaller(t, "https://releases.omnara.test/omnarad"),
		filepath.Join(unsafeHome, ".omnarad"), nil,
		"OMNARA_DAEMON_SEED_PATH="+manualSeed,
		"OMNARA_MACHINE_TOKEN=secret-token",
		"OMNARA_TEST_CREATE_CANONICAL=1",
		"SHELL=/bin/zsh",
	)
	if err != nil {
		t.Fatalf("run unsafe-path installer: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(unsafeHome, ".zshrc")); !os.IsNotExist(err) {
		t.Fatalf("unsafe path modified zsh config: %v", err)
	}
	if !strings.Contains(stdout, "its directory must be added to PATH manually") {
		t.Fatalf("unsafe path output = %q", stdout)
	}
}

func TestOmnaradInstallerUsesDefaultHomeForRestart(t *testing.T) {
	userHome := t.TempDir()
	marker := filepath.Join(t.TempDir(), "args")
	seed := filepath.Join(t.TempDir(), "omnarad")
	writeDelegatingSeed(t, seed, marker)
	script := writeInstaller(t, "https://releases.omnara.test/omnarad")
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(shell, script)
	cmd.Env = []string{
		"HOME=" + userHome,
		"PATH=" + os.Getenv("PATH"),
		"TMPDIR=" + t.TempDir(),
		"SHELL=/bin/unknown-shell",
		"OMNARA_DAEMON_SEED_PATH=" + seed,
		"OMNARA_MACHINE_TOKEN=secret-token",
		"OMNARA_TEST_CREATE_CANONICAL=1",
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run default-home installer: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(readTestFile(t, marker+".restart")); got != "restart" {
		t.Fatalf("restart invocation = %q", got)
	}
	if _, err := os.Stat(filepath.Join(userHome, ".omnarad", "bin", "omnarad")); err != nil {
		t.Fatalf("stat default canonical daemon: %v", err)
	}
}

func TestOmnaradInstallerWithoutHomeStillRestarts(t *testing.T) {
	requireInstallerShell(t)
	daemonHome := filepath.Join(t.TempDir(), "daemon")
	marker := filepath.Join(t.TempDir(), "args")
	seed := filepath.Join(t.TempDir(), "omnarad")
	writeDelegatingSeed(t, seed, marker)
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(shell, writeInstaller(t, "https://releases.omnara.test/omnarad"))
	cmd.Env = []string{
		"OMNARA_HOME=" + daemonHome,
		"PATH=" + os.Getenv("PATH"),
		"TMPDIR=" + t.TempDir(),
		"OMNARA_DAEMON_SEED_PATH=" + seed,
		"OMNARA_MACHINE_TOKEN=secret-token",
		"OMNARA_TEST_CREATE_CANONICAL=1",
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run installer without HOME: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(readTestFile(t, marker+".restart")); got != "restart" {
		t.Fatalf("restart invocation = %q", got)
	}
	want := `export PATH="` + filepath.Join(daemonHome, "bin") + `:$PATH"`
	if !strings.Contains(string(output), want) {
		t.Fatalf("manual PATH output = %q, want %q", output, want)
	}
}

func TestOmnaradInstallerTimeoutTerminatesCommand(t *testing.T) {
	requireInstallerShell(t)
	home := filepath.Join(t.TempDir(), "home")
	marker := filepath.Join(t.TempDir(), "pid")
	wrapperDir := t.TempDir()
	writeExecutable(t, filepath.Join(wrapperDir, "uname"), `#!/bin/sh
printf '%s\n' "$$" > `+shellQuote(marker)+`
exec sleep 30
`)
	script := writeInstaller(t, "https://releases.omnara.test/omnarad")
	_, stderr, err := runInstaller(t, script, home, []string{"--install-only"},
		"PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	if err == nil {
		t.Fatal("installer succeeded after command timeout")
	}
	if !strings.Contains(stderr, "unable to detect operating system") {
		t.Fatalf("installer stderr = %q", stderr)
	}
	pidBody, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read timed command pid: %v", err)
	}
	pid := strings.TrimSpace(string(pidBody))
	if _, err := strconv.Atoi(pid); err != nil {
		t.Fatalf("timed command pid = %q", pid)
	}
	t.Cleanup(func() { _ = exec.Command("kill", "-KILL", pid).Run() })
	if err := exec.Command("kill", "-0", pid).Run(); err == nil {
		t.Fatalf("timed command %s is still running", pid)
	}
}

func TestOmnaradInstallerRejectsInvalidReleaseURL(t *testing.T) {
	requireInstallerShell(t)
	tests := []struct {
		name       string
		releaseURL string
		want       string
	}{
		{
			name:       "query",
			releaseURL: "https://releases.omnara.test/omnarad?channel=stable",
			want:       "must not contain query parameters",
		},
		{name: "missing host", releaseURL: "https://:443/daemon", want: "release URL is invalid"},
		{name: "invalid port", releaseURL: "https://releases.omnara.test:bad/daemon", want: "release URL is invalid"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "home")
			seed := filepath.Join(t.TempDir(), "seed")
			writeExecutable(t, seed, "#!/bin/sh\nprintf '%s\\n' '1.2.3'\n")
			_, stderr, err := runInstaller(
				t,
				writeInstaller(t, testCase.releaseURL),
				home,
				[]string{"--install-only"},
				"OMNARA_DAEMON_SEED_PATH="+seed,
			)
			if err == nil || !strings.Contains(stderr, testCase.want) {
				t.Fatalf("installer error = %v stderr = %q, want %q", err, stderr, testCase.want)
			}
		})
	}
}

func TestOmnaradInstallerDownloadsVerifiesAndDelegates(t *testing.T) {
	requireInstallerShell(t)
	curlPath, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl is unavailable")
	}
	checksumPath, err := exec.LookPath("sha256sum")
	checksumArgs := ""
	if err != nil {
		checksumPath, err = exec.LookPath("shasum")
		checksumArgs = " -a 256"
	}
	if err != nil {
		t.Skip("sha256sum and shasum are unavailable")
	}
	marker := filepath.Join(t.TempDir(), "install-args")
	artifact := []byte(`#!/bin/sh
case "${1:-}" in
  --version)
    [ -z "${OMNARA_MACHINE_TOKEN+x}" ] || exit 91
    printf '%s\n' '2.3.4'
    ;;
  install)
    [ "${OMNARA_MACHINE_TOKEN:-}" = secret-token ] || exit 92
    printf '%s\n' "$@" > ` + shellQuote(marker) + `
    ;;
  *) exit 90 ;;
esac
`)
	digest := sha256.Sum256(artifact)
	var manifestRequests atomic.Int32
	var artifactRequests atomic.Int32
	var releaseServer *httptest.Server
	releaseServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/stable/") && strings.HasSuffix(r.URL.Path, ".txt"):
			manifestRequests.Add(1)
			_, _ = fmt.Fprintf(w, "version=2.3.4\nurl=%s/artifact\nsha256=%x\n", releaseServer.URL, digest)
		case strings.HasPrefix(r.URL.Path, "/bad/") && strings.HasSuffix(r.URL.Path, ".txt"):
			manifestRequests.Add(1)
			_, _ = fmt.Fprintf(w, "version=2.3.4\nurl=%s/artifact\nsha256=%s\n", releaseServer.URL, strings.Repeat("0", 64))
		case r.URL.Path == "/artifact":
			artifactRequests.Add(1)
			_, _ = w.Write(artifact)
		default:
			http.NotFound(w, r)
		}
	}))
	defer releaseServer.Close()

	wrapperDir := t.TempDir()
	writeExecutable(t, filepath.Join(wrapperDir, "curl"), `#!/bin/sh
[ -z "${OMNARA_MACHINE_TOKEN+x}" ] || exit 91
exec `+shellQuote(curlPath)+` "$@"
`)
	writeExecutable(t, filepath.Join(wrapperDir, "sha256sum"), `#!/bin/sh
[ -z "${OMNARA_MACHINE_TOKEN+x}" ] || exit 91
exec `+shellQuote(checksumPath)+checksumArgs+` "$@"
`)
	pathEnv := "PATH=" + wrapperDir + string(os.PathListSeparator) + os.Getenv("PATH")
	home := filepath.Join(t.TempDir(), "home")
	_, stderr, err := runInstaller(
		t,
		writeInstaller(t, "https://releases.omnara.test/default"),
		home,
		[]string{"--install-only"},
		pathEnv,
		"OMNARA_DAEMON_RELEASE_URL="+releaseServer.URL+"/stable",
		"OMNARA_DAEMON_SEED_PATH=/does/not/exist",
		"OMNARA_MACHINE_TOKEN=secret-token",
		"NO_PROXY=localhost,127.0.0.1",
		"no_proxy=localhost,127.0.0.1",
	)
	if err != nil {
		t.Fatalf("run download installer: %v\nstderr: %s", err, stderr)
	}
	assertInstallInvocation(t, marker, true)
	manifestURL := strings.Split(strings.TrimSpace(readTestFile(t, marker)), "\n")[2]
	if !strings.Contains(manifestURL, "/stable/") || strings.Contains(manifestURL, "/latest/") {
		t.Fatalf("release manifest URL = %q", manifestURL)
	}

	badHome := filepath.Join(t.TempDir(), "home")
	_, stderr, err = runInstaller(
		t,
		writeInstaller(t, releaseServer.URL+"/bad"),
		badHome,
		[]string{"--install-only"},
		pathEnv,
		"OMNARA_MACHINE_TOKEN=secret-token",
		"NO_PROXY=localhost,127.0.0.1",
		"no_proxy=localhost,127.0.0.1",
	)
	if err == nil || !strings.Contains(stderr, "omnarad checksum mismatch") {
		t.Fatalf("bad checksum error = %v stderr = %q", err, stderr)
	}
	if manifestRequests.Load() != 2 || artifactRequests.Load() != 2 {
		t.Fatalf("release requests = manifest:%d artifact:%d", manifestRequests.Load(), artifactRequests.Load())
	}
}

func assertInstallInvocation(t *testing.T, path string, noStart bool) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(readTestFile(t, path)), "\n")
	wantLen := 3
	if noStart {
		wantLen = 4
	}
	if len(lines) != wantLen || lines[0] != "install" || lines[1] != "--release-manifest-url" ||
		!strings.HasSuffix(lines[2], ".txt") {
		t.Fatalf("install invocation = %q", lines)
	}
	if noStart && lines[3] != "--no-start" {
		t.Fatalf("install invocation = %q", lines)
	}
}

func assertShellSyntax(t *testing.T, script []byte) {
	t.Helper()
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh is unavailable")
	}
	shells := []string{shell}
	if dash, err := exec.LookPath("dash"); err == nil && dash != shell {
		shells = append(shells, dash)
	}
	for _, shell := range shells {
		cmd := exec.Command(shell, "-n")
		cmd.Stdin = strings.NewReader(string(script))
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("installer shell syntax with %s: %v\n%s", shell, err, output)
		}
	}
}

func requireInstallerShell(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is unavailable")
	}
}

func writeInstaller(t *testing.T, releaseURL string) string {
	t.Helper()
	script, err := renderOmnaradInstallScript(releaseURL, "https://app.omnara.test")
	if err != nil {
		t.Fatalf("render installer: %v", err)
	}
	assertShellSyntax(t, script)
	path := filepath.Join(t.TempDir(), "omnarad.sh")
	if err := os.WriteFile(path, script, 0o700); err != nil {
		t.Fatalf("write installer: %v", err)
	}
	return path
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}

func writeDelegatingSeed(t *testing.T, path, marker string) {
	t.Helper()
	writeExecutable(t, path, `#!/bin/sh
case "${1:-}" in
  --version)
    [ -z "${OMNARA_MACHINE_TOKEN+x}" ] || exit 91
    printf '%s\n' '1.2.3'
    ;;
  install)
    daemon_home=${OMNARA_HOME:-$HOME/.omnarad}
    [ "${OMNARA_MACHINE_TOKEN:-}" = secret-token ] || exit 93
    [ "${OMNARA_API_URL:-}" = https://app.omnara.test ] || exit 94
    umask > `+shellQuote(marker+".umask")+`
    printf '%s\n' "$@" > `+shellQuote(marker)+`
    if [ "${OMNARA_TEST_CREATE_CANONICAL:-0}" = 1 ]; then
      mkdir -p "$daemon_home/bin"
      cp "$0" "$daemon_home/bin/omnarad"
      chmod 0700 "$daemon_home/bin/omnarad"
    fi
    ;;
  restart)
    for install_dir in "${TMPDIR:-/tmp}"/omnarad-install.*; do
      [ ! -e "$install_dir" ] || exit 95
    done
    printf '%s\n' "$@" > `+shellQuote(marker)+`.restart
    ;;
  *) exit 90 ;;
esac
`)
}

func runInstaller(t *testing.T, script, home string, args []string, extraEnv ...string) (string, string, error) {
	t.Helper()
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Fatalf("find sh: %v", err)
	}
	commandArgs := append([]string{script}, args...)
	cmd := exec.Command(shell, commandArgs...)
	cmd.Env = append([]string{
		"HOME=" + filepath.Dir(home),
		"OMNARA_HOME=" + home,
		"PATH=" + os.Getenv("PATH"),
		"TMPDIR=" + t.TempDir(),
	}, extraEnv...)
	var stdout strings.Builder
	var stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	return stdout.String(), stderr.String(), err
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}
