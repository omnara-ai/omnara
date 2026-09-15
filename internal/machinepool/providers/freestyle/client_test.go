package freestyle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestRESTClientUsesFreestyleV5Lifecycle(t *testing.T) {
	paths := make([]string, 0, 6)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
			http.Error(w, "test handler failed", http.StatusInternalServerError)
			return
		}
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "POST /v5/vms":
			var request createVMRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode create request: %v", err)
				http.Error(w, "test handler failed", http.StatusInternalServerError)
				return
			}
			if request.SnapshotID != "snapshot-1" || request.Slug != "machine-1" ||
				request.AutoDeleteSeconds != -1 || !request.AutomaticRestart {
				t.Errorf("create request = %+v", request)
			}
			_ = json.NewEncoder(w).Encode(vm{ID: "vm-1", Slug: "machine-1", State: "starting"})
		case "GET /v5/vms/vm-1":
			_ = json.NewEncoder(w).Encode(vm{ID: "vm-1", Slug: "machine-1", State: "running"})
		case "POST /v5/vms/vm-1/resize":
			var request resizeVMRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil ||
				request.CPU != 2 || request.MemoryMB != 4096 {
				t.Errorf("resize request = %+v, error %v", request, err)
			}
			_ = json.NewEncoder(w).Encode(vm{ID: "vm-1", Resources: vmResources{CPU: 2, MemoryMB: 4096}})
		case "POST /v5/vms/vm-1/start":
			_ = json.NewEncoder(w).Encode(vm{ID: "vm-1", State: "starting"})
		case "POST /v5/vms/vm-1/exec-await":
			var request execVMRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil ||
				request.Command != "true" || request.LinuxUser != "root" || request.TimeoutMS != 30000 {
				t.Errorf("exec request = %+v, error %v", request, err)
			}
			statusCode := 0
			_ = json.NewEncoder(w).Encode(execVMResponse{StatusCode: &statusCode})
		case "DELETE /v5/vms/vm-1":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newRESTClient(server.URL, "test-token", server.Client())
	created, err := client.CreateVM(context.Background(), createVMRequest{
		SnapshotID:        "snapshot-1",
		Slug:              "machine-1",
		AutoDeleteSeconds: -1,
		AutomaticRestart:  true,
	})
	if err != nil || created.ID != "vm-1" {
		t.Fatalf("create VM = %+v, error %v", created, err)
	}
	if current, found, err := client.GetVM(context.Background(), "vm-1"); err != nil || !found || current.State != "running" {
		t.Fatalf("get VM = %+v, found %v, error %v", current, found, err)
	}
	if _, err := client.ResizeVM(context.Background(), "vm-1", resizeVMRequest{CPU: 2, MemoryMB: 4096}); err != nil {
		t.Fatalf("resize VM: %v", err)
	}
	if _, err := client.StartVM(context.Background(), "vm-1"); err != nil {
		t.Fatalf("start VM: %v", err)
	}
	if _, err := client.ExecVM(context.Background(), "vm-1", execVMRequest{
		Command: "true", LinuxUser: "root", TimeoutMS: 30000,
	}); err != nil {
		t.Fatalf("exec VM: %v", err)
	}
	if err := client.DeleteVM(context.Background(), "vm-1"); err != nil {
		t.Fatalf("delete VM: %v", err)
	}
	if len(paths) != 6 {
		t.Fatalf("request paths = %#v", paths)
	}
}

func TestRESTClientTreatsNotFoundAsAbsentAndSanitizesErrors(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v5/vms/missing" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"code":"rate_limited","message":"provider secret"}`))
	}))
	defer server.Close()
	client := newRESTClient(server.URL, "test-token", server.Client())

	if _, found, err := client.GetVM(context.Background(), "missing"); err != nil || found {
		t.Fatalf("missing VM found = %v, error %v", found, err)
	}
	err := client.DeleteVM(context.Background(), "limited")
	if err == nil || !strings.Contains(err.Error(), "rate_limited") ||
		strings.Contains(err.Error(), "provider secret") {
		t.Fatalf("sanitized API error = %v", err)
	}
	if delay, ok := providers.RetryAfter(err); !ok || delay != 3*time.Second {
		t.Fatalf("retry-after = %s, found %v", delay, ok)
	}
}
