package maritime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func newTestRESTClient(t *testing.T, handler http.HandlerFunc) (*restClient, string) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	return newRESTClient(server.URL+"/v1", "mk_test", server.Client()), server.URL
}

func TestMaritimeRESTClientCreatesSandboxes(t *testing.T) {
	var received map[string]json.RawMessage
	client, _ := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/sandboxes" {
			t.Errorf("create request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer mk_test" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode create request: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"sbx-abc123","name":"omnara-mch-abc","state":"running",` +
			`"resources":{"vcpu":2,"memoryMib":4096,"diskGib":12},` +
			`"dataPlaneUrl":"https://api-sbx-abc123.sbx.maritime.sh/v1","secret":"mst_1",` +
			`"metadata":{"omnara-machine":"mch_1"},"endedReason":null}`))
	})
	created, err := client.CreateSandbox(context.Background(), createSandboxRequest{
		Name:      "omnara-mch-abc",
		From:      "base",
		VCPU:      2,
		MemoryMiB: 4096,
		Metadata:  map[string]string{"omnara-machine": "mch_1"},
		KeepAwake: true,
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if created.ID != "sbx-abc123" || created.State != sandboxStateRunning || created.Resources.MemoryMiB != 4096 ||
		created.Secret != "mst_1" || created.Metadata["omnara-machine"] != "mch_1" {
		t.Fatalf("created = %+v", created)
	}
	for _, field := range []string{"name", "from", "vcpu", "memoryMib", "metadata", "keepAwake"} {
		if _, ok := received[field]; !ok {
			t.Errorf("create request is missing %s: %v", field, received)
		}
	}
	if _, ok := received["diskGib"]; ok {
		t.Errorf("create request sent diskGib without a disk size: %v", received)
	}
}

func TestMaritimeRESTClientListsByMetadataAcrossPages(t *testing.T) {
	calls := 0
	client, _ := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/sandboxes" || r.URL.Query().Get("metadata[omnara-machine]") != "mch_1" {
			t.Errorf("list request = %s", r.URL.String())
		}
		if calls == 1 {
			_, _ = w.Write([]byte(`{"data":[{"id":"sbx-a","state":"ended"}],"nextCursor":"c2"}`))
			return
		}
		if r.URL.Query().Get("cursor") != "c2" {
			t.Errorf("second page cursor = %q", r.URL.Query().Get("cursor"))
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"sbx-b","state":"running"}],"nextCursor":null}`))
	})
	sandboxes, err := client.ListSandboxes(context.Background(), map[string]string{"omnara-machine": "mch_1"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(sandboxes) != 2 || sandboxes[0].ID != "sbx-a" || sandboxes[1].ID != "sbx-b" {
		t.Fatalf("sandboxes = %+v", sandboxes)
	}
}

func TestMaritimeRESTClientTreatsMissingSandboxesAsAbsent(t *testing.T) {
	client, _ := newTestRESTClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"sandbox_not_found","message":"No such sandbox."}`))
	})
	if _, found, err := client.GetSandbox(context.Background(), "sbx-gone"); err != nil || found {
		t.Fatalf("get missing = found %v, %v", found, err)
	}
	if err := client.DeleteSandbox(context.Background(), "sbx-gone"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
}

func TestMaritimeRESTClientDecodesErrors(t *testing.T) {
	client, _ := newTestRESTClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"code":"concurrency_limit","message":"At the plan limit.","limit":5}`))
	})
	_, err := client.CreateSandbox(context.Background(), createSandboxRequest{Name: "omnara-mch-abc"})
	var apiErr apiError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooManyRequests || apiErr.Code != "concurrency_limit" {
		t.Fatalf("error = %#v", err)
	}
	if !transientAPIError(err) || rejectedBeforeCreate(err) {
		t.Fatalf("concurrency_limit should be transient: %v", err)
	}
	if delay, ok := providers.RetryAfter(err); !ok || delay != 7*time.Second {
		t.Fatalf("retry after = %v, %v", delay, ok)
	}
}

func TestMaritimeRESTClientRunsOnTheDataPlaneWithTheSandboxSecret(t *testing.T) {
	client, serverURL := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/dp/v1/commands" {
			t.Errorf("run request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get(dataPlaneHeader) != "mst_1" || r.Header.Get("Authorization") != "" {
			t.Errorf("data plane headers = %v", r.Header)
		}
		var request runRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode run request: %v", err)
		}
		if len(request.Cmd) != 2 || request.Env["A"] != "b" || request.TimeoutSeconds != 30 {
			t.Errorf("run request = %+v", request)
		}
		_, _ = w.Write([]byte(`{"id":"cmd-1","state":"exited","exitCode":0,"stdout":"ok","stderr":""}`))
	})
	result, err := client.Run(
		context.Background(),
		sandbox{ID: "sbx-abc123", DataPlaneURL: serverURL + "/dp/v1/", Secret: "mst_1"},
		runRequest{Cmd: []string{"/bin/true", "x"}, Env: map[string]string{"A": "b"}, TimeoutSeconds: 30},
	)
	if err != nil || result.ExitCode == nil || *result.ExitCode != 0 || result.Stdout != "ok" {
		t.Fatalf("run = %+v, %v", result, err)
	}
}

func TestMaritimeRESTClientRefusesUnsafeDataPlaneURLs(t *testing.T) {
	client := newRESTClient("https://api.maritime.test/v1", "mk_test", http.DefaultClient)
	for _, raw := range []string{"", "http://api-sbx.sbx.maritime.sh/v1", "https://user@host/v1", "https://host/v1?x=1"} {
		if _, err := client.Run(
			context.Background(), sandbox{ID: "sbx-1", DataPlaneURL: raw, Secret: "s"}, runRequest{},
		); err == nil {
			t.Errorf("run with data plane URL %q succeeded", raw)
		}
	}
	if _, err := client.Run(
		context.Background(), sandbox{ID: "sbx-1", DataPlaneURL: "https://host/v1"}, runRequest{},
	); err == nil {
		t.Error("run without a secret succeeded")
	}
}
