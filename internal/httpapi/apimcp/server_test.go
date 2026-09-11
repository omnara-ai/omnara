package apimcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/testutil"
)

const (
	testBasePath = "/api/v1"
	testOrgID    = "org_abcdefghijklmnopqrstuvwxyz"
	testProject  = "proj_abcdefghijklmnopqrstuvwxyz"
	testAgentID  = "agt_abcdefghijklmnopqrstuvwxyz"
	testConfigID = "acfg_abcdefghijklmnopqrstuvwxyz"
)

type echoedRequest struct {
	Method         string              `json:"method"`
	Path           string              `json:"path"`
	Query          map[string][]string `json:"query"`
	Body           json.RawMessage     `json:"body,omitempty"`
	ContentType    string              `json:"content_type"`
	IdempotencyKey string              `json:"idempotency_key"`
}

type allowOperations map[string]bool

func (a allowOperations) Allows(_ context.Context, operationID string) (bool, error) {
	return a[operationID], nil
}

type failingGrants struct{ err error }

func (f failingGrants) Allows(context.Context, string) (bool, error) { return false, f.err }

func staticGrants(grants Grants) GrantResolver {
	return func(context.Context) Grants { return grants }
}

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

func loadSpec(t *testing.T) *openapi3.T {
	t.Helper()
	spec, err := openapi.GetSpec()
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	return spec
}

func newServer(t *testing.T, tools []Tool, options Options) *mcp.Server {
	t.Helper()
	server, err := NewServer(loadSpec(t), tools, options)
	if err != nil {
		t.Fatalf("build mcp server: %v", err)
	}
	return server
}

func connect(t *testing.T, tools []Tool, options Options) *mcp.ClientSession {
	t.Helper()
	server := newServer(t, tools, options)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	return connectClient(t, clientTransport)
}

func connectClient(t *testing.T, transport mcp.Transport) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(context.Background(), transport, nil)
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

func listTools(t *testing.T, session *mcp.ClientSession) *mcp.ListToolsResult {
	t.Helper()
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	return listed
}

func listedToolsByName(t *testing.T, session *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()
	listed := listTools(t, session)
	tools := make(map[string]*mcp.Tool, len(listed.Tools))
	for _, tool := range listed.Tools {
		tools[tool.Name] = tool
	}
	return tools
}

func containsKey(value any, key string) bool {
	switch typed := value.(type) {
	case map[string]any:
		if _, has := typed[key]; has {
			return true
		}
		for _, child := range typed {
			if containsKey(child, key) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsKey(child, key) {
				return true
			}
		}
	}
	return false
}

func TestManifestCompiles(t *testing.T) {
	t.Parallel()
	session := connect(t, Tools, echoOptions(t))
	listed := listTools(t, session)
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
		if containsKey(schema, defaultKey) {
			t.Errorf("%s schema carries a default, which the sdk would inject into the request", tool.Name)
		}
		if tool.Description == "" {
			t.Errorf("%s has no description", tool.Name)
		}
	}
}

func TestAnnotationsDeriveFromMethod(t *testing.T) {
	t.Parallel()
	session := connect(t, Tools, echoOptions(t))
	tools := listedToolsByName(t, session)
	cases := []struct {
		name        string
		readOnly    bool
		idempotent  bool
		destructive *bool
	}{
		{name: "agents_list", readOnly: true, idempotent: true, destructive: new(false)},
		{name: "agents_launch"},
		{name: "secrets_update"},
		{name: "pools_update", idempotent: true},
		{name: "models_delete", idempotent: true, destructive: new(true)},
		{name: "agents_cancel", destructive: new(true)},
		{name: "agents_archive", destructive: new(true)},
	}
	for _, tc := range cases {
		annotations := tools[tc.name].Annotations
		if annotations.ReadOnlyHint != tc.readOnly || annotations.IdempotentHint != tc.idempotent {
			t.Errorf("%s readOnly=%v idempotent=%v, want %v/%v",
				tc.name, annotations.ReadOnlyHint, annotations.IdempotentHint, tc.readOnly, tc.idempotent)
		}
		switch {
		case tc.destructive == nil && annotations.DestructiveHint != nil:
			t.Errorf("%s destructiveHint=%v, want unset", tc.name, *annotations.DestructiveHint)
		case tc.destructive != nil && (annotations.DestructiveHint == nil || *annotations.DestructiveHint != *tc.destructive):
			t.Errorf("%s destructiveHint=%v, want %v", tc.name, annotations.DestructiveHint, *tc.destructive)
		}
	}
}

func TestDispatchMapsArguments(t *testing.T) {
	t.Parallel()
	session := connect(t, Tools, echoOptions(t))

	echoed, result := callEcho(t, session, "agents_list", map[string]any{
		"orgID":     testOrgID,
		"projectID": testProject,
		"limit":     25.0,
		"sort":      "-created_at",
	})
	if echoed.Method != http.MethodGet || echoed.Path != "/api/v1/orgs/"+testOrgID+"/projects/"+testProject+"/agents" {
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
		"orgID":     testOrgID,
		"projectID": testProject,
		"config":    testConfigID,
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
		"orgID":    testOrgID,
		"metadata": map[string]any{"env": "prod"},
	})
	if got := echoed.Query["metadata[env]"]; len(got) != 1 || got[0] != "prod" {
		t.Fatalf("deepObject query = %v", echoed.Query)
	}
}

func TestInputSchemaIsEnforced(t *testing.T) {
	t.Parallel()
	session := connect(t, Tools, echoOptions(t))
	cases := []struct {
		name      string
		tool      string
		arguments map[string]any
	}{
		{name: "missing path param", tool: "agents_get", arguments: map[string]any{
			"orgID": testOrgID, "projectID": testProject,
		}},
		{name: "unknown argument", tool: "agents_get", arguments: map[string]any{
			"orgID": testOrgID, "projectID": testProject, "agentID": testAgentID, "bogus": true,
		}},
		{name: "wrong query type", tool: "agents_list", arguments: map[string]any{
			"orgID": testOrgID, "projectID": testProject, "limit": "25",
		}},
		{name: "wrong body type", tool: "agents_launch", arguments: map[string]any{
			"orgID": testOrgID, "projectID": testProject, "config": testConfigID, "name": 7,
		}},
	}
	for _, tc := range cases {
		_, result := callEcho(t, session, tc.tool, tc.arguments)
		if !result.IsError {
			t.Errorf("%s: expected a tool error", tc.name)
		}
	}
}

func TestGrantsFilterDiscoveryAndCalls(t *testing.T) {
	t.Parallel()
	options := echoOptions(t)
	options.Grants = staticGrants(allowOperations{"ListAgents": true, "GetCurrentUser": true})
	session := connect(t, Tools, options)

	listed := listTools(t, session)
	if listed.CacheScope != cacheScopePrivate {
		t.Fatalf("cacheScope = %q, want %q", listed.CacheScope, cacheScopePrivate)
	}
	names := make(map[string]bool, len(listed.Tools))
	for _, tool := range listed.Tools {
		names[tool.Name] = true
	}
	if !names["agents_list"] || !names["whoami"] {
		t.Fatalf("granted tools missing from %v", names)
	}
	if names["agents_launch"] || names["agents_cancel"] {
		t.Fatalf("ungranted tools listed in %v", names)
	}

	ctx := context.Background()
	_, hiddenErr := session.CallTool(ctx, &mcp.CallToolParams{Name: "agents_launch", Arguments: map[string]any{
		"orgID": testOrgID, "projectID": testProject,
	}})
	_, unknownErr := session.CallTool(ctx, &mcp.CallToolParams{Name: "does_not_exist"})
	if hiddenErr == nil || unknownErr == nil {
		t.Fatalf("hidden/unknown tools must be protocol errors, got %v / %v", hiddenErr, unknownErr)
	}
	hiddenMessage := strings.ReplaceAll(hiddenErr.Error(), "agents_launch", "does_not_exist")
	if hiddenMessage != unknownErr.Error() {
		t.Fatalf("hidden tool error %q is distinguishable from unknown tool error %q", hiddenErr, unknownErr)
	}
	echoed, _ := callEcho(t, session, "agents_list", map[string]any{"orgID": testOrgID, "projectID": testProject})
	if echoed.Method != http.MethodGet {
		t.Fatalf("granted tool did not dispatch: %+v", echoed)
	}
}

func TestGrantResolutionFailuresAreOpaque(t *testing.T) {
	t.Parallel()
	options := echoOptions(t)
	options.Grants = staticGrants(failingGrants{err: errors.New("failed to connect to host=10.0.3.7 (SQLSTATE 08006)")})
	session := connect(t, Tools, options)
	ctx := context.Background()
	for _, call := range []func() error{
		func() error { _, err := session.ListTools(ctx, nil); return err },
		func() error {
			_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "whoami"})
			return err
		},
	} {
		err := call()
		if err == nil {
			t.Fatal("expected a protocol error")
		}
		if strings.Contains(err.Error(), "SQLSTATE") || !strings.Contains(err.Error(), "internal server error") {
			t.Fatalf("error %q leaks the underlying failure", err)
		}
	}
}

func TestDispatchPanicDoesNotKillTheServer(t *testing.T) {
	t.Parallel()
	calls := 0
	options := Options{APIBasePath: testBasePath, Dispatch: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			panic("probe panic from api handler")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})}
	httpServer := httptest.NewServer(NewHandler(newServer(t, Tools, options)))
	t.Cleanup(httpServer.Close)
	session := connectClient(t, &mcp.StreamableClientTransport{Endpoint: httpServer.URL + Path})

	ctx := context.Background()
	_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "whoami"})
	if err == nil || !strings.Contains(err.Error(), "internal server error") {
		t.Fatalf("panicking dispatch returned %v, want an internal protocol error", err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "whoami"})
	if err != nil || result.IsError {
		t.Fatalf("server did not survive the panic: err=%v result=%+v", err, result)
	}
}

func TestManifestRejectsUnknownOperation(t *testing.T) {
	t.Parallel()
	_, err := NewServer(loadSpec(t), []Tool{{Name: "nope", OperationID: "doesNotExist"}}, echoOptions(t))
	if err == nil {
		t.Fatal("expected an error for an unknown operation id")
	}
}
