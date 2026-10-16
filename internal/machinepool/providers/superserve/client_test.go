package superserve

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
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	return newRESTClient(
		regionEndpoints{apiBaseURL: server.URL, sandboxHost: strings.TrimPrefix(server.URL, "https://")},
		"test-key",
		server.Client(),
	)
}

func TestSuperserveEndpointsFollowTheAPIKeyRegion(t *testing.T) {
	suffix := strings.Repeat("a", 32)
	for key, want := range map[string]regionEndpoints{
		"ss_live_use_" + suffix: knownRegions["use"],
		"ss_live_usw_" + suffix: knownRegions["usw"],
		"ss_live_xyz_" + suffix: knownRegions["use"],
		"not-a-superserve-key":  knownRegions["use"],
	} {
		if got := endpointsForAPIKey(key); got != want {
			t.Errorf("endpoints for %q = %+v, want %+v", key, got, want)
		}
	}
}

func TestSuperserveRESTClientSendsTheAPIKeyAndDecodesBareLists(t *testing.T) {
	client := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(apiKeyHeader) != "test-key" {
			t.Errorf("api key header = %q", r.Header.Get(apiKeyHeader))
		}
		if r.URL.Path != "/templates" || r.URL.Query().Get("q") != "superserve/base" || r.URL.Query().Get("limit") != "200" {
			t.Errorf("templates request = %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`[{"id":"tpl-1","name":"superserve/base","status":"ready","vcpu":1,"memory_mib":1024}]`))
	})
	templates, err := client.ListTemplates(context.Background(), "superserve/base")
	if err != nil {
		t.Fatalf("list templates: %v", err)
	}
	if len(templates) != 1 || templates[0].Name != "superserve/base" ||
		templates[0].VCPU != 1 || templates[0].MemoryMiB != 1024 {
		t.Fatalf("templates = %+v, want the base template", templates)
	}
}

func TestSuperserveRESTClientDecodesItemEnvelopes(t *testing.T) {
	client := newTestRESTClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":"tpl-1","name":"custom","status":"building"}]}`))
	})
	templates, err := client.ListTemplates(context.Background(), "custom")
	if err != nil {
		t.Fatalf("list templates: %v", err)
	}
	if len(templates) != 1 || templates[0].Status != "building" {
		t.Fatalf("templates = %+v, want the enveloped template", templates)
	}
}

func TestSuperserveRESTClientCreatesSandboxes(t *testing.T) {
	var received map[string]json.RawMessage
	client := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sandboxes" {
			t.Errorf("create request = %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode create request: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"sb-123","name":"omnara-mch-abc","status":"active","vcpu_count":1,"memory_mib":1024,` +
			`"metadata":{"omnara-machine":"mch_1"},"access_token":"tok","routing_hint":"hint"}`))
	})
	created, err := client.CreateSandbox(context.Background(), createSandboxRequest{
		Name:         "omnara-mch-abc",
		FromTemplate: "superserve/base",
		Metadata:     map[string]string{"omnara-machine": "mch_1"},
	})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if created.ID != "sb-123" || created.AccessToken != "tok" || created.RoutingHint != "hint" ||
		created.Metadata["omnara-machine"] != "mch_1" || created.VCPUCount != 1 {
		t.Fatalf("created sandbox = %+v", created)
	}
	for _, key := range []string{"env_vars", "timeout_seconds", "auto_delete_seconds"} {
		if _, ok := received[key]; ok {
			t.Fatalf("create request sent %s = %s", key, received[key])
		}
	}
	var fromTemplate string
	if err := json.Unmarshal(received["from_template"], &fromTemplate); err != nil || fromTemplate != "superserve/base" {
		t.Fatalf("create request from_template = %s (%v)", received["from_template"], err)
	}
}

func TestSuperserveRESTClientPagesThroughMetadataFilteredSandboxes(t *testing.T) {
	var offsets []string
	client := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if r.URL.Path != "/sandboxes" || query.Get("metadata.omnara-machine") != "mch_1" ||
			query.Get("metadata.omnara-installation") != "ins_1" || query.Get("limit") != "200" {
			t.Errorf("list request = %s", r.URL.String())
		}
		offsets = append(offsets, query.Get("offset"))
		if query.Get("offset") == "0" {
			items := make([]string, listPageSize)
			for i := range items {
				items[i] = `{"id":"sb-page"}`
			}
			_, _ = w.Write([]byte("[" + strings.Join(items, ",") + "]"))
			return
		}
		_, _ = w.Write([]byte(`[{"id":"sb-last"}]`))
	})
	sandboxes, err := client.ListSandboxes(context.Background(), map[string]string{
		"omnara-installation": "ins_1",
		"omnara-machine":      "mch_1",
	})
	if err != nil {
		t.Fatalf("list sandboxes: %v", err)
	}
	if len(sandboxes) != listPageSize+1 || sandboxes[listPageSize].ID != "sb-last" {
		t.Fatalf("sandboxes = %d, want both pages", len(sandboxes))
	}
	if strings.Join(offsets, ",") != "0,200" {
		t.Fatalf("offsets = %v, want every page read", offsets)
	}
}

func TestSuperserveRESTClientTreatsAMissingSandboxAsAbsent(t *testing.T) {
	client := newTestRESTClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"Sandbox not found"}}`))
	})
	current, found, err := client.GetSandbox(context.Background(), "sb-gone")
	if err != nil || found || current.ID != "" {
		t.Fatalf("get sandbox = %+v found %v error %v, want an absent sandbox", current, found, err)
	}
	if err := client.DeleteSandbox(context.Background(), "sb-gone"); err != nil {
		t.Fatalf("delete missing sandbox: %v", err)
	}
}

func TestSuperserveRESTClientActivatesAndDeletes(t *testing.T) {
	var requests []string
	client := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.EscapedPath())
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(`{"id":"sb/1","status":"active","access_token":"fresh"}`))
	})
	activated, err := client.ActivateSandbox(context.Background(), "sb/1")
	if err != nil || activated.AccessToken != "fresh" || activated.Status != sandboxStatusActive {
		t.Fatalf("activate sandbox = %+v, %v", activated, err)
	}
	if err := client.DeleteSandbox(context.Background(), "sb/1"); err != nil {
		t.Fatalf("delete sandbox: %v", err)
	}
	if strings.Join(requests, ";") != "POST /sandboxes/sb%2F1/activate;DELETE /sandboxes/sb%2F1" {
		t.Fatalf("requests = %v, want escaped ids", requests)
	}
}

func TestSuperserveRESTClientExecUsesTheDataPlane(t *testing.T) {
	var received execRequest
	client := newTestRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/exec" {
			t.Errorf("exec request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get(apiKeyHeader) != "" {
			t.Error("exec request leaked the API key to the data plane")
		}
		if r.Header.Get(sandboxIDHeader) != "sb-123" || r.Header.Get(accessTokenHeader) != "tok" ||
			r.Header.Get(routingHintHeader) != "hint" {
			t.Errorf("exec headers = %v", r.Header)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode exec request: %v", err)
		}
		_, _ = w.Write([]byte(`{"stdout":"ok\n","stderr":"","exit_code":3}`))
	})
	result, err := client.Exec(
		context.Background(),
		execSession{SandboxID: "sb-123", AccessToken: "tok", RoutingHint: "hint"},
		execRequest{Command: "/bin/sh", Args: []string{"-c", "true"}, Env: map[string]string{"A": "b"}, TimeoutSeconds: 30},
	)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if result.ExitCode != 3 || result.Stdout != "ok\n" {
		t.Fatalf("exec result = %+v", result)
	}
	if received.Command != "/bin/sh" || len(received.Args) != 2 || received.Env["A"] != "b" ||
		received.TimeoutSeconds != 30 {
		t.Fatalf("exec request = %+v", received)
	}
}

func TestSuperserveRESTClientReportsAPIErrorsWithRetryAfter(t *testing.T) {
	client := newTestRESTClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"too_many_sandboxes","message":"limit reached"}}`))
	})
	_, err := client.CreateSandbox(context.Background(), createSandboxRequest{Name: "x"})
	var apiErr apiError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooManyRequests || apiErr.Code != "too_many_sandboxes" {
		t.Fatalf("create error = %v, want the API error carrying status and code", err)
	}
	if !strings.Contains(err.Error(), "limit reached") {
		t.Fatalf("create error = %v, want the message included", err)
	}
	if delay, ok := providers.RetryAfter(err); !ok || delay != 7*time.Second {
		t.Fatalf("retry after = %v %v, want 7s", delay, ok)
	}
	if !transientAPIError(err) || rejectedBeforeCreate(err) {
		t.Fatal("a capacity limit must be retried, not treated as permanent")
	}
}

func TestSuperserveRESTClientToleratesPlainTextErrors(t *testing.T) {
	client := newTestRESTClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("invalid access token"))
	})
	_, err := client.Exec(
		context.Background(),
		execSession{SandboxID: "sb-123", AccessToken: "stale"},
		execRequest{Command: "true"},
	)
	if apiStatusCode(err) != http.StatusUnauthorized || !sandboxUnavailable(err) {
		t.Fatalf("exec error = %v, want a 401 the caller can fix with activate", err)
	}
}
