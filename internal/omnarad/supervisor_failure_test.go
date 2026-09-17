package omnarad

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/machinedaemon"
	"github.com/stretchr/testify/require"
)

func TestSupervisorReportsUnexpectedFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script string
		want   string
	}{
		{"exit", "#!/bin/sh\nprintf 'stdout detail\\n'\nprintf 'stderr detail\\n' >&2\nexit 7\n", "exit status 7"},
		{"signal", "#!/bin/sh\nkill -KILL $$\n", "signal: killed"},
		{
			"fatal",
			"#!/bin/sh\nexec \"$OMNARA_FATAL_TEST_BINARY\" -test.run '^TestSupervisorRuntimeFatalHelper$'\n",
			"fatal error: stack overflow",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OMNARA_FATAL_TEST_BINARY", os.Args[0])
			t.Setenv("OMNARA_FATAL_TEST_HELPER", "1")
			t.Setenv("GOTRACEBACK", "none")
			home := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(home, "bin"), 0o700))
			writeTestExecutable(t, canonicalDaemonPath(home), tc.script)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				query := "stage=daemon_runtime"
				if tc.name == "fatal" {
					query = "capture_status=1&stage=daemon_runtime"
					if len(body) != machinedaemon.MaxFailureDetailBytes || !strings.Contains(string(body), "exit status 2") {
						t.Errorf("runtime fatal report length=%d, body=%q", len(body), body)
					}
					if !strings.HasPrefix(string(body), "runtime: goroutine stack exceeds 32768-byte limit\n") {
						t.Errorf("runtime fatal report missing stack limit: %q", body)
					}
				}
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/daemon/failures" ||
					r.URL.RawQuery != query || r.Header.Get("Authorization") != "Bearer token-a" {
					t.Errorf("unexpected report request: %s %s", r.Method, r.URL)
				}
				if !strings.Contains(string(body), tc.want) {
					t.Errorf("failure detail = %q", body)
				}
				if !strings.Contains(string(body), " after ") {
					t.Errorf("missing child uptime: %q", body)
				}
				_, memory, found := strings.Cut(string(body), "wait_max_rss_mib=")
				peakMiB, parseErr := strconv.ParseFloat(memory, 64)
				if !found || parseErr != nil || peakMiB <= 0 {
					t.Errorf("missing or invalid child memory usage: %q", body)
				}
				if tc.name == "exit" && (!strings.Contains(string(body), "stdout detail") ||
					!strings.Contains(string(body), "stderr detail")) {
					t.Errorf("missing captured output: %q", body)
				}
				w.WriteHeader(http.StatusInternalServerError)
				cancel()
			}))
			defer server.Close()
			setConfiguredDaemonEnvironment(t, home, server.URL, "")
			stdout, stderr := supervisorChildWriters(enospcWriter{}, enospcWriter{}, enospcWriter{})
			require.NoError(t, runSupervisorLoop(
				ctx, home, longBackoffDelay, make(chan os.Signal), stdout, stderr, discardLogger(), nil,
			))
			require.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestSupervisorReturnsStartFailure(t *testing.T) {
	for _, name := range []string{"missing", "not_executable"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			want := os.ErrNotExist
			if name == "not_executable" {
				require.NoError(t, os.MkdirAll(filepath.Join(home, "bin"), 0o700))
				require.NoError(t, os.WriteFile(canonicalDaemonPath(home), []byte("#!/bin/sh\nexit 0\n"), 0o600))
				want = os.ErrPermission
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			setConfiguredDaemonEnvironment(t, home, server.URL, "")
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := runSupervisorLoop(
				ctx, home, longBackoffDelay, make(chan os.Signal), io.Discard, io.Discard, discardLogger(), nil,
			)
			require.ErrorIs(t, err, want)
			require.ErrorContains(t, err, "start supervised daemon")
			require.NoError(t, ctx.Err())
			require.Zero(t, calls.Load())
		})
	}
}

func TestSupervisorRuntimeFatalHelper(t *testing.T) {
	if os.Getenv("OMNARA_FATAL_TEST_HELPER") != "1" {
		return
	}
	var ready sync.WaitGroup
	ready.Add(100)
	for range 100 {
		go func() {
			ready.Done()
			select {}
		}()
	}
	ready.Wait()
	debug.SetMaxStack(32 * 1024)
	supervisorTestStackOverflow(0)
}

func supervisorTestStackOverflow(n int) int {
	if n == 100000 {
		return n
	}
	var block [1024]byte
	block[n%len(block)] = byte(n)
	return supervisorTestStackOverflow(n+1) + int(block[n%len(block)])
}

func TestSupervisorReportCancellation(t *testing.T) {
	for _, reason := range []string{"timeout", "shutdown"} {
		t.Run(reason, func(t *testing.T) {
			home := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(home, "bin"), 0o700))
			t.Setenv("SUPERVISOR_COUNT", filepath.Join(home, "count"))
			writeTestExecutable(t, canonicalDaemonPath(home), `#!/bin/sh
[ -f "$SUPERVISOR_COUNT" ] && exit 0
touch "$SUPERVISOR_COUNT"
exit 7
`)
			started := make(chan struct{})
			canceled := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				close(started)
				<-r.Context().Done()
				close(canceled)
			}))
			defer server.Close()
			setConfiguredDaemonEnvironment(t, home, server.URL, "")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			restart := make(chan os.Signal, 1)
			done := make(chan error, 1)
			logs := newLineChannelWriter()
			go func() {
				done <- runSupervisorLoop(
					ctx, home, longBackoffDelay, restart, io.Discard, io.Discard, slog.New(slog.NewJSONHandler(logs, nil)), nil,
				)
			}()
			readSupervisorRestartLog(t, logs.lines)
			waitForReportStart(t, ctx, started)
			start := time.Now()
			if reason == "shutdown" {
				cancel()
			}
			select {
			case <-canceled:
			case <-time.After(5 * time.Second):
				t.Fatal("report was not canceled")
			}
			if reason != "timeout" {
				require.Less(t, time.Since(start), supervisorFailureReportTimeout)
			} else {
				select {
				case line := <-logs.lines:
					require.Contains(t, line, "report daemon runtime failure failed")
					require.Contains(t, line, "context deadline exceeded")
				case <-time.After(time.Second):
					t.Fatal("report timeout was not logged")
				}
				cancel()
			}
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("report delayed supervisor exit")
			}
			select {
			case line := <-logs.lines:
				t.Fatalf("unexpected warning after shutdown: %s", line)
			default:
			}
		})
	}
}

func TestSupervisorReportSurvivesRestart(t *testing.T) {
	for _, reason := range []string{"backoff", "manual"} {
		t.Run(reason, func(t *testing.T) {
			home := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(home, "bin"), 0o700))
			t.Setenv("SUPERVISOR_COUNT", filepath.Join(home, "count"))
			writeTestExecutable(t, canonicalDaemonPath(home), `#!/bin/sh
if [ ! -f "$SUPERVISOR_COUNT" ]; then
  touch "$SUPERVISOR_COUNT"
  exit 7
fi
trap 'exit 0' TERM
printf 'restarted\n'
while :; do sleep 0.1; done
`)
			started := make(chan struct{})
			release := make(chan struct{})
			delivered := make(chan bool, 1)
			server := newBlockingReportServer(started, release, delivered)
			defer server.Close()
			setConfiguredDaemonEnvironment(t, home, server.URL, "")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			restart := make(chan os.Signal, 1)
			output := newLineChannelWriter()
			done := make(chan error, 1)
			delay := 500 * time.Millisecond
			if reason == "manual" {
				delay = time.Hour
			}
			go func() {
				done <- runSupervisorLoop(ctx, home, delay, restart, output, io.Discard, discardLogger(), nil)
			}()
			waitForReportStart(t, ctx, started)
			if reason == "manual" {
				restart <- daemonRestartSignal
			}
			waitForMarkerLine(t, output.lines, "restarted")
			close(release)
			select {
			case ok := <-delivered:
				require.True(t, ok, "restart canceled the in-flight report")
			case <-ctx.Done():
				t.Fatal("report did not finish")
			}
			cancel()
			require.NoError(t, <-done)
		})
	}
}

func TestSupervisorCleanExitWaitsForReport(t *testing.T) {
	for _, outcome := range []string{"complete", "timeout", "shutdown"} {
		t.Run(outcome, func(t *testing.T) {
			home := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(home, "bin"), 0o700))
			writeTestExecutable(t, canonicalDaemonPath(home), `#!/bin/sh
if [ ! -f "$OMNARA_HOME/once" ]; then
  touch "$OMNARA_HOME/once"
  exit 7
fi
printf 'clean exit\n'
exit 0
`)
			started := make(chan struct{})
			release := make(chan struct{})
			delivered := make(chan bool, 1)
			server := newBlockingReportServer(started, release, delivered)
			defer server.Close()
			setConfiguredDaemonEnvironment(t, home, server.URL, "")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			restart := make(chan os.Signal, 1)
			output := newLineChannelWriter()
			done := make(chan error, 1)
			go func() {
				done <- runSupervisorLoop(ctx, home, longBackoffDelay, restart, output, io.Discard, discardLogger(), nil)
			}()
			waitForReportStart(t, ctx, started)
			restart <- daemonRestartSignal
			waitForMarkerLine(t, output.lines, "clean exit")
			select {
			case err := <-done:
				t.Fatalf("supervisor returned before its pending report finished: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			start := time.Now()
			switch outcome {
			case "complete":
				close(release)
			case "shutdown":
				cancel()
			}
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(supervisorFailureReportTimeout + time.Second):
				t.Fatal("supervisor did not finish within the report deadline")
			}
			if outcome == "shutdown" {
				require.Less(t, time.Since(start), supervisorFailureReportTimeout)
			} else {
				require.NoError(t, ctx.Err())
			}
			select {
			case ok := <-delivered:
				require.Equal(t, outcome == "complete", ok)
			case <-time.After(time.Second):
				t.Fatal("report handler did not finish")
			}
		})
	}
}

func TestSupervisorStartFailureWaitsForReport(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, "bin"), 0o700))
	writeTestExecutable(t, canonicalDaemonPath(home), "#!/bin/sh\nexit 7\n")
	started := make(chan struct{})
	release := make(chan struct{})
	delivered := make(chan bool, 1)
	server := newBlockingReportServer(started, release, delivered)
	defer server.Close()
	setConfiguredDaemonEnvironment(t, home, server.URL, "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	restart := make(chan os.Signal, 1)
	done := make(chan error, 1)
	go func() {
		done <- runSupervisorLoop(ctx, home, longBackoffDelay, restart, io.Discard, io.Discard, discardLogger(), nil)
	}()
	waitForReportStart(t, ctx, started)
	require.NoError(t, os.Remove(canonicalDaemonPath(home)))
	restart <- daemonRestartSignal
	select {
	case err := <-done:
		t.Fatalf("supervisor returned before its pending report finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		require.ErrorIs(t, err, os.ErrNotExist)
		require.ErrorContains(t, err, "start supervised daemon")
	case <-ctx.Done():
		t.Fatal("supervisor did not return its start failure")
	}
	require.True(t, <-delivered)
}

func TestSupervisorReportResetsForEachChild(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, "bin"), 0o700))
	t.Setenv("SUPERVISOR_COUNT", filepath.Join(home, "count"))
	writeTestExecutable(t, canonicalDaemonPath(home), `#!/bin/sh
if [ -f "$SUPERVISOR_COUNT" ]; then
  printf 'second child\n'
else
  touch "$SUPERVISOR_COUNT"
  printf 'first child\n'
fi
exit 7
`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type report struct {
		detail string
		auth   string
	}
	reports := make(chan report, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		reports <- report{detail: string(body), auth: r.Header.Get("Authorization")}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	setConfiguredDaemonEnvironment(t, home, server.URL, "")
	t.Setenv("OMNARA_MACHINE_TOKEN", "")
	restart := make(chan os.Signal, 1)
	done := make(chan error, 1)
	go func() {
		done <- runSupervisorLoop(ctx, home, longBackoffDelay, restart, io.Discard, io.Discard, discardLogger(), nil)
	}()
	for _, child := range []string{"first", "second"} {
		select {
		case got := <-reports:
			require.Contains(t, got.detail, child+" child")
			if child == "first" {
				require.Equal(t, "Bearer token-a", got.auth)
				config, err := loadDaemonConfig(home)
				require.NoError(t, err)
				config.MachineToken = "second"
				writeTestDaemonConfig(t, home, *config)
				restart <- daemonRestartSignal
			} else {
				require.Equal(t, "Bearer second", got.auth)
				require.NotContains(t, got.detail, "first child")
			}
		case <-ctx.Done():
			t.Fatal("report did not arrive")
		}
	}
	cancel()
	require.NoError(t, <-done)
}

func TestSupervisorReportingConfigSource(t *testing.T) {
	for _, mode := range []string{"supplied_home", "config_disappears", "environment_override"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(home, "bin"), 0o700))
			script := "#!/bin/sh\nexit 7\n"
			if mode == "config_disappears" {
				script = "#!/bin/sh\nmv \"$OMNARA_HOME/daemon.json\" \"$OMNARA_HOME/config.saved\" || exit 8\nexit 7\n"
			}
			writeTestExecutable(t, canonicalDaemonPath(home), script)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				wantToken := "token-a"
				if mode == "environment_override" {
					wantToken = "override-token"
				}
				if r.Header.Get("Authorization") != "Bearer "+wantToken {
					t.Errorf("report used wrong credentials: %q", r.Header.Get("Authorization"))
				}
				if r.URL.Path != "/api/v1/daemon/failures" {
					t.Errorf("report used wrong API path: %s", r.URL.Path)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || !strings.Contains(string(body), "exit status 7") {
					t.Errorf("unexpected crash report: %q, err=%v", body, err)
				}
				w.WriteHeader(http.StatusNoContent)
				cancel()
			}))
			defer server.Close()
			setConfiguredDaemonEnvironment(t, home, server.URL, "")
			setDaemonEnvironment(t, home, "", "")
			if mode == "supplied_home" {
				otherHome := t.TempDir()
				config, err := loadDaemonConfig(home)
				require.NoError(t, err)
				config.MachineToken = "other-home-token"
				writeTestDaemonConfig(t, otherHome, *config)
				t.Setenv("OMNARA_HOME", otherHome)
			} else if mode == "environment_override" {
				config, err := loadDaemonConfig(home)
				require.NoError(t, err)
				config.APIURL = server.URL + "/wrong-base"
				writeTestDaemonConfig(t, home, *config)
				t.Setenv("OMNARA_API_URL", server.URL+"/api/v1")
				t.Setenv("OMNARA_MACHINE_TOKEN", "override-token")
			}
			require.NoError(t, runSupervisorLoop(
				ctx, home, longBackoffDelay, make(chan os.Signal), io.Discard, io.Discard, discardLogger(), nil,
			))
			require.EqualValues(t, 1, calls.Load())
			if mode == "config_disappears" {
				_, err := os.Stat(filepath.Join(home, daemonConfigFileName))
				require.ErrorIs(t, err, os.ErrNotExist)
			}
		})
	}
}

func TestSupervisorReportingConfigFailureDoesNotPreventLaunch(t *testing.T) {
	home := t.TempDir()
	setDaemonEnvironment(t, home, "", "")
	require.NoError(t, os.MkdirAll(filepath.Join(home, "bin"), 0o700))
	writeTestExecutable(t, canonicalDaemonPath(home), "#!/bin/sh\nexit 0\n")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, runSupervisorLoop(
		ctx, home, longBackoffDelay, make(chan os.Signal), io.Discard, io.Discard, discardLogger(), nil,
	))
	require.NoError(t, ctx.Err())
}

func TestSupervisorDoesNotReportIntentionalExit(t *testing.T) {
	for _, action := range []string{"clean", "shutdown", "restart"} {
		t.Run(action, func(t *testing.T) {
			home := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(home, "bin"), 0o700))
			script := "#!/bin/sh\nexit 0\n"
			if action != "clean" {
				script = "#!/bin/sh\ntrap 'exit 7' TERM USR1\nprintf 'ready\\n'\nwhile :; do sleep 0.1; done\n"
			}
			writeTestExecutable(t, canonicalDaemonPath(home), script)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			setConfiguredDaemonEnvironment(t, home, server.URL, "")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			restart := make(chan os.Signal, 1)
			output := newLineChannelWriter()
			done := make(chan error, 1)
			go func() {
				done <- runSupervisorLoop(ctx, home, longBackoffDelay, restart, output, io.Discard, discardLogger(), nil)
			}()
			if action != "clean" {
				waitForMarkerLine(t, output.lines, "ready")
				if action == "restart" {
					restart <- daemonRestartSignal
					waitForMarkerLine(t, output.lines, "ready")
				}
				cancel()
			}
			require.NoError(t, <-done)
			require.EqualValues(t, 0, calls.Load())
		})
	}
}

func newBlockingReportServer(
	started chan<- struct{}, release <-chan struct{}, delivered chan<- bool,
) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-release:
			w.WriteHeader(http.StatusNoContent)
			delivered <- true
		case <-r.Context().Done():
			delivered <- false
		}
	}))
}

func waitForReportStart(t *testing.T, ctx context.Context, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("report did not start")
	}
}
