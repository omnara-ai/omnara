package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	mlog "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/mcp"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/testutil/mcptest"
)

// silentCtx returns a context bound to a discard slog logger so the
// client's SSE warnings don't spam test output.
func silentCtx() context.Context {
	return mlog.WithLogger(context.Background(),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func newClient(t *testing.T) mcp.Client {
	t.Helper()
	return mcp.New(mcp.Options{
		HTTPClient: mcpTestHTTPClient(true),
	})
}

func mcpTestHTTPClient(allowLoopback bool) *http.Client {
	return outboundhttp.NewPublicClient(outboundhttp.PublicClientOptions{
		AllowLoopback: allowLoopback,
	})
}

func initialized(t *testing.T, client mcp.Client, endpoint string) mcp.Conn {
	t.Helper()
	ctx := context.Background()
	sessionID, result, err := client.Initialize(ctx, mcp.Conn{EndpointURL: endpoint}, mcp.ProtocolVersion)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	conn := mcp.Conn{
		EndpointURL:     endpoint,
		MCPSessionID:    sessionID,
		ProtocolVersion: result.ProtocolVersion,
	}
	if err := client.Notify(ctx, conn, "notifications/initialized", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("notifications/initialized: %v", err)
	}
	return conn
}

func TestInitializeJSON(t *testing.T) {
	ts := mcptest.NewJSONServer(t)
	client := newClient(t)

	sessionID, result, err := client.Initialize(
		context.Background(),
		mcp.Conn{EndpointURL: ts.URL},
		mcp.ProtocolVersion,
	)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if sessionID == "" {
		t.Errorf("expected non-empty session id, got empty")
	}
	if result.ProtocolVersion == "" {
		t.Errorf("expected non-empty negotiated protocol version")
	}
	if len(result.ServerInfo) == 0 || string(result.ServerInfo) == "{}" {
		t.Errorf("expected ServerInfo to be populated, got %+v", result.ServerInfo)
	}
}

func TestInitializeSSE(t *testing.T) {
	ts := mcptest.NewServer(t)
	client := newClient(t)

	sessionID, result, err := client.Initialize(
		context.Background(),
		mcp.Conn{EndpointURL: ts.URL},
		mcp.ProtocolVersion,
	)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if sessionID == "" {
		t.Errorf("expected non-empty session id, got empty")
	}
	if result.ProtocolVersion == "" {
		t.Errorf("expected non-empty negotiated protocol version")
	}
}

func TestRoundTripJSON(t *testing.T) {
	ts := mcptest.NewJSONServer(t)
	client := newClient(t)
	conn := initialized(t, client, ts.URL)

	tools, err := client.ListTools(context.Background(), conn, 2)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(tools) == 0 {
		t.Fatalf("expected non-empty tool list")
	}
	if !containsTool(tools, "greet") {
		t.Errorf("expected `greet` in tool list, got %v", toolNames(tools))
	}

	args, err := json.Marshal(map[string]any{"name": "world"})
	if err != nil {
		t.Fatalf("marshal tool args: %v", err)
	}
	result, err := client.CallTool(context.Background(), conn, 3, "greet", args)
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if got := contentText(t, result); got != "Hi world" {
		t.Errorf("expected 'Hi world', got %q", got)
	}
}

func TestClientSendsBearerAuthorization(t *testing.T) {
	var gotAuth atomic.Value
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		http.Error(w, "stop", http.StatusTeapot)
	}))
	defer ts.Close()

	client := newClient(t)
	_, err := client.CallTool(
		context.Background(),
		mcp.Conn{EndpointURL: ts.URL, BearerToken: "secret-token"},
		3,
		"greet",
		json.RawMessage(`{}`),
	)
	if err == nil {
		t.Fatal("expected server error")
	}
	if got := gotAuth.Load(); got != "Bearer secret-token" {
		t.Fatalf("Authorization header = %v, want bearer token", got)
	}
}

func TestRoundTripSSE(t *testing.T) {
	ts := mcptest.NewServer(t)
	client := newClient(t)
	conn := initialized(t, client, ts.URL)

	args, err := json.Marshal(map[string]any{"name": "world"})
	if err != nil {
		t.Fatalf("marshal tool args: %v", err)
	}
	result, err := client.CallTool(context.Background(), conn, 3, "greet", args)
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if got := contentText(t, result); got != "Hi world" {
		t.Errorf("expected 'Hi world', got %q", got)
	}
}

func TestSSEInterleavedNotifications(t *testing.T) {
	ts := mcptest.NewServer(t)
	client := newClient(t)
	conn := initialized(t, client, ts.URL)

	params, err := json.Marshal(map[string]any{
		"name":      "noisy",
		"arguments": map[string]any{"steps": 3},
		"_meta": map[string]any{
			"progressToken": "test-token",
		},
	})
	if err != nil {
		t.Fatalf("marshal noisy tool params: %v", err)
	}
	result, err := client.Call(context.Background(), conn, "tools/call", params, 99)
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}

	var ctr sdkmcp.CallToolResult
	if err := json.Unmarshal(result, &ctr); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if got := callToolResultText(t, &ctr); got != "done" {
		t.Errorf("expected 'done', got %q", got)
	}
}

func TestSessionResumption(t *testing.T) {
	ts := mcptest.NewServer(t)

	clientA := newClient(t)
	conn := initialized(t, clientA, ts.URL)
	if conn.MCPSessionID == "" {
		t.Fatalf("expected a session id from initialize")
	}

	clientB := newClient(t)
	resumed := mcp.Conn{
		EndpointURL:     ts.URL,
		MCPSessionID:    conn.MCPSessionID,
		ProtocolVersion: conn.ProtocolVersion,
	}

	args, err := json.Marshal(map[string]any{"name": "resumed"})
	if err != nil {
		t.Fatalf("marshal resumed tool args: %v", err)
	}
	result, err := clientB.CallTool(context.Background(), resumed, 100, "greet", args)
	if err != nil {
		t.Fatalf("resumed call: %v", err)
	}
	if got := contentText(t, result); got != "Hi resumed" {
		t.Errorf("expected 'Hi resumed', got %q", got)
	}
}

func TestSessionExpired404(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "session not found", http.StatusNotFound)
	}))
	t.Cleanup(ts.Close)

	client := newClient(t)
	conn := mcp.Conn{
		EndpointURL:     ts.URL,
		MCPSessionID:    "stale-session",
		ProtocolVersion: mcp.ProtocolVersion,
	}
	_, err := client.Call(context.Background(), conn, "tools/list", json.RawMessage(`{}`), 1)
	if !errors.Is(err, mcp.ErrSessionExpired) {
		t.Errorf("expected ErrSessionExpired, got %v", err)
	}
}

func TestUnsupportedResponseContentType(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<xml/>"))
	}))
	t.Cleanup(ts.Close)

	client := newClient(t)
	conn := mcp.Conn{EndpointURL: ts.URL}
	_, err := client.Call(context.Background(), conn, "tools/list", json.RawMessage(`{}`), 1)
	if !errors.Is(err, mcp.ErrUnsupportedResponse) {
		t.Errorf("expected ErrUnsupportedResponse, got %v", err)
	}
}

func TestSSEIncomplete(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n"))
	}))
	t.Cleanup(ts.Close)

	client := newClient(t)
	conn := mcp.Conn{EndpointURL: ts.URL}
	_, err := client.Call(context.Background(), conn, "tools/list", json.RawMessage(`{}`), 1)
	if !errors.Is(err, mcp.ErrIncompleteStream) {
		t.Errorf("expected ErrIncompleteStream, got %v", err)
	}
}

func TestBodyCapJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		body := strings.Repeat("x", 1024*1024)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)

	client := mcp.New(mcp.Options{
		HTTPClient: mcpTestHTTPClient(true),
		BodyLimit:  1024,
	})
	conn := mcp.Conn{EndpointURL: ts.URL}
	_, err := client.Call(context.Background(), conn, "tools/list", json.RawMessage(`{}`), 1)
	if !errors.Is(err, mcp.ErrResponseTooLarge) {
		t.Errorf("expected ErrResponseTooLarge, got %v", err)
	}
}

// TestBodyCapJSONValidPrefix verifies that a valid JSON-RPC response
// followed by extra bytes past the cap is rejected. The old +N pattern
// would have happily decoded the valid prefix and ignored the rest.
func TestBodyCapJSONValidPrefix(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		_, _ = w.Write([]byte(strings.Repeat(" ", 4096)))
	}))
	t.Cleanup(ts.Close)

	client := mcp.New(mcp.Options{
		HTTPClient: mcpTestHTTPClient(true),
		BodyLimit:  64,
	})
	conn := mcp.Conn{EndpointURL: ts.URL}
	_, err := client.Call(context.Background(), conn, "ping", json.RawMessage(`{}`), 1)
	if !errors.Is(err, mcp.ErrResponseTooLarge) {
		t.Errorf("expected ErrResponseTooLarge, got %v", err)
	}
}

func TestBodyCapSSE(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Emit a single oversized SSE event before any matching response.
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write([]byte(strings.Repeat("x", 8192)))
		_, _ = w.Write([]byte("\n\n"))
	}))
	t.Cleanup(ts.Close)

	client := mcp.New(mcp.Options{
		HTTPClient: mcpTestHTTPClient(true),
		BodyLimit:  1024,
	})
	conn := mcp.Conn{EndpointURL: ts.URL}
	_, err := client.Call(silentCtx(), conn, "ping", json.RawMessage(`{}`), 1)
	if !errors.Is(err, mcp.ErrResponseTooLarge) {
		t.Errorf("expected ErrResponseTooLarge, got %v", err)
	}
}

func TestAcceptHeader(t *testing.T) {
	var got atomic.Value
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.Header.Get("Accept"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	t.Cleanup(ts.Close)

	client := newClient(t)
	conn := mcp.Conn{EndpointURL: ts.URL}
	_, err := client.Call(context.Background(), conn, "ping", json.RawMessage(`{}`), 1)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if want := "application/json, text/event-stream"; got.Load() != want {
		t.Errorf("Accept header = %q, want %q", got.Load(), want)
	}
}

func TestProtocolVersionHeader(t *testing.T) {
	var got atomic.Value
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.Header.Get("Mcp-Protocol-Version"))
		body, _ := readBody(r)
		id := extractID(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{}}`, id)
	}))
	t.Cleanup(ts.Close)

	client := newClient(t)

	noVersion := mcp.Conn{EndpointURL: ts.URL}
	_, err := client.Call(context.Background(), noVersion, "ping", json.RawMessage(`{}`), 1)
	if err != nil {
		t.Fatalf("call without version: %v", err)
	}
	v, ok := got.Load().(string)
	if !ok {
		t.Fatalf("protocol header value shape = %T", got.Load())
	}
	if v != "" {
		t.Errorf("expected no MCP-Protocol-Version header, got %q", v)
	}

	withVersion := mcp.Conn{EndpointURL: ts.URL, ProtocolVersion: mcp.ProtocolVersion}
	_, err = client.Call(context.Background(), withVersion, "ping", json.RawMessage(`{}`), 2)
	if err != nil {
		t.Fatalf("call with version: %v", err)
	}
	v, ok = got.Load().(string)
	if !ok {
		t.Fatalf("protocol header value shape = %T", got.Load())
	}
	if v != mcp.ProtocolVersion {
		t.Errorf("expected MCP-Protocol-Version=%s, got %q", mcp.ProtocolVersion, v)
	}
}

func TestJSONRPCErrorSurfacesAsRPCError(t *testing.T) {
	ts := mcptest.NewJSONServer(t)
	client := newClient(t)
	conn := initialized(t, client, ts.URL)

	args, err := json.Marshal(map[string]any{})
	if err != nil {
		t.Fatalf("marshal missing tool args: %v", err)
	}
	_, err = client.CallTool(context.Background(), conn, 99, "does_not_exist", args)
	if err == nil {
		t.Fatalf("expected an error for unknown tool")
	}
	var rpcErr *mcp.RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("expected *mcp.RPCError, got %T: %v", err, err)
	}
	if rpcErr.Code == 0 {
		t.Errorf("expected non-zero JSON-RPC error code, got %d", rpcErr.Code)
	}
}

func TestSSRFLoopbackBlocked(t *testing.T) {
	ts := mcptest.NewJSONServer(t)

	client := mcp.New(mcp.Options{
		HTTPClient: mcpTestHTTPClient(false),
	})
	conn := mcp.Conn{EndpointURL: ts.URL}
	_, err := client.Call(context.Background(), conn, "ping", json.RawMessage(`{}`), 1)
	if !errors.Is(err, ssrf.ErrBlockedAddress) {
		t.Errorf("expected ErrBlockedAddress, got %v", err)
	}
}

func TestSSRFBlockedRanges(t *testing.T) {
	cases := []struct {
		name string
		host string
	}{
		{"loopback v4", "127.0.0.1"},
		{"private 10/8", "10.0.0.1"},
		{"private 192.168/16", "192.168.1.1"},
		{"private 172.16/12", "172.16.0.1"},
		{"link-local", "169.254.0.1"},
		{"cloud metadata", "169.254.169.254"},
		{"cgnat", "100.64.0.1"},
		{"current network", "0.0.0.0"},
		{"benchmark", "198.18.0.1"},
		{"loopback v6", "[::1]"},
		{"link-local v6", "[fe80::1]"},
		{"ula v6", "[fc00::1]"},
		{"ipv4-mapped loopback", "[::ffff:127.0.0.1]"},
		{"test-net-1", "192.0.2.1"},
		{"test-net-2", "198.51.100.1"},
		{"test-net-3", "203.0.113.1"},
		{"reserved class-e", "240.0.0.1"},
	}

	client := mcp.New(mcp.Options{
		HTTPClient: mcpTestHTTPClient(false),
	})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := fmt.Sprintf("https://%s:443/mcp", tc.host)
			conn := mcp.Conn{EndpointURL: url}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, err := client.Call(ctx, conn, "ping", json.RawMessage(`{}`), 1)
			if !errors.Is(err, ssrf.ErrBlockedAddress) {
				t.Errorf("expected ErrBlockedAddress for %s, got %v", tc.host, err)
			}
		})
	}
}

func TestInitializeRejectsEmptyProtocolVersion(t *testing.T) {
	client := newClient(t)
	_, _, err := client.Initialize(context.Background(), mcp.Conn{EndpointURL: "http://example.invalid"}, "  ")
	if err == nil {
		t.Fatal("expected error for empty protocol version, got nil")
	}
}

func TestNotifyAccepts204(t *testing.T) {
	var hits int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(ts.Close)

	client := newClient(t)
	conn := mcp.Conn{EndpointURL: ts.URL}
	if err := client.Notify(context.Background(), conn, "notifications/initialized", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("notify returned error on 204: %v", err)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("expected 1 server hit, got %d", hits)
	}
}

func TestNotifySessionExpired(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no session", http.StatusNotFound)
	}))
	t.Cleanup(ts.Close)

	client := newClient(t)
	conn := mcp.Conn{EndpointURL: ts.URL, MCPSessionID: "stale"}
	err := client.Notify(context.Background(), conn, "notifications/canceled", json.RawMessage(`{}`))
	if !errors.Is(err, mcp.ErrSessionExpired) {
		t.Errorf("expected ErrSessionExpired, got %v", err)
	}
}

func TestHTTPErrorTypedStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	t.Cleanup(ts.Close)

	client := newClient(t)
	conn := mcp.Conn{EndpointURL: ts.URL}
	_, err := client.Call(context.Background(), conn, "ping", json.RawMessage(`{}`), 1)

	var httpErr *mcp.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected *mcp.HTTPError, got %T: %v", err, err)
	}
	if httpErr.Status != http.StatusBadGateway {
		t.Errorf("expected status 502, got %d", httpErr.Status)
	}
}

func TestRedirectsBlocked(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://evil.example/")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	t.Cleanup(ts.Close)

	client := newClient(t)
	conn := mcp.Conn{EndpointURL: ts.URL, MCPSessionID: "secret-session"}
	_, err := client.Call(context.Background(), conn, "ping", json.RawMessage(`{}`), 1)
	if !errors.Is(err, outboundhttp.ErrRedirect) {
		t.Errorf("expected ErrRedirect, got %v", err)
	}
}

func TestContentTypeStrictMatch(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		wantOK      bool
	}{
		{"json bare", "application/json", true},
		{"json with charset", "application/json; charset=utf-8", true},
		{"jsonwhatever", "application/jsonwhatever", false},
		{"json-seq", "application/json-seq", false},
		{"sse bare", "text/event-stream", true},
		{"sse with charset", "text/event-stream; charset=utf-8", true},
		{"text/plain", "text/plain", false},
		{"garbage", "this is not a media type", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(http.StatusOK)
				if strings.HasPrefix(tc.contentType, "text/event-stream") {
					_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n"))
				} else {
					_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
				}
			}))
			t.Cleanup(ts.Close)

			client := newClient(t)
			conn := mcp.Conn{EndpointURL: ts.URL}
			_, err := client.Call(context.Background(), conn, "ping", json.RawMessage(`{}`), 1)
			if tc.wantOK && err != nil {
				t.Errorf("expected success for %q, got %v", tc.contentType, err)
			}
			if !tc.wantOK && !errors.Is(err, mcp.ErrUnsupportedResponse) {
				t.Errorf("expected ErrUnsupportedResponse for %q, got %v", tc.contentType, err)
			}
		})
	}
}

func TestJSONRPCBatchResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readBody(r)
		id := extractID(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Send a batch with an unrelated notification + the real
		// response. The client should pick out the matching id.
		_, _ = fmt.Fprintf(
			w,
			`[{"jsonrpc":"2.0","method":"notifications/log","params":{}},{"jsonrpc":"2.0","id":%s,"result":{"ok":true}}]`,
			id,
		)
	}))
	t.Cleanup(ts.Close)

	client := newClient(t)
	conn := mcp.Conn{EndpointURL: ts.URL}
	result, err := client.Call(context.Background(), conn, "ping", json.RawMessage(`{}`), 42)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(string(result), `"ok":true`) {
		t.Errorf("expected ok:true in result, got %s", result)
	}
}

func TestStringIDMatch(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readBody(r)
		id := extractID(body) // numeric, e.g. "7"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Echo the id back as a string. JSON-RPC spec violation, but
		// some servers do this; our matcher should tolerate it.
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":"%s","result":{}}`, strings.Trim(id, `"`))
	}))
	t.Cleanup(ts.Close)

	client := newClient(t)
	conn := mcp.Conn{EndpointURL: ts.URL}
	_, err := client.Call(context.Background(), conn, "ping", json.RawMessage(`{}`), 7)
	if err != nil {
		t.Errorf("expected string-id to match int request id, got %v", err)
	}
}

func TestSSEIgnoresNullIDResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// First a parse-error response with id:null (must be skipped),
		// then the real response.
		_, _ = w.Write(
			[]byte(
				"data: {\"jsonrpc\":\"2.0\",\"id\":null,\"error\":{\"code\":-32700,\"message\":\"parse error\"}}\n\n",
			),
		)
		_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"id\":5,\"result\":{}}\n\n"))
	}))
	t.Cleanup(ts.Close)

	client := newClient(t)
	conn := mcp.Conn{EndpointURL: ts.URL}
	_, err := client.Call(context.Background(), conn, "ping", json.RawMessage(`{}`), 5)
	if err != nil {
		t.Errorf("expected null-id response to be ignored, got %v", err)
	}
}

func TestSSEWallClockTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte(": keepalive\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	t.Cleanup(ts.Close)

	client := mcp.New(mcp.Options{
		HTTPClient:          mcpTestHTTPClient(true),
		SSEWallClockTimeout: 200 * time.Millisecond,
	})
	conn := mcp.Conn{EndpointURL: ts.URL}
	start := time.Now()
	_, err := client.Call(context.Background(), conn, "ping", json.RawMessage(`{}`), 1)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected an error from SSE wall-clock timeout")
	}
	if elapsed > 3*time.Second {
		t.Errorf("expected timeout to fire within ~200ms, took %s", elapsed)
	}
}

func TestSSECanOutliveRequestTimeoutAfterHeaders(t *testing.T) {
	sseHeadersSent := make(chan struct{})
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.URL.Path == "/hang" {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		if flusher != nil {
			flusher.Flush()
		}
		close(sseHeadersSent)

		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"ok\":true}}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	t.Cleanup(ts.Close)

	client := mcp.New(mcp.Options{
		HTTPClient:          mcpTestHTTPClient(true),
		RequestTimeout:      50 * time.Millisecond,
		SSEWallClockTimeout: 30 * time.Second,
	})

	type callResult struct {
		result json.RawMessage
		err    error
	}
	done := make(chan callResult, 1)
	go func() {
		conn := mcp.Conn{EndpointURL: ts.URL}
		result, err := client.Call(context.Background(), conn, "ping", json.RawMessage(`{}`), 1)
		done <- callResult{result: result, err: err}
	}()

	<-sseHeadersSent
	probeConn := mcp.Conn{EndpointURL: ts.URL + "/hang"}
	if _, err := client.Call(context.Background(), probeConn, "ping", json.RawMessage(`{}`), 2); err == nil {
		t.Fatal("expected request timeout error from hanging endpoint")
	}
	close(release)

	out := <-done
	if out.err != nil {
		t.Fatalf("call: %v", out.err)
	}
	if !strings.Contains(string(out.result), `"ok":true`) {
		t.Errorf("expected ok:true in result, got %s", out.result)
	}
}

type panicRoundTripper struct{}

func (panicRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	panic("transport failure")
}

func TestHTTPClientContainsTransportPanic(t *testing.T) {
	client := mcp.New(mcp.Options{
		HTTPClient: &http.Client{Transport: panicRoundTripper{}},
	})
	_, _, err := client.Initialize(
		context.Background(),
		mcp.Conn{EndpointURL: "https://example.com/mcp"},
		mcp.ProtocolVersion,
	)
	if err == nil || !strings.Contains(err.Error(), "mcp: HTTP request panicked: transport failure") {
		t.Fatalf("initialize error = %v, want contained transport panic", err)
	}
}

func containsTool(tools []*sdkmcp.Tool, name string) bool {
	for _, t := range tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

func toolNames(tools []*sdkmcp.Tool) []string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Name
	}
	return out
}

func contentText(t *testing.T, r *sdkmcp.CallToolResult) string {
	t.Helper()
	return callToolResultText(t, r)
}

func callToolResultText(t *testing.T, r *sdkmcp.CallToolResult) string {
	t.Helper()
	if len(r.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(r.Content))
	}
	text, ok := r.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("expected *TextContent, got %T", r.Content[0])
	}
	return text.Text
}

func readBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	var buf bytes.Buffer
	_, err := buf.ReadFrom(r.Body)
	return buf.Bytes(), err
}

func extractID(body []byte) string {
	var msg struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(body, &msg) != nil || len(msg.ID) == 0 {
		return "null"
	}
	return string(msg.ID)
}
