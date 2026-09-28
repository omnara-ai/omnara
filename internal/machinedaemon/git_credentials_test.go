package machinedaemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitCredentialHelperWithRealGit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test executable alias requires symlink permission")
	}
	git, err := exec.LookPath("git")
	require.NoError(t, err)
	ctx := t.Context()
	processID, err := publicid.Encode(publicid.KindProcess, uuid.New())
	require.NoError(t, err)
	var calls atomic.Int32
	var transientFailures atomic.Int32
	var permanentFailure atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, "/daemon/processes/"+processID+"/git-credentials", r.URL.Path)
		assert.Equal(t, "Bearer machine-secret", r.Header.Get("Authorization"))
		if status := permanentFailure.Load(); status != 0 {
			http.Error(w, "do-not-echo-upstream-body", int(status))
			return
		}
		if transientFailures.Add(-1) >= 0 {
			http.Error(w, "temporary failure", http.StatusServiceUnavailable)
			return
		}
		assert.NoError(t, json.NewEncoder(w).Encode(daemonprotocol.GitCredentials{
			Token: "fresh-token", ExpiresAt: time.Now().Add(time.Hour),
		}))
	}))
	defer upstream.Close()
	client := New(Config{
		APIURL: upstream.URL, MachineToken: "machine-secret", OmnaraHome: t.TempDir(),
		ExpectedInstallationID: "inst_test", ExpectedMachineID: "mch_test",
	}, nil, nil)
	defer client.closeState()
	state, err := client.stateStore(ctx)
	require.NoError(t, err)
	require.NoError(t, state.ReserveProcess(ctx, statedb.Process{
		ProcessID: processID, SupervisorInstanceID: "supervisor", SupervisorToken: "supervisor-secret",
	}))
	stop, err := client.startGitCredentialServer(ctx)
	require.NoError(t, err)
	defer stop()
	assignment := ProcessAssignment{ID: processID, GitCredentials: true}
	client.prepareGitCredentials(&assignment, "supervisor-secret")
	require.Empty(t, assignment.PreparationError)
	alias := filepath.Join(t.TempDir(), "helper's path !")
	require.NoError(t, os.Symlink(assignment.GitCredentialHelper.Executable, alias))
	assignment.GitCredentialHelper.Executable = alias
	stored := filepath.Join(t.TempDir(), "unexpected-store")
	ambient := "!f() { if [ \"$1\" = store ]; then printf leaked > " + gitShellQuote(stored) +
		"; fi; echo username=ambient; echo password=ambient; }; f"
	base := append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0="+ambient,
		"GIT_CONFIG_KEY_1=color.ui", "GIT_CONFIG_VALUE_1=never",
		"GIT_CONFIG_PARAMETERS="+gitShellQuote("credential.https://github.com.helper=!echo username=old; echo password=old"))
	env := gitCredentialEnvironment(base, processID, assignment.GitCredentialHelper)
	for _, entry := range env {
		if strings.HasPrefix(entry, "GIT_CONFIG_PARAMETERS=") {
			require.NotContains(t, entry, assignment.GitCredentialHelper.Capability)
		}
	}
	run := func(operation, host string, environment []string) (string, string, error) {
		command := exec.CommandContext(ctx, git, "credential", operation)
		command.Env = environment
		input := "protocol=https\nhost=" + host + "\n"
		if operation == "approve" {
			input += "username=x-access-token\npassword=fresh-token\n"
		}
		command.Stdin = strings.NewReader(input + "\n")
		var output, stderr bytes.Buffer
		command.Stdout, command.Stderr = &output, &stderr
		err := command.Run()
		return output.String(), stderr.String(), err
	}
	output, stderr, err := run("fill", "github.com", env)
	require.NoError(t, err, stderr)
	require.Contains(t, output, "password=fresh-token")
	require.NotContains(t, output, "ambient")
	require.Empty(t, stderr)
	_, stderr, err = run("approve", "github.com", env)
	require.NoError(t, err, stderr)
	require.NoFileExists(t, stored, "installation tokens must not reach the ambient helper's store operation")
	executable, err := os.Executable()
	require.NoError(t, err)
	require.NoError(t, os.Symlink(executable, alias+".next"))
	require.NoError(t, os.Rename(alias+".next", alias))
	transientFailures.Store(2)
	beforeRetry := calls.Load()
	output, stderr, err = run("fill", "github.com", env)
	require.NoError(t, err, stderr)
	require.Contains(t, output, "password=fresh-token")
	require.Equal(t, beforeRetry+3, calls.Load())
	for _, status := range []int{http.StatusForbidden, http.StatusConflict} {
		permanentFailure.Store(int32(status))
		beforeFailure := calls.Load()
		output, stderr, err = run("fill", "github.com", env)
		require.Error(t, err)
		require.Empty(t, output)
		require.NotContains(t, stderr, "do-not-echo-upstream-body")
		require.Equal(t, beforeFailure+1, calls.Load(), "permanent errors must not retry")
	}
	permanentFailure.Store(0)
	output, stderr, err = run("fill", "gitlab.com", env)
	require.NoError(t, err, stderr)
	require.Contains(t, output, "password=ambient")
	command := exec.CommandContext(ctx, git, "config", "color.ui")
	command.Env = env
	value, err := command.Output()
	require.NoError(t, err)
	require.Equal(t, "never\n", string(value))
	before := calls.Load()
	_, stderr, err = run("reject", "github.com", env)
	require.NoError(t, err, stderr)
	require.Equal(t, before, calls.Load())
	assignment.GitCredentialHelper.Capability = "wrong-capability"
	output, stderr, err = run("fill", "github.com",
		gitCredentialEnvironment(base, processID, assignment.GitCredentialHelper))
	require.Error(t, err)
	require.Empty(t, output)
	require.Contains(t, stderr, "quit")
	require.NotContains(t, stderr, "wrong-capability")
	require.NotContains(t, stderr, "machine-secret")
	require.Equal(t, before, calls.Load())
}

func TestGitCredentialsRunningShellSurvivesDaemonRestartAndTokenRotation(t *testing.T) {
	for _, mode := range []processcmd.IOMode{processcmd.IOModePipe, processcmd.IOModePTY} {
		t.Run(string(mode), func(t *testing.T) { testGitCredentialsRunningShellRestart(t, mode) })
	}
}

func testGitCredentialsRunningShellRestart(t *testing.T, mode processcmd.IOMode) {
	if runtime.GOOS == "windows" {
		t.Skip("shell journey uses POSIX shell")
	}
	_, err := exec.LookPath("git")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var generation atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, credential := "old-machine-token", "first-credential"
		if generation.Load() == 1 {
			token, credential = "new-machine-token", "next-credential"
		}
		assert.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
		assert.NoError(t, json.NewEncoder(w).Encode(daemonprotocol.GitCredentials{
			Token: credential, ExpiresAt: time.Now().Add(time.Hour),
		}))
	}))
	defer upstream.Close()
	processID, err := publicid.Encode(publicid.KindProcess, uuid.New())
	require.NoError(t, err)
	gate := filepath.Join(t.TempDir(), "continue")
	fill := "printf 'protocol=https\\nhost=github.com\\n\\n' | git credential fill"
	fixture := newDetachedSupervisorTestFixtureWithConfig(t, ctx,
		Config{APIURL: upstream.URL, MachineToken: "old-machine-token"}, ProcessAssignment{
			ID: processID, GitCredentials: true, TimeoutSeconds: 25,
			Process: Process{
				Command:       fill + "; while ! test -f " + gitShellQuote(gate) + "; do sleep 0.05; done; " + fill,
				ShellSelector: processcmd.ShellSH, IOMode: mode, Cwd: t.TempDir(),
			},
			Env: map[string]string{"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1", "GIT_TERMINAL_PROMPT": "0"},
		})
	stop, err := fixture.client.startGitCredentialServer(ctx)
	require.NoError(t, err)
	fixture.acceptAndStart(t, ctx)
	fixture.waitForOutput(t, ctx, "password=first-credential")
	stop()
	require.NoError(t, fixture.client.closeState())
	generation.Store(1)
	cfg := fixture.client.cfg
	cfg.MachineToken = "new-machine-token"
	replacement := New(cfg, nil, nil)
	replacement.bootstrap = fixture.client.bootstrap
	fixture.client = &replacement
	fixture.store, err = replacement.stateStore(ctx)
	require.NoError(t, err)
	stop, err = replacement.startGitCredentialServer(ctx)
	require.NoError(t, err)
	defer stop()
	require.NoError(t, os.WriteFile(gate, nil, 0o600))
	fixture.waitForOutput(t, ctx, "password=next-credential")
	fixture.waitClosed(t, 5*time.Second)
}

func TestGitCredentialPreparationHonorsMachineOptOut(t *testing.T) {
	client := New(Config{GitCredentialsDisabled: true}, nil, nil)
	assignment := ProcessAssignment{GitCredentials: true}
	client.prepareGitCredentials(&assignment, "unused")
	require.Contains(t, assignment.PreparationError, "disabled")
	require.Nil(t, assignment.GitCredentialHelper)
	assignment = ProcessAssignment{}
	client.prepareGitCredentials(&assignment, "unused")
	require.Empty(t, assignment.PreparationError)
	client.cfg.GitCredentialsDisabled = false
	client.gitCredentialsUnavailable = true
	client.prepareGitCredentials(&assignment, "unused")
	require.Empty(t, assignment.PreparationError)
	assignment.GitCredentials = true
	client.prepareGitCredentials(&assignment, "unused")
	require.Contains(t, assignment.PreparationError, "service is unavailable")
}

func TestGitCredentialHelperRejectsInvalidInputWithoutTokenDisclosure(t *testing.T) {
	for _, input := range []string{
		"protocol=http\nhost=github.com\n\n",
		"protocol=https\nhost=example.com\n\n",
		strings.Repeat("x", 65537),
	} {
		var output bytes.Buffer
		err := RunGitCredentialHelper(t.Context(), "unused", "unused", "get", strings.NewReader(input), &output)
		require.Error(t, err)
		require.Equal(t, "quit=true\n\n", output.String())
	}
	for _, operation := range []string{"store", "erase"} {
		require.NoError(t, RunGitCredentialHelper(t.Context(), "", "", operation, nil, io.Discard))
	}
}
