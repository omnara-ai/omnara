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

func TestCreateOSRESTClientReadsTheRootFSCatalog(t *testing.T) {
	client := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rootfs" {
			t.Errorf("rootfs path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"rootfs":["devbox:1"],"default":"devbox:1"}}`))
	})
	catalog, err := client.ListRootFS(context.Background())
	if err != nil {
		t.Fatalf("list rootfs: %v", err)
	}
	if catalog.Default != "devbox:1" || len(catalog.Names) != 1 {
		t.Fatalf("rootfs catalog = %+v, want one entry defaulting to devbox:1", catalog)
	}
}

func TestCreateOSRESTClientCreatesSandboxesWithTheRequestedShape(t *testing.T) {
	var received createSandboxRequest
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
		Region: "us",
		Envs:   map[string]string{"OMNARA_API_URL": "https://api.omnara.test/v1"},
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if created.ID != "sb-123" {
		t.Fatalf("created sandbox = %+v, want sb-123", created)
	}
	if received.Shape != "s-1vcpu-1gb" || received.Region != "us" || received.Envs["OMNARA_API_URL"] == "" {
		t.Fatalf("create request = %+v, want the shape, region and env carried through", received)
	}
}

func TestCreateOSRESTClientPagesThroughSandboxes(t *testing.T) {
	client := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "500" || r.URL.Query().Get("offset") != "1000" {
			t.Errorf("list query = %q", r.URL.RawQuery)
			http.Error(w, "test handler failed", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"data":[{"id":"sb-1"}],"pagination":{"total":1001}}}`))
	})
	items, total, err := client.ListSandboxes(context.Background(), 500, 1000)
	if err != nil {
		t.Fatalf("list sandboxes: %v", err)
	}
	if len(items) != 1 || total != 1001 {
		t.Fatalf("list sandboxes = %d items of %d total", len(items), total)
	}
}

func TestCreateOSRESTClientPaginationTotalComesFromTheEnvelope(t *testing.T) {
	client := newTestRESTClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"data":[],"pagination":{"total":0}}}`))
	})
	items, total, err := client.ListSandboxes(context.Background(), 500, 0)
	if err != nil {
		t.Fatalf("list sandboxes: %v", err)
	}
	if len(items) != 0 || total != 0 {
		t.Fatalf("list sandboxes = %d items of %d total, want an empty page", len(items), total)
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

func TestCreateOSRESTClientReadsProcessesWithoutAnEnvelope(t *testing.T) {
	client := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sandboxes/sb-123/processes" {
			t.Errorf("processes path = %q", r.URL.Path)
			http.Error(w, "test handler failed", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"processes":[{"process_id":"proc-1","state":"running","leader_exited":false}]}`))
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
	var received createProcessRequest
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
		_, _ = w.Write([]byte(`{"process_id":"proc-1","state":"starting"}`))
	})
	created, err := client.CreateProcess(context.Background(), "sb-123", createProcessRequest{
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

func TestCreateOSRESTClientAcceptsEmptyResponses(t *testing.T) {
	client := newTestRESTClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if err := client.DeleteSandbox(context.Background(), "sb-123"); err != nil {
		t.Fatalf("delete sandbox: %v", err)
	}
}
