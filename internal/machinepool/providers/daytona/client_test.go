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
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/snapshots/team-snapshot":
			_ = json.NewEncoder(w).Encode(snapshot{Name: "team-snapshot", State: "active", CPU: 1, Memory: 1})
		case "/sandbox":
			if r.Method != http.MethodPost {
				t.Fatalf("create sandbox method = %s", r.Method)
			}
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode create sandbox request: %v", err)
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal create sandbox request: %v", err)
			}
			var request createSandboxRequest
			if err := json.Unmarshal(raw, &request); err != nil {
				t.Fatalf("parse create sandbox request: %v", err)
			}
			if request.Name != "machine-1" || request.Snapshot != "team-snapshot" || request.Target != "us" ||
				request.Env["TEST"] != "value" || request.Labels["omnara-machine"] != "machine-1" ||
				request.AutoStopInterval != 0 || request.AutoDeleteInterval != -1 {
				t.Fatalf("create sandbox request = %+v", request)
			}
			if _, ok := body["autoStopInterval"]; !ok {
				t.Fatal("create sandbox request omitted autoStopInterval")
			}
			_ = json.NewEncoder(w).Encode(sandbox{ID: "sandbox-1"})
		case "/toolbox/sandbox-1/process/session/session-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"sessionId": "session-1", "commands": []any{}})
		case "/toolbox/sandbox-1/process/session/session-1/exec":
			if r.Method != http.MethodPost {
				t.Fatalf("execute session method = %s", r.Method)
			}
			var request sessionExecuteRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode execute session request: %v", err)
			}
			if request.Command != "run daemon" || !request.RunAsync {
				t.Fatalf("execute session request = %+v", request)
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
