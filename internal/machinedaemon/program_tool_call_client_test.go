package machinedaemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localipc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeToolCallDaemon struct {
	t         *testing.T
	processID string

	mu       sync.Mutex
	requests []fakeToolCallRequest
	respond  func(n int, request fakeToolCallRequest) (int, http.Header, string)
}

type fakeToolCallRequest struct {
	Body daemonprotocol.ProgramToolCallRequest
}

func startFakeToolCallDaemon(
	t *testing.T,
	respond func(n int, request fakeToolCallRequest) (int, http.Header, string),
) *fakeToolCallDaemon {
	t.Helper()
	daemon := &fakeToolCallDaemon{t: t, processID: mustProcessID(t), respond: respond}
	endpoint := filepath.Join(t.TempDir(), "tool-calls.sock")
	listener, err := localipc.Listen(t.Context(), endpoint)
	require.NoError(t, err)
	server := &http.Server{Handler: http.HandlerFunc(daemon.serve), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = localipc.Cleanup(endpoint)
	})
	t.Setenv(programToolProcessIDEnv, daemon.processID)
	t.Setenv(programToolEndpointEnv, endpoint)
	t.Setenv(programToolCapabilityEnv, "cli-capability")
	return daemon
}

func (d *fakeToolCallDaemon) serve(w http.ResponseWriter, r *http.Request) {
	assert.Equal(d.t, "Bearer cli-capability", r.Header.Get("Authorization"))
	var request fakeToolCallRequest
	assert.Equal(d.t, http.MethodPost, r.Method)
	assert.Equal(d.t, "/tool-calls/"+d.processID, r.URL.Path)
	assert.NoError(d.t, json.NewDecoder(r.Body).Decode(&request.Body))
	d.mu.Lock()
	n := len(d.requests)
	d.requests = append(d.requests, request)
	d.mu.Unlock()
	status, header, body := d.respond(n, request)
	for name, values := range header {
		w.Header()[name] = values
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func (d *fakeToolCallDaemon) recorded() []fakeToolCallRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]fakeToolCallRequest(nil), d.requests...)
}

func runToolCallCLI(t *testing.T, options ProgramToolCallOptions, stdin string) (int, string, string) {
	t.Helper()
	if options.Timeout == 0 {
		options.Timeout = 10 * time.Minute
	}
	var stdout, stderr bytes.Buffer
	code := RunProgramToolCall(t.Context(), options, strings.NewReader(stdin), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func reply(status int, body string) func(int, fakeToolCallRequest) (int, http.Header, string) {
	return func(int, fakeToolCallRequest) (int, http.Header, string) { return status, nil, body }
}

func TestProgramToolCallCLIPrintsStructuredResult(t *testing.T) {
	daemon := startFakeToolCallDaemon(t, reply(http.StatusOK,
		`{"state":"completed","outcome":"succeeded","content":[{"type":"text","text":"ignored"},`+
			`{"type":"structured_data","value":{ "files": [ "a", "b" ] }},{"type":"structured_data","value":2}]}`))
	code, stdout, stderr := runToolCallCLI(t, ProgramToolCallOptions{
		Tool: "storage_list", Input: `{ "prefix": "docs/" }`,
	}, "")
	require.Equal(t, ProgramToolCallExitSucceeded, code, stderr)
	require.Equal(t, `{"files":["a","b"]}`+"\n", stdout)
	require.Empty(t, stderr)
	requests := daemon.recorded()
	require.Len(t, requests, 1)
	require.Equal(t, "storage_list", requests[0].Body.Name)
	require.JSONEq(t, `{"prefix":"docs/"}`, string(requests[0].Body.Input))
	require.NotEmpty(t, requests[0].Body.Key)
}

func TestProgramToolCallCLIWaitsForApprovalThenPrintsTextAsJSONString(t *testing.T) {
	daemon := startFakeToolCallDaemon(t, reply(http.StatusOK,
		`{"tool_call_id":"tc_1","state":"awaiting_approval"}`+"\n"+
			`{"tool_call_id":"tc_1","state":"awaiting_approval"}`+"\n"+
			`{"tool_call_id":"tc_1","state":"completed","outcome":"succeeded",`+
			`"content":[{"type":"text","text":"hello"},{"type":"media_ref","artifact_id":"art_1"},`+
			`{"type":"text","text":"world"}]}`+"\n"))
	code, stdout, stderr := runToolCallCLI(t, ProgramToolCallOptions{Tool: "send_email"}, "")
	require.Equal(t, ProgramToolCallExitSucceeded, code, stderr)
	require.Equal(t, `"hello\nworld"`+"\n", stdout)
	require.Equal(t, "omnarad: waiting for approval of send_email in Omnara...\n", stderr,
		"the approval notice is printed once")
	requests := daemon.recorded()
	require.Len(t, requests, 1, "one request waits out the whole call")
	require.JSONEq(t, `{}`, string(requests[0].Body.Input), "input defaults to an empty object")
}

func TestProgramToolCallCLIPrintsTextOutput(t *testing.T) {
	startFakeToolCallDaemon(t, reply(http.StatusOK, `{"state":"completed","outcome":"succeeded",`+
		`"content":[{"type":"text","text":"hello"},{"type":"text","text":"world\n"}]}`))
	code, stdout, _ := runToolCallCLI(t, ProgramToolCallOptions{Tool: "fetch", Output: ProgramToolCallOutputText}, "")
	require.Equal(t, ProgramToolCallExitSucceeded, code)
	require.Equal(t, "hello\nworld\n", stdout)

	startFakeToolCallDaemon(t, reply(http.StatusOK,
		`{"state":"completed","outcome":"succeeded","content":[{"type":"structured_data","value":{"a":1}}]}`))
	code, stdout, _ = runToolCallCLI(t, ProgramToolCallOptions{Tool: "list", Output: ProgramToolCallOutputText}, "")
	require.Equal(t, ProgramToolCallExitSucceeded, code)
	require.Equal(t, `{"a":1}`+"\n", stdout, "text output falls back to the result without text")

	startFakeToolCallDaemon(t, reply(http.StatusOK, `{"state":"completed","outcome":"succeeded","content":[`+
		`{"type":"text","text":"The file changed since you last read it."},`+
		`{"type":"structured_data","value":{"content":"page"}}]}`))
	code, stdout, _ = runToolCallCLI(t,
		ProgramToolCallOptions{Tool: "storage_read", Output: ProgramToolCallOutputText}, "")
	require.Equal(t, ProgramToolCallExitSucceeded, code)
	require.Equal(t, `{"content":"page"}`+"\n", stdout)
}

func TestProgramToolCallCLIRejectsUnknownOutput(t *testing.T) {
	daemon := startFakeToolCallDaemon(t, reply(http.StatusOK, `{"state":"completed","outcome":"succeeded"}`))
	code, stdout, stderr := runToolCallCLI(t, ProgramToolCallOptions{Tool: "list", Output: "json"}, "")
	require.Equal(t, ProgramToolCallExitUnavailable, code)
	require.Empty(t, stdout)
	require.Equal(t, "omnarad call: --output must be result, envelope or text\n", stderr)
	require.Empty(t, daemon.recorded())
}

func TestProgramToolCallCLIPrintsContentWithoutText(t *testing.T) {
	startFakeToolCallDaemon(t, reply(http.StatusOK, `{"state":"completed","outcome":"succeeded",`+
		`"content":[{"type":"media_ref","artifact_id":"art_1"}]}`))
	code, stdout, _ := runToolCallCLI(t, ProgramToolCallOptions{Tool: "render"}, "")
	require.Equal(t, ProgramToolCallExitSucceeded, code)
	require.JSONEq(t, `[{"type":"media_ref","artifact_id":"art_1"}]`, stdout)
	require.True(t, strings.HasSuffix(stdout, "\n"))
}

func TestProgramToolCallCLIReportsToolFailure(t *testing.T) {
	for _, outcome := range []string{"failed", "denied", "canceled"} {
		t.Run(outcome, func(t *testing.T) {
			envelope := `{"tool_call_id":"tc_1","state":"completed","outcome":"` + outcome +
				`","content":[{"type":"text","text":"nope"}]}`
			startFakeToolCallDaemon(t, reply(http.StatusOK, envelope))
			code, stdout, stderr := runToolCallCLI(t, ProgramToolCallOptions{Tool: "storage_read"}, "")
			require.Equal(t, ProgramToolCallExitToolFailed, code)
			require.Empty(t, stdout)
			require.JSONEq(t, `{"outcome":"`+outcome+`","content":[{"type":"text","text":"nope"}]}`, stderr)

			code, stdout, stderr = runToolCallCLI(t,
				ProgramToolCallOptions{Tool: "storage_read", Output: ProgramToolCallOutputEnvelope}, "")
			require.Equal(t, ProgramToolCallExitToolFailed, code, "the envelope keeps the outcome's exit code")
			require.JSONEq(t, envelope, stdout)
			require.Empty(t, stderr)
		})
	}
}

func TestProgramToolCallCLIPrintsEnvelope(t *testing.T) {
	envelope := `{"tool_call_id":"tc_1","state":"completed","outcome":"succeeded",` +
		`"content":[{"type":"structured_data","value":{"a":1}}]}`
	startFakeToolCallDaemon(t, reply(http.StatusOK, envelope))
	code, stdout, _ := runToolCallCLI(t,
		ProgramToolCallOptions{Tool: "storage_list", Output: ProgramToolCallOutputEnvelope}, "")
	require.Equal(t, ProgramToolCallExitSucceeded, code)
	require.JSONEq(t, envelope, stdout)
	require.True(t, strings.HasSuffix(stdout, "\n"))
}

func TestProgramToolCallCLIReadsInputFromStdin(t *testing.T) {
	daemon := startFakeToolCallDaemon(t, reply(http.StatusOK, `{"state":"completed","outcome":"succeeded"}`))
	code, stdout, stderr := runToolCallCLI(t, ProgramToolCallOptions{Tool: "storage_write", Input: "-"},
		"{\n  \"path\": \"a.txt\",\n  \"content\": \"x\"\n}\n")
	require.Equal(t, ProgramToolCallExitSucceeded, code, stderr)
	require.Equal(t, "null\n", stdout, "a result with no content is still one JSON value")
	require.Equal(t, `{"path":"a.txt","content":"x"}`, string(daemon.recorded()[0].Body.Input))
}

func TestProgramToolCallCLIRejectsInvalidInput(t *testing.T) {
	daemon := startFakeToolCallDaemon(t, reply(http.StatusOK, `{"state":"completed","outcome":"succeeded"}`))
	for _, input := range []string{"[1]", "null", "42", `{"a":1} {"b":2}`, "{", `"text"`} {
		code, _, stderr := runToolCallCLI(t, ProgramToolCallOptions{Tool: "storage_list", Input: input}, "")
		require.Equal(t, ProgramToolCallExitUnavailable, code, input)
		require.Contains(t, stderr, "JSON object", input)
	}
	code, _, _ := runToolCallCLI(t, ProgramToolCallOptions{Tool: "storage_list", Input: "-"}, "not json")
	require.Equal(t, ProgramToolCallExitUnavailable, code)
	require.Empty(t, daemon.recorded())
}

func TestProgramToolCallCLIRequiresProcessEnvironment(t *testing.T) {
	for _, missing := range []string{programToolProcessIDEnv, programToolEndpointEnv, programToolCapabilityEnv} {
		t.Run(missing, func(t *testing.T) {
			startFakeToolCallDaemon(t, reply(http.StatusOK, `{"state":"completed","outcome":"succeeded"}`))
			t.Setenv(missing, "")
			code, stdout, stderr := runToolCallCLI(t, ProgramToolCallOptions{Tool: "storage_list"}, "")
			require.Equal(t, ProgramToolCallExitUnavailable, code)
			require.Empty(t, stdout)
			require.Equal(t, programToolCallUnavailableMessage+"\n", stderr)
		})
	}
}

func TestProgramToolCallCLIExitCodesForDaemonErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   int
	}{
		{http.StatusForbidden, ProgramToolCallExitUnavailable},
		{http.StatusNotFound, ProgramToolCallExitUnavailable},
		{http.StatusUnprocessableEntity, ProgramToolCallExitUnavailable},
		{http.StatusConflict, ProgramToolCallExitTransport},
		{http.StatusInternalServerError, ProgramToolCallExitTransport},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			daemon := startFakeToolCallDaemon(t, reply(tc.status,
				`{"error":"tool is not callable from commands","code":"forbidden"}`))
			code, stdout, stderr := runToolCallCLI(t, ProgramToolCallOptions{Tool: "spawn_agent"}, "")
			require.Equal(t, tc.want, code)
			require.Empty(t, stdout)
			require.Contains(t, stderr, "spawn_agent: tool is not callable from commands (forbidden)")
			require.Len(t, daemon.recorded(), 1, "non-retryable errors are not retried")
		})
	}
}

func TestProgramToolCallCLIExitCodesForRejectedOffers(t *testing.T) {
	daemon := startFakeToolCallDaemon(t, reply(http.StatusOK,
		`{"ack_status":"permanent_reject","error_code":"tool_call_unavailable","error":"tool is not callable"}`))
	code, stdout, stderr := runToolCallCLI(t, ProgramToolCallOptions{Tool: "spawn_agent"}, "")
	require.Equal(t, ProgramToolCallExitUnavailable, code)
	require.Empty(t, stdout)
	require.Contains(t, stderr, "spawn_agent: tool is not callable (tool_call_unavailable)")
	require.Len(t, daemon.recorded(), 1, "a rejected call is not retried")
}

func TestProgramToolCallCLIRetriesWithTheSameKey(t *testing.T) {
	daemon := startFakeToolCallDaemon(t, func(n int, _ fakeToolCallRequest) (int, http.Header, string) {
		switch n {
		case 0:
			return http.StatusServiceUnavailable, nil, `{"error":"busy","code":"service_unavailable"}`
		case 1:
			return http.StatusTooManyRequests, http.Header{"Retry-After": {"1"}},
				`{"error":"slow down","code":"rate_limited"}`
		case 2:
			return http.StatusOK, nil, `{"tool_call_id":"tc_1","state":"awaiting_approval"}` + "\n"
		case 3:
			return http.StatusOK, nil,
				`{"ack_status":"transient_error","error_code":"storage_unavailable","error":"try again"}`
		default:
			return http.StatusOK, nil, `{"tool_call_id":"tc_1","state":"completed","outcome":"succeeded",` +
				`"content":[{"type":"text","text":"done"}]}`
		}
	})
	started := time.Now()
	code, stdout, stderr := runToolCallCLI(t, ProgramToolCallOptions{Tool: "storage_list"}, "")
	require.Equal(t, ProgramToolCallExitSucceeded, code, stderr)
	require.Equal(t, `"done"`+"\n", stdout)
	require.GreaterOrEqual(t, time.Since(started), time.Second, "Retry-After must be honored")
	requests := daemon.recorded()
	require.Len(t, requests, 5)
	key := requests[0].Body.Key
	require.NotEmpty(t, key)
	for _, request := range requests {
		require.Equal(t, key, request.Body.Key)
	}
}

func TestProgramToolCallCLIUsesANewKeyPerInvocation(t *testing.T) {
	daemon := startFakeToolCallDaemon(t, reply(http.StatusOK, `{"state":"completed","outcome":"succeeded"}`))
	runToolCallCLI(t, ProgramToolCallOptions{Tool: "storage_list"}, "")
	runToolCallCLI(t, ProgramToolCallOptions{Tool: "storage_list"}, "")
	requests := daemon.recorded()
	require.Len(t, requests, 2)
	require.NotEqual(t, requests[0].Body.Key, requests[1].Body.Key)
}

func TestProgramToolCallCLITimesOut(t *testing.T) {
	startFakeToolCallDaemon(t, reply(http.StatusOK, `{"tool_call_id":"tc_slow","state":"awaiting_approval"}`))
	code, stdout, stderr := runToolCallCLI(t, ProgramToolCallOptions{
		Tool: "storage_list", Timeout: 1500 * time.Millisecond,
	}, "")
	require.Equal(t, ProgramToolCallExitTransport, code)
	require.Empty(t, stdout)
	require.Contains(t, stderr, "timed out; tool call tc_slow may still complete")
}

func TestProgramToolCallCLIReportsUnreachableDaemon(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the daemon dial retry window")
	}
	t.Setenv(programToolProcessIDEnv, mustProcessID(t))
	t.Setenv(programToolEndpointEnv, filepath.Join(t.TempDir(), "missing.sock"))
	t.Setenv(programToolCapabilityEnv, "cli-capability")
	code, _, stderr := runToolCallCLI(t, ProgramToolCallOptions{Tool: "storage_list", Timeout: 2 * time.Second}, "")
	require.Equal(t, ProgramToolCallExitTransport, code)
	require.Contains(t, stderr, "timed out")
}

func TestReadProgramToolCallInputLimitsSize(t *testing.T) {
	_, err := readProgramToolCallInput("-", strings.NewReader(`{"a":"`+
		strings.Repeat("x", daemonprotocol.ProgramToolCallMaxInputBytes)+`"}`))
	require.ErrorContains(t, err, "too large")
	_, err = readProgramToolCallInput("-", errReader{})
	require.ErrorContains(t, err, "stdin")
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("broken stdin") }
