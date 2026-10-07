package createos

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestRESTClient(t *testing.T, handler http.HandlerFunc) *restClient {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	return newRESTClient(server.URL, "test-token", server.Client())
}

func TestCreateOSRESTClientSendsTheAPIKeyAndUnwrapsTheEnvelope(t *testing.T) {
	client := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "test-token" {
			t.Errorf("api key header = %q", r.Header.Get("X-Api-Key"))
			http.Error(w, "test handler failed", http.StatusInternalServerError)
			return
		}
		if r.URL.Path != "/v1/shapes" {
			t.Errorf("shapes path = %q", r.URL.Path)
			http.Error(w, "test handler failed", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"data":[{"id":"s-1vcpu-1gb","vcpu":1,"mem_mib":1024}]}}`))
	})
	shapes, err := client.ListShapes(context.Background())
	if err != nil {
		t.Fatalf("list shapes: %v", err)
	}
	if len(shapes) != 1 || shapes[0].ID != "s-1vcpu-1gb" || shapes[0].VCPU != 1 || shapes[0].MemMiB != 1024 {
		t.Fatalf("shapes = %+v, want the catalog entry", shapes)
	}
}

func TestCreateOSRESTClientCreatesSandboxesWithTheRequestedShape(t *testing.T) {
	var received map[string]json.RawMessage
	client := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/sandboxes" {
			t.Errorf("create request = %s %s", r.Method, r.URL.Path)
			http.Error(w, "test handler failed", http.StatusInternalServerError)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode create request: %v", err)
			http.Error(w, "test handler failed", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"sb-123","name":"omnara-abc"}}`))
	})
	created, err := client.CreateSandbox(context.Background(), createSandboxRequest{
		Shape:  "s-1vcpu-1gb",
		RootFS: "devbox:1",
		Name:   "omnara-abc",
		Envs:   map[string]string{"OMNARA_API_URL": "https://api.omnara.test/v1"},
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if created.ID != "sb-123" {
		t.Fatalf("created sandbox = %+v, want sb-123", created)
	}
	if _, ok := received["region"]; ok {
		t.Fatalf("create request = %s, want region omitted", received["region"])
	}
	if _, ok := received["bandwidth_quota_bytes"]; ok {
		t.Fatalf("create request = %s, want bandwidth quota omitted", received["bandwidth_quota_bytes"])
	}
	var shape string
	var envs map[string]string
	if err := json.Unmarshal(received["shape"], &shape); err != nil {
		t.Fatalf("decode shape: %v", err)
	}
	if err := json.Unmarshal(received["envs"], &envs); err != nil {
		t.Fatalf("decode envs: %v", err)
	}
	if shape != "s-1vcpu-1gb" || envs["OMNARA_API_URL"] == "" {
		t.Fatalf("create request = %+v, want the shape and env carried through", received)
	}
}

func TestCreateOSRESTClientPagesThroughSandboxes(t *testing.T) {
	client := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("status") != "running" || query.Get("limit") != "500" || query.Get("offset") != "1000" {
			t.Errorf("list query = %q", r.URL.RawQuery)
			http.Error(w, "test handler failed", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"data":[{"id":"sb-1"}],"pagination":{"total":1001}}}`))
	})
	items, total, err := client.ListSandboxes(context.Background(), sandboxStatusRunning, 500, 1000)
	if err != nil {
		t.Fatalf("list sandboxes: %v", err)
	}
	if len(items) != 1 || total != 1001 {
		t.Fatalf("list sandboxes = %d items of %d total", len(items), total)
	}
}

func TestCreateOSRESTClientTreatsAMissingSandboxAsAbsent(t *testing.T) {
	client := newTestRESTClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	target, found, err := client.GetSandbox(context.Background(), "sb-gone")
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if found || target.ID != "" {
		t.Fatalf("get sandbox = %+v found %v, want an absent sandbox", target, found)
	}
}

func TestCreateOSRESTClientDeletingAMissingSandboxSucceeds(t *testing.T) {
	client := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("delete method = %s", r.Method)
		}
		w.WriteHeader(http.StatusNotFound)
	})
	if err := client.DeleteSandbox(context.Background(), "sb-gone"); err != nil {
		t.Fatalf("delete sandbox: %v", err)
	}
}

func TestCreateOSRESTClientResumesSandboxes(t *testing.T) {
	client := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/sandboxes/sb-123/resume" {
			t.Errorf("resume request = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"sb-123","status":"resuming"}}`))
	})
	if err := client.ResumeSandbox(context.Background(), "sb-123"); err != nil {
		t.Fatalf("resume sandbox: %v", err)
	}
}

func TestCreateOSRESTClientExecRequiresASuccessfulExit(t *testing.T) {
	for body, wantErr := range map[string]bool{
		`{"status":"success","data":{"result":{"exit_code":0}}}`:                             false,
		`{"status":"success","data":{"result":{"exit_code":7}}}`:                             true,
		`{"status":"success","data":{"result":{"exit_code":0,"error":"exec format error"}}}`: true,
		`{"status":"success","data":{}}`:                                                     true,
	} {
		var received commandRequest
		client := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/v1/sandboxes/sb-123/exec" {
				t.Errorf("exec request = %s %s", r.Method, r.URL.Path)
			}
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				t.Errorf("decode exec request: %v", err)
			}
			_, _ = w.Write([]byte(body))
		})
		err := client.Exec(context.Background(), "sb-123", commandRequest{Command: "curl", Args: []string{"-q"}})
		if (err != nil) != wantErr || received.Command != "curl" {
			t.Fatalf("exec with %s error = %v request = %+v", body, err, received)
		}
	}
}

func TestCreateOSRESTClientReportsAPIFailures(t *testing.T) {
	client := newTestRESTClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	_, err := client.ListShapes(context.Background())
	var apiErr apiError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("list shapes error = %v, want an API error carrying the status code", err)
	}
}

func TestCreateOSRESTClientEscapesSandboxIDsInPaths(t *testing.T) {
	var path string
	client := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{"status":"success","data":{"id":"sb-1"}}`))
	})
	if _, _, err := client.GetSandbox(context.Background(), "sb/../secret"); err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if path != "/v1/sandboxes/sb%2F..%2Fsecret" {
		t.Fatalf("request path = %q, want the id escaped", path)
	}
}

func TestCreateOSRESTClientListsProcesses(t *testing.T) {
	client := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sandboxes/sb-123/processes" {
			t.Errorf("processes path = %q", r.URL.Path)
			http.Error(w, "test handler failed", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(
			`{"status":"success","data":{"processes":[{"process_id":"proc-1","state":"running","leader_exited":false}]}}`,
		))
	})
	processes, err := client.ListProcesses(context.Background(), "sb-123")
	if err != nil {
		t.Fatalf("list processes: %v", err)
	}
	if len(processes) != 1 || processes[0].ID != "proc-1" || processes[0].State != "running" {
		t.Fatalf("processes = %+v, want the running daemon", processes)
	}
}

func TestCreateOSRESTClientStartsProcesses(t *testing.T) {
	var received commandRequest
	client := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("create process method = %s", r.Method)
			http.Error(w, "test handler failed", http.StatusInternalServerError)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode create process request: %v", err)
			http.Error(w, "test handler failed", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"process_id":"proc-1","state":"starting"}}`))
	})
	created, err := client.CreateProcess(context.Background(), "sb-123", commandRequest{
		Command: "/bin/sh",
		Args:    []string{"-c", "true"},
	})
	if err != nil {
		t.Fatalf("create process: %v", err)
	}
	if created.ID != "proc-1" {
		t.Fatalf("created process = %+v, want proc-1", created)
	}
	if received.Command != "/bin/sh" || len(received.Args) != 2 {
		t.Fatalf("create process request = %+v, want the command and args carried through", received)
	}
}
