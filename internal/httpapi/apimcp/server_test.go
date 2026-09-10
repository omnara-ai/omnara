package apimcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/testutil"
)

const testBasePath = "/api/v1"

type echoedRequest struct {
	Method         string              `json:"method"`
	Path           string              `json:"path"`
	Query          map[string][]string `json:"query"`
	Body           json.RawMessage     `json:"body,omitempty"`
	ContentType    string              `json:"content_type"`
	IdempotencyKey string              `json:"idempotency_key"`
}

type allowOperations map[string]bool

func (a allowOperations) Allows(operationID string) bool { return a[operationID] }

func echoDispatch(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read dispatched body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(echoedRequest{
			Method:         r.Method,
			Path:           r.URL.Path,
			Query:          r.URL.Query(),
			Body:           body,
			ContentType:    r.Header.Get("Content-Type"),
			IdempotencyKey: r.Header.Get(idempotencyHeader),
		}); err != nil {
			t.Errorf("encode echoed request: %v", err)
		}
	})
}

func connect(t *testing.T, tools []Tool, options Options) *mcp.ClientSession {
	t.Helper()
	spec, err := openapi.GetSpec()
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	server, err := NewServer(spec, tools, options)
	if err != nil {
		t.Fatalf("build mcp server: %v", err)
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func echoOptions(t *testing.T) Options {
	t.Helper()
	return Options{Dispatch: echoDispatch(t), APIBasePath: testBasePath}
}

func callEcho(
	t *testing.T,
	session *mcp.ClientSession,
	name string,
	arguments map[string]any,
) (echoedRequest, *mcp.CallToolResult) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if result.IsError {
		return echoedRequest{}, result
	}
	text := testutil.RequireType[*mcp.TextContent](t, result.Content[0])
	var echoed echoedRequest
	if err := json.Unmarshal([]byte(text.Text), &echoed); err != nil {
		t.Fatalf("decode echoed request %q: %v", text.Text, err)
	}
	return echoed, result
}

func listedToolNames(t *testing.T, session *mcp.ClientSession) map[string]bool {
	t.Helper()
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	names := make(map[string]bool, len(listed.Tools))
	for _, tool := range listed.Tools {
		names[tool.Name] = true
	}
	return names
}

func TestManifestCompiles(t *testing.T) {
	t.Parallel()
	session := connect(t, Tools, echoOptions(t))
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(listed.Tools) != len(Tools) {
		t.Fatalf("listed %d tools, want %d", len(listed.Tools), len(Tools))
	}
	for _, tool := range listed.Tools {
		schema := testutil.RequireType[map[string]any](t, tool.InputSchema)
		encoded, err := json.Marshal(schema)
		if err != nil {
			t.Fatalf("marshal %s schema: %v", tool.Name, err)
		}
		if referenced := referencedComponentNames(encoded); len(referenced) > 0 {
			t.Errorf("%s schema still references components: %v", tool.Name, referenced)
		}
		if tool.Description == "" {
			t.Errorf("%s has no description", tool.Name)
		}
	}
}

func TestDispatchMapsArguments(t *testing.T) {
	t.Parallel()
	session := connect(t, Tools, echoOptions(t))

	echoed, result := callEcho(t, session, "agents_list", map[string]any{
		"orgID":     "org_1",
		"projectID": "proj_1",
		"limit":     25,
		"sort":      "created_at:desc",
	})
	if echoed.Method != http.MethodGet || echoed.Path != "/api/v1/orgs/org_1/projects/proj_1/agents" {
		t.Fatalf("unexpected request %s %s", echoed.Method, echoed.Path)
	}
	if got := echoed.Query["limit"]; len(got) != 1 || got[0] != "25" {
		t.Fatalf("limit query = %v", got)
	}
	if len(echoed.Body) > 0 {
		t.Fatalf("GET carried a body: %s", echoed.Body)
	}
	if result.StructuredContent == nil {
		t.Fatal("object responses should populate structuredContent")
	}

	echoed, _ = callEcho(t, session, "agents_launch", map[string]any{
		"orgID":     "org_1",
		"projectID": "proj_1",
		"name":      "Nightly",
		"message":   "hello",
	})
	if echoed.Method != http.MethodPost || echoed.ContentType != "application/json" {
		t.Fatalf("unexpected launch request %s %q", echoed.Method, echoed.ContentType)
	}
	var body map[string]any
	if err := json.Unmarshal(echoed.Body, &body); err != nil {
		t.Fatalf("decode launch body: %v", err)
	}
	if body["name"] != "Nightly" || body["message"] != "hello" {
		t.Fatalf("launch body = %v", body)
	}
	if _, leaked := body["orgID"]; leaked {
		t.Fatal("path params leaked into the body")
	}
	if echoed.IdempotencyKey == "" {
		t.Fatal("createAgent should carry a generated Idempotency-Key")
	}

	echoed, _ = callEcho(t, session, "secrets_list", map[string]any{
		"orgID":    "org_1",
		"metadata": map[string]any{"env": "prod"},
	})
	if got := echoed.Query["metadata[env]"]; len(got) != 1 || got[0] != "prod" {
		t.Fatalf("deepObject query = %v", echoed.Query)
	}

	_, result = callEcho(t, session, "agents_get", map[string]any{"orgID": "org_1", "projectID": "proj_1"})
	if !result.IsError {
		t.Fatal("missing path param should be a tool error")
	}
	_, result = callEcho(t, session, "agents_get", map[string]any{
		"orgID": "org_1", "projectID": "proj_1", "agentID": "agt_1", "bogus": true,
	})
	if !result.IsError {
		t.Fatal("unknown argument should be a tool error")
	}
}

func TestGrantsFilterDiscoveryAndCalls(t *testing.T) {
	t.Parallel()
	options := echoOptions(t)
	options.Grants = func(context.Context) (Grants, error) {
		return allowOperations{"ListAgents": true, "GetCurrentUser": true}, nil
	}
	session := connect(t, Tools, options)

	names := listedToolNames(t, session)
	if !names["agents_list"] || !names["whoami"] {
		t.Fatalf("granted tools missing from %v", names)
	}
	if names["agents_launch"] || names["agents_cancel"] {
		t.Fatalf("ungranted tools listed in %v", names)
	}

	_, result := callEcho(t, session, "agents_launch", map[string]any{"orgID": "org_1", "projectID": "proj_1"})
	if !result.IsError {
		t.Fatal("calling a hidden tool should return a tool error")
	}
	echoed, _ := callEcho(t, session, "agents_list", map[string]any{"orgID": "org_1", "projectID": "proj_1"})
	if echoed.Method != http.MethodGet {
		t.Fatalf("granted tool did not dispatch: %+v", echoed)
	}
}

func TestManifestRejectsUnknownOperation(t *testing.T) {
	t.Parallel()
	spec, err := openapi.GetSpec()
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	_, err = NewServer(spec, []Tool{{Name: "nope", OperationID: "doesNotExist"}}, echoOptions(t))
	if err == nil {
		t.Fatal("expected an error for an unknown operation id")
	}
}
