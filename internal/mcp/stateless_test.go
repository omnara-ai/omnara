package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/omnara-ai/omnara/internal/mcp"
	"github.com/omnara-ai/omnara/internal/testutil/mcptest"
)

func statelessConn(endpoint string) mcp.Conn {
	return mcp.Conn{EndpointURL: endpoint, ProtocolVersion: mcp.StatelessProtocolVersion}
}

func TestDiscoverAgainstStatelessServer(t *testing.T) {
	ts := mcptest.NewStatelessServer(t, mcptest.StatelessOptions{
		JSONResponse: true,
		ToolsTTL:     90 * time.Second,
		CacheScope:   "private",
	})
	client := newClient(t)

	result, err := client.Discover(context.Background(), mcp.Conn{EndpointURL: ts.URL}, mcp.StatelessProtocolVersion)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if result.ProtocolVersion != mcp.StatelessProtocolVersion {
		t.Fatalf("negotiated protocol version = %q", result.ProtocolVersion)
	}
	var capabilities map[string]json.RawMessage
	if err := json.Unmarshal(result.ServerCapabilities, &capabilities); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if _, ok := capabilities["tools"]; !ok {
		t.Fatalf("capabilities = %s, want tools", result.ServerCapabilities)
	}
	var info struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(result.ServerInfo, &info); err != nil || info.Name != "mcptest" {
		t.Fatalf("server info = %s (err %v), want mcptest", result.ServerInfo, err)
	}
	if result.Cache.TTLMs != 90_000 || result.Cache.CacheScope != "private" {
		t.Fatalf("cache hint = %+v", result.Cache)
	}
	if ts.DiscoverCalls.Load() != 1 {
		t.Fatalf("discover calls = %d", ts.DiscoverCalls.Load())
	}
}

func TestStatelessListToolsAndCallTool(t *testing.T) {
	for _, jsonResponse := range []bool{true, false} {
		t.Run(fmt.Sprintf("json=%t", jsonResponse), func(t *testing.T) {
			ts := mcptest.NewStatelessServer(t, mcptest.StatelessOptions{JSONResponse: jsonResponse, ToolsTTL: time.Minute})
			client := newClient(t)
			conn := statelessConn(ts.URL)

			page, err := client.ListTools(silentCtx(), conn, 7, "")
			if err != nil {
				t.Fatalf("list tools: %v", err)
			}
			names := map[string]bool{}
			for _, tool := range page.Tools {
				names[tool.Name] = true
			}
			if !names["greet"] || !names["noisy"] {
				t.Fatalf("tools = %v", names)
			}
			if jsonResponse && (page.TTLMs != 60_000 || page.CacheScope != "public") {
				t.Fatalf("cache hints = ttl %d scope %q", page.TTLMs, page.CacheScope)
			}

			args, _ := json.Marshal(map[string]any{"name": "Ada"})
			result, err := client.CallTool(silentCtx(), conn, 8, mcp.ToolCall{Name: "greet", Arguments: args})
			if err != nil {
				t.Fatalf("call tool: %v", err)
			}
			if got := contentText(t, result); got != "Hi Ada" {
				t.Fatalf("greet result = %q", got)
			}
			if ts.ListToolsCalls.Load() != 1 || ts.CallToolCalls.Load() != 1 {
				t.Fatalf("calls: list=%d call=%d", ts.ListToolsCalls.Load(), ts.CallToolCalls.Load())
			}
		})
	}
}

func TestStatelessRequestsCarryMetaAndHeaders(t *testing.T) {
	var mu sync.Mutex
	var headers http.Header
	var body map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		headers = r.Header.Clone()
		raw, _ := readBody(r)
		_ = json.Unmarshal(raw, &body)
		id := extractID(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(
			w,
			`{"jsonrpc":"2.0","id":%s,"result":{"resultType":"complete","tools":[],"ttlMs":5,"cacheScope":"public"}}`,
			id,
		)
	}))
	t.Cleanup(ts.Close)

	client := newClient(t)
	conn := statelessConn(ts.URL)
	conn.MCPSessionID = "must-not-be-sent"
	if _, err := client.ListTools(context.Background(), conn, 3, ""); err != nil {
		t.Fatalf("list tools: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := headers.Get("Mcp-Protocol-Version"); got != mcp.StatelessProtocolVersion {
		t.Fatalf("MCP-Protocol-Version = %q", got)
	}
	if got := headers.Get("Mcp-Method"); got != "tools/list" {
		t.Fatalf("Mcp-Method = %q", got)
	}
	if got := headers.Get("Mcp-Session-Id"); got != "" {
		t.Fatalf("Mcp-Session-Id = %q, want none", got)
	}
	params, _ := body["params"].(map[string]any)
	meta, _ := params["_meta"].(map[string]any)
	if meta["io.modelcontextprotocol/protocolVersion"] != mcp.StatelessProtocolVersion {
		t.Fatalf("_meta = %v", meta)
	}
	if _, ok := meta["io.modelcontextprotocol/clientCapabilities"].(map[string]any); !ok {
		t.Fatalf("_meta clientCapabilities missing: %v", meta)
	}
	info, _ := meta["io.modelcontextprotocol/clientInfo"].(map[string]any)
	if info["name"] != "omnara-mcp" {
		t.Fatalf("_meta clientInfo = %v", info)
	}
}

func TestStatelessCallToolMirrorsNameAndParamHeaders(t *testing.T) {
	var mu sync.Mutex
	var headers http.Header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headers = r.Header.Clone()
		mu.Unlock()
		raw, _ := readBody(r)
		id := extractID(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(
			w,
			`{"jsonrpc":"2.0","id":%s,"result":{"resultType":"complete","content":[{"type":"text","text":"ok"}]}}`,
			id,
		)
	}))
	t.Cleanup(ts.Close)

	client := newClient(t)
	conn := statelessConn(ts.URL)
	args := json.RawMessage(`{"region":"us-west1","nested":{"greeting":"Hello, 世界"},"count":42,"flag":true}`)
	_, err := client.CallTool(context.Background(), conn, 9, mcp.ToolCall{
		Name:      "execute sql",
		Arguments: args,
		Headers: []mcp.ToolHeader{
			{Name: "Region", Path: []string{"region"}},
			{Name: "Greeting", Path: []string{"nested", "greeting"}},
			{Name: "Count", Path: []string{"count"}},
			{Name: "Flag", Path: []string{"flag"}},
			{Name: "Missing", Path: []string{"absent"}},
		},
	})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := map[string]string{
		"Mcp-Method":         "tools/call",
		"Mcp-Name":           "execute sql",
		"Mcp-Param-Region":   "us-west1",
		"Mcp-Param-Greeting": "=?base64?SGVsbG8sIOS4lueVjA==?=",
		"Mcp-Param-Count":    "42",
		"Mcp-Param-Flag":     "true",
	}
	for name, value := range want {
		if got := headers.Get(name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}
	if _, present := headers["Mcp-Param-Missing"]; present {
		t.Errorf("Mcp-Param-Missing should be omitted when the argument is absent")
	}
}

func TestDiscoverAgainstLegacyServerIndicatesLegacy(t *testing.T) {
	ts := mcptest.NewJSONServer(t)
	client := newClient(t)

	_, err := client.Discover(context.Background(), mcp.Conn{EndpointURL: ts.URL}, mcp.StatelessProtocolVersion)
	if err == nil {
		t.Fatal("expected legacy server to reject server/discover")
	}
	if mcp.IsStatelessProtocolError(err) {
		t.Fatalf("legacy rejection classified as stateless protocol error: %v", err)
	}
	if !mcp.IndicatesLegacyServer(err) {
		t.Fatalf("expected legacy server indication, got %v", err)
	}
}

func TestUnsupportedProtocolVersionErrorIsDecoded(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := readBody(r)
		id := extractID(raw)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(
			w,
			`{"jsonrpc":"2.0","id":%s,"error":{"code":-32022,"message":"Unsupported protocol version",`+
				`"data":{"supported":["2027-01-01","2025-11-25"],"requested":"2026-07-28"}}}`,
			id,
		)
	}))
	t.Cleanup(ts.Close)

	client := newClient(t)
	_, err := client.Discover(context.Background(), mcp.Conn{EndpointURL: ts.URL}, mcp.StatelessProtocolVersion)
	if !mcp.IsStatelessProtocolError(err) || mcp.IndicatesLegacyServer(err) {
		t.Fatalf("error classification wrong for %v", err)
	}
	supported, ok := mcp.UnsupportedProtocolVersions(err)
	if !ok || len(supported) != 2 || supported[1] != mcp.LegacyProtocolVersion {
		t.Fatalf("supported versions = %v ok=%t", supported, ok)
	}
	if _, ok := mcp.NegotiateStatelessProtocolVersion(supported); ok {
		t.Fatalf("expected no stateless overlap in %v", supported)
	}
}

func TestHeaderMismatchErrorIsDecoded(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := readBody(r)
		id := extractID(raw)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32020,"message":"Header mismatch"}}`, id)
	}))
	t.Cleanup(ts.Close)

	client := newClient(t)
	_, err := client.CallTool(context.Background(), statelessConn(ts.URL), 1, mcp.ToolCall{Name: "greet"})
	var rpcErr *mcp.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != mcp.CodeHeaderMismatch || rpcErr.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("expected header mismatch error, got %v", err)
	}
	if mcp.IsRetryableConnectionFailure(err) {
		t.Fatalf("header mismatch must not be retryable: %v", err)
	}
}

func TestStatelessInputRequiredResultIsRejected(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := readBody(r)
		id := extractID(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"resultType":"input_required","inputRequests":[]}}`, id)
	}))
	t.Cleanup(ts.Close)

	client := newClient(t)
	_, err := client.CallTool(context.Background(), statelessConn(ts.URL), 1, mcp.ToolCall{Name: "greet"})
	if !errors.Is(err, mcp.ErrInputRequired) {
		t.Fatalf("expected ErrInputRequired, got %v", err)
	}
}

func TestPlainTextServerErrorWithJSONRPCBodyKeepsRetryability(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := readBody(r)
		id := extractID(raw)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":"overloaded"}}`, id)
	}))
	t.Cleanup(ts.Close)

	client := newClient(t)
	_, err := client.Discover(context.Background(), mcp.Conn{EndpointURL: ts.URL}, mcp.StatelessProtocolVersion)
	if !mcp.IsRetryableConnectionFailure(err) {
		t.Fatalf("503 with JSON-RPC body should stay retryable: %v", err)
	}
	if mcp.IndicatesLegacyServer(err) {
		t.Fatalf("503 must not be read as a legacy server: %v", err)
	}
}

func TestEmptyStringHeaderAgainstSDKServer(t *testing.T) {
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "headers", Version: "test"}, nil)
	tool := &sdkmcp.Tool{Name: "echo", InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{
			"region": map[string]any{"type": "string", "x-mcp-header": "Region"},
		},
	}}
	srv.AddTool(tool, func(context.Context, *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "ok"}}}, nil
	})
	ts := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return srv },
		&sdkmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer ts.Close()
	headers, err := mcp.ToolHeaders(tool)
	if err != nil {
		t.Fatal(err)
	}
	result, err := newClient(t).CallTool(silentCtx(), statelessConn(ts.URL), 1, mcp.ToolCall{
		Name: "echo", Arguments: json.RawMessage(`{"region":""}`), Headers: headers,
	})
	if err != nil {
		t.Fatalf("empty string rejected by SDK: %v", err)
	}
	if got := contentText(t, result); got != "ok" {
		t.Fatalf("result = %q", got)
	}
}
