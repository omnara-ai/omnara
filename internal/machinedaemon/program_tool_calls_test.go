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
	"strconv"
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

// answerProgramToolCallOffers connects a websocket transport with no backend
// behind it and answers each offer the daemon sends with answer's reports.
func answerProgramToolCallOffers(
	t *testing.T, client *Client, answer func(daemonprotocol.ToolCallOffer) []daemonprotocol.ToolCallReport,
) *daemonSocketTransport {
	t.Helper()
	transport := newDaemonSocketTransport(client, DaemonRuntime{}, localStartupState{})
	client.setTransport(transport)
	t.Cleanup(func() { client.clearTransport(transport) })
	go func() {
		for {
			select {
			case <-t.Context().Done():
				return
			case message := <-transport.send:
				if !assert.Equal(t, daemonprotocol.MessageToolCallOffer, message.Type) {
					return
				}
				for _, report := range answer(*message.ToolCallOffer) {
					report.ProcessID, report.Key = message.ToolCallOffer.ProcessID, message.ToolCallOffer.Key
					assert.NoError(t, transport.handleMessage(t.Context(), daemonprotocol.Message{
						Type: daemonprotocol.MessageToolCallReport, ToolCallReport: &report}))
				}
			}
		}
	}()
	return transport
}

func completedProgramToolCall(value string) daemonprotocol.ToolCallReport {
	return daemonprotocol.ToolCallReport{ProgramToolCallResult: daemonprotocol.ProgramToolCallResult{
		ToolCallID: "tc_1", State: daemonprotocol.ProgramToolCallStateCompleted,
		Outcome: daemonprotocol.ProgramToolCallOutcomeSucceeded,
		Content: []daemonprotocol.ProgramToolCallContent{
			{Type: daemonprotocol.ProgramToolCallContentStructuredData, Value: json.RawMessage(value)}},
	}}
}

func TestProgramToolCallServerOffersToBackend(t *testing.T) {
	client, processID := newProgramToolCallTestClient(t, "http://unused.invalid")
	stop, err := client.startProgramToolCallServer(t.Context())
	require.NoError(t, err)
	defer stop()
	var offers atomic.Int32
	answerProgramToolCallOffers(t, client, func(offer daemonprotocol.ToolCallOffer) []daemonprotocol.ToolCallReport {
		offers.Add(1)
		assert.Equal(t, processID, offer.ProcessID)
		assert.Equal(t, "storage_list", offer.Name)
		assert.JSONEq(t, `{"prefix":"a"}`, string(offer.Input))
		if offer.Key == "rejected" {
			return []daemonprotocol.ToolCallReport{{AckStatus: daemonprotocol.AckStatusPermanentReject,
				ErrorCode: daemonprotocol.ErrorCodeToolCallUnavailable, Error: "not callable"}}
		}
		return []daemonprotocol.ToolCallReport{
			{ProgramToolCallResult: daemonprotocol.ProgramToolCallResult{
				ToolCallID: "tc_1", State: daemonprotocol.ProgramToolCallStateAwaitingApproval}},
			completedProgramToolCall(`{"ok":true}`),
		}
	})
	endpoint, err := client.processServiceEndpoint(programToolCallService)
	require.NoError(t, err)
	require.Equal(t, "tool-calls.sock", filepath.Base(endpoint))
	local := programToolCallSocketClient(endpoint)
	capability := programToolCallService.capability("supervisor-secret", processID)
	reports := func(body []byte) []daemonprotocol.ToolCallReport {
		var all []daemonprotocol.ToolCallReport
		decoder := json.NewDecoder(bytes.NewReader(body))
		for decoder.More() {
			var report daemonprotocol.ToolCallReport
			require.NoError(t, decoder.Decode(&report))
			all = append(all, report)
		}
		return all
	}

	// The response streams the call's reports until the completed one. Reports
	// only move a call forward, so a slow reader may see just the latest.
	response, body := programToolCallLocalRequest(t, local, http.MethodPost, "/tool-calls/"+processID, capability,
		`{"key":"k-1","name":"storage_list","input":{"prefix":"a"}}`)
	require.Equal(t, http.StatusOK, response.StatusCode, string(body))
	require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	streamed := reports(body)
	require.NotEmpty(t, streamed)
	last := streamed[len(streamed)-1]
	require.Equal(t, "k-1", last.Key)
	require.Equal(t, daemonprotocol.ProgramToolCallStateCompleted, last.State)
	require.JSONEq(t, `{"ok":true}`, string(last.Content[0].Value))

	// A rejected offer ends the stream with the backend's reason.
	_, body = programToolCallLocalRequest(t, local, http.MethodPost, "/tool-calls/"+processID, capability,
		`{"key":"rejected","name":"storage_list","input":{"prefix":"a"}}`)
	streamed = reports(body)
	require.Len(t, streamed, 1)
	require.Equal(t, daemonprotocol.AckStatusPermanentReject, streamed[0].AckStatus)
	require.Equal(t, "not callable", streamed[0].Error)

	before := offers.Load()
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
		{"nested path", http.MethodPost, "/tool-calls/" + processID + "/tc_1", capability, `{}`, http.StatusNotFound},
		{"wrong method", http.MethodGet, "/tool-calls/" + processID, capability, "", http.StatusMethodNotAllowed},
		{"missing key", http.MethodPost, "/tool-calls/" + processID, capability, `{"name":"n","input":{}}`,
			http.StatusBadRequest},
		{"not JSON", http.MethodPost, "/tool-calls/" + processID, capability, `nope`, http.StatusBadRequest},
		{"too large", http.MethodPost, "/tool-calls/" + processID, capability,
			`{"key":"k","name":"n","input":{"a":"` + strings.Repeat("x", daemonprotocol.ProgramToolCallMaxInputBytes) + `"}}`,
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
	require.Equal(t, before, offers.Load(), "rejected requests must not reach the backend")
}

func mustProcessID(t *testing.T) string {
	t.Helper()
	id, err := publicid.Encode(publicid.KindProcess, uuid.New())
	require.NoError(t, err)
	return id
}

// A call made while the websocket is down, or cut off by a reconnect, is
// offered on the next connection.
func TestProgramToolCallServerOffersAgainOnReconnect(t *testing.T) {
	client, processID := newProgramToolCallTestClient(t, "http://unused.invalid")
	handler := client.newProgramToolCallHandler()
	client.programToolCalls = handler
	server := httptest.NewServer(handler)
	defer server.Close()
	done := make(chan []byte, 1)
	go func() {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
			server.URL+"/tool-calls/"+processID, strings.NewReader(`{"key":"k-1","name":"storage_list","input":{}}`))
		if !assert.NoError(t, err) {
			return
		}
		request.Header.Set("Authorization", "Bearer "+programToolCallService.capability("supervisor-secret", processID))
		response, err := server.Client().Do(request)
		if !assert.NoError(t, err) {
			return
		}
		defer func() { _ = response.Body.Close() }()
		body, _ := io.ReadAll(response.Body)
		done <- body
	}()
	require.Eventually(t, func() bool { return len(handler.offers()) == 1 }, 5*time.Second, 10*time.Millisecond)
	want := daemonprotocol.ToolCallOffer{ProcessID: processID, ProgramToolCallRequest: daemonprotocol.ProgramToolCallRequest{
		Key: "k-1", Name: "storage_list", Input: json.RawMessage(`{}`)}}
	require.Equal(t, []daemonprotocol.ToolCallOffer{want}, handler.offers())
	select {
	case body := <-done:
		t.Fatalf("call returned without a report: %s", body)
	case <-time.After(100 * time.Millisecond):
	}
	// Reports for other calls are ignored.
	handler.report(daemonprotocol.ToolCallReport{ProcessID: processID, Key: "other"})
	report := completedProgramToolCall(`1`)
	report.ProcessID, report.Key = processID, "k-1"
	handler.report(report)
	select {
	case body := <-done:
		require.Contains(t, string(body), `"state":"completed"`)
	case <-time.After(5 * time.Second):
		t.Fatal("call did not return after its report")
	}
	require.Empty(t, handler.offers(), "a finished call is not offered again")
}

func TestProgramToolCallServerLimitsConcurrencyPerProcess(t *testing.T) {
	client, processID := newProgramToolCallTestClient(t, "http://unused.invalid")
	handler := client.newProgramToolCallHandler()
	server := httptest.NewServer(handler)
	defer server.Close()
	capability := programToolCallService.capability("supervisor-secret", processID)
	post := func(key string) (int, string, error) {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
			server.URL+"/tool-calls/"+processID, strings.NewReader(`{"key":"`+key+`","name":"n","input":{}}`))
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
	for i := range programToolCallProcessLimit {
		wg.Go(func() {
			status, _, err := post("k-" + strconv.Itoa(i))
			assert.NoError(t, err)
			statuses <- status
		})
	}
	require.Eventually(t, func() bool { return len(handler.offers()) == programToolCallProcessLimit },
		5*time.Second, 10*time.Millisecond)
	status, retryAfter, err := post("one-more")
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "1", retryAfter)
	for _, offer := range handler.offers() {
		report := completedProgramToolCall(`1`)
		report.ProcessID, report.Key = offer.ProcessID, offer.Key
		handler.report(report)
	}
	wg.Wait()
	close(statuses)
	for status := range statuses {
		require.Equal(t, http.StatusOK, status)
	}
	require.Eventually(t, func() bool { return handler.acquire(processID) }, 5*time.Second, 10*time.Millisecond,
		"slots must be released")
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
	client, processID := newProgramToolCallTestClient(t, "http://unused.invalid")
	stop, err := client.startProgramToolCallServer(t.Context())
	require.NoError(t, err)
	defer stop()
	approved := make(chan struct{})
	answerProgramToolCallOffers(t, client, func(offer daemonprotocol.ToolCallOffer) []daemonprotocol.ToolCallReport {
		assert.Equal(t, "storage_list", offer.Name)
		assert.JSONEq(t, `{"prefix":"x"}`, string(offer.Input))
		go func() {
			<-approved
			report := completedProgramToolCall(`{"files":["x/a"]}`)
			report.ProcessID, report.Key = offer.ProcessID, offer.Key
			client.programToolCalls.report(report)
		}()
		return []daemonprotocol.ToolCallReport{{ProgramToolCallResult: daemonprotocol.ProgramToolCallResult{
			ToolCallID: "tc_1", State: daemonprotocol.ProgramToolCallStateAwaitingApproval}}}
	})
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
	stderr := &approvalNotice{waiting: approved}
	var stdout bytes.Buffer
	code := RunProgramToolCall(t.Context(), ProgramToolCallOptions{
		Tool: "storage_list", Input: `{"prefix":"x"}`, Timeout: time.Minute,
	}, nil, &stdout, stderr)
	require.Equal(t, ProgramToolCallExitSucceeded, code, stderr.String())
	require.Equal(t, `{"files":["x/a"]}`+"\n", stdout.String())
	require.Contains(t, stderr.String(), "waiting for approval of storage_list")

	t.Setenv(programToolCapabilityEnv, "forged")
	stdout.Reset()
	var denied bytes.Buffer
	code = RunProgramToolCall(t.Context(), ProgramToolCallOptions{Tool: "storage_list", Timeout: time.Minute},
		nil, &stdout, &denied)
	require.Equal(t, ProgramToolCallExitUnavailable, code)
	require.Contains(t, denied.String(), "tool call request denied")
}

// approvalNotice is a stderr that approves the call once `omnarad call` says
// it is waiting.
type approvalNotice struct {
	bytes.Buffer
	once    sync.Once
	waiting chan struct{}
}

func (n *approvalNotice) Write(p []byte) (int, error) {
	n.once.Do(func() { close(n.waiting) })
	return n.Buffer.Write(p)
}
