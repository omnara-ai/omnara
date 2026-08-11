//go:build integration

package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/mcp"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type ownedMCPToolClient struct {
	initializeCount atomic.Int32
	callToolCount   atomic.Int32
	callTool        func(context.Context) (*sdkmcp.CallToolResult, error)
}

func (c *ownedMCPToolClient) Initialize(
	context.Context,
	mcp.Conn,
	string,
) (string, mcp.InitializeResult, error) {
	c.initializeCount.Add(1)
	return "refreshed-session", mcp.InitializeResult{
		ProtocolVersion:    mcp.ProtocolVersion,
		ServerCapabilities: json.RawMessage(`{"tools":{}}`),
		ServerInfo:         json.RawMessage(`{"name":"fixture"}`),
	}, nil
}

func (*ownedMCPToolClient) Notify(context.Context, mcp.Conn, string, json.RawMessage) error {
	return nil
}

func (*ownedMCPToolClient) Call(
	context.Context,
	mcp.Conn,
	string,
	json.RawMessage,
	int64,
) (json.RawMessage, error) {
	return nil, errors.New("unexpected generic MCP call")
}

func (*ownedMCPToolClient) ListTools(
	context.Context,
	mcp.Conn,
	int64,
) ([]*sdkmcp.Tool, error) {
	return []*sdkmcp.Tool{{Name: "greet", InputSchema: map[string]any{"type": "object"}}}, nil
}

func (c *ownedMCPToolClient) CallTool(
	ctx context.Context,
	_ mcp.Conn,
	_ int64,
	_ string,
	_ json.RawMessage,
) (*sdkmcp.CallToolResult, error) {
	c.callToolCount.Add(1)
	if c.callTool == nil {
		return nil, errors.New("unexpected MCP tool call")
	}
	return c.callTool(ctx)
}

func TestMCPConnectionRefreshesAndPersistsExpiredOAuthToken(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixtureWithMCP(t, ctx, "mcp-oauth-refresh", true)

	var refreshCount atomic.Int32
	firstRefreshStarted := make(chan struct{})
	releaseFirstRefresh := make(chan struct{})
	secondRefreshStarted := make(chan struct{})
	releaseSecondRefresh := make(chan struct{})
	firstRefreshReleased := false
	secondRefreshReleased := false
	defer func() {
		if !firstRefreshReleased {
			close(releaseFirstRefresh)
		}
		if !secondRefreshReleased {
			close(releaseSecondRefresh)
		}
	}()
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("resource") != "https://example.com/mcp" {
			http.Error(w, "invalid refresh request", http.StatusBadRequest)
			return
		}
		var release <-chan struct{}
		var response string
		switch refreshCount.Add(1) {
		case 1:
			if r.Form.Get("refresh_token") != "refresh-old" {
				http.Error(w, "invalid first refresh token", http.StatusBadRequest)
				return
			}
			close(firstRefreshStarted)
			release = releaseFirstRefresh
			response = `{"access_token":"access-new","refresh_token":"refresh-new","token_type":"Bearer","expires_in":3600}`
		case 2:
			if r.Form.Get("refresh_token") != "refresh-new" {
				http.Error(w, "invalid second refresh token", http.StatusBadRequest)
				return
			}
			close(secondRefreshStarted)
			release = releaseSecondRefresh
			response = `{"access_token":"access-stale","refresh_token":"refresh-stale","token_type":"Bearer","expires_in":3600}`
		default:
			http.Error(w, "unexpected refresh request", http.StatusBadRequest)
			return
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(response)); err != nil {
			t.Errorf("write OAuth refresh response: %v", err)
		}
	}))
	t.Cleanup(tokenServer.Close)

	secret, initialVersion, err := fixture.Store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          toolsTestOrgID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: toolsTestProjectID,
		Name:           "mcp-oauth-refresh",
		Material: secrets.OAuthTokenSetMaterial{
			AccessToken:         "access-old",
			AccessTokenLifetime: secrets.FixedOAuthAccessTokenLifetime(time.Hour),
			Refresh: &secrets.OAuthRefreshMaterial{
				RefreshToken:  "refresh-old",
				TokenEndpoint: tokenServer.URL,
				ClientID:      "client-id",
				Resource:      "https://example.com/mcp",
			},
		},
		Actor: toolsTestUserPrincipal(fixture.User.ID),
	})
	if err != nil {
		t.Fatalf("create MCP OAuth secret: %v", err)
	}
	if _, err := fixture.Pool.Exec(ctx, `
		UPDATE secret_versions
		SET created_at = statement_timestamp() - interval '2 hours',
		    oauth_access_token_expires_at = statement_timestamp() - interval '1 hour'
		WHERE org_id = $1 AND id = $2
	`, toolsTestOrgID, initialVersion.ID); err != nil {
		t.Fatalf("expire MCP OAuth access token: %v", err)
	}
	secretPublicID, err := publicid.Encode(publicid.KindSecret, secret.ID)
	if err != nil {
		t.Fatalf("encode MCP OAuth secret ID: %v", err)
	}
	conn, found, err := fixture.Store.Execution().GetMCPConnection(ctx, toolsTestProjectID, fixture.Agent.ID, "docs")
	if err != nil || !found {
		t.Fatalf("load MCP connection: found=%t err=%v", found, err)
	}
	server := agentconfig.RuntimeMCPServer{
		ServerKey: "docs",
		URL:       conn.EndpointURL,
		Auth: &agentconfig.RuntimeMCPAuth{
			Type:     agentconfig.MCPAuthTypeOAuth,
			SecretID: secretPublicID,
		},
	}
	manager := mcp.Manager{
		Execution:            fixture.Store.Execution(),
		Secrets:              fixture.Store.Secrets(),
		OAuthHTTPClient:      tokenServer.Client(),
		OAuthRefreshLeaseTTL: 10 * time.Second,
	}

	type connectionResult struct {
		conn mcp.Conn
		err  error
	}
	refreshCtx, cancelRefresh := context.WithCancel(ctx)
	connected := make(chan connectionResult, 1)
	go func() {
		wireConn, connectErr := manager.Connection(
			refreshCtx,
			toolsTestOrgID,
			toolsTestProjectID,
			conn,
			server,
			"",
			"",
		)
		connected <- connectionResult{conn: wireConn, err: connectErr}
	}()
	select {
	case <-firstRefreshStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("first OAuth refresh request did not start")
	}
	cancelRefresh()
	close(releaseFirstRefresh)
	firstRefreshReleased = true
	connection := <-connected
	if connection.err != nil {
		t.Fatalf("connect with expired MCP OAuth token: %v", connection.err)
	}
	wireConn := connection.conn
	if wireConn.BearerToken != "access-new" || refreshCount.Load() != 1 {
		t.Fatalf("refreshed MCP connection token=%q refreshes=%d", wireConn.BearerToken, refreshCount.Load())
	}
	payload, err := fixture.Store.Secrets().ReadProjectAvailableSecretPayload(
		ctx,
		secretstore.ReadProjectAvailableSecretPayloadInput{
			OrgID: toolsTestOrgID, ProjectID: toolsTestProjectID, SecretID: secret.ID, Kind: secrets.KindOAuthTokenSet,
		},
	)
	if err != nil {
		t.Fatalf("read refreshed MCP OAuth secret: %v", err)
	}
	if payload.CurrentVersionID == initialVersion.ID || payload.Payload[secrets.KeyAccessToken] != "access-new" ||
		payload.Payload[secrets.KeyRefreshToken] != "refresh-new" ||
		payload.OAuthAccessTokenRemaining <= 50*time.Minute {
		t.Fatalf("refreshed MCP OAuth secret = %+v", payload)
	}
	if _, err := fixture.Pool.Exec(ctx, `
		UPDATE secret_versions
		SET created_at = statement_timestamp() - interval '2 hours',
		    oauth_access_token_expires_at = statement_timestamp() - interval '1 hour'
		WHERE org_id = $1 AND id = $2
	`, toolsTestOrgID, payload.CurrentVersionID); err != nil {
		t.Fatalf("expire refreshed MCP OAuth access token: %v", err)
	}
	connected = make(chan connectionResult, 1)
	go func() {
		wireConn, connectErr := manager.Connection(
			context.Background(),
			toolsTestOrgID,
			toolsTestProjectID,
			conn,
			server,
			"",
			"",
		)
		connected <- connectionResult{conn: wireConn, err: connectErr}
	}()
	select {
	case <-secondRefreshStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("second OAuth refresh request did not start")
	}
	manualSecret, _, err := fixture.Store.Secrets().CreateSecretVersion(ctx, secretstore.CreateSecretVersionInput{
		OrgID:    toolsTestOrgID,
		SecretID: secret.ID,
		Material: secrets.OAuthTokenSetMaterial{
			AccessToken:         "access-manual",
			AccessTokenLifetime: secrets.FixedOAuthAccessTokenLifetime(time.Hour),
			Refresh: &secrets.OAuthRefreshMaterial{
				RefreshToken:  "refresh-manual",
				TokenEndpoint: tokenServer.URL,
				ClientID:      "client-id",
				Resource:      "https://example.com/mcp",
			},
		},
		Actor: toolsTestUserPrincipal(fixture.User.ID),
	})
	if err != nil {
		t.Fatalf("manual OAuth update during provider refresh: %v", err)
	}
	close(releaseSecondRefresh)
	secondRefreshReleased = true
	connection = <-connected
	if connection.err != nil {
		t.Fatalf("connect after manual OAuth replacement: %v", connection.err)
	}
	if connection.conn.BearerToken != "access-manual" || refreshCount.Load() != 2 {
		t.Fatalf(
			"connection after manual replacement token=%q refreshes=%d",
			connection.conn.BearerToken,
			refreshCount.Load(),
		)
	}
	payload, err = fixture.Store.Secrets().ReadProjectAvailableSecretPayload(
		ctx,
		secretstore.ReadProjectAvailableSecretPayloadInput{
			OrgID: toolsTestOrgID, ProjectID: toolsTestProjectID, SecretID: secret.ID, Kind: secrets.KindOAuthTokenSet,
		},
	)
	if err != nil {
		t.Fatalf("read manually replaced MCP OAuth secret: %v", err)
	}
	if payload.CurrentVersionID != manualSecret.CurrentVersionID ||
		payload.Payload[secrets.KeyAccessToken] != "access-manual" ||
		payload.Payload[secrets.KeyRefreshToken] != "refresh-manual" {
		t.Fatalf("manually replaced MCP OAuth secret = %+v", payload)
	}

	wireConn, err = manager.Connection(ctx, toolsTestOrgID, toolsTestProjectID, conn, server, "", "")
	if err != nil {
		t.Fatalf("connect with persisted MCP OAuth token: %v", err)
	}
	if wireConn.BearerToken != "access-manual" || refreshCount.Load() != 2 {
		t.Fatalf("persisted MCP connection token=%q refreshes=%d", wireConn.BearerToken, refreshCount.Load())
	}
}

func TestSigV4MCPConnectionRechecksSecretGrant(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixtureWithMCP(t, ctx, "mcp-aws-grant", true)
	var authorization atomic.Value
	var sessionToken atomic.Value
	var requests atomic.Int32
	endpoint := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		authorization.Store(r.Header.Get("Authorization"))
		sessionToken.Store(r.Header.Get("X-Amz-Security-Token"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer endpoint.Close()
	secret, _, err := fixture.Store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     toolsTestOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "aws-read-only",
		Material: secrets.AWSCredentialsMaterial{
			AccessKeyID:     "AKIAEXAMPLE",
			SecretAccessKey: "secret",
			SessionToken:    "session-token",
		},
		Actor: toolsTestUserPrincipal(fixture.User.ID),
	})
	if err != nil {
		t.Fatalf("create AWS credentials secret: %v", err)
	}
	grant, err := fixture.Store.Secrets().CreateSecretGrant(ctx, secretstore.CreateSecretGrantInput{
		OrgID:           toolsTestOrgID,
		SecretID:        secret.ID,
		TargetProjectID: toolsTestProjectID,
		Actor:           toolsTestUserPrincipal(fixture.User.ID),
	})
	if err != nil {
		t.Fatalf("grant AWS credentials secret: %v", err)
	}
	secretPublicID, err := publicid.Encode(publicid.KindSecret, secret.ID)
	if err != nil {
		t.Fatalf("encode AWS credentials secret ID: %v", err)
	}
	conn, found, err := fixture.Store.Execution().GetMCPConnection(ctx, toolsTestProjectID, fixture.Agent.ID, "docs")
	if err != nil || !found {
		t.Fatalf("load MCP connection: found=%t err=%v", found, err)
	}
	conn.EndpointURL = endpoint.URL
	server := agentconfig.RuntimeMCPServer{
		ServerKey: "docs",
		URL:       conn.EndpointURL,
		Auth: &agentconfig.RuntimeMCPAuth{
			Type:     agentconfig.MCPAuthTypeSigV4,
			SecretID: secretPublicID,
			Service:  "execute-api",
			Region:   "us-west-2",
		},
	}
	manager := mcp.Manager{Secrets: fixture.Store.Secrets()}
	wireConn, err := manager.Connection(ctx, toolsTestOrgID, toolsTestProjectID, conn, server, "", "")
	if err != nil {
		t.Fatalf("connect with granted AWS credentials: %v", err)
	}
	client := mcp.New(mcp.Options{HTTPClient: endpoint.Client()})
	if _, err := client.Call(ctx, wireConn, "ping", json.RawMessage(`{}`), 1); err != nil {
		t.Fatalf("call with granted AWS credentials: %v", err)
	}
	gotAuthorization, _ := authorization.Load().(string)
	if !strings.Contains(gotAuthorization, "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/") ||
		!strings.Contains(gotAuthorization, "/us-west-2/execute-api/aws4_request") {
		t.Fatalf("unexpected AWS Authorization header %q", gotAuthorization)
	}
	if gotSessionToken, _ := sessionToken.Load().(string); gotSessionToken != "session-token" {
		t.Fatalf("AWS session token = %q", gotSessionToken)
	}
	if _, err := fixture.Store.Secrets().DeleteSecretGrant(ctx, secretstore.DeleteSecretGrantInput{
		OrgID:    toolsTestOrgID,
		SecretID: secret.ID,
		GrantID:  grant.ID,
		Actor:    toolsTestUserPrincipal(fixture.User.ID),
	}); err != nil {
		t.Fatalf("delete AWS credentials grant: %v", err)
	}
	if _, err := manager.Connection(ctx, toolsTestOrgID, toolsTestProjectID, conn, server, "", ""); err == nil {
		t.Fatal("expected revoked AWS credentials grant to block the connection")
	}
	if requests.Load() != 1 {
		t.Fatalf("AWS MCP requests after grant revocation = %d, want 1", requests.Load())
	}
}

func TestMCPDispatchRunsAsynchronouslyUnderRuntimeOwnership(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixtureWithMCP(t, ctx, "mcp-async-runtime", true)
	markIntegrationMCPConnectionReady(t, ctx, fixture)
	call := fixture.recordToolCall(
		t,
		ctx,
		"call_mcp_async_runtime",
		toolcatalog.MCPRuntimeToolName("docs", "greet"),
		`{"name":"Ada"}`,
		fixture.Now.Add(20*time.Second),
	)

	started := make(chan struct{})
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	client := &ownedMCPToolClient{callTool: func(ctx context.Context) (*sdkmcp.CallToolResult, error) {
		close(started)
		select {
		case <-release:
			return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "hello"}}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	executor := Executor{Store: fixture.Store, MCP: client}
	scope := NewAsyncExecutionScope(nil)
	result, err := executor.Dispatch(WithAsyncExecutionScope(ctx, scope), fixture.turn(), call)
	if err != nil {
		t.Fatalf("dispatch MCP tool: %v", err)
	}
	if result.Disposition != DispatchDeferred {
		t.Fatalf("MCP dispatch result = %+v, want deferred", result)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("MCP HTTP call did not start")
	}
	toolCallID := fixture.toolCallID(t, ctx, call.ID)
	var toolType string
	var state string
	var runtimeLockID storage.ID
	if err := fixture.Pool.QueryRow(
		ctx,
		`
SELECT call.type, call.state, call.runtime_lock_id
FROM tool_calls call
WHERE call.id = $1
`,
		toolCallID,
	).Scan(&toolType, &state, &runtimeLockID); err != nil {
		t.Fatalf("load running MCP tool call: %v", err)
	}
	if toolType != toolcatalog.ToolTypeMCP ||
		state != "running" ||
		runtimeLockID != fixture.Lock.ID {
		t.Fatalf(
			"running MCP tool call type=%q state=%q runtime=%s, want mcp/running/%s",
			toolType,
			state,
			runtimeLockID,
			fixture.Lock.ID,
		)
	}
	duplicateScope := NewAsyncExecutionScope(NewAsyncExecutionLimiter(1))
	duplicateResult, err := executor.Dispatch(
		WithAsyncExecutionScope(ctx, duplicateScope),
		fixture.turn(),
		call,
	)
	if err != nil {
		t.Fatalf("duplicate MCP dispatch: %v", err)
	}
	if duplicateResult.Disposition != DispatchDeferred {
		t.Fatalf("duplicate MCP dispatch result = %+v, want deferred", duplicateResult)
	}
	duplicateScope.Seal()
	select {
	case <-duplicateScope.Done():
	case <-time.After(time.Second):
		t.Fatal("duplicate MCP dispatch did not release its unused async reservation")
	}
	if duplicateScope.Started() {
		t.Fatal("duplicate MCP dispatch marked its unused async reservation as started")
	}
	if calls := client.callToolCount.Load(); calls != 1 {
		t.Fatalf("MCP tool calls after duplicate dispatch = %d, want 1", calls)
	}
	released = true
	close(release)
	settleIntegrationAsyncDispatch(t, scope)
	assertIntegrationToolCallState(t, ctx, fixture, toolCallID, "completed")
}

func TestMCPDispatchRecordsRemoteToolErrorAsFailed(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixtureWithMCP(t, ctx, "mcp-remote-error", true)
	markIntegrationMCPConnectionReady(t, ctx, fixture)
	call := fixture.recordToolCall(
		t,
		ctx,
		"call_mcp_remote_error",
		toolcatalog.MCPRuntimeToolName("docs", "greet"),
		`{"name":"Ada"}`,
		fixture.Now.Add(20*time.Second),
	)
	client := &ownedMCPToolClient{callTool: func(context.Context) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "remote tool rejected the request"}},
			IsError: true,
		}, nil
	}}
	executor := Executor{Store: fixture.Store, MCP: client}
	scope := NewAsyncExecutionScope(nil)
	result, err := executor.Dispatch(WithAsyncExecutionScope(ctx, scope), fixture.turn(), call)
	if err != nil {
		t.Fatalf("dispatch MCP tool: %v", err)
	}
	if result.Disposition != DispatchDeferred {
		t.Fatalf("MCP dispatch result = %+v, want deferred", result)
	}
	toolCallID := fixture.toolCallID(t, ctx, call.ID)
	settleIntegrationAsyncDispatch(t, scope)
	assertIntegrationToolCallState(t, ctx, fixture, toolCallID, "completed")
	record, err := fixture.Store.Execution().GetToolCall(
		ctx,
		toolsTestProjectID,
		fixture.Agent.ID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("load failed MCP tool call: %v", err)
	}
	if record.Outcome != executionstore.ToolResultOutcomeFailed ||
		!strings.Contains(string(record.ResultContentParts), "remote tool rejected the request") {
		t.Fatalf("failed MCP tool call = %+v", record)
	}
}

func TestMCPDispatchPersistsMediaAndStructuredContentInOrder(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixtureWithMCP(
		t,
		ctx,
		"mcp-media-result",
		true,
		storage.WithBlobStore(integrationblob.MustOpen(t, ctx)),
	)
	markIntegrationMCPConnectionReady(t, ctx, fixture)
	call := fixture.recordToolCall(
		t,
		ctx,
		"call_mcp_media_result",
		toolcatalog.MCPRuntimeToolName("docs", "greet"),
		`{"name":"Ada"}`,
		fixture.Now.Add(20*time.Second),
	)
	media := []byte("\x89PNG\r\n\x1a\nmcp-result")
	client := &ownedMCPToolClient{callTool: func(context.Context) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: "before"},
				&sdkmcp.ImageContent{MIMEType: "image/png", Data: media},
			},
			StructuredContent: map[string]any{"count": 1},
		}, nil
	}}
	executor := Executor{Store: fixture.Store, MCP: client}
	scope := NewAsyncExecutionScope(nil)
	result, err := executor.Dispatch(WithAsyncExecutionScope(ctx, scope), fixture.turn(), call)
	if err != nil {
		t.Fatalf("dispatch MCP media tool: %v", err)
	}
	if result.Disposition != DispatchDeferred {
		t.Fatalf("MCP dispatch result = %+v, want deferred", result)
	}
	toolCallID := fixture.toolCallID(t, ctx, call.ID)
	settleIntegrationAsyncDispatch(t, scope)
	assertIntegrationToolCallState(t, ctx, fixture, toolCallID, "completed")
	record, err := fixture.Store.Execution().GetToolCall(
		ctx,
		toolsTestProjectID,
		fixture.Agent.ID,
		toolCallID,
	)
	if err != nil {
		t.Fatalf("load completed MCP media tool: %v", err)
	}
	var parts []map[string]any
	if err := json.Unmarshal(record.ResultContentParts, &parts); err != nil {
		t.Fatalf("decode MCP media result: %v", err)
	}
	if len(parts) != 3 ||
		parts[0]["type"] != "text" ||
		parts[1]["type"] != "media_ref" ||
		parts[2]["type"] != "structured_data" {
		t.Fatalf("MCP media result order = %s", record.ResultContentParts)
	}
	rawArtifactID, ok := parts[1]["artifact_id"].(string)
	if !ok {
		t.Fatalf("MCP media result artifact id = %v", parts[1]["artifact_id"])
	}
	artifactID, err := storage.ParseID(rawArtifactID)
	if err != nil {
		t.Fatalf("parse MCP result artifact id: %v", err)
	}
	content, artifact, err := fixture.Store.Artifacts().GetArtifactBlob(
		ctx,
		toolsTestProjectID,
		fixture.Agent.ID,
		artifactID,
	)
	if err != nil {
		t.Fatalf("load MCP result artifact: %v", err)
	}
	if artifact.ContentType != "image/png" || !bytes.Equal(content, media) {
		t.Fatalf(
			"MCP result artifact content_type=%q content=%q",
			artifact.ContentType,
			content,
		)
	}
}

func TestMCPDispatchRejectsInactiveRuntimeBeforeExternalCall(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixtureWithMCP(t, ctx, "mcp-inactive-replay", true)
	markIntegrationMCPConnectionReady(t, ctx, fixture)
	call := fixture.recordToolCall(
		t,
		ctx,
		"call_mcp_inactive_replay",
		toolcatalog.MCPRuntimeToolName("docs", "greet"),
		`{"name":"Ada"}`,
		fixture.Now.Add(20*time.Second),
	)

	if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
		ctx,
		toolsTestProjectID,
		fixture.Agent.ID,
		fixture.Lock.ID,
	); err != nil {
		t.Fatalf("release runtime lock: %v", err)
	}
	client := &ownedMCPToolClient{callTool: func(context.Context) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{}, nil
	}}
	executor := Executor{Store: fixture.Store, MCP: client}
	if _, err := executor.Dispatch(ctx, fixture.turn(), call); !errors.Is(err, storeerr.ErrRuntimeLockInactive) {
		t.Fatalf("dispatch with inactive runtime error = %v, want ErrRuntimeLockInactive", err)
	}
	if client.callToolCount.Load() != 0 {
		t.Fatalf("MCP tool calls = %d, want 0", client.callToolCount.Load())
	}
}

func TestMCPDispatchDoesNotRefreshAfterOwnershipLoss(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixtureWithMCP(t, ctx, "mcp-lost-before-refresh", true)
	markIntegrationMCPConnectionReady(t, ctx, fixture)
	call := fixture.recordToolCall(
		t,
		ctx,
		"call_mcp_lost_before_refresh",
		toolcatalog.MCPRuntimeToolName("docs", "greet"),
		`{"name":"Ada"}`,
		fixture.Now.Add(20*time.Second),
	)

	client := &ownedMCPToolClient{}
	client.callTool = func(context.Context) (*sdkmcp.CallToolResult, error) {
		if err := fixture.Store.Execution().ReleaseAgentRuntimeLock(
			ctx,
			toolsTestProjectID,
			fixture.Agent.ID,
			fixture.Lock.ID,
		); err != nil {
			t.Fatalf("release runtime lock after first MCP call: %v", err)
		}
		return nil, mcp.ErrSessionExpired
	}
	executor := Executor{Store: fixture.Store, MCP: client}
	scope := NewAsyncExecutionScope(nil)
	result, err := executor.Dispatch(WithAsyncExecutionScope(ctx, scope), fixture.turn(), call)
	if err != nil {
		t.Fatalf("dispatch MCP tool before ownership loss: %v", err)
	}
	if result.Disposition != DispatchDeferred {
		t.Fatalf("MCP dispatch result = %+v, want deferred", result)
	}
	toolCallID := fixture.toolCallID(t, ctx, call.ID)
	settleIntegrationAsyncDispatch(t, scope)
	assertIntegrationToolCallState(t, ctx, fixture, toolCallID, "completed")
	if client.callToolCount.Load() != 1 {
		t.Fatalf("MCP tool calls = %d, want 1", client.callToolCount.Load())
	}
	if client.initializeCount.Load() != 0 {
		t.Fatalf("MCP refresh attempts = %d, want 0", client.initializeCount.Load())
	}
}

func TestMCPDispatchRecordsUnknownOutcomeWhenExecutionIsInterrupted(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixtureWithMCP(t, ctx, "mcp-interrupted", true)
	markIntegrationMCPConnectionReady(t, ctx, fixture)
	call := fixture.recordToolCall(
		t,
		ctx,
		"call_mcp_interrupted",
		toolcatalog.MCPRuntimeToolName("docs", "greet"),
		`{"name":"Ada"}`,
		fixture.Now.Add(20*time.Second),
	)

	started := make(chan struct{})
	client := &ownedMCPToolClient{callTool: func(ctx context.Context) (*sdkmcp.CallToolResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	executor := Executor{Store: fixture.Store, MCP: client}
	executionCtx, cancelExecution := context.WithCancel(ctx)
	scope := NewAsyncExecutionScope(nil)
	result, err := executor.Dispatch(WithAsyncExecutionScope(executionCtx, scope), fixture.turn(), call)
	if err != nil {
		t.Fatalf("dispatch MCP tool: %v", err)
	}
	if result.Disposition != DispatchDeferred {
		t.Fatalf("MCP dispatch result = %+v, want deferred", result)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("MCP HTTP call did not start")
	}
	cancelExecution()

	toolCallID := fixture.toolCallID(t, ctx, call.ID)
	settleIntegrationAsyncDispatch(t, scope)
	assertIntegrationToolCallState(t, ctx, fixture, toolCallID, "completed")
	record, err := fixture.Store.Execution().GetToolCall(ctx, toolsTestProjectID, fixture.Agent.ID, toolCallID)
	if err != nil {
		t.Fatalf("load interrupted MCP tool call: %v", err)
	}
	if !strings.Contains(string(record.ResultContentParts), "async_tool_interrupted") ||
		!strings.Contains(string(record.ResultContentParts), executionstore.RuntimeToolInterruptedMessage) {
		t.Fatalf("interrupted MCP result = %s", record.ResultContentParts)
	}
}

func settleIntegrationAsyncDispatch(t *testing.T, scope *AsyncExecutionScope) {
	t.Helper()
	scope.Seal()
	select {
	case <-scope.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("async MCP tool execution did not finish")
	}
}

func assertIntegrationToolCallState(
	t *testing.T,
	ctx context.Context,
	fixture integrationToolFixture,
	toolCallID storage.ID,
	want string,
) {
	t.Helper()
	var state string
	if err := fixture.Pool.QueryRow(ctx, `SELECT state FROM tool_calls WHERE id = $1`, toolCallID).Scan(&state); err != nil {
		t.Fatalf("load MCP tool call state: %v", err)
	}
	if state != want {
		t.Fatalf("MCP tool call state = %q, want %q", state, want)
	}
}

func markIntegrationMCPConnectionReady(
	t *testing.T,
	ctx context.Context,
	fixture integrationToolFixture,
) {
	t.Helper()
	conn, found, err := fixture.Store.Execution().GetMCPConnection(
		ctx,
		toolsTestProjectID,
		fixture.Agent.ID,
		"docs",
	)
	if err != nil || !found {
		t.Fatalf("load MCP connection: found=%t err=%v", found, err)
	}
	if _, err := fixture.Store.Execution().MarkMCPConnectionReady(ctx, executionstore.MarkMCPConnectionReadyInput{
		ProjectID:          toolsTestProjectID,
		AgentID:            fixture.Agent.ID,
		ID:                 conn.ID,
		GenerationObserved: conn.Generation,
		MCPSessionID:       "active-session",
		ProtocolVersion:    mcp.ProtocolVersion,
		ServerCapabilities: json.RawMessage(`{"tools":{}}`),
		ServerInfo:         json.RawMessage(`{"name":"fixture"}`),
		ToolsSnapshot:      json.RawMessage(`[{"name":"greet"}]`),
	}); err != nil {
		t.Fatalf("mark MCP connection ready: %v", err)
	}
}
