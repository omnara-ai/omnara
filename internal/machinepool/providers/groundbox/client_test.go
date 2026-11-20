package groundbox

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func newTestRESTClient(t *testing.T, handler http.HandlerFunc) *restClient {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gbx_test" {
			t.Errorf("authorization header = %q", r.Header.Get("Authorization"))
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			t.Errorf("request path = %q, want the /v1 base", r.URL.Path)
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return newRESTClient(server.URL+"/v1/", "gbx_test", server.Client(), server.Client())
}

func TestGroundboxRESTClientCreatesSandboxesWithAnIdempotencyKey(t *testing.T) {
	var received map[string]json.RawMessage
	client := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/sandboxes" {
			t.Errorf("create request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Idempotency-Key") != "omnara-mch-abc" {
			t.Errorf("idempotency key = %q", r.Header.Get("Idempotency-Key"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode create request: %v", err)
		}
		_, _ = w.Write([]byte(`{"id":"sbx_abcdefgh","status":"running","cpus":2,"memory_gb":4,"disk_gb":20,` +
			`"booted":{"cpus":2,"memory_mb":3938},"metadata":{"omnara-machine":"mch_1"},"error":null}`))
	})
	created, err := client.CreateSandbox(context.Background(), "omnara-mch-abc", createSandboxRequest{
		CPUs:           2,
		MemoryGB:       4,
		TimeoutMinutes: sandboxTimeoutMinutes,
		Metadata:       map[string]string{"omnara-machine": "mch_1"},
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if created.ID != "sbx_abcdefgh" || created.Status != sandboxStatusRunning || created.CPUs != 2 ||
		created.MemoryGB != 4 || created.Metadata["omnara-machine"] != "mch_1" {
		t.Fatalf("created sandbox = %+v", created)
	}
	if string(received["cpus"]) != "2" || string(received["memory_gb"]) != "4" ||
		string(received["timeout_minutes"]) != "43200" {
		t.Fatalf("create request = %v, want the size and the longest timeout", received)
	}
	for _, key := range []string{"template", "idle_timeout_seconds", "env"} {
		if _, ok := received[key]; ok {
			t.Fatalf("create request sent %s = %s", key, received[key])
		}
	}
}

func TestGroundboxRESTClientListsSandboxesByOwnership(t *testing.T) {
	client := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if r.URL.Path != "/v1/sandboxes" || query.Get("live") != "true" || query.Get("limit") != "2" ||
			query.Get("metadata[omnara-machine]") != "mch_1" || query.Get("metadata[omnara-installation]") != "ins_1" {
			t.Errorf("list request = %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"sbx_abcdefgh","status":"paused"}],"next_cursor":null}`))
	})
	listed, err := client.ListSandboxes(
		context.Background(),
		map[string]string{"omnara-machine": "mch_1", "omnara-installation": "ins_1"},
	)
	if err != nil || len(listed) != 1 || listed[0].Status != sandboxStatusPaused {
		t.Fatalf("list sandboxes = %+v, %v", listed, err)
	}
}

func TestGroundboxRESTClientLifecycleCalls(t *testing.T) {
	var routes []string
	client := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		routes = append(routes, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/sandboxes/sbx_abcdefgh":
			_, _ = w.Write([]byte(`{"id":"sbx_abcdefgh","status":"paused"}`))
		case "POST /v1/sandboxes/sbx_abcdefgh/resume", "POST /v1/sandboxes/sbx_abcdefgh/start":
			_, _ = w.Write([]byte(`{"id":"sbx_abcdefgh","status":"running"}`))
		case "DELETE /v1/sandboxes/sbx_abcdefgh":
			_, _ = w.Write([]byte(`{"id":"sbx_abcdefgh","status":"stopped","stop_reason":"deleted"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"No sandbox sbx_missing1"}}`))
		}
	})
	ctx := context.Background()
	current, found, err := client.GetSandbox(ctx, "sbx_abcdefgh")
	if err != nil || !found || current.Status != sandboxStatusPaused {
		t.Fatalf("get sandbox = %+v %t %v", current, found, err)
	}
	if _, found, err := client.GetSandbox(ctx, "sbx_missing1"); err != nil || found {
		t.Fatalf("get missing sandbox = %t %v, want absent", found, err)
	}
	for name, call := range map[string]func(context.Context, string) (sandbox, error){
		"resume": client.ResumeSandbox,
		"start":  client.StartSandbox,
	} {
		if current, err := call(ctx, "sbx_abcdefgh"); err != nil || current.Status != sandboxStatusRunning {
			t.Fatalf("%s = %+v, %v", name, current, err)
		}
	}
	if err := client.DeleteSandbox(ctx, "sbx_abcdefgh"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := client.DeleteSandbox(ctx, "sbx_missing1"); err != nil {
		t.Fatalf("delete of a missing sandbox: %v", err)
	}
	if len(routes) != 6 {
		t.Fatalf("routes = %v", routes)
	}
}

func TestGroundboxRESTClientCommands(t *testing.T) {
	var started map[string]json.RawMessage
	client := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/sandboxes/sbx_abcdefgh/commands":
			if r.URL.Query().Get("state") != "running" {
				t.Errorf("command list query = %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"ex_0123456789abcdef","cmd":"sleep 1","state":"running",` +
				`"background":true}],"running":1}`))
		case "POST /v1/sandboxes/sbx_abcdefgh/commands":
			if err := json.NewDecoder(r.Body).Decode(&started); err != nil {
				t.Errorf("decode command: %v", err)
			}
			_, _ = w.Write([]byte(`{"id":"ex_fedcba9876543210","cmd":"keep","state":"running"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	})
	running, err := client.ListRunningCommands(context.Background(), "sbx_abcdefgh")
	if err != nil || len(running) != 1 || running[0].Cmd != "sleep 1" || running[0].State != commandStateRunning {
		t.Fatalf("running commands = %+v, %v", running, err)
	}
	command, err := client.StartCommand(context.Background(), "sbx_abcdefgh", startCommandRequest{
		Cmd:      "keep",
		Env:      map[string]string{"OMNARA_MACHINE_TOKEN": "token"},
		TimeoutS: keeperTimeoutSeconds,
	})
	if err != nil || command.ID != "ex_fedcba9876543210" {
		t.Fatalf("start command = %+v, %v", command, err)
	}
	if string(started["cmd"]) != `"keep"` || string(started["env"]) != `{"OMNARA_MACHINE_TOKEN":"token"}` ||
		string(started["timeout_s"]) != "2592000" {
		t.Fatalf("start command request = %v", started)
	}
}

func TestGroundboxRESTClientReportsAPIErrors(t *testing.T) {
	client := newTestRESTClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limited","message":"Slow down","request_id":"req_1"}}`))
	})
	_, err := client.ListSandboxes(context.Background(), nil)
	var apiErr apiError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooManyRequests || apiErr.Code != "rate_limited" {
		t.Fatalf("list error = %v, want the decoded API error", err)
	}
	if !strings.Contains(err.Error(), "HTTP 429 (rate_limited): Slow down") || !transientAPIError(err) {
		t.Fatalf("error = %q", err)
	}
	if retryAfter, ok := providers.RetryAfter(err); !ok || retryAfter != 7*time.Second {
		t.Fatalf("retry after = %v %t", retryAfter, ok)
	}
}

func TestGroundboxErrorClassification(t *testing.T) {
	for status, transient := range map[int]bool{
		http.StatusBadRequest:          false,
		http.StatusUnauthorized:        false,
		http.StatusPaymentRequired:     false,
		http.StatusForbidden:           false,
		http.StatusNotFound:            false,
		http.StatusConflict:            true,
		http.StatusTooManyRequests:     true,
		http.StatusInternalServerError: true,
		http.StatusServiceUnavailable:  true,
	} {
		if got := transientAPIError(apiError{StatusCode: status}); got != transient {
			t.Errorf("HTTP %d transient = %t, want %t", status, got, transient)
		}
	}
	if !transientAPIError(errors.New("connection reset")) {
		t.Error("a transport error must be transient")
	}
}
