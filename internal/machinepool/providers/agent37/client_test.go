package agent37

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func newTestRESTClient(t *testing.T, handler http.HandlerFunc) *restClient {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/sandbox/") {
			if r.Header.Get("X-Agent37-Key") != "sk_live_test" || r.Header.Get("Authorization") != "" {
				t.Errorf("exec auth = key %q authorization %q, want the raw key alone",
					r.Header.Get("X-Agent37-Key"), r.Header.Get("Authorization"))
			}
		} else if r.Header.Get("Authorization") != "Bearer sk_live_test" || r.Header.Get("X-Agent37-Key") != "" {
			t.Errorf("authorization header = %q", r.Header.Get("Authorization"))
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return newRESTClient(server.URL, server.URL+"/sandbox", "sk_live_test", server.Client(), server.Client())
}

func TestAgent37RESTClientCreatesInstances(t *testing.T) {
	var received map[string]json.RawMessage
	client := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/instances" {
			t.Errorf("create request = %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode create request: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"a37inst001","status":"running","template":"acme-agent-box",` +
			`"resources":{"cpu":2,"memory":4,"disk":4},"metadata":{"omnara-machine":"mch_1","extra":{"a":1}}}`))
	})
	created, err := client.CreateInstance(context.Background(), createInstanceRequest{
		Template:  testTemplate,
		Name:      "omnara-mch-abc",
		Metadata:  map[string]string{"omnara-machine": "mch_1"},
		Resources: instanceResources{CPU: 2, MemoryGB: 4},
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	if created.ID != "a37inst001" || created.Status != instanceStatusRunning ||
		created.Resources != (instanceResources{CPU: 2, MemoryGB: 4}) ||
		created.Metadata["omnara-machine"] != "mch_1" {
		t.Fatalf("created instance = %+v", created)
	}
	if string(received["resources"]) != `{"cpu":2,"memory":4}` || string(received["template"]) != `"acme-agent-box"` {
		t.Fatalf("create request = %v, want the template and size", received)
	}
	for _, key := range []string{"env", "auto_sleep", "type", "region"} {
		if _, ok := received[key]; ok {
			t.Fatalf("create request sent %s = %s", key, received[key])
		}
	}
}

func TestAgent37RESTClientListsAndReadsInstances(t *testing.T) {
	client := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/instances":
			_, _ = w.Write([]byte(`{"data":[{"id":"a37inst001","status":"running"},{"id":"a37inst002","status":"stopped"}]}`))
		case "/v1/instances/a37inst001":
			_, _ = w.Write([]byte(`{"id":"a37inst001","status":"sleeping"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"Instance not found."}}`))
		}
	})
	instances, err := client.ListInstances(context.Background())
	if err != nil || len(instances) != 2 || instances[1].Status != instanceStatusStopped {
		t.Fatalf("list instances = %+v error %v", instances, err)
	}
	current, found, err := client.GetInstance(context.Background(), "a37inst001")
	if err != nil || !found || current.Status != instanceStatusSleeping {
		t.Fatalf("get instance = %+v found %v error %v", current, found, err)
	}
	if _, found, err := client.GetInstance(context.Background(), "a37missing"); err != nil || found {
		t.Fatalf("get missing instance = found %v error %v, want absent", found, err)
	}
}

func TestAgent37RESTClientRunsLifecycleActions(t *testing.T) {
	var calls []string
	client := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/templates":
			_, _ = w.Write([]byte(`{"data":[]}`))
		case "POST /v1/instances/a37inst001/start":
			_, _ = w.Write([]byte(`{"id":"a37inst001","status":"running"}`))
		case "POST /sandbox/v1/instances/a37inst001/exec":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["command"] != "echo hi" || len(body) != 1 {
				t.Errorf("exec body = %v error %v", body, err)
			}
			_, _ = w.Write([]byte(`{"exit_code":3,"stdout":"hi\n","stderr":"oops","truncated":false}`))
		case "DELETE /v1/instances/a37inst001":
			_, _ = w.Write([]byte(`{"id":"a37inst001","deleted":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	ctx := context.Background()
	if err := client.ListTemplates(ctx); err != nil {
		t.Fatalf("list templates: %v", err)
	}
	if err := client.StartInstance(ctx, "a37inst001"); err != nil {
		t.Fatalf("start instance: %v", err)
	}
	result, err := client.Exec(ctx, "a37inst001", "echo hi")
	if err != nil || result != (execResult{ExitCode: 3, Stdout: "hi\n", Stderr: "oops"}) {
		t.Fatalf("exec = %+v error %v", result, err)
	}
	if err := client.DeleteInstance(ctx, "a37inst001"); err != nil {
		t.Fatalf("delete instance: %v", err)
	}
	if err := client.DeleteInstance(ctx, "a37gone001"); err != nil {
		t.Fatalf("delete missing instance: %v, want a quiet no-op", err)
	}
	if len(calls) != 5 {
		t.Fatalf("calls = %v", calls)
	}
}

func TestAgent37RESTClientReportsAPIErrors(t *testing.T) {
	client := newTestRESTClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"no_capacity","message":"No host has room."}}`))
	})
	_, err := client.CreateInstance(context.Background(), createInstanceRequest{Template: testTemplate})
	var apiErr apiError
	if !errors.As(err, &apiErr) || apiErr.Code != "no_capacity" || apiStatusCode(err) != http.StatusServiceUnavailable ||
		err.Error() != "agent37 API returned HTTP 503 (no_capacity): No host has room." {
		t.Fatalf("create error = %v", err)
	}
	if status, ok := providers.HTTPStatus(err); !ok || status != http.StatusServiceUnavailable {
		t.Fatalf("http status = %d %v", status, ok)
	}
	if delay, ok := providers.RetryAfter(err); !ok || delay.Seconds() != 7 {
		t.Fatalf("retry after = %v %v", delay, ok)
	}

	flat := newTestRESTClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_api_key","message":"The API key is invalid."}`))
	})
	_, err = flat.Exec(context.Background(), "a37inst001", "true")
	if err == nil || err.Error() != "agent37 API returned HTTP 401 (invalid_api_key): The API key is invalid." {
		t.Fatalf("exec error = %v, want the flat error code and message", err)
	}
}
