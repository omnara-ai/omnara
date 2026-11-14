package daytona

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDaytonaRESTClientUsesMainAndToolboxAPIs(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
			http.Error(w, "test handler failed", http.StatusInternalServerError)
			return
		}
		switch r.URL.Path {
		case "/snapshots/team-snapshot":
			_ = json.NewEncoder(w).Encode(snapshot{Name: "team-snapshot", State: "active", CPU: 1, Memory: 1})
		case "/sandbox":
			if r.Method != http.MethodPost {
				t.Errorf("create sandbox method = %s", r.Method)
				http.Error(w, "test handler failed", http.StatusInternalServerError)
				return
			}
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode create sandbox request: %v", err)
				http.Error(w, "test handler failed", http.StatusInternalServerError)
				return
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Errorf("marshal create sandbox request: %v", err)
				http.Error(w, "test handler failed", http.StatusInternalServerError)
				return
			}
			var request createSandboxRequest
			if err := json.Unmarshal(raw, &request); err != nil {
				t.Errorf("parse create sandbox request: %v", err)
				http.Error(w, "test handler failed", http.StatusInternalServerError)
				return
			}
			if request.Name != "machine-1" || request.Snapshot != "team-snapshot" || request.Target != "us" ||
				request.Env["TEST"] != "value" || request.Labels["omnara-machine"] != "machine-1" ||
				request.AutoStopInterval != 0 || request.AutoDeleteInterval != -1 {
				t.Errorf("create sandbox request = %+v", request)
				http.Error(w, "test handler failed", http.StatusInternalServerError)
				return
			}
			if _, ok := body["autoStopInterval"]; !ok {
				t.Error("create sandbox request omitted autoStopInterval")
				http.Error(w, "test handler failed", http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(sandbox{ID: "sandbox-1"})
		case "/toolbox/sandbox-1/process/session/session-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"sessionId": "session-1", "commands": []any{}})
		case "/toolbox/sandbox-1/process/session/session-1/exec":
			if r.Method != http.MethodPost {
				t.Errorf("execute session method = %s", r.Method)
				http.Error(w, "test handler failed", http.StatusInternalServerError)
				return
			}
			var request sessionExecuteRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode execute session request: %v", err)
				http.Error(w, "test handler failed", http.StatusInternalServerError)
				return
			}
			if request.Command != "run daemon" || !request.RunAsync {
				t.Errorf("execute session request = %+v", request)
				http.Error(w, "test handler failed", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(sessionExecuteResponse{CommandID: "command-1"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newRESTClient(server.URL, "test-token", server.Client())
	if _, err := client.GetSnapshot(context.Background(), "team-snapshot"); err != nil {
		t.Fatalf("get snapshot: %v", err)
	}
	created, err := client.CreateSandbox(context.Background(), createSandboxRequest{
		Name:               "machine-1",
		Snapshot:           "team-snapshot",
		Env:                map[string]string{"TEST": "value"},
		Labels:             map[string]string{"omnara-machine": "machine-1"},
		Target:             "us",
		AutoStopInterval:   0,
		AutoDeleteInterval: -1,
	})
	if err != nil || created.ID != "sandbox-1" {
		t.Fatalf("create sandbox = %+v error %v", created, err)
	}
	session, found, err := client.GetSession(
		context.Background(),
		sandbox{ID: "sandbox-1", ToolboxProxyURL: server.URL + "/toolbox"},
		"session-1",
	)
	if err != nil || !found || len(session.Commands) != 0 {
		t.Fatalf("get session = %+v found %v error %v", session, found, err)
	}
	executed, err := client.ExecuteSessionCommand(
		context.Background(),
		sandbox{ID: "sandbox-1", ToolboxProxyURL: server.URL + "/toolbox"},
		"session-1",
		sessionExecuteRequest{Command: "run daemon", RunAsync: true},
	)
	if err != nil || executed.CommandID != "command-1" {
		t.Fatalf("execute session = %+v error %v", executed, err)
	}
}

func TestDaytonaRESTClientListsSandboxesWithCursorAndStateFilters(t *testing.T) {
	nextCursor := "next-page"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/sandbox" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
			http.Error(w, "test handler failed", http.StatusInternalServerError)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
			http.Error(w, "test handler failed", http.StatusInternalServerError)
			return
		}
		query := r.URL.Query()
		if query.Get("cursor") != "current-page" || query.Get("limit") != "200" {
			t.Errorf("pagination query = %q", r.URL.RawQuery)
			http.Error(w, "test handler failed", http.StatusInternalServerError)
			return
		}
		states := query["states"]
		if len(states) != 2 || states[0] != "started" || states[1] != "stopped" {
			t.Errorf("state filters = %#v", states)
			http.Error(w, "test handler failed", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{
			"items":[{
				"id":"sandbox-1",
				"organizationId":"organization-1",
				"name":"omnara-mch-example",
				"target":"us",
				"user":"daytona",
				"state":"started",
				"public":false,
				"cpu":2,
				"gpu":0,
				"memory":4,
				"disk":10,
				"labels":{"omnara-machine":"omnara-mch-example"},
				"toolboxProxyUrl":"https://proxy.app.daytona.io/toolbox"
			}],
			"nextCursor":"next-page"
		}`))
	}))
	defer server.Close()

	client := newRESTClient(server.URL, "test-token", server.Client())
	page, err := client.ListSandboxes(context.Background(), listSandboxesQuery{
		Cursor: "current-page",
		Limit:  200,
		States: []sandboxState{sandboxStateStarted, sandboxStateStopped},
	})
	if err != nil {
		t.Fatalf("list sandboxes: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "sandbox-1" ||
		page.Items[0].Name != "omnara-mch-example" ||
		page.Items[0].State != sandboxStateStarted ||
		page.Items[0].Target != "us" ||
		page.Items[0].Labels["omnara-machine"] != "omnara-mch-example" ||
		page.NextCursor == nil || *page.NextCursor != nextCursor {
		t.Fatalf("list response = %+v", page)
	}
}

func TestDaytonaRESTClientManagesMachineTokenSecrets(t *testing.T) {
	var deleted []string
	remounted := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/secret":
			var request createSecretRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Name != "omnara-mch-a" ||
				request.Value != "token" || len(request.Hosts) != 1 || request.Hosts[0] != "api.omnara.test" {
				t.Errorf("create secret request = %+v err %v", request, err)
			}
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && r.URL.Path == "/secret/paginated":
			if r.URL.Query().Get("name") != "omnara-mch-a" {
				t.Errorf("find secret query = %q", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"items":[{"id":"id-ab","name":"omnara-mch-ab"},{"id":"id-a","name":"omnara-mch-a"}]}`))
		case r.Method == http.MethodDelete:
			deleted = append(deleted, r.URL.Path)
			http.NotFound(w, r)
		case r.Method == http.MethodPut && r.URL.Path == "/sandbox/sandbox-1/secrets":
			var request map[string][]map[string]string
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request["secrets"]) != 1 ||
				request["secrets"][0]["OMNARA_MACHINE_TOKEN"] != "omnara-mch-a" {
				t.Errorf("update sandbox secrets request = %+v err %v", request, err)
			}
			remounted = true
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			http.Error(w, "test handler failed", http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	client := newRESTClient(server.URL, "test-token", server.Client())
	ctx := context.Background()
	if err := client.CreateSecret(ctx, createSecretRequest{
		Name: "omnara-mch-a", Value: "token", Hosts: []string{"api.omnara.test"},
	}); err != nil {
		t.Fatalf("create secret: %v", err)
	}
	id, found, err := client.FindSecret(ctx, "omnara-mch-a")
	if err != nil || !found || id != "id-a" {
		t.Fatalf("find secret = %q %v %v, want the exact name match", id, found, err)
	}
	if err := client.DeleteSecret(ctx, id); err != nil || len(deleted) != 1 || deleted[0] != "/secret/id-a" {
		t.Fatalf("delete secret error = %v requests %v, want a quiet 404 by id", err, deleted)
	}
	err = client.UpdateSandboxSecrets(ctx, "sandbox-1", []map[string]string{{"OMNARA_MACHINE_TOKEN": "omnara-mch-a"}})
	if err != nil || !remounted {
		t.Fatalf("update sandbox secrets error = %v remounted %v", err, remounted)
	}
}
