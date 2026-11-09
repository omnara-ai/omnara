package machinedaemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localipc"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newProgramToolCallTestClient(t *testing.T, apiURL string) (*Client, string) {
	t.Helper()
	processID, err := publicid.Encode(publicid.KindProcess, uuid.New())
	require.NoError(t, err)
	client := New(Config{
		APIURL: apiURL, MachineToken: "machine-secret", OmnaraHome: t.TempDir(),
		ExpectedInstallationID: "inst_test", ExpectedMachineID: "mch_test",
	}, nil, nil)
	t.Cleanup(func() { _ = client.closeState() })
	state, err := client.stateStore(t.Context())
	require.NoError(t, err)
	require.NoError(t, state.ReserveProcess(t.Context(), statedb.Process{
		ProcessID: processID, SupervisorInstanceID: "supervisor", SupervisorToken: "supervisor-secret",
	}))
	return &client, processID
}

func programToolCallSocketClient(endpoint string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return localipc.Dial(ctx, endpoint)
		},
	}}
}

func programToolCallLocalRequest(
	t *testing.T,
	client *http.Client,
	method, path, capability string,
	body string,
) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequestWithContext(t.Context(), method, "http://daemon"+path, reader)
	require.NoError(t, err)
	if capability != "" {
		request.Header.Set("Authorization", "Bearer "+capability)
	}
	response, err := client.Do(request)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response, data
}

func TestProgramToolCallServerForwardsToBackend(t *testing.T) {
	var upstreamCalls atomic.Int32
	var mu sync.Mutex
	var seen []*http.Request
	var seenBodies []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, r.Clone(r.Context()))
		seenBodies = append(seenBodies, string(body))
		mu.Unlock()
		assert.Equal(t, "Bearer machine-secret", r.Header.Get("Authorization"))
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/tool-calls"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"tool_call_id":"tc 1","state":"pending"}`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/tool-calls/missing"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"tool call not found","code":"not_found"}`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/tool-calls/limited"):
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":"slow down","code":"rate_limited"}`)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"tool_call_id":"tc 1","state":"completed","outcome":"succeeded"}`)
		}
	}))
	defer upstream.Close()
	client, processID := newProgramToolCallTestClient(t, upstream.URL)
	stop, err := client.startProgramToolCallServer(t.Context())
	require.NoError(t, err)
	defer stop()
	endpoint, err := client.processServiceEndpoint(programToolCallService)
	require.NoError(t, err)
	require.Equal(t, "tool-calls.sock", filepath.Base(endpoint))
	local := programToolCallSocketClient(endpoint)
	capability := programToolCallService.capability("supervisor-secret", processID)

	response, body := programToolCallLocalRequest(t, local, http.MethodPost, "/tool-calls/"+processID, capability,
		`{"key":"k-1","name":"storage_list","input":{"prefix":"a"},"extra":true}`)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.JSONEq(t, `{"tool_call_id":"tc 1","state":"pending"}`, string(body))
	require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	mu.Lock()
	require.Equal(t, "/daemon/processes/"+processID+"/tool-calls", seen[0].URL.Path)
	require.Equal(t, "application/json", seen[0].Header.Get("Content-Type"))
	require.JSONEq(t, `{"key":"k-1","name":"storage_list","input":{"prefix":"a"},"extra":true}`, seenBodies[0],
		"the backend validates the request")
	mu.Unlock()

	response, _ = programToolCallLocalRequest(t, local, http.MethodGet,
		"/tool-calls/"+processID+"/tc%201?wait_ms=99999", capability, "")
	require.Equal(t, http.StatusOK, response.StatusCode)
	mu.Lock()
	require.Equal(t, "/daemon/processes/"+processID+"/tool-calls/tc%201", seen[1].URL.EscapedPath())
	require.Equal(t, "25000", seen[1].URL.Query().Get("wait_ms"), "wait_ms must be clamped")
	mu.Unlock()

	response, _ = programToolCallLocalRequest(t, local, http.MethodGet,
		"/tool-calls/"+processID+"/tc1?wait_ms=1200", capability, "")
	require.Equal(t, http.StatusOK, response.StatusCode)
	mu.Lock()
	require.Equal(t, "1200", seen[2].URL.Query().Get("wait_ms"))
	mu.Unlock()

	response, body = programToolCallLocalRequest(t, local, http.MethodGet,
		"/tool-calls/"+processID+"/limited", capability, "")
	require.Equal(t, http.StatusTooManyRequests, response.StatusCode)
	require.Equal(t, "7", response.Header.Get("Retry-After"))
	require.JSONEq(t, `{"error":"slow down","code":"rate_limited"}`, string(body))

	response, body = programToolCallLocalRequest(t, local, http.MethodGet,
		"/tool-calls/"+processID+"/missing", capability, "")
	require.Equal(t, http.StatusNotFound, response.StatusCode)
	require.JSONEq(t, `{"error":"tool call not found","code":"not_found"}`, string(body))

	before := upstreamCalls.Load()
	for _, tc := range []struct {
		name, method, path, capability, body string
		status                               int
	}{
		{"wrong capability", http.MethodPost, "/tool-calls/" + processID, "wrong", `{}`, http.StatusForbidden},
		{"missing capability", http.MethodPost, "/tool-calls/" + processID, "", `{}`, http.StatusForbidden},
		{"other process capability", http.MethodPost, "/tool-calls/" + processID,
			programToolCallService.capability("supervisor-secret", "prc_other"), `{}`, http.StatusForbidden},
		{"git capability", http.MethodPost, "/tool-calls/" + processID,
			gitCredentialService.capability("supervisor-secret", processID), `{}`, http.StatusForbidden},
		{"unknown process", http.MethodPost, "/tool-calls/" + mustProcessID(t),
			capability, `{}`, http.StatusForbidden},
		{"invalid process", http.MethodPost, "/tool-calls/not-a-process", capability, `{}`, http.StatusNotFound},
		{"nested tool call id", http.MethodGet, "/tool-calls/" + processID + "/a/b", capability, "",
			http.StatusNotFound},
		{"wrong method", http.MethodGet, "/tool-calls/" + processID, capability, "", http.StatusMethodNotAllowed},
		{"invalid wait", http.MethodGet, "/tool-calls/" + processID + "/tc1?wait_ms=-1", capability, "",
			http.StatusBadRequest},
		{"too large", http.MethodPost, "/tool-calls/" + processID, capability,
			`{"key":"k","name":"n","input":{"a":"` + strings.Repeat("x", daemonprotocol.ProgramToolCallMaxRequestBytes) + `"}}`,
			http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, body := programToolCallLocalRequest(t, local, tc.method, tc.path, tc.capability, tc.body)
			require.Equal(t, tc.status, response.StatusCode, string(body))
			var apiError programToolCallError
			require.NoError(t, json.Unmarshal(body, &apiError))
			require.NotEmpty(t, apiError.Code)
			require.NotContains(t, string(body), "machine-secret")
		})
	}
	require.Equal(t, before, upstreamCalls.Load(), "rejected requests must not reach the backend")
}

func mustProcessID(t *testing.T) string {
	t.Helper()
	id, err := publicid.Encode(publicid.KindProcess, uuid.New())
	require.NoError(t, err)
	return id
}

func TestProgramToolCallServerMapsNetworkFailuresTo503(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	upstreamURL := upstream.URL
	upstream.Close()
	client, processID := newProgramToolCallTestClient(t, upstreamURL)
	server := httptest.NewServer(client.newProgramToolCallHandler())
	defer server.Close()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		server.URL+"/tool-calls/"+processID+"/tc1", nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+programToolCallService.capability("supervisor-secret", processID))
	response, err := server.Client().Do(request)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	require.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
}

func TestProgramToolCallServerLimitsConcurrencyPerProcess(t *testing.T) {
	release := make(chan struct{})
	var inFlight atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inFlight.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		_, _ = io.WriteString(w, `{"tool_call_id":"tc1","state":"pending"}`)
	}))
	defer upstream.Close()
	client, processID := newProgramToolCallTestClient(t, upstream.URL)
	server := httptest.NewServer(client.newProgramToolCallHandler())
	defer server.Close()
	capability := programToolCallService.capability("supervisor-secret", processID)
	get := func() (int, string, error) {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
			server.URL+"/tool-calls/"+processID+"/tc1?wait_ms=25000", nil)
		if err != nil {
			return 0, "", err
		}
		request.Header.Set("Authorization", "Bearer "+capability)
		response, err := server.Client().Do(request)
		if err != nil {
			return 0, "", err
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		return response.StatusCode, response.Header.Get("Retry-After"), nil
	}
	var wg sync.WaitGroup
	statuses := make(chan int, programToolCallProcessLimit)
	for range programToolCallProcessLimit {
		wg.Go(func() {
			status, _, err := get()
			assert.NoError(t, err)
			statuses <- status
		})
	}
	require.Eventually(t, func() bool { return inFlight.Load() == programToolCallProcessLimit },
		5*time.Second, 10*time.Millisecond)
	status, retryAfter, err := get()
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "1", retryAfter)
	close(release)
	wg.Wait()
	close(statuses)
	for status := range statuses {
		require.Equal(t, http.StatusOK, status)
	}
	status, _, err = get()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "slots must be released")
}

func TestProgramToolCallPrepare(t *testing.T) {
	shell := processcmd.ForShell("echo hi", processcmd.ShellSH, processcmd.IOModePipe)
	transfer := processcmd.ForFileTransfer(processcmd.FileTransfer{
		Direction: processcmd.FileTransferUpload, LocalPath: "report.txt",
	})
	for _, tc := range []struct {
		name        string
		spec        processcmd.ExecutionSpec
		offered     bool
		unavailable bool
		preparation string
		want        bool
	}{
		{name: "enabled shell", spec: shell, offered: true, want: true},
		{name: "not offered", spec: shell},
		{name: "file transfer", spec: transfer, offered: true},
		{name: "server unavailable", spec: shell, offered: true, unavailable: true},
		{name: "preparation failed", spec: shell, offered: true, preparation: "bad cwd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := New(Config{
				OmnaraHome: t.TempDir(), ExpectedInstallationID: "inst_test", ExpectedMachineID: "mch_test",
			}, nil, nil)
			client.programToolCallsUnavailable = tc.unavailable
			processID := mustProcessID(t)
			assignment := ProcessAssignment{
				ID: processID, ProgrammaticToolCalls: tc.offered, PreparationError: tc.preparation,
				Process:           Process{ExecutionSpec: tc.spec},
				ProgramToolHelper: &processServiceHelper{Capability: "stale"},
			}
			client.prepareProgramToolCalls(&assignment, "supervisor-secret")
			require.Equal(t, tc.preparation, assignment.PreparationError, "the feature must never fail preparation")
			if !tc.want {
				require.Nil(t, assignment.ProgramToolHelper)
				require.False(t, assignment.ProgrammaticToolCalls)
				return
			}
			require.NotNil(t, assignment.ProgramToolHelper)
			require.True(t, filepath.IsAbs(assignment.ProgramToolHelper.Executable))
			require.Equal(t, "tool-calls.sock", filepath.Base(assignment.ProgramToolHelper.Endpoint))
			require.Equal(t, programToolCallService.capability("supervisor-secret", processID),
				assignment.ProgramToolHelper.Capability)
			require.NotEqual(t, gitCredentialService.capability("supervisor-secret", processID),
				assignment.ProgramToolHelper.Capability)
		})
	}
}

func TestProgramToolCallEnvironmentReplacesUserValues(t *testing.T) {
	helper := &processServiceHelper{Executable: "/opt/omnara/omnarad", Endpoint: "/run/tool-calls.sock", Capability: "cap"}
	base := workloadProcessEnv(
		[]string{"PATH=/bin", "OMNARA_TOOL_ENDPOINT=/ambient.sock", "OMNARA_HOME=/home/omnara"},
		"/bin",
		map[string]string{"OMNARA_TOOL_CAPABILITY": "forged", "OMNARA_BIN": "/tmp/evil", "KEEP": "1"},
	)
	for _, entry := range base {
		require.False(t, strings.HasPrefix(entry, "OMNARA_TOOL_"), entry)
		require.False(t, strings.HasPrefix(entry, "OMNARA_BIN="), entry)
	}
	env := programToolCallEnvironment(
		append(base, "omnara_tool_endpoint=/late.sock", "OMNARA_PROCESS_ID=prc_forged"), "prc_real", helper)
	values := map[string][]string{}
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		values[strings.ToUpper(name)] = append(values[strings.ToUpper(name)], value)
	}
	require.Equal(t, []string{"prc_real"}, values["OMNARA_PROCESS_ID"])
	require.Equal(t, []string{"/run/tool-calls.sock"}, values["OMNARA_TOOL_ENDPOINT"])
	require.Equal(t, []string{"cap"}, values["OMNARA_TOOL_CAPABILITY"])
	require.Equal(t, []string{"/opt/omnara/omnarad"}, values["OMNARA_BIN"])
	require.Equal(t, []string{"1"}, values["KEEP"])
	require.Equal(t, []string{"/home/omnara"}, values["OMNARA_HOME"])
}

func TestProgramToolCallOfferReachesAssignment(t *testing.T) {
	for _, offered := range []bool{false, true} {
		var offer daemonprotocol.ProcessOffer
		payload := `{"execution_spec":{"kind":"shell","shell":{"command":"echo","shell_selector":"sh",` +
			`"io_mode":"pipe"}},"process_id":"prc_offer_tools"`
		if offered {
			payload += `,"programmatic_tool_calls":true`
		}
		require.NoError(t, json.NewDecoder(bytes.NewReader([]byte(payload+"}"))).Decode(&offer))
		assignments := make(chan ProcessAssignment, 1)
		release := make(chan struct{})
		client := New(Config{}, nil, nil)
		client.runnerLauncher = blockingPrepareLauncher{
			started: make(chan struct{}), release: release, assignments: assignments,
		}
		transport := newDaemonSocketTransport(&client, DaemonRuntime{}, localStartupState{})
		transport.offerProcess(t.Context(), offer)
		select {
		case assignment := <-assignments:
			require.Equal(t, offered, assignment.ProgrammaticToolCalls)
		case <-time.After(time.Second):
			t.Fatal("offer did not reach process preparation")
		}
		close(release)
		transport.stopAndWait(func() {})
	}
}

func TestProgramToolCallEnvironmentReachesPreparedCommand(t *testing.T) {
	bootstrap, _, _ := acceptedRunnerServerState(t)
	helper := &processServiceHelper{Executable: "/opt/omnara/omnarad", Endpoint: "/run/tool-calls.sock", Capability: "cap"}
	for _, withHelper := range []bool{false, true} {
		assignment := ProcessAssignment{
			ID: bootstrap.ProcessID,
			Process: Process{
				ExecutionSpec: processcmd.ForShell("true", processcmd.ShellSH, processcmd.IOModePipe),
				Cwd:           t.TempDir(),
			},
			Env: map[string]string{"OMNARA_TOOL_ENDPOINT": "/forged.sock", "OMNARA_PROCESS_ID": "prc_forged"},
		}
		if withHelper {
			assignment.ProgrammaticToolCalls, assignment.ProgramToolHelper = true, helper
		}
		runner, err := prepareLocalRunner(t.Context(), bootstrap, assignment)
		require.NoError(t, err)
		require.NoError(t, runner.startErr)
		t.Cleanup(func() { _ = runner.output.Close() })
		env := strings.Join(runner.cmd.Env, "\n")
		require.NotContains(t, env, "forged")
		if withHelper {
			require.Contains(t, runner.cmd.Env, "OMNARA_TOOL_ENDPOINT=/run/tool-calls.sock")
			require.Contains(t, runner.cmd.Env, "OMNARA_PROCESS_ID="+bootstrap.ProcessID)
			require.Contains(t, runner.cmd.Env, "OMNARA_TOOL_CAPABILITY=cap")
			require.Contains(t, runner.cmd.Env, "OMNARA_BIN=/opt/omnara/omnarad")
		} else {
			require.NotContains(t, env, "OMNARA_TOOL_")
		}
	}
}

func TestProgramToolCallCLIThroughDaemonServer(t *testing.T) {
	var polls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer machine-secret", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			var request daemonprotocol.ProgramToolCallRequest
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			assert.Equal(t, "storage_list", request.Name)
			assert.JSONEq(t, `{"prefix":"x"}`, string(request.Input))
			_, _ = io.WriteString(w, `{"tool_call_id":"tc_1","state":"awaiting_approval"}`)
			return
		}
		assert.Equal(t, "25000", r.URL.Query().Get("wait_ms"))
		polls.Add(1)
		_, _ = io.WriteString(w, `{"tool_call_id":"tc_1","state":"completed","outcome":"succeeded",`+
			`"content":[{"type":"structured_data","value":{"files":["x/a"]}}]}`)
	}))
	defer upstream.Close()
	client, processID := newProgramToolCallTestClient(t, upstream.URL)
	stop, err := client.startProgramToolCallServer(t.Context())
	require.NoError(t, err)
	defer stop()
	assignment := ProcessAssignment{
		ID: processID, ProgrammaticToolCalls: true,
		Process: Process{ExecutionSpec: processcmd.ForShell("true", processcmd.ShellSH, processcmd.IOModePipe)},
	}
	client.prepareProgramToolCalls(&assignment, "supervisor-secret")
	require.NotNil(t, assignment.ProgramToolHelper)
	for _, entry := range programToolCallEnvironment(nil, processID, assignment.ProgramToolHelper) {
		name, value, _ := strings.Cut(entry, "=")
		t.Setenv(name, value)
	}
	var stdout, stderr bytes.Buffer
	code := RunProgramToolCall(t.Context(), ProgramToolCallOptions{
		Tool: "storage_list", Input: `{"prefix":"x"}`, Timeout: time.Minute,
	}, nil, &stdout, &stderr)
	require.Equal(t, ProgramToolCallExitSucceeded, code, stderr.String())
	require.Equal(t, `{"files":["x/a"]}`+"\n", stdout.String())
	require.Contains(t, stderr.String(), "waiting for approval of storage_list")
	require.Equal(t, int32(1), polls.Load())

	t.Setenv(programToolCapabilityEnv, "forged")
	stdout.Reset()
	stderr.Reset()
	code = RunProgramToolCall(t.Context(), ProgramToolCallOptions{Tool: "storage_list", Timeout: time.Minute},
		nil, &stdout, &stderr)
	require.Equal(t, ProgramToolCallExitUnavailable, code)
	require.Contains(t, stderr.String(), "tool call request denied")
}
