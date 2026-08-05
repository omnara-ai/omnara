package blaxel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestBlaxelRESTClientSandboxLifecycle(t *testing.T) {
	var deleteCalls atomic.Int32
	dataPlane := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected data-plane request: %s %s", r.Method, r.URL.Path)
	}))
	defer dataPlane.Close()

	management := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer api-token" ||
			r.Header.Get("X-Blaxel-Workspace") != "omnara" {
			t.Fatalf("unexpected authentication headers: %+v", r.Header)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/sandboxes":
			if r.URL.Query().Get("createIfNotExist") != "true" {
				t.Fatalf("createIfNotExist query = %q", r.URL.RawQuery)
			}
			var request struct {
				Metadata map[string]any `json:"metadata"`
				Spec     struct {
					Runtime map[string]any `json:"runtime"`
				} `json:"spec"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode create body: %v", err)
			}
			if request.Metadata["name"] != "omnara-mch-test" {
				t.Fatalf("metadata = %+v", request.Metadata)
			}
			if _, ok := request.Spec.Runtime["envs"]; ok {
				t.Fatalf("create body carries env: %+v", request.Spec.Runtime)
			}
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"errorCode":"SANDBOX_ALREADY_EXISTS"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/sandboxes/omnara-mch-test":
			_, _ = w.Write([]byte(`{"metadata":{"name":"omnara-mch-test","url":"` + dataPlane.URL + `"},"status":"DEPLOYED"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/sandboxes/missing":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodDelete && r.URL.Path == "/sandboxes/omnara-mch-test":
			if deleteCalls.Add(1) > 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected management request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer management.Close()

	client := newTestRESTClient(management.URL)
	_, err := client.CreateSandbox(context.Background(), createSandboxRequest{
		Metadata: resourceMetadata{Name: "omnara-mch-test"},
		Spec: sandboxSpec{
			Region: "us-pdx-1",
			Runtime: sandboxRuntime{
				Image: "blaxel/base-image:latest", Memory: 1024,
			},
		},
	})
	if !isConflict(err) {
		t.Fatalf("create error = %v, want conflict", err)
	}
	sandbox, found, err := client.GetSandbox(context.Background(), "omnara-mch-test")
	if err != nil || !found || sandbox.Metadata.URL != dataPlane.URL {
		t.Fatalf("sandbox=%+v found=%t err=%v", sandbox, found, err)
	}
	if _, found, err := client.GetSandbox(context.Background(), "missing"); err != nil || found {
		t.Fatalf("missing sandbox found=%t err=%v", found, err)
	}
	if err := client.DeleteSandbox(context.Background(), "omnara-mch-test"); err != nil {
		t.Fatalf("delete sandbox: %v", err)
	}
	if err := client.DeleteSandbox(context.Background(), "omnara-mch-test"); err != nil {
		t.Fatalf("repeat delete sandbox: %v", err)
	}
}

func TestBlaxelRESTClientProcessUsesDataPlane(t *testing.T) {
	var calls atomic.Int32
	dataPlane := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer api-token" ||
			r.Header.Get("X-Blaxel-Workspace") != "omnara" {
			t.Fatalf("unexpected authentication headers: %+v", r.Header)
		}
		if r.Method != http.MethodPost || r.URL.Path != "/process" {
			t.Fatalf("unexpected process request: %s %s", r.Method, r.URL.Path)
		}
		calls.Add(1)
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode process request: %v", err)
		}
		if request["keepAlive"] != true || request["timeout"] != float64(0) ||
			request["waitForCompletion"] != false {
			t.Fatalf("process lifecycle fields = %+v", request)
		}
		_, _ = w.Write([]byte(`{"pid":"4321","status":"running","exitCode":0,"keepAlive":true}`))
	}))
	defer dataPlane.Close()

	client := newTestRESTClient("https://api.invalid.test")
	client.httpClient = dataPlane.Client()
	target := sandbox{
		Metadata: resourceMetadata{Name: "omnara-mch-test", URL: dataPlane.URL},
	}
	process, err := client.StartSandboxProcess(context.Background(), target, processRequest{
		Name: daemonProcessName, Command: "sleep 1", KeepAlive: true,
	})
	if err != nil || process.PID != "4321" || process.Status != processStatusRunning ||
		process.ExitCode == nil || *process.ExitCode != 0 || !process.KeepAlive || calls.Load() != 1 {
		t.Fatalf("process=%+v calls=%d err=%v", process, calls.Load(), err)
	}
	_, err = client.StartSandboxProcess(context.Background(), sandbox{
		Metadata: resourceMetadata{Name: "missing-url"},
	}, processRequest{})
	if err == nil || !strings.Contains(err.Error(), "missing its data-plane url") {
		t.Fatalf("missing URL error = %v", err)
	}
	_, err = client.StartSandboxProcess(context.Background(), sandbox{
		Metadata: resourceMetadata{Name: "plain-http", URL: "http://sbx.example.com"},
	}, processRequest{})
	if err == nil || !strings.Contains(err.Error(), "invalid data-plane url") {
		t.Fatalf("plain HTTP error = %v", err)
	}
}

func TestBlaxelRESTClientDoesNotRetryProcessStart(t *testing.T) {
	var calls atomic.Int32
	dataPlane := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer dataPlane.Close()

	client := newTestRESTClient("https://api.invalid.test")
	client.httpClient = dataPlane.Client()
	_, err := client.StartSandboxProcess(context.Background(), sandbox{
		Metadata: resourceMetadata{Name: "omnara-mch-test", URL: dataPlane.URL},
	}, processRequest{Name: daemonProcessName, Command: "sleep 1"})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("process start calls=%d err=%v, want one failed call", calls.Load(), err)
	}
}

func TestBlaxelRESTClientDataPlaneCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := newTestRESTClient("https://api.invalid.test")
	client.httpClient.Transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Body:       cancelingResponseBody{cancel: cancel},
			Header:     make(http.Header),
			Request:    request,
		}, nil
	})

	err := client.doDataPlaneRequest(ctx, http.MethodGet, "https://sandbox.invalid.test/process", nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("data-plane error = %v, want context canceled", err)
	}
}

func TestBlaxelRESTClientDoesNotExposeErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(
			`{"errorCode":"UPSTREAM_FAILURE","error":"OMNARA_MACHINE_TOKEN=secret-token"}`,
		))
	}))
	defer server.Close()

	client := newTestRESTClient(server.URL)
	_, _, err := client.GetSandbox(context.Background(), "omnara-mch-test")
	if err == nil || err.Error() != "blaxel API returned HTTP 500" {
		t.Fatalf("provider error = %v, want status without response body", err)
	}
}

func newTestRESTClient(apiBaseURL string) *restClient {
	return &restClient{
		apiBaseURL: apiBaseURL,
		workspace:  "omnara",
		apiToken:   "api-token",
		httpClient: &http.Client{Timeout: providers.HTTPClientTimeout},
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type cancelingResponseBody struct {
	cancel context.CancelFunc
}

func (b cancelingResponseBody) Read([]byte) (int, error) {
	b.cancel()
	return 0, io.EOF
}

func (cancelingResponseBody) Close() error {
	return nil
}
