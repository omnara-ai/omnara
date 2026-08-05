package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const InitializeMaxAttempts = 3

type Manager struct {
	Execution *executionstore.Store
	Secrets   *secretstore.Store
	Client    Client
	Backoff   func(attempt int) time.Duration

	OAuthHTTPClient *http.Client

	OAuthRefreshLeaseTTL time.Duration
	OAuthRefreshWait     func(attempt int) time.Duration
	OAuthRefreshMaxWaits int
}

type ConnectionResult struct {
	Conn    executionstore.MCPConnectionRecord
	Changed bool
	Ready   bool
}

type InitializationError struct {
	Cause    error
	Recorded bool
	Err      error
}

func (e *InitializationError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *InitializationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func InitializationCause(err error) error {
	var initErr *InitializationError
	if errors.As(err, &initErr) {
		return initErr.Cause
	}
	return nil
}

func InitializationRecorded(err error) bool {
	var initErr *InitializationError
	return errors.As(err, &initErr) && initErr.Recorded
}

func ShouldInitialize(state executionstore.MCPConnectionState) bool {
	switch state {
	case executionstore.MCPConnectionStateInitializing,
		executionstore.MCPConnectionStateFailed,
		executionstore.MCPConnectionStateExpired:
		return true
	default:
		return false
	}
}

func (m Manager) InitializePending(
	ctx context.Context,
	orgID, projectID, agentID storage.ID,
	conn executionstore.MCPConnectionRecord,
	server agentconfig.RuntimeMCPServer,
) (ConnectionResult, error) {
	begun, changed, err := m.Execution.BeginMCPConnectionInitialization(ctx, projectID, agentID, conn.ID)
	if err != nil {
		return ConnectionResult{}, err
	}
	if !changed {
		return ConnectionResult{}, nil
	}
	result, err := m.InitializeOrMarkFailed(ctx, orgID, projectID, agentID, begun, server)
	result.Changed = true
	return result, err
}

func (m Manager) RefreshExpired(
	ctx context.Context,
	orgID, projectID, agentID storage.ID,
	conn executionstore.MCPConnectionRecord,
	server agentconfig.RuntimeMCPServer,
) (ConnectionResult, error) {
	expired, changed, err := m.Execution.MarkMCPConnectionExpired(
		ctx,
		projectID,
		agentID,
		conn.ID,
		conn.Generation,
	)
	if err != nil {
		return ConnectionResult{}, err
	}
	if !changed {
		ready, err := m.readyConnectionByID(ctx, projectID, agentID, conn.ID)
		return ConnectionResult{Conn: ready, Ready: true}, err
	}
	begun, changed, err := m.Execution.BeginMCPConnectionInitialization(ctx, projectID, agentID, expired.ID)
	if err != nil {
		return ConnectionResult{}, err
	}
	if !changed {
		ready, err := m.readyConnectionByID(ctx, projectID, agentID, conn.ID)
		return ConnectionResult{Conn: ready, Ready: true}, err
	}
	result, err := m.InitializeOrMarkFailed(ctx, orgID, projectID, agentID, begun, server)
	result.Changed = true
	return result, err
}

func (m Manager) InitializeOrMarkFailed(
	ctx context.Context,
	orgID, projectID, agentID storage.ID,
	conn executionstore.MCPConnectionRecord,
	server agentconfig.RuntimeMCPServer,
) (ConnectionResult, error) {
	ready, cause := m.InitializeWithRetry(ctx, orgID, projectID, agentID, conn, server)
	if cause == nil {
		return ConnectionResult{Conn: ready, Ready: true}, nil
	}
	failed, markErr := m.Execution.MarkMCPConnectionFailed(
		ctx,
		projectID,
		agentID,
		conn.ID,
		conn.Generation,
		cause.Error(),
	)
	if markErr != nil {
		return ConnectionResult{}, &InitializationError{
			Cause:    cause,
			Recorded: false,
			Err:      fmt.Errorf("%w: %w", cause, markErr),
		}
	}
	return ConnectionResult{Conn: failed}, &InitializationError{Cause: cause, Recorded: true, Err: cause}
}

func (m Manager) InitializeWithRetry(
	ctx context.Context,
	orgID, projectID, agentID storage.ID,
	conn executionstore.MCPConnectionRecord,
	server agentconfig.RuntimeMCPServer,
) (executionstore.MCPConnectionRecord, error) {
	var cause error
	for attempt := 1; attempt <= InitializeMaxAttempts; attempt++ {
		ready, err := m.initialize(ctx, orgID, projectID, agentID, conn, server)
		cause = err
		if cause == nil {
			return ready, nil
		}
		cause = ClarifyTransportError(cause, conn.EndpointURL)
		if attempt == InitializeMaxAttempts || !IsRetryableConnectionFailure(cause) {
			break
		}
		backoff := defaultMCPInitializationBackoff(attempt)
		if m.Backoff != nil {
			backoff = m.Backoff(attempt)
		}
		if err := sleepBackoff(ctx, backoff); err != nil {
			cause = errors.Join(cause, err)
			break
		}
	}
	return executionstore.MCPConnectionRecord{}, cause
}

func (m Manager) readyConnectionByID(
	ctx context.Context,
	projectID, agentID, id storage.ID,
) (executionstore.MCPConnectionRecord, error) {
	conn, found, err := m.Execution.GetMCPConnectionByID(ctx, projectID, agentID, id)
	if err != nil {
		return executionstore.MCPConnectionRecord{}, err
	}
	if !found || conn.State != executionstore.MCPConnectionStateReady {
		return executionstore.MCPConnectionRecord{}, fmt.Errorf("mcp connection %s is not ready after refresh", id)
	}
	return conn, nil
}

// ClarifyTransportError rewrites worker-side transport guard errors
// (SSRF block, redirect block) into operator-readable messages that point at
// the configuration that caused them. The original sentinel is preserved in
// the error chain so callers can still match it with errors.Is.
func ClarifyTransportError(cause error, endpointURL string) error {
	if cause == nil {
		return nil
	}
	if errors.Is(cause, ssrf.ErrBlockedAddress) {
		return fmt.Errorf(
			"mcp endpoint %s resolves to a blocked address (loopback/private/reserved); "+
				"recompile the agent with a public https URL, or run the worker with "+
				"OMNARA_ALLOW_INSECURE_DEV_DEFAULTS=1 for local development: %w",
			endpointURL,
			cause,
		)
	}
	if errors.Is(cause, outboundhttp.ErrRedirect) {
		return fmt.Errorf(
			"mcp endpoint %s issued an HTTP redirect; configure a stable endpoint URL "+
				"(redirects are blocked to avoid leaking the MCP session header): %w",
			endpointURL,
			cause,
		)
	}
	return cause
}

func (m Manager) initialize(
	ctx context.Context,
	orgID, projectID, agentID storage.ID,
	conn executionstore.MCPConnectionRecord,
	server agentconfig.RuntimeMCPServer,
) (executionstore.MCPConnectionRecord, error) {
	wireConn, err := m.Connection(ctx, orgID, projectID, conn, server, "", "")
	if err != nil {
		return executionstore.MCPConnectionRecord{}, err
	}
	mcpSessionID, result, err := m.Client.Initialize(ctx, wireConn, ProtocolVersion)
	if err != nil {
		return executionstore.MCPConnectionRecord{}, fmt.Errorf("initialize mcp server %q: %w", conn.ServerKey, err)
	}
	negotiatedProtocol := result.ProtocolVersion
	if negotiatedProtocol == "" {
		negotiatedProtocol = ProtocolVersion
	}
	wireConn.MCPSessionID = mcpSessionID
	wireConn.ProtocolVersion = negotiatedProtocol
	if err := m.Client.Notify(ctx, wireConn, "notifications/initialized", json.RawMessage(`{}`)); err != nil {
		return executionstore.MCPConnectionRecord{}, fmt.Errorf(
			"send mcp initialized notification for %q: %w",
			conn.ServerKey,
			err,
		)
	}
	seq, err := m.Execution.NextMCPRequestSequence(ctx, projectID, agentID, conn.ID)
	if err != nil {
		return executionstore.MCPConnectionRecord{}, fmt.Errorf(
			"allocate mcp tools/list request sequence for %q: %w",
			conn.ServerKey,
			err,
		)
	}
	tools, err := m.Client.ListTools(ctx, wireConn, seq)
	if err != nil {
		return executionstore.MCPConnectionRecord{}, fmt.Errorf("list mcp tools for %q: %w", conn.ServerKey, err)
	}
	if err := validateDiscoveredTools(conn.ServerKey, tools); err != nil {
		return executionstore.MCPConnectionRecord{}, fmt.Errorf(
			"validate mcp tools for %q: %w",
			conn.ServerKey,
			err,
		)
	}
	toolsSnapshot, err := json.Marshal(tools)
	if err != nil {
		return executionstore.MCPConnectionRecord{}, fmt.Errorf("marshal mcp tools snapshot for %q: %w", conn.ServerKey, err)
	}
	ready, err := m.Execution.MarkMCPConnectionReady(ctx, executionstore.MarkMCPConnectionReadyInput{
		ProjectID:          projectID,
		AgentID:            agentID,
		ID:                 conn.ID,
		GenerationObserved: conn.Generation,
		MCPSessionID:       mcpSessionID,
		ProtocolVersion:    negotiatedProtocol,
		ServerCapabilities: result.ServerCapabilities,
		ServerInfo:         result.ServerInfo,
		ToolsSnapshot:      toolsSnapshot,
	})
	if err != nil {
		return executionstore.MCPConnectionRecord{}, fmt.Errorf("mark mcp connection %q ready: %w", conn.ServerKey, err)
	}
	return ready, nil
}

func validateDiscoveredTools(serverKey string, tools []*sdkmcp.Tool) error {
	seen := make(map[string]struct{}, len(tools))
	for index, tool := range tools {
		if tool == nil {
			return fmt.Errorf("tool at index %d is null", index)
		}
		runtimeName := toolcatalog.MCPRuntimeToolName(serverKey, tool.Name)
		if !toolcatalog.IsMCPRuntimeToolName(runtimeName) {
			return fmt.Errorf("tool name %q cannot be exposed to the model", tool.Name)
		}
		if _, duplicate := seen[tool.Name]; duplicate {
			return fmt.Errorf("duplicate tool name %q", tool.Name)
		}
		seen[tool.Name] = struct{}{}
	}
	return nil
}

func (m Manager) Connection(
	ctx context.Context,
	orgID, projectID storage.ID,
	conn executionstore.MCPConnectionRecord,
	server agentconfig.RuntimeMCPServer,
	sessionID, protocolVersion string,
) (Conn, error) {
	wireConn := Conn{EndpointURL: conn.EndpointURL, MCPSessionID: sessionID, ProtocolVersion: protocolVersion}
	if server.Auth == nil {
		return wireConn, nil
	}
	if server.ServerKey != "" && server.ServerKey != conn.ServerKey {
		return Conn{}, fmt.Errorf(
			"mcp auth server key mismatch: connection %q config %q",
			conn.ServerKey,
			server.ServerKey,
		)
	}
	secretID, err := publicid.Decode(publicid.KindSecret, server.Auth.SecretID)
	if err != nil {
		return Conn{}, fmt.Errorf("decode mcp auth secret id for %q: %w", conn.ServerKey, err)
	}
	kind, err := mcpAuthSecretKind(server.Auth.Type)
	if err != nil {
		return Conn{}, fmt.Errorf("resolve mcp auth secret kind for %q: %w", conn.ServerKey, err)
	}
	secretPayload, err := m.Secrets.ReadProjectAvailableSecretPayload(
		ctx,
		secretstore.ReadProjectAvailableSecretPayloadInput{
			OrgID:     orgID,
			ProjectID: projectID,
			SecretID:  secretID,
			Kind:      kind,
		},
	)
	if err != nil {
		return Conn{}, fmt.Errorf("read mcp auth secret for %q: %w", conn.ServerKey, err)
	}
	payload := secretPayload.Payload
	switch server.Auth.Type {
	case agentconfig.MCPAuthTypeBearer:
		wireConn.BearerToken = payload[secrets.KeyValue]
	case agentconfig.MCPAuthTypeOAuth:
		token, err := m.oauthBearerToken(ctx, conn, orgID, projectID, secretID, secretPayload)
		if err != nil {
			return Conn{}, err
		}
		wireConn.BearerToken = token
	default:
		return Conn{}, fmt.Errorf("unsupported mcp auth type %q", server.Auth.Type)
	}
	if wireConn.BearerToken == "" {
		return Conn{}, fmt.Errorf("mcp auth secret for %q is missing bearer token material", conn.ServerKey)
	}
	return wireConn, nil
}

const oauthRefreshSkew = time.Minute

const (
	defaultOAuthRefreshLeaseTTL = 30 * time.Second
	defaultOAuthRefreshMaxWaits = 160
	defaultOAuthRefreshWait     = 250 * time.Millisecond
	maxDefaultOAuthRefreshWait  = time.Second
	oauthRefreshOwnerHeadroom   = 2 * time.Second
)

func (m Manager) oauthBearerToken(
	ctx context.Context,
	conn executionstore.MCPConnectionRecord,
	orgID, projectID, secretID storage.ID,
	secretPayload secretstore.SecretPayloadRecord,
) (string, error) {
	token, fresh, err := m.oauthAccessToken(conn, secretPayload)
	if err != nil {
		return "", err
	}
	if fresh {
		return token, nil
	}
	return m.refreshOAuthBearerTokenWithLease(ctx, conn, orgID, projectID, secretID)
}

func (m Manager) refreshOAuthBearerTokenWithLease(
	ctx context.Context,
	conn executionstore.MCPConnectionRecord,
	orgID, projectID, secretID storage.ID,
) (string, error) {
	maxWaits := m.OAuthRefreshMaxWaits
	if maxWaits <= 0 {
		maxWaits = defaultOAuthRefreshMaxWaits
	}
	for attempt := 0; ; attempt++ {
		leaseTTL := m.oauthRefreshLeaseTTL()
		leaseAttemptStarted := time.Now()
		lease, acquired, err := m.Secrets.AcquireProjectOAuthRefreshLease(
			ctx,
			secretstore.AcquireProjectOAuthRefreshLeaseInput{
				OrgID:     orgID,
				ProjectID: projectID,
				SecretID:  secretID,
				TTL:       leaseTTL,
			},
		)
		if err != nil {
			return "", fmt.Errorf("acquire mcp oauth refresh lease for %q: %w", conn.ServerKey, err)
		}
		if acquired {
			ownerTimeout := leaseTTL - time.Since(leaseAttemptStarted) - oauthRefreshOwnerHeadroom
			return m.refreshOAuthBearerTokenAsLeaseOwner(
				ctx,
				conn,
				projectID,
				lease,
				ownerTimeout,
			)
		}
		if attempt >= maxWaits {
			return "", fmt.Errorf("mcp oauth refresh lease for %q is busy", conn.ServerKey)
		}
		if err := sleepBackoff(ctx, m.oauthRefreshWait(attempt)); err != nil {
			return "", err
		}
		secretPayload, err := m.Secrets.ReadProjectAvailableSecretPayload(
			ctx,
			secretstore.ReadProjectAvailableSecretPayloadInput{
				OrgID:     orgID,
				ProjectID: projectID,
				SecretID:  secretID,
				Kind:      secrets.KindOAuthTokenSet,
			},
		)
		if err != nil {
			return "", fmt.Errorf("read mcp auth secret for %q after refresh wait: %w", conn.ServerKey, err)
		}
		token, fresh, err := m.oauthAccessToken(conn, secretPayload)
		if err != nil {
			return "", err
		}
		if fresh {
			return token, nil
		}
	}
}

func (m Manager) refreshOAuthBearerTokenAsLeaseOwner(
	callerCtx context.Context,
	conn executionstore.MCPConnectionRecord,
	projectID storage.ID,
	lease secretstore.OAuthRefreshLeaseRecord,
	timeout time.Duration,
) (string, error) {
	defer func() { _ = m.Secrets.ReleaseProjectOAuthRefreshLease(context.WithoutCancel(callerCtx), lease) }()
	if timeout <= 0 {
		return "", fmt.Errorf("mcp oauth refresh lease for %q has insufficient remaining time", conn.ServerKey)
	}
	if err := callerCtx.Err(); err != nil {
		return "", err
	}
	leaseOwnerCtx, cancel := context.WithTimeout(context.WithoutCancel(callerCtx), timeout)
	defer cancel()
	secretPayload, err := m.Secrets.ReadProjectAvailableSecretPayload(
		leaseOwnerCtx,
		secretstore.ReadProjectAvailableSecretPayloadInput{
			OrgID:     lease.OrgID,
			ProjectID: projectID,
			SecretID:  lease.SecretID,
			Kind:      secrets.KindOAuthTokenSet,
		},
	)
	if err != nil {
		return "", fmt.Errorf("read mcp auth secret for %q as refresh lease owner: %w", conn.ServerKey, err)
	}
	payload := secretPayload.Payload
	token, fresh, err := m.oauthAccessToken(conn, secretPayload)
	if err != nil {
		return "", err
	}
	if fresh {
		return token, nil
	}
	if secretPayload.CurrentVersionID != lease.ExpectedCurrentVersionID {
		return "", fmt.Errorf("mcp oauth refresh lease for %q no longer owns the current secret version", conn.ServerKey)
	}
	if payload[secrets.KeyRefreshToken] == "" {
		return "", fmt.Errorf("mcp oauth secret for %q is expired and has no refresh token", conn.ServerKey)
	}
	refreshed, err := RefreshOAuthToken(leaseOwnerCtx, OAuthRefreshInput{
		TokenEndpoint: payload[secrets.KeyTokenEndpoint],
		ClientID:      payload[secrets.KeyClientID],
		ClientSecret:  payload[secrets.KeyClientSecret],
		RefreshToken:  payload[secrets.KeyRefreshToken],
		Resource:      payload[secrets.KeyResource],
		HTTPClient:    m.OAuthHTTPClient,
	})
	if err != nil {
		return "", fmt.Errorf("refresh mcp oauth token for %q: %w", conn.ServerKey, err)
	}
	refreshedPayload := cloneSecretPayload(payload)
	refreshedPayload[secrets.KeyAccessToken] = refreshed.AccessToken
	if refreshed.RefreshToken != "" {
		refreshedPayload[secrets.KeyRefreshToken] = refreshed.RefreshToken
	}
	if refreshed.IDToken != "" {
		refreshedPayload[secrets.KeyIDToken] = refreshed.IDToken
	}
	refreshedPayload[secrets.KeyTokenType] = refreshed.TokenType
	if len(refreshed.Scopes) > 0 {
		refreshedPayload[secrets.KeyScopes] = strings.Join(refreshed.Scopes, " ")
	}
	material, err := secrets.OAuthTokenSetMaterialFromPayload(
		refreshedPayload,
		refreshed.AccessTokenLifetime(),
	)
	if err != nil {
		return "", fmt.Errorf("normalize refreshed mcp oauth token for %q: %w", conn.ServerKey, err)
	}
	if _, err := m.Secrets.RotateProjectAvailableOAuthSecret(
		leaseOwnerCtx,
		secretstore.RotateProjectAvailableOAuthSecretInput{
			ProjectID: projectID,
			Lease:     lease,
			Material:  material,
		},
	); err != nil {
		if errors.Is(err, storeerr.ErrConflict) {
			current, readErr := m.Secrets.ReadProjectAvailableSecretPayload(
				leaseOwnerCtx,
				secretstore.ReadProjectAvailableSecretPayloadInput{
					OrgID:     lease.OrgID,
					ProjectID: projectID,
					SecretID:  lease.SecretID,
					Kind:      secrets.KindOAuthTokenSet,
				},
			)
			if readErr == nil {
				currentToken, fresh, tokenErr := m.oauthAccessToken(conn, current)
				if tokenErr == nil && fresh {
					return currentToken, nil
				}
			}
		}
		return "", fmt.Errorf("store refreshed mcp oauth token for %q: %w", conn.ServerKey, err)
	}
	return refreshed.AccessToken, nil
}

func (m Manager) oauthAccessToken(
	conn executionstore.MCPConnectionRecord,
	secretPayload secretstore.SecretPayloadRecord,
) (string, bool, error) {
	accessToken := secretPayload.Payload[secrets.KeyAccessToken]
	if accessToken == "" {
		return "", false, fmt.Errorf("mcp oauth secret for %q is missing access token", conn.ServerKey)
	}
	if !secretPayload.OAuthAccessTokenExpires || secretPayload.OAuthAccessTokenRemaining > oauthRefreshSkew {
		return accessToken, true, nil
	}
	return accessToken, false, nil
}

func (m Manager) oauthRefreshLeaseTTL() time.Duration {
	if m.OAuthRefreshLeaseTTL > 0 {
		return m.OAuthRefreshLeaseTTL
	}
	return defaultOAuthRefreshLeaseTTL
}

func (m Manager) oauthRefreshWait(attempt int) time.Duration {
	if m.OAuthRefreshWait != nil {
		return m.OAuthRefreshWait(attempt)
	}
	wait := defaultOAuthRefreshWait * time.Duration(attempt+1)
	if wait > maxDefaultOAuthRefreshWait {
		return maxDefaultOAuthRefreshWait
	}
	return wait
}

func cloneSecretPayload(payload secrets.Payload) secrets.Payload {
	cloned := make(secrets.Payload, len(payload))
	for key, value := range payload {
		cloned[key] = value
	}
	return cloned
}

func mcpAuthSecretKind(authType string) (secrets.Kind, error) {
	switch authType {
	case agentconfig.MCPAuthTypeBearer:
		return secrets.KindGeneric, nil
	case agentconfig.MCPAuthTypeOAuth:
		return secrets.KindOAuthTokenSet, nil
	default:
		return "", fmt.Errorf("unsupported mcp auth type %q", authType)
	}
}

func sleepBackoff(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func defaultMCPInitializationBackoff(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 250 * time.Millisecond
	case 2:
		return 500 * time.Millisecond
	default:
		return time.Second
	}
}
