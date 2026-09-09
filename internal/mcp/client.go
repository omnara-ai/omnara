package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	jsonrpc "github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	mlog "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
)

const (
	headerSessionID       = "Mcp-Session-Id"
	headerProtocolVersion = "Mcp-Protocol-Version"

	mediaTypeJSON = "application/json"
	mediaTypeSSE  = "text/event-stream"
	acceptHeader  = mediaTypeJSON + ", " + mediaTypeSSE

	defaultBodyLimit = 4 * 1024 * 1024

	initializeRequestID = -1
	discoverRequestID   = -2
)

// Client is an MCP client that speaks both the stateless protocol (per-request
// metadata, no session) and the legacy initialize handshake, where it can
// reconnect to an existing session ID without reinitializing.
type Client interface {
	Discover(ctx context.Context, conn Conn, protocolVersion string) (DiscoverResult, error)
	Initialize(
		ctx context.Context,
		conn Conn,
		clientProtocolVersion string,
	) (mcpSessionID string, result InitializeResult, err error)
	Notify(ctx context.Context, conn Conn, method string, params json.RawMessage) error
	Call(
		ctx context.Context,
		conn Conn,
		method string,
		params json.RawMessage,
		requestID int64,
	) (json.RawMessage, error)
	ListTools(ctx context.Context, conn Conn, requestID int64, cursor string) (ToolsPage, error)
	CallTool(
		ctx context.Context,
		conn Conn,
		requestID int64,
		call ToolCall,
	) (*sdkmcp.CallToolResult, error)
}

type ToolsPage struct {
	Tools      []*sdkmcp.Tool `json:"tools"`
	NextCursor string         `json:"nextCursor,omitempty"`
	TTLMs      int            `json:"ttlMs,omitempty"`
	CacheScope string         `json:"cacheScope,omitempty"`
}

type InitializeResult struct {
	ProtocolVersion    string
	ServerCapabilities json.RawMessage
	ServerInfo         json.RawMessage
}

type Conn struct {
	EndpointURL     string
	MCPSessionID    string
	ProtocolVersion string
	BearerToken     string
	prepareRequest  func(context.Context, *http.Request, []byte) error
}

func (c Conn) HasSession() bool { return c.MCPSessionID != "" }

func (c Conn) Stateless() bool { return IsStatelessProtocolVersion(c.ProtocolVersion) }

func (c Conn) withoutSession() Conn {
	c.MCPSessionID = ""
	c.ProtocolVersion = ""
	return c
}

func marshalObject(value any) (json.RawMessage, error) {
	if value == nil {
		return json.RawMessage(`{}`), nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if string(raw) == "null" {
		return json.RawMessage(`{}`), nil
	}
	return raw, nil
}

type Options struct {
	HTTPClient *http.Client

	BodyLimit int64

	ClientInfo *sdkmcp.Implementation

	RequestTimeout time.Duration

	SSEWallClockTimeout time.Duration
}

func New(opts Options) Client {
	if opts.HTTPClient == nil {
		opts.HTTPClient = outboundhttp.NewPublicClient(outboundhttp.PublicClientOptions{})
	}
	opts.HTTPClient = outboundhttp.CloneRejectingRedirects(opts.HTTPClient)
	if opts.BodyLimit == 0 {
		opts.BodyLimit = defaultBodyLimit
	}
	if opts.ClientInfo == nil {
		opts.ClientInfo = &sdkmcp.Implementation{Name: "omnara-mcp", Version: "v0"}
	}
	if opts.RequestTimeout == 0 {
		opts.RequestTimeout = 30 * time.Second
	}
	if opts.SSEWallClockTimeout == 0 {
		opts.SSEWallClockTimeout = 5 * time.Minute
	}
	return &httpClient{
		http:           opts.HTTPClient,
		bodyLimit:      opts.BodyLimit,
		clientInfo:     opts.ClientInfo,
		requestTimeout: opts.RequestTimeout,
		sseTimeout:     opts.SSEWallClockTimeout,
	}
}

type httpClient struct {
	http           *http.Client
	bodyLimit      int64
	clientInfo     *sdkmcp.Implementation
	requestTimeout time.Duration
	sseTimeout     time.Duration
}

func (c *httpClient) Initialize(
	ctx context.Context,
	conn Conn,
	clientProtocolVersion string,
) (string, InitializeResult, error) {
	if strings.TrimSpace(clientProtocolVersion) == "" {
		return "", InitializeResult{}, errors.New("mcp: clientProtocolVersion is required")
	}
	if IsStatelessProtocolVersion(clientProtocolVersion) {
		return "", InitializeResult{}, fmt.Errorf(
			"mcp: protocol version %s has no initialize handshake; use Discover",
			clientProtocolVersion,
		)
	}
	conn.ProtocolVersion = ""

	params, err := json.Marshal(map[string]any{
		"protocolVersion": clientProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      c.clientInfo,
	})
	if err != nil {
		return "", InitializeResult{}, fmt.Errorf("mcp: marshal initialize params: %w", err)
	}

	sessionID, resultJSON, err := c.doRequest(ctx, conn, wireRequest{
		method:    "initialize",
		params:    params,
		requestID: initializeRequestID,
	})
	if err != nil {
		return "", InitializeResult{}, err
	}

	var result sdkmcp.InitializeResult
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		return "", InitializeResult{}, fmt.Errorf("mcp: decode initialize result: %w", err)
	}
	serverCapabilities, err := marshalObject(result.Capabilities)
	if err != nil {
		return "", InitializeResult{}, fmt.Errorf("mcp: marshal initialize capabilities: %w", err)
	}
	serverInfo, err := marshalObject(result.ServerInfo)
	if err != nil {
		return "", InitializeResult{}, fmt.Errorf("mcp: marshal initialize server info: %w", err)
	}
	return sessionID, InitializeResult{
		ProtocolVersion:    result.ProtocolVersion,
		ServerCapabilities: serverCapabilities,
		ServerInfo:         serverInfo,
	}, nil
}

func (c *httpClient) Discover(ctx context.Context, conn Conn, protocolVersion string) (DiscoverResult, error) {
	if !IsStatelessProtocolVersion(protocolVersion) {
		return DiscoverResult{}, fmt.Errorf("mcp: protocol version %q does not support server/discover", protocolVersion)
	}
	conn.ProtocolVersion = protocolVersion
	conn.MCPSessionID = ""
	_, resultJSON, err := c.doRequest(ctx, conn, wireRequest{
		method:    "server/discover",
		params:    json.RawMessage(`{}`),
		requestID: discoverRequestID,
	})
	if err != nil {
		return DiscoverResult{}, err
	}
	var result struct {
		SupportedVersions []string        `json:"supportedVersions"`
		Capabilities      json.RawMessage `json:"capabilities"`
		Instructions      string          `json:"instructions"`
		TTLMs             int             `json:"ttlMs"`
		CacheScope        string          `json:"cacheScope"`
		Meta              json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		return DiscoverResult{}, fmt.Errorf("mcp: decode server/discover result: %w", err)
	}
	negotiated, ok := NegotiateStatelessProtocolVersion(result.SupportedVersions)
	if !ok {
		data, err := json.Marshal(map[string]any{"supported": result.SupportedVersions, "requested": protocolVersion})
		if err != nil {
			return DiscoverResult{}, err
		}
		return DiscoverResult{}, &RPCError{
			Code:    CodeUnsupportedProtocolVersion,
			Message: "server/discover advertised no mutually supported stateless protocol version",
			Data:    data,
		}
	}
	capabilities := result.Capabilities
	if len(capabilities) == 0 || string(capabilities) == "null" {
		capabilities = json.RawMessage(`{}`)
	}
	serverInfo, err := serverInfoFromMeta(result.Meta)
	if err != nil {
		return DiscoverResult{}, err
	}
	return DiscoverResult{
		ProtocolVersion:    negotiated,
		SupportedVersions:  result.SupportedVersions,
		ServerCapabilities: capabilities,
		ServerInfo:         serverInfo,
		Instructions:       result.Instructions,
		Cache:              CacheHint{TTLMs: max(result.TTLMs, 0), CacheScope: result.CacheScope},
	}, nil
}

func (c *httpClient) Notify(ctx context.Context, conn Conn, method string, params json.RawMessage) error {
	if conn.Stateless() {
		return fmt.Errorf("mcp: protocol version %s defines no client notifications over HTTP", conn.ProtocolVersion)
	}
	body, err := jsonrpc.EncodeMessage(&jsonrpc.Request{
		Method: method,
		Params: params,
	})
	if err != nil {
		return fmt.Errorf("mcp: marshal notification: %w", err)
	}

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.requestTimeout)
		defer cancel()
	}

	req, err := c.buildHTTP(ctx, conn, body, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound && conn.HasSession() {
		return ErrSessionExpired
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusAccepted, http.StatusNoContent:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, c.bodyLimit))
		return nil
	default:
		return c.statusError(resp)
	}
}

func (c *httpClient) Call(
	ctx context.Context,
	conn Conn,
	method string,
	params json.RawMessage,
	requestID int64,
) (json.RawMessage, error) {
	_, result, err := c.doRequest(ctx, conn, wireRequest{method: method, params: params, requestID: requestID})
	return result, err
}

func (c *httpClient) ListTools(
	ctx context.Context,
	conn Conn,
	requestID int64,
	cursor string,
) (ToolsPage, error) {
	params := json.RawMessage(`{}`)
	if cursor != "" {
		encoded, err := json.Marshal(map[string]string{"cursor": cursor})
		if err != nil {
			return ToolsPage{}, fmt.Errorf("mcp: marshal tools/list params: %w", err)
		}
		params = encoded
	}
	result, err := c.Call(ctx, conn, "tools/list", params, requestID)
	if err != nil {
		return ToolsPage{}, err
	}
	var page ToolsPage
	if err := json.Unmarshal(result, &page); err != nil {
		return ToolsPage{}, fmt.Errorf("mcp: decode tools/list: %w", err)
	}
	return page, nil
}

func (c *httpClient) CallTool(
	ctx context.Context,
	conn Conn,
	requestID int64,
	call ToolCall,
) (*sdkmcp.CallToolResult, error) {
	if call.Name == "" {
		return nil, errors.New("mcp: tool name is required")
	}
	args := call.Arguments
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	params, err := json.Marshal(map[string]any{
		"name":      call.Name,
		"arguments": args,
	})
	if err != nil {
		return nil, fmt.Errorf("mcp: marshal tools/call params: %w", err)
	}
	request := wireRequest{method: "tools/call", params: params, requestID: requestID, name: call.Name}
	if conn.Stateless() {
		request.paramHeaders, err = toolHeaderValues(call.Headers, args)
		if err != nil {
			return nil, err
		}
	}
	_, result, err := c.doRequest(ctx, conn, request)
	if err != nil {
		return nil, err
	}
	var out sdkmcp.CallToolResult
	if err := json.Unmarshal(result, &out); err != nil {
		return nil, fmt.Errorf("mcp: decode tools/call: %w", err)
	}
	return &out, nil
}

type wireRequest struct {
	method       string
	params       json.RawMessage
	requestID    int64
	name         string
	paramHeaders map[string]string
}

func (c *httpClient) doRequest(
	ctx context.Context,
	conn Conn,
	request wireRequest,
) (string, json.RawMessage, error) {
	id, err := jsonrpc.MakeID(float64(request.requestID))
	if err != nil {
		return "", nil, fmt.Errorf("mcp: build request id: %w", err)
	}
	params := request.params
	var headers http.Header
	if conn.Stateless() {
		params, err = withRequestMeta(params, conn.ProtocolVersion, c.clientInfo)
		if err != nil {
			return "", nil, err
		}
		headers, err = statelessRequestHeaders(request.method, request.name, request.paramHeaders)
		if err != nil {
			return "", nil, err
		}
	}
	body, err := jsonrpc.EncodeMessage(&jsonrpc.Request{
		ID:     id,
		Method: request.method,
		Params: params,
	})
	if err != nil {
		return "", nil, fmt.Errorf("mcp: marshal request: %w", err)
	}

	httpReq, cancel, err := c.buildHTTPForResponse(ctx, conn, body, headers)
	if err != nil {
		return "", nil, err
	}
	defer cancel()

	resp, err := c.doHTTPWithResponseTimeout(ctx, httpReq, cancel)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	sessionID := resp.Header.Get(headerSessionID)

	if resp.StatusCode == http.StatusNotFound && conn.HasSession() {
		return "", nil, ErrSessionExpired
	}
	if resp.StatusCode != http.StatusOK {
		return "", nil, c.statusError(resp)
	}

	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return "", nil, fmt.Errorf("%w: %q", ErrUnsupportedResponse, resp.Header.Get("Content-Type"))
	}
	var result json.RawMessage
	switch mediaType {
	case mediaTypeJSON:
		result, err = c.handleJSON(resp.Body, request.requestID)
	case mediaTypeSSE:
		result, err = c.handleSSE(ctx, resp.Body, request.requestID)
	default:
		return "", nil, fmt.Errorf("%w: %q", ErrUnsupportedResponse, mediaType)
	}
	if err != nil {
		return "", nil, err
	}
	if conn.Stateless() {
		if err := checkResultType(result); err != nil {
			return "", nil, err
		}
	}
	return sessionID, result, nil
}

func (c *httpClient) buildHTTP(
	ctx context.Context,
	conn Conn,
	body []byte,
	extra http.Header,
) (*http.Request, error) {
	if conn.EndpointURL == "" {
		return nil, errors.New("mcp: empty endpoint URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, conn.EndpointURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("mcp: build request: %w", err)
	}
	req.Header.Set("Content-Type", mediaTypeJSON)
	req.Header.Set("Accept", acceptHeader)
	if conn.MCPSessionID != "" && !conn.Stateless() {
		req.Header.Set(headerSessionID, conn.MCPSessionID)
	}
	if conn.ProtocolVersion != "" {
		req.Header.Set(headerProtocolVersion, conn.ProtocolVersion)
	}
	for name, values := range extra {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	if conn.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+conn.BearerToken)
	}
	if conn.prepareRequest != nil {
		if err := conn.prepareRequest(ctx, req, body); err != nil {
			return nil, fmt.Errorf("mcp: prepare request: %w", err)
		}
	}
	return req, nil
}

func (c *httpClient) buildHTTPForResponse(
	ctx context.Context,
	conn Conn,
	body []byte,
	extra http.Header,
) (*http.Request, context.CancelFunc, error) {
	if _, ok := ctx.Deadline(); ok {
		req, err := c.buildHTTP(ctx, conn, body, extra)
		return req, func() {}, err
	}

	reqCtx, cancel := context.WithCancel(ctx)
	req, err := c.buildHTTP(reqCtx, conn, body, extra)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return req, cancel, nil
}

func (c *httpClient) doHTTPWithResponseTimeout(
	ctx context.Context,
	req *http.Request,
	cancel context.CancelFunc,
) (*http.Response, error) {
	if _, ok := ctx.Deadline(); ok {
		return c.http.Do(req)
	}

	type result struct {
		resp *http.Response
		err  error
	}
	done := make(chan result, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- result{err: fmt.Errorf(
					"mcp: HTTP request panicked: %v",
					recovered,
				)}
			}
		}()
		resp, err := c.http.Do(req) //nolint:bodyclose // Ownership is transferred through done.
		done <- result{resp: resp, err: err}
	}()

	timer := time.NewTimer(c.requestTimeout)
	defer timer.Stop()

	select {
	case out := <-done:
		return out.resp, out.err
	case <-timer.C:
		cancel()
		out := <-done
		if out.resp != nil {
			_ = out.resp.Body.Close()
		}
		if out.err != nil {
			return nil, out.err
		}
		return nil, context.DeadlineExceeded
	case <-ctx.Done():
		cancel()
		out := <-done
		if out.resp != nil {
			_ = out.resp.Body.Close()
		}
		if out.err != nil {
			return nil, out.err
		}
		return nil, ctx.Err()
	}
}

func (c *httpClient) handleJSON(body io.Reader, wantID int64) (json.RawMessage, error) {
	raw, err := io.ReadAll(io.LimitReader(body, c.bodyLimit+1))
	if err != nil {
		return nil, fmt.Errorf("mcp: read response: %w", err)
	}
	if int64(len(raw)) > c.bodyLimit {
		return nil, ErrResponseTooLarge
	}

	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var batch []json.RawMessage
		if err := json.Unmarshal(raw, &batch); err != nil {
			return nil, fmt.Errorf("mcp: decode response batch: %w", err)
		}
		for _, rawMsg := range batch {
			msg, err := jsonrpc.DecodeMessage(rawMsg)
			if err != nil {
				continue
			}
			resp, ok := msg.(*jsonrpc.Response)
			if ok && idMatches(resp.ID, wantID) {
				return resolveResponse(resp)
			}
		}
		return nil, fmt.Errorf("mcp: no response in batch matched request id %d", wantID)
	}

	msg, err := jsonrpc.DecodeMessage(raw)
	if err != nil {
		return nil, fmt.Errorf("mcp: decode response envelope: %w", err)
	}
	resp, ok := msg.(*jsonrpc.Response)
	if !ok {
		return nil, fmt.Errorf("mcp: expected JSON-RPC response, got %T", msg)
	}
	if !idMatches(resp.ID, wantID) {
		return nil, fmt.Errorf("mcp: response id mismatch: got %v, want %d", resp.ID.Raw(), wantID)
	}
	return resolveResponse(resp)
}

func (c *httpClient) handleSSE(ctx context.Context, body io.ReadCloser, wantID int64) (json.RawMessage, error) {
	timer := time.AfterFunc(c.sseTimeout, func() { _ = body.Close() })
	defer timer.Stop()

	limited := io.LimitReader(body, c.bodyLimit+1)
	var bytesRead int64
	logger := mlog.LoggerFromContext(ctx)

	for ev, err := range scanSSEEvents(&countingReader{r: limited, n: &bytesRead}) {
		if err != nil {
			if bytesRead > c.bodyLimit {
				return nil, ErrResponseTooLarge
			}
			return nil, fmt.Errorf("mcp: scan SSE: %w", err)
		}

		msg, err := jsonrpc.DecodeMessage(ev.Data)
		if err != nil {
			logger.WarnContext(ctx, "mcp: dropping malformed SSE event",
				"err", err,
				"event_type", ev.Type,
				"last_event_id", ev.LastEventID,
				"data_len", len(ev.Data),
			)
			continue
		}

		resp, ok := msg.(*jsonrpc.Response)
		if !ok {
			continue
		}
		if !idMatches(resp.ID, wantID) {
			logger.WarnContext(ctx, "mcp: skipping non-matching SSE response",
				"response_id", resp.ID.Raw(),
				"want_id", wantID,
				"event_type", ev.Type,
			)
			continue
		}
		return resolveResponse(resp)
	}

	if bytesRead > c.bodyLimit {
		return nil, ErrResponseTooLarge
	}
	return nil, ErrIncompleteStream
}

func idMatches(id jsonrpc.ID, want int64) bool {
	switch got := id.Raw().(type) {
	case int64:
		return got == want
	case float64:
		return int64(got) == want
	case string:
		return got == fmt.Sprint(want)
	default:
		return false
	}
}

func resolveResponse(resp *jsonrpc.Response) (json.RawMessage, error) {
	if resp.Error != nil {
		var wireErr *jsonrpc.Error
		if errors.As(resp.Error, &wireErr) {
			return nil, &RPCError{Code: int(wireErr.Code), Message: wireErr.Message, Data: wireErr.Data}
		}
		return nil, &RPCError{Message: resp.Error.Error()}
	}
	return resp.Result, nil
}

const (
	statusErrorPreviewBytes = 512
	statusErrorDecodeBytes  = 8 * 1024
)

func (c *httpClient) statusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, statusErrorDecodeBytes))
	if rpcErr, ok := decodeRPCErrorBody(body); ok {
		rpcErr.HTTPStatus = resp.StatusCode
		return rpcErr
	}
	preview := body
	if len(preview) > statusErrorPreviewBytes {
		preview = preview[:statusErrorPreviewBytes]
	}
	return &HTTPError{Status: resp.StatusCode, Body: bytes.TrimSpace(preview)}
}

func decodeRPCErrorBody(body []byte) (*RPCError, bool) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, false
	}
	msg, decodeErr := jsonrpc.DecodeMessage(trimmed)
	if decodeErr == nil {
		if resp, ok := msg.(*jsonrpc.Response); ok {
			var rpcErr *RPCError
			if _, err := resolveResponse(resp); errors.As(err, &rpcErr) {
				return rpcErr, true
			}
		}
	}
	return nil, false
}

type countingReader struct {
	r io.Reader
	n *int64
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	*cr.n += int64(n)
	return n, err
}
