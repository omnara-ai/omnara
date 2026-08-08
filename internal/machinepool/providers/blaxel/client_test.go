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
		t.Errorf("unexpected data-plane request: %s %s", r.Method, r.URL.Path)
	}))
	defer dataPlane.Close()

	management := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Blaxel-Authorization") != "Bearer api-token" ||
			r.Header.Get("X-Blaxel-Workspace") != "omnara" ||
			r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected authentication headers: %+v", r.Header)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/sandboxes":
			if r.URL.Query().Get("createIfNotExist") != "true" {
				t.Errorf("createIfNotExist query = %q", r.URL.RawQuery)
				return
			}
			var request struct {
				Metadata map[string]any `json:"metadata"`
				Spec     struct {
					Runtime map[string]any `json:"runtime"`
				} `json:"spec"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode create body: %v", err)
				return
			}
			if request.Metadata["name"] != "omnara-mch-test" {
				t.Errorf("metadata = %+v", request.Metadata)
				return
			}
			if _, ok := request.Spec.Runtime["envs"]; ok {
				t.Errorf("create body carries env: %+v", request.Spec.Runtime)
				return
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
			t.Errorf("unexpected management request: %s %s", r.Method, r.URL.Path)
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
		if r.Header.Get("X-Blaxel-Authorization") != "Bearer api-token" ||
			r.Header.Get("X-Blaxel-Workspace") != "omnara" ||
			r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected authentication headers: %+v", r.Header)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/process":
			calls.Add(1)
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode process request: %v", err)
				return
			}
			if request["keepAlive"] != true || request["timeout"] != float64(0) ||
				request["waitForCompletion"] != false {
				t.Errorf("process lifecycle fields = %+v", request)
				return
			}
			env, ok := request["env"].(map[string]any)
			if !ok || env["OMNARA_MACHINE_TOKEN"] != "machine-token" || env["CUSTOMER_SECRET"] != nil {
				t.Errorf("process env = %+v", request["env"])
				return
			}
			_, _ = w.Write([]byte(
				`{"pid":"4321","name":"omnara-daemon","command":"sleep 1","status":"running","keepAlive":true}`,
			))
		case r.Method == http.MethodGet && r.URL.Path == "/process/omnara-daemon":
			_, _ = w.Write([]byte(
				`{"pid":"4321","name":"omnara-daemon","command":"# omnara-managed-scoped-bootstrap:v1\nset -eu","status":"running","keepAlive":true}`,
			))
		case r.Method == http.MethodGet && r.URL.Path == "/process/missing":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected process request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer dataPlane.Close()

	client := newTestRESTClient("https://api.invalid.test")
	client.httpClient = dataPlane.Client()
	target := sandbox{
		Metadata: resourceMetadata{Name: "omnara-mch-test", URL: dataPlane.URL},
	}
	process, err := client.StartSandboxProcess(context.Background(), target, processRequest{
		Name: daemonProcessName, Command: "sleep 1", KeepAlive: true,
		Env: map[string]string{"OMNARA_MACHINE_TOKEN": "machine-token"},
	})
	if err != nil || process.PID != "4321" || process.Name != daemonProcessName ||
		process.Command != "sleep 1" || process.Status != processStatusRunning ||
		!process.KeepAlive || calls.Load() != 1 {
		t.Fatalf("process=%+v calls=%d err=%v", process, calls.Load(), err)
	}
	existing, found, err := client.GetSandboxProcess(context.Background(), target, daemonProcessName)
	if err != nil || !found || !providers.IsManagedScopedBootScript(existing.Command) {
		t.Fatalf("existing process=%+v found=%t err=%v", existing, found, err)
	}
	if _, found, err := client.GetSandboxProcess(
		context.Background(), target, "missing",
	); err != nil || found {
		t.Fatalf("missing process found=%t err=%v", found, err)
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

func TestBlaxelRESTClientUploadsPrivateSandboxFile(t *testing.T) {
	var fileDeleteCalls atomic.Int32
	var directoryDeleteCalls atomic.Int32
	dataPlane := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Blaxel-Authorization") != "Bearer api-token" ||
			r.Header.Get("X-Blaxel-Workspace") != "omnara" ||
			r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected authentication headers: %+v", r.Header)
			return
		}
		switch {
		case r.Method == http.MethodPut &&
			r.URL.EscapedPath() == "/filesystem/%2Froot%2F.omnara-bootstrap-0123456789abcdef0123456789abcdef":
			var request fileRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode directory request: %v", err)
				return
			}
			if request.Content != "" || !request.IsDirectory || request.Permissions != "0700" {
				t.Errorf("directory request = %+v", request)
				return
			}
		case r.Method == http.MethodPut &&
			r.URL.EscapedPath() == "/filesystem/%2Froot%2F.omnara-bootstrap-0123456789abcdef0123456789abcdef%2Fstartup-env":
			var request fileRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode file request: %v", err)
				return
			}
			if request.Content != "export SECRET='value'\n" || request.IsDirectory ||
				request.Permissions != "0600" {
				t.Errorf("file request = %+v", request)
				return
			}
		case r.Method == http.MethodDelete &&
			r.URL.EscapedPath() == "/filesystem/%2Froot%2F.omnara-bootstrap-0123456789abcdef0123456789abcdef%2Fstartup-env":
			if fileDeleteCalls.Add(1) > 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
		case r.Method == http.MethodDelete &&
			r.URL.EscapedPath() == "/filesystem/%2Froot%2F.omnara-bootstrap-0123456789abcdef0123456789abcdef":
			if r.URL.Query().Get("recursive") != "" {
				t.Errorf("directory delete unexpectedly recursive: %s", r.URL.RawQuery)
				return
			}
			if directoryDeleteCalls.Add(1) > 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
		default:
			t.Errorf("unexpected filesystem request: %s %s", r.Method, r.URL.EscapedPath())
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer dataPlane.Close()

	client := newTestRESTClient("https://api.invalid.test")
	client.httpClient = dataPlane.Client()
	target := sandbox{Metadata: resourceMetadata{Name: "omnara-mch-test", URL: dataPlane.URL}}
	if err := client.CreateSandboxDirectory(
		context.Background(),
		target,
		"/root/.omnara-bootstrap-0123456789abcdef0123456789abcdef",
	); err != nil {
		t.Fatalf("create sandbox directory: %v", err)
	}
	err := client.UploadSandboxFile(
		context.Background(),
		target,
		"/root/.omnara-bootstrap-0123456789abcdef0123456789abcdef/startup-env",
		"export SECRET='value'\n",
	)
	if err != nil {
		t.Fatalf("upload sandbox file: %v", err)
	}
	for _, path := range []string{"", "relative", "/tmp/../secret", "/tmp//secret"} {
		if err := client.UploadSandboxFile(
			context.Background(),
			target,
			path,
			"secret",
		); err == nil || !strings.Contains(err.Error(), "clean absolute path") {
			t.Fatalf("upload path %q error = %v, want path rejection", path, err)
		}
	}
	if err := client.DeleteSandboxPath(
		context.Background(), target, "/root/.omnara-bootstrap-0123456789abcdef0123456789abcdef/startup-env",
	); err != nil {
		t.Fatalf("delete sandbox file: %v", err)
	}
	if err := client.DeleteSandboxPath(
		context.Background(), target, "/root/.omnara-bootstrap-0123456789abcdef0123456789abcdef/startup-env",
	); err != nil {
		t.Fatalf("repeat delete sandbox file: %v", err)
	}
	if err := client.DeleteSandboxPath(
		context.Background(), target, "/root/.omnara-bootstrap-0123456789abcdef0123456789abcdef",
	); err != nil {
		t.Fatalf("delete sandbox directory: %v", err)
	}
	if err := client.DeleteSandboxPath(
		context.Background(), target, "/root/.omnara-bootstrap-0123456789abcdef0123456789abcdef",
	); err != nil {
		t.Fatalf("repeat delete sandbox directory: %v", err)
	}
}

func TestBlaxelRESTClientWakeUsesDataPlane(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantError  string
	}{
		{name: "daemon acknowledged wake", statusCode: http.StatusNoContent},
		{
			name:       "unexpected success response",
			statusCode: http.StatusOK,
			wantError:  "blaxel wake endpoint returned HTTP 200, want HTTP 204",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataPlane := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Blaxel-Authorization") != "Bearer api-token" ||
					r.Header.Get("X-Blaxel-Workspace") != "omnara" ||
					r.Header.Get("Authorization") != "" {
					t.Errorf("unexpected authentication headers: %+v", r.Header)
					return
				}
				if r.Method != http.MethodGet || r.URL.Path != "/port/8377/" {
					t.Errorf("unexpected wake request: %s %s", r.Method, r.URL.Path)
					return
				}
				w.WriteHeader(test.statusCode)
			}))
			defer dataPlane.Close()

			client := newTestRESTClient("https://api.invalid.test")
			client.httpClient = dataPlane.Client()
			err := client.WakeSandbox(context.Background(), sandbox{
				Metadata: resourceMetadata{Name: "omnara-mch-test", URL: dataPlane.URL},
			})
			if test.wantError == "" && err != nil {
				t.Fatalf("wake sandbox: %v", err)
			}
			if test.wantError != "" && (err == nil || err.Error() != test.wantError) {
				t.Fatalf("wake error = %v, want %q", err, test.wantError)
			}
		})
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

	_, err := client.doDataPlaneRequest(
		ctx,
		http.MethodGet,
		"https://sandbox.invalid.test/process",
		nil,
		nil,
	)
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
