package mcptest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	StatelessProtocolVersion = "2026-07-28"
	legacyProtocolVersions   = "2025-11-25, 2025-06-18, 2025-03-26, 2024-11-05"

	metaProtocolVersionKey = "io.modelcontextprotocol/protocolVersion"
	methodHeader           = "Mcp-Method"
	nameHeader             = "Mcp-Name"

	codeMethodNotFound  = -32601
	codeInvalidParams   = -32602
	codeHeaderMismatch  = -32020
	maxInspectedBodyLen = 1_048_576
)

type greetArgs struct {
	Name string `json:"name" jsonschema:"the name to greet"`
}

func greet(_ context.Context, _ *sdkmcp.CallToolRequest, args greetArgs) (*sdkmcp.CallToolResult, any, error) {
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "Hi " + args.Name}},
	}, nil, nil
}

type noisyArgs struct {
	Steps int `json:"steps" jsonschema:"number of progress notifications to emit before returning"`
}

func noisy(ctx context.Context, req *sdkmcp.CallToolRequest, args noisyArgs) (*sdkmcp.CallToolResult, any, error) {
	token := req.Params.GetProgressToken()
	if token == nil {
		token = "noisy"
	}
	for i := range args.Steps {
		_ = req.Session.NotifyProgress(ctx, &sdkmcp.ProgressNotificationParams{
			ProgressToken: token,
			Progress:      float64(i + 1),
			Total:         float64(args.Steps),
			Message:       fmt.Sprintf("step %d/%d", i+1, args.Steps),
		})
	}
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "done"}},
	}, nil, nil
}

func newSDKServer(greetTool sdkmcp.ToolHandlerFor[greetArgs, any]) *sdkmcp.Server {
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "mcptest", Version: "v0"}, nil)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "greet", Description: "say hi"}, greetTool)
	sdkmcp.AddTool(srv, &sdkmcp.Tool{Name: "noisy", Description: "emit N progress notifications, then return"}, noisy)
	return srv
}

func newHandler(jsonResponse bool, greetTool sdkmcp.ToolHandlerFor[greetArgs, any]) http.Handler {
	srv := newSDKServer(greetTool)
	return legacyOnly(sdkmcp.NewStreamableHTTPHandler(
		func(*http.Request) *sdkmcp.Server { return srv },
		&sdkmcp.StreamableHTTPOptions{JSONResponse: jsonResponse},
	))
}

func newServer(tb testing.TB, jsonResponse bool) *httptest.Server {
	tb.Helper()
	h := newHandler(jsonResponse, greet)
	ts := httptest.NewServer(h)
	tb.Cleanup(ts.Close)
	return ts
}

func NewServer(tb testing.TB) *httptest.Server { return newServer(tb, false) }

func NewJSONServer(tb testing.TB) *httptest.Server { return newServer(tb, true) }

func NewJSONServerWithGreetResult(tb testing.TB, result string) *httptest.Server {
	tb.Helper()
	h := newHandler(true, greetResult(result))
	ts := httptest.NewServer(h)
	tb.Cleanup(ts.Close)
	return ts
}

func greetResult(result string) sdkmcp.ToolHandlerFor[greetArgs, any] {
	return func(context.Context, *sdkmcp.CallToolRequest, greetArgs) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: result}},
		}, nil, nil
	}
}

type inspectedRequest struct {
	ID        json.RawMessage
	Method    string
	Stateless bool
}

func inspectRequest(r *http.Request) (inspectedRequest, []byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxInspectedBodyLen))
	if err != nil {
		return inspectedRequest{}, nil, err
	}
	var envelope struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Meta map[string]json.RawMessage `json:"_meta"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return inspectedRequest{}, body, fmt.Errorf("decode JSON-RPC request: %w", err)
	}
	_, stateless := envelope.Params.Meta[metaProtocolVersionKey]
	return inspectedRequest{ID: envelope.ID, Method: envelope.Method, Stateless: stateless}, body, nil
}

func legacyOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		inspected, body, err := inspectRequest(r)
		if err != nil {
			http.Error(w, "Bad Request: "+err.Error(), http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		if version := r.Header.Get("Mcp-Protocol-Version"); version >= StatelessProtocolVersion {
			http.Error(
				w,
				"Bad Request: Unsupported protocol version (supported versions: "+legacyProtocolVersions+")",
				http.StatusBadRequest,
			)
			return
		}
		if inspected.Stateless || inspected.Method == "server/discover" {
			http.Error(w, "Bad Request: Mcp-Session-Id header is required", http.StatusBadRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type StatelessOptions struct {
	JSONResponse bool
	ToolsTTL     time.Duration
	CacheScope   string
	GreetResult  string
}

type StatelessServer struct {
	*httptest.Server

	DiscoverCalls  atomic.Int64
	ListToolsCalls atomic.Int64
	CallToolCalls  atomic.Int64
}

func NewStatelessServer(tb testing.TB, opts StatelessOptions) *StatelessServer {
	tb.Helper()
	greetTool := greet
	if opts.GreetResult != "" {
		greetTool = greetResult(opts.GreetResult)
	}
	srv := newSDKServer(greetTool)
	server := &StatelessServer{}
	sdkHandler := sdkmcp.NewStreamableHTTPHandler(
		func(*http.Request) *sdkmcp.Server { return srv },
		&sdkmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: opts.JSONResponse},
	)
	server.Server = httptest.NewServer(server.handler(sdkHandler, opts))
	tb.Cleanup(server.Close)
	return server
}

func (s *StatelessServer) handler(next http.Handler, opts StatelessOptions) http.Handler {
	scope := opts.CacheScope
	if scope == "" {
		scope = "public"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		inspected, body, err := inspectRequest(r)
		if err != nil {
			http.Error(w, "Bad Request: "+err.Error(), http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		if inspected.Method == "initialize" {
			writeRPCError(w, http.StatusNotFound, inspected.ID, codeMethodNotFound,
				`"initialize" is not supported; supported protocol versions: `+StatelessProtocolVersion)
			return
		}
		if !inspected.Stateless {
			writeRPCError(w, http.StatusBadRequest, inspected.ID, codeInvalidParams,
				"request _meta must carry "+metaProtocolVersionKey)
			return
		}
		if r.Header.Get(methodHeader) != inspected.Method {
			writeRPCError(w, http.StatusBadRequest, inspected.ID, codeHeaderMismatch,
				fmt.Sprintf("header mismatch: %s header value %q does not match body value %q",
					methodHeader, r.Header.Get(methodHeader), inspected.Method))
			return
		}
		if inspected.Method == "tools/call" && r.Header.Get(nameHeader) == "" {
			writeRPCError(w, http.StatusBadRequest, inspected.ID, codeHeaderMismatch,
				"missing required "+nameHeader+" header for method \"tools/call\"")
			return
		}
		switch inspected.Method {
		case "server/discover":
			s.DiscoverCalls.Add(1)
		case "tools/list":
			s.ListToolsCalls.Add(1)
		case "tools/call":
			s.CallToolCalls.Add(1)
		}
		if !opts.JSONResponse {
			next.ServeHTTP(w, r)
			return
		}
		recorder := httptest.NewRecorder()
		next.ServeHTTP(recorder, r)
		for key, values := range recorder.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		payload := recorder.Body.Bytes()
		if recorder.Code == http.StatusOK && strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json") {
			payload = rewriteCacheHints(payload, opts.ToolsTTL, scope)
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(payload)
	})
}

func rewriteCacheHints(payload []byte, ttl time.Duration, scope string) []byte {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return payload
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(envelope["result"], &result); err != nil || result == nil {
		return payload
	}
	if _, cacheable := result["ttlMs"]; !cacheable {
		return payload
	}
	result["ttlMs"] = json.RawMessage(fmt.Sprint(ttl.Milliseconds()))
	result["cacheScope"] = json.RawMessage(fmt.Sprintf("%q", scope))
	encodedResult, err := json.Marshal(result)
	if err != nil {
		return payload
	}
	envelope["result"] = encodedResult
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return payload
	}
	return encoded
}

func writeRPCError(w http.ResponseWriter, status int, id json.RawMessage, code int, message string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":%d,"message":%q}}`, id, code, message)
}
