package mcp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/sigv4"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/textutil"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const (
	InitializeMaxAttempts       = 3
	maxInitializationErrorRunes = 2_000
)

type Manager struct {
	Execution            *executionstore.Store
	Secrets              *secretstore.Store
	Client               Client
	Backoff              func(attempt int) time.Duration
	SigV4CredentialCache *sigv4.CredentialCache

	OAuthHTTPClient *http.Client

	OAuthRefreshLeaseTTL time.Duration
	OAuthRefreshWait     func(attempt int) time.Duration
	OAuthRefreshMaxWaits int

	CatalogRefreshLeaseTTL time.Duration
	CatalogRefreshWait     func(attempt int) time.Duration
	CatalogRefreshMaxWaits int
	CatalogMinFreshness    time.Duration

	Now func() time.Time
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

type ConnectionTrigger uint8

const (
	TriggerTurnStart ConnectionTrigger = iota + 1
	TriggerTurnResume
	TriggerTurnContinue
)

func (m Manager) EnsureConnection(
	ctx context.Context,
	orgID, projectID, agentID storage.ID,
	conn executionstore.MCPConnectionRecord,
	server agentconfig.RuntimeMCPServer,
	trigger ConnectionTrigger,
) (ConnectionResult, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	switch conn.State {
	case executionstore.MCPConnectionStateInitializing, executionstore.MCPConnectionStateExpired:
		return m.initializePending(ctx, orgID, projectID, agentID, conn, server)
	case executionstore.MCPConnectionStateFailed:
		if trigger == TriggerTurnStart {
			return m.initializePending(ctx, orgID, projectID, agentID, conn, server)
		}
	case executionstore.MCPConnectionStateReady:
		if trigger == TriggerTurnContinue {
			break
		}
		if !conn.UsesCatalog() {
			return m.refreshExpired(ctx, orgID, projectID, agentID, conn, server)
		}
		if trigger == TriggerTurnStart {
			return m.refreshReadyOrMarkFailed(ctx, orgID, projectID, agentID, conn, server)
		}
	}
	return ConnectionResult{Conn: conn, Ready: conn.State == executionstore.MCPConnectionStateReady}, nil
}

func (m Manager) refreshReadyOrMarkFailed(
	ctx context.Context,
	orgID, projectID, agentID storage.ID,
	conn executionstore.MCPConnectionRecord,
	server agentconfig.RuntimeMCPServer,
) (ConnectionResult, error) {
	result, err := m.refreshReadyCatalog(ctx, orgID, projectID, agentID, conn, server)
	var initErr *InitializationError
	if err == nil || errors.As(err, &initErr) {
		return result, err
	}
	return m.markConnectionFailed(ctx, projectID, agentID, conn, err)
}

func (m Manager) initializePending(
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
	result, err := m.initializeOrMarkFailed(ctx, orgID, projectID, agentID, begun, server)
	result.Changed = true
	return result, err
}

func (m Manager) refreshExpired(
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
	result, err := m.initializeOrMarkFailed(ctx, orgID, projectID, agentID, begun, server)
	result.Changed = true
	return result, err
}

func (m Manager) initializeOrMarkFailed(
	ctx context.Context,
	orgID, projectID, agentID storage.ID,
	conn executionstore.MCPConnectionRecord,
	server agentconfig.RuntimeMCPServer,
) (ConnectionResult, error) {
	ready, cause := m.initializeWithRetry(ctx, orgID, projectID, agentID, conn, server)
	if cause == nil {
		return ConnectionResult{Conn: ready, Ready: true}, nil
	}
	return m.markConnectionFailed(ctx, projectID, agentID, conn, cause)
}

func (m Manager) markConnectionFailed(
	ctx context.Context,
	projectID, agentID storage.ID,
	conn executionstore.MCPConnectionRecord,
	cause error,
) (ConnectionResult, error) {
	ctx, cancel := failureRecordContext(ctx)
	defer cancel()
	failed, markErr := m.Execution.MarkMCPConnectionFailed(
		ctx,
		projectID,
		agentID,
		conn.ID,
		conn.Generation,
		sanitizeInitializationError(cause.Error()),
	)
	if markErr != nil {
		return ConnectionResult{}, &InitializationError{
			Cause:    cause,
			Recorded: false,
			Err:      fmt.Errorf("%w: %w", cause, markErr),
		}
	}
	return ConnectionResult{Conn: failed, Changed: true}, &InitializationError{Cause: cause, Recorded: true, Err: cause}
}

func sanitizeInitializationError(value string) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	value = strings.ReplaceAll(value, "\x00", "")
	return textutil.TruncateRunes(strings.TrimSpace(value), maxInitializationErrorRunes)
}

func (m Manager) initializeWithRetry(
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
		backoff := time.Second
		switch attempt {
		case 1:
			backoff = 250 * time.Millisecond
		case 2:
			backoff = 500 * time.Millisecond
		}
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
	wireConn, identity, err := m.Connection(ctx, orgID, projectID, conn, server, "", "")
	if err != nil {
		return executionstore.MCPConnectionRecord{}, err
	}
	catalog, _, err := m.Execution.GetMCPServerCatalog(ctx, identity)
	if err != nil {
		return executionstore.MCPConnectionRecord{}, err
	}
	if catalogUsable(catalog, true) {
		return m.initializeStateless(
			ctx, projectID, agentID, conn, server, identity, catalog, m.statelessCatalogFetch(wireConn, nil),
		)
	}
	probe, err := m.probeServer(ctx, wireConn)
	if err != nil {
		return executionstore.MCPConnectionRecord{}, fmt.Errorf("probe mcp server %q: %w", conn.ServerKey, err)
	}
	if probe.Stateless {
		return m.initializeStateless(
			ctx, projectID, agentID, conn, server, identity, catalog, m.statelessCatalogFetch(wireConn, &probe.Discover),
		)
	}
	return m.initializeLegacy(ctx, projectID, agentID, conn, server, wireConn, identity)
}

func validateDiscoveredTools(server agentconfig.RuntimeMCPServer, tools []*sdkmcp.Tool) error {
	seen := make(map[string]struct{}, len(tools))
	for index, tool := range tools {
		if tool == nil {
			return fmt.Errorf("tool at index %d is null", index)
		}
		if _, enabled := server.ResolveTool(tool.Name); enabled {
			if err := toolcatalog.ValidateMCPRuntimeToolName(server.ServerKey, tool.Name); err != nil {
				return fmt.Errorf("tool name %q cannot be exposed to the model: %w", tool.Name, err)
			}
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
) (Conn, executionstore.MCPServerCatalogIdentity, error) {
	if server.Auth != nil && server.ServerKey != "" && server.ServerKey != conn.ServerKey {
		return Conn{}, executionstore.MCPServerCatalogIdentity{}, fmt.Errorf(
			"mcp auth server key mismatch: connection %q config %q",
			conn.ServerKey,
			server.ServerKey,
		)
	}
	return m.connection(ctx, orgID, projectID, conn.ServerKey, conn.EndpointURL, server.Auth, sessionID, protocolVersion)
}

func (m Manager) connection(
	ctx context.Context,
	orgID, projectID storage.ID,
	serverKey, endpointURL string,
	auth *agentconfig.RuntimeMCPAuth,
	sessionID, protocolVersion string,
) (Conn, executionstore.MCPServerCatalogIdentity, error) {
	wireConn := Conn{EndpointURL: endpointURL, MCPSessionID: sessionID, ProtocolVersion: protocolVersion}
	identity := executionstore.MCPServerCatalogIdentity{OrgID: orgID, EndpointURL: endpointURL}
	if auth == nil {
		return wireConn, identity, nil
	}
	secretID, err := publicid.Decode(publicid.KindSecret, auth.SecretID)
	if err != nil {
		return Conn{}, identity, fmt.Errorf("decode mcp auth secret id for %q: %w", serverKey, err)
	}
	kind, err := mcpAuthSecretKind(auth.Type)
	if err != nil {
		return Conn{}, identity, fmt.Errorf("resolve mcp auth secret kind for %q: %w", serverKey, err)
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
		return Conn{}, identity, fmt.Errorf("read mcp auth secret for %q: %w", serverKey, err)
	}
	payload := secretPayload.Payload
	credential := &executionstore.MCPServerCatalogCredential{
		SecretID:        secretID,
		SecretVersionID: secretPayload.CurrentVersionID,
	}
	identity.Credential = credential
	switch auth.Type {
	case agentconfig.MCPAuthTypeBearer:
		wireConn.BearerToken = payload[secrets.KeyValue]
	case agentconfig.MCPAuthTypeOAuth:
		token, versionID, err := m.oauthBearerToken(ctx, serverKey, orgID, projectID, secretID, secretPayload)
		if err != nil {
			return Conn{}, identity, err
		}
		wireConn.BearerToken = token
		credential.SecretVersionID = versionID
	case agentconfig.MCPAuthTypeSigV4:
		credential.AWSRegion = auth.Region
		credential.AWSService = auth.Service
		provider, err := sigv4.ResolveCredentialProvider(
			m.SigV4CredentialCache,
			secretID,
			secretPayload.CurrentVersionID,
			auth.Region,
			payload,
		)
		if err != nil {
			return Conn{}, identity, fmt.Errorf("prepare SigV4 MCP auth for %q: %w", serverKey, err)
		}
		signer, err := sigv4.NewSigner(auth.Service, auth.Region, provider)
		if err != nil {
			return Conn{}, identity, fmt.Errorf("prepare SigV4 MCP auth for %q: %w", serverKey, err)
		}
		wireConn.prepareRequest = signer.Sign
		return wireConn, identity, nil
	default:
		return Conn{}, identity, fmt.Errorf("unsupported mcp auth type %q", auth.Type)
	}
	if wireConn.BearerToken == "" {
		return Conn{}, identity, fmt.Errorf("mcp auth secret for %q is missing bearer token material", serverKey)
	}
	return wireConn, identity, nil
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
	serverKey string,
	orgID, projectID, secretID storage.ID,
	secretPayload secretstore.SecretPayloadRecord,
) (string, storage.ID, error) {
	token, fresh, err := m.oauthAccessToken(serverKey, secretPayload)
	if err != nil {
		return "", storage.NilID, err
	}
	if fresh {
		return token, secretPayload.CurrentVersionID, nil
	}
	return m.refreshOAuthBearerTokenWithLease(ctx, serverKey, orgID, projectID, secretID)
}

func (m Manager) refreshOAuthBearerTokenWithLease(
	ctx context.Context,
	serverKey string,
	orgID, projectID, secretID storage.ID,
) (string, storage.ID, error) {
	maxWaits := m.OAuthRefreshMaxWaits
	if maxWaits <= 0 {
		maxWaits = defaultOAuthRefreshMaxWaits
	}
	leaseTTL := m.OAuthRefreshLeaseTTL
	if leaseTTL <= 0 {
		leaseTTL = defaultOAuthRefreshLeaseTTL
	}
	for attempt := 0; ; attempt++ {
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
			return "", storage.NilID, fmt.Errorf("acquire mcp oauth refresh lease for %q: %w", serverKey, err)
		}
		if acquired {
			ownerTimeout := leaseTTL - time.Since(leaseAttemptStarted) - oauthRefreshOwnerHeadroom
			return m.refreshOAuthBearerTokenAsLeaseOwner(
				ctx,
				serverKey,
				projectID,
				lease,
				ownerTimeout,
			)
		}
		if attempt >= maxWaits {
			return "", storage.NilID, fmt.Errorf("mcp oauth refresh lease for %q is busy", serverKey)
		}
		wait := min(defaultOAuthRefreshWait*time.Duration(attempt+1), maxDefaultOAuthRefreshWait)
		if m.OAuthRefreshWait != nil {
			wait = m.OAuthRefreshWait(attempt)
		}
		if err := sleepBackoff(ctx, wait); err != nil {
			return "", storage.NilID, err
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
			return "", storage.NilID, fmt.Errorf("read mcp auth secret for %q after refresh wait: %w", serverKey, err)
		}
		token, fresh, err := m.oauthAccessToken(serverKey, secretPayload)
		if err != nil {
			return "", storage.NilID, err
		}
		if fresh {
			return token, secretPayload.CurrentVersionID, nil
		}
	}
}

func (m Manager) refreshOAuthBearerTokenAsLeaseOwner(
	callerCtx context.Context,
	serverKey string,
	projectID storage.ID,
	lease secretstore.OAuthRefreshLeaseRecord,
	timeout time.Duration,
) (string, storage.ID, error) {
	defer func() { _ = m.Secrets.ReleaseProjectOAuthRefreshLease(context.WithoutCancel(callerCtx), lease) }()
	if timeout <= 0 {
		return "", storage.NilID, fmt.Errorf("mcp oauth refresh lease for %q has insufficient remaining time", serverKey)
	}
	if err := callerCtx.Err(); err != nil {
		return "", storage.NilID, err
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
		return "", storage.NilID, fmt.Errorf("read mcp auth secret for %q as refresh lease owner: %w", serverKey, err)
	}
	payload := secretPayload.Payload
	token, fresh, err := m.oauthAccessToken(serverKey, secretPayload)
	if err != nil {
		return "", storage.NilID, err
	}
	if fresh {
		return token, secretPayload.CurrentVersionID, nil
	}
	if secretPayload.CurrentVersionID != lease.ExpectedCurrentVersionID {
		return "", storage.NilID, fmt.Errorf("mcp oauth refresh lease for %q no longer owns the current secret version", serverKey)
	}
	if payload[secrets.KeyRefreshToken] == "" {
		return "", storage.NilID, fmt.Errorf("mcp oauth secret for %q is expired and has no refresh token", serverKey)
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
		return "", storage.NilID, fmt.Errorf("refresh mcp oauth token for %q: %w", serverKey, err)
	}
	refreshedPayload := maps.Clone(payload)
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
		return "", storage.NilID, fmt.Errorf("normalize refreshed mcp oauth token for %q: %w", serverKey, err)
	}
	rotated, err := m.Secrets.RotateProjectAvailableOAuthSecret(
		leaseOwnerCtx,
		secretstore.RotateProjectAvailableOAuthSecretInput{
			ProjectID: projectID,
			Lease:     lease,
			Material:  material,
		},
	)
	if err != nil {
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
				currentToken, fresh, tokenErr := m.oauthAccessToken(serverKey, current)
				if tokenErr == nil && fresh {
					return currentToken, current.CurrentVersionID, nil
				}
			}
		}
		return "", storage.NilID, fmt.Errorf("store refreshed mcp oauth token for %q: %w", serverKey, err)
	}
	return refreshed.AccessToken, rotated.CurrentVersionID, nil
}

func (m Manager) oauthAccessToken(
	serverKey string,
	secretPayload secretstore.SecretPayloadRecord,
) (string, bool, error) {
	accessToken := secretPayload.Payload[secrets.KeyAccessToken]
	if accessToken == "" {
		return "", false, fmt.Errorf("mcp oauth secret for %q is missing access token", serverKey)
	}
	if !secretPayload.OAuthAccessTokenExpires || secretPayload.OAuthAccessTokenRemaining > oauthRefreshSkew {
		return accessToken, true, nil
	}
	return accessToken, false, nil
}

func mcpAuthSecretKind(authType string) (secrets.Kind, error) {
	switch authType {
	case agentconfig.MCPAuthTypeBearer:
		return secrets.KindGeneric, nil
	case agentconfig.MCPAuthTypeOAuth:
		return secrets.KindOAuthTokenSet, nil
	case agentconfig.MCPAuthTypeSigV4:
		return secrets.KindAWSCredentials, nil
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
