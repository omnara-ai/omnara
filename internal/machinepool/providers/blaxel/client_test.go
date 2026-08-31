package blaxel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
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
		if r.Method != http.MethodPost || r.URL.Path != "/process" {
			t.Errorf("unexpected process request: %s %s", r.Method, r.URL.Path)
			return
		}
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
		_, _ = w.Write([]byte(`{"pid":"4321","status":"running","keepAlive":true}`))
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
		!process.KeepAlive || calls.Load() != 1 {
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

func TestBlaxelRESTClientListsSandboxesWithVersionedCursor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/sandboxes" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			return
		}
		if r.Header.Get("Blaxel-Version") != apiVersion ||
			r.Header.Get("X-Blaxel-Authorization") != "Bearer api-token" ||
			r.Header.Get("X-Blaxel-Workspace") != "omnara" {
			t.Errorf("unexpected headers: %+v", r.Header)
			return
		}
		query := r.URL.Query()
		if query.Get("cursor") != "cursor+/=" ||
			query.Get("limit") != "200" ||
			query.Get("q") != "omnara-mch-" ||
			query.Has("showTerminated") ||
			query.Has("sort") {
			t.Errorf("unexpected list query: %s", r.URL.RawQuery)
			return
		}
		_, _ = w.Write([]byte(`{
			"data":[{"metadata":{"name":"omnara-mch-test"},"state":"RUNNING","status":"DEPLOYED"}],
			"meta":{"hasMore":true,"nextCursor":"next+/=","total":2}
		}`))
	}))
	defer server.Close()

	page, err := newTestRESTClient(server.URL).ListSandboxes(
		context.Background(), "cursor+/=", maxSandboxListLimit,
	)
	if err != nil {
		t.Fatalf("list sandboxes: %v", err)
	}
	if !page.HasMore || page.NextCursor != "next+/=" ||
		len(page.Sandboxes) != 1 || page.Sandboxes[0].State != "RUNNING" ||
		page.Sandboxes[0].Status != "DEPLOYED" {
		t.Fatalf("unexpected page: %+v", page)
	}
}

func TestBlaxelRESTClientListFitsMaximumManagedEnvironment(t *testing.T) {
	machineEnv := make(map[string]string, executionstore.MaxResolvedEnvironmentEntries)
	usedBytes := 0
	for index := range executionstore.MaxResolvedEnvironmentEntries {
		name := fmt.Sprintf("ENTRY_%04d", index)
		machineEnv[name] = ""
		usedBytes += len(name)
	}
	machineEnv["ENTRY_0000"] = strings.Repeat(
		"\x01",
		executionstore.MaxResolvedEnvironmentBytes-usedBytes,
	)
	managedEnv, err := providers.BuildManagedMachineEnv(
		"https://app.omnara.test",
		"machine-token",
		strings.Repeat("x", 64*1024),
		machineEnv,
	)
	if err != nil {
		t.Fatalf("build maximum managed environment: %v", err)
	}
	listedEnvs := make([]map[string]any, 0, len(managedEnv))
	for _, item := range sandboxEnvsFromMap(managedEnv) {
		listedEnvs = append(listedEnvs, map[string]any{
			"name": item.Name, "secret": false, "value": item.Value,
		})
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []any{map[string]any{
				"metadata": resourceMetadata{Name: "omnara-mch-test"},
				"spec": map[string]any{
					"runtime": map[string]any{"envs": listedEnvs},
				},
				"state":  "RUNNING",
				"status": "DEPLOYED",
			}},
			"meta": map[string]any{"hasMore": false, "total": 1},
		})
	}))
	defer server.Close()

	page, err := newTestRESTClient(server.URL).ListSandboxes(context.Background(), "", 1)
	if err != nil {
		t.Fatalf("list maximum managed sandbox response: %v", err)
	}
	if len(page.Sandboxes) != 1 || page.Sandboxes[0].State != sandboxRuntimeRunning {
		t.Fatalf("maximum managed sandbox page = %+v", page)
	}
}

func TestBlaxelRESTClientRejectsMalformedSandboxList(t *testing.T) {
	for _, response := range []string{
		`{}`,
		`{"data":null,"meta":{"hasMore":false,"nextCursor":""}}`,
		`{"data":[],"meta":{}}`,
	} {
		t.Run(response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(response))
			}))
			defer server.Close()

			if _, err := newTestRESTClient(server.URL).ListSandboxes(
				context.Background(), "", 1,
			); err == nil || !strings.Contains(err.Error(), "decode blaxel response") {
				t.Fatalf("malformed list error = %v", err)
			}
		})
	}
	if _, err := newTestRESTClient("https://api.invalid.test").ListSandboxes(
		context.Background(), "", maxSandboxListLimit+1,
	); err == nil || !strings.Contains(err.Error(), "limit must be between") {
		t.Fatalf("invalid limit error = %v", err)
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
