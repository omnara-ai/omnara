package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	mlog "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	DefaultCatalogMinFreshness = 5 * time.Minute

	operationTimeout              = 15 * time.Second
	catalogRefreshOwnerHeadroom   = 2 * time.Second
	defaultCatalogRefreshLeaseTTL = operationTimeout + catalogRefreshOwnerHeadroom
	defaultCatalogRefreshMaxWaits = 240
	defaultCatalogRefreshWait     = 250 * time.Millisecond
	maxDefaultCatalogRefreshWait  = time.Second
)

var errNoMutualProtocolVersion = errors.New("mcp: server supports no protocol version this client speaks")

func failureRecordContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), catalogRefreshOwnerHeadroom)
}

type serverProbe struct {
	Stateless bool
	Discover  DiscoverResult
}

func (m Manager) probeServer(ctx context.Context, wireConn Conn) (serverProbe, error) {
	discover, err := m.Client.Discover(ctx, wireConn, StatelessProtocolVersion)
	if err == nil {
		return serverProbe{Stateless: true, Discover: discover}, nil
	}
	if supported, ok := UnsupportedProtocolVersions(err); ok {
		if version, ok := NegotiateStatelessProtocolVersion(supported); ok && version != StatelessProtocolVersion {
			discover, err := m.Client.Discover(ctx, wireConn, version)
			if err != nil {
				return serverProbe{}, err
			}
			return serverProbe{Stateless: true, Discover: discover}, nil
		}
		if slices.Contains(supported, LegacyProtocolVersion) {
			return serverProbe{}, nil
		}
		return serverProbe{}, fmt.Errorf("%w (server supports %v)", errNoMutualProtocolVersion, supported)
	}
	if IsStatelessProtocolError(err) {
		return serverProbe{}, err
	}
	if IndicatesLegacyServer(err) {
		return serverProbe{}, nil
	}
	return serverProbe{}, err
}

type catalogContents struct {
	ProtocolVersion    string
	ServerCapabilities json.RawMessage
	ServerInfo         json.RawMessage
	Instructions       string
	Discover           CacheHint
	DiscoverFreshFor   time.Duration
	Listing            ToolsListing
}

type catalogFetch func(
	ctx context.Context,
	current executionstore.MCPServerCatalogRecord,
) (catalogContents, error)

func (m Manager) statelessCatalogFetch(wireConn Conn, discover *DiscoverResult) catalogFetch {
	return func(ctx context.Context, _ executionstore.MCPServerCatalogRecord) (catalogContents, error) {
		if discover == nil {
			probe, err := m.probeServer(ctx, wireConn)
			if err != nil {
				return catalogContents{}, err
			}
			if !probe.Stateless {
				return catalogContents{}, fmt.Errorf(
					"mcp server %s no longer speaks a stateless protocol version",
					wireConn.EndpointURL,
				)
			}
			discover = &probe.Discover
		}
		wireConn.ProtocolVersion = discover.ProtocolVersion
		wireConn.MCPSessionID = ""
		listing, err := listAllTools(ctx, m.Client, wireConn, func(context.Context) (int64, error) {
			return NextStatelessRequestID(), nil
		})
		if err != nil {
			return catalogContents{}, fmt.Errorf("list mcp tools for %s: %w", wireConn.EndpointURL, err)
		}
		return catalogContents{
			ProtocolVersion:    discover.ProtocolVersion,
			ServerCapabilities: discover.ServerCapabilities,
			ServerInfo:         discover.ServerInfo,
			Instructions:       discover.Instructions,
			Discover:           discover.Cache,
			DiscoverFreshFor:   m.catalogFreshFor(discover.Cache),
			Listing:            listing,
		}, nil
	}
}

type legacySession struct {
	Conn               Conn
	Label              string
	NextRequestID      func(context.Context) (int64, error)
	ServerCapabilities json.RawMessage
	ServerInfo         json.RawMessage
}

func (m Manager) openLegacySession(ctx context.Context, wireConn Conn) (Conn, InitializeResult, error) {
	mcpSessionID, result, err := m.Client.Initialize(ctx, wireConn, LegacyProtocolVersion)
	if err != nil {
		return Conn{}, InitializeResult{}, fmt.Errorf("initialize mcp server: %w", err)
	}
	if result.ProtocolVersion == "" {
		result.ProtocolVersion = LegacyProtocolVersion
	}
	wireConn.MCPSessionID = mcpSessionID
	wireConn.ProtocolVersion = result.ProtocolVersion
	if err := m.Client.Notify(ctx, wireConn, "notifications/initialized", json.RawMessage(`{}`)); err != nil {
		return Conn{}, InitializeResult{}, fmt.Errorf("send mcp initialized notification: %w", err)
	}
	return wireConn, result, nil
}

func (m Manager) connectionRequestIDs(
	projectID, agentID, connectionID storage.ID,
) func(context.Context) (int64, error) {
	return func(ctx context.Context) (int64, error) {
		seq, err := m.Execution.NextMCPRequestSequence(ctx, projectID, agentID, connectionID)
		if err != nil {
			return 0, fmt.Errorf("allocate mcp request sequence: %w", err)
		}
		return seq, nil
	}
}

func (m Manager) legacyCatalogFetch(session legacySession) catalogFetch {
	return func(ctx context.Context, current executionstore.MCPServerCatalogRecord) (catalogContents, error) {
		listing, err := listAllTools(ctx, m.Client, session.Conn, session.NextRequestID)
		if err != nil {
			return catalogContents{}, fmt.Errorf("list mcp tools for %s: %w", session.Label, err)
		}
		capabilities := session.ServerCapabilities
		if len(capabilities) == 0 {
			capabilities = current.ServerCapabilities
		}
		serverInfo := session.ServerInfo
		if len(serverInfo) == 0 {
			serverInfo = current.ServerInfo
		}
		return catalogContents{
			ProtocolVersion:    session.Conn.ProtocolVersion,
			ServerCapabilities: capabilities,
			ServerInfo:         serverInfo,
			Instructions:       current.Instructions,
			DiscoverFreshFor:   m.catalogFreshFor(CacheHint{}),
			Listing:            listing,
		}, nil
	}
}

func (m Manager) negotiatedCatalogFetch(wireConn Conn, legacy catalogFetch) catalogFetch {
	return func(ctx context.Context, current executionstore.MCPServerCatalogRecord) (catalogContents, error) {
		if catalogUsable(current, true) {
			return m.statelessCatalogFetch(wireConn, nil)(ctx, current)
		}
		probe, err := m.probeServer(ctx, wireConn)
		if err != nil {
			return catalogContents{}, err
		}
		if probe.Stateless {
			return m.statelessCatalogFetch(wireConn, &probe.Discover)(ctx, current)
		}
		return legacy(ctx, current)
	}
}

func (m Manager) initializeStateless(
	ctx context.Context,
	projectID, agentID storage.ID,
	conn executionstore.MCPConnectionRecord,
	server agentconfig.RuntimeMCPServer,
	identity executionstore.MCPServerCatalogIdentity,
	catalog executionstore.MCPServerCatalogRecord,
	fetch catalogFetch,
) (executionstore.MCPConnectionRecord, error) {
	if !catalogUsable(catalog, true) || !catalog.ToolsFreshAt(m.now()) {
		refreshed, err := m.refreshCatalog(ctx, identity, true, fetch)
		if err != nil {
			return executionstore.MCPConnectionRecord{}, err
		}
		catalog = refreshed
	}
	return m.bindCatalog(ctx, projectID, agentID, conn, server, "", catalog.ProtocolVersion, catalog)
}

func (m Manager) initializeLegacy(
	ctx context.Context,
	projectID, agentID storage.ID,
	conn executionstore.MCPConnectionRecord,
	server agentconfig.RuntimeMCPServer,
	wireConn Conn,
	identity executionstore.MCPServerCatalogIdentity,
) (executionstore.MCPConnectionRecord, error) {
	session, result, err := m.openLegacySession(ctx, wireConn)
	if err != nil {
		return executionstore.MCPConnectionRecord{}, fmt.Errorf("%s %q: %w", "mcp server", conn.ServerKey, err)
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
	if !catalogUsable(catalog, false) || !catalog.ToolsFreshAt(m.now()) {
		refreshed, err := m.refreshCatalog(ctx, identity, false, m.legacyCatalogFetch(legacySession{
			Conn:               session,
			Label:              conn.ServerKey,
			NextRequestID:      m.connectionRequestIDs(projectID, agentID, conn.ID),
			ServerCapabilities: result.ServerCapabilities,
			ServerInfo:         result.ServerInfo,
		}))
		if err != nil {
			return executionstore.MCPConnectionRecord{}, err
		}
		catalog = refreshed
	}
	mcpSessionID, protocolVersion := boundSession(catalog, session.MCPSessionID, session.ProtocolVersion)
	return m.bindCatalog(ctx, projectID, agentID, conn, server, mcpSessionID, protocolVersion, catalog)
}

func boundSession(
	catalog executionstore.MCPServerCatalogRecord,
	mcpSessionID, protocolVersion string,
) (string, string) {
	if IsStatelessProtocolVersion(catalog.ProtocolVersion) {
		return "", catalog.ProtocolVersion
	}
	return mcpSessionID, protocolVersion
}

func (m Manager) bindCatalog(
	ctx context.Context,
	projectID, agentID storage.ID,
	conn executionstore.MCPConnectionRecord,
	server agentconfig.RuntimeMCPServer,
	mcpSessionID, protocolVersion string,
	catalog executionstore.MCPServerCatalogRecord,
) (executionstore.MCPConnectionRecord, error) {
	tools, err := decodeToolsSnapshot(catalog.ToolsSnapshot)
	if err != nil {
		return executionstore.MCPConnectionRecord{}, fmt.Errorf("decode cached mcp tools for %q: %w", conn.ServerKey, err)
	}
	if err := validateDiscoveredTools(server, tools); err != nil {
		return executionstore.MCPConnectionRecord{}, fmt.Errorf("validate mcp tools for %q: %w", conn.ServerKey, err)
	}
	ready, err := m.Execution.MarkMCPConnectionReady(ctx, executionstore.MarkMCPConnectionReadyInput{
		ProjectID:          projectID,
		AgentID:            agentID,
		ID:                 conn.ID,
		GenerationObserved: conn.Generation,
		MCPSessionID:       mcpSessionID,
		ProtocolVersion:    protocolVersion,
		CatalogID:          catalog.ID,
	})
	if err != nil {
		return executionstore.MCPConnectionRecord{}, fmt.Errorf("mark mcp connection %q ready: %w", conn.ServerKey, err)
	}
	return ready, nil
}

func catalogUsable(catalog executionstore.MCPServerCatalogRecord, stateless bool) bool {
	return catalog.Fetched() && IsStatelessProtocolVersion(catalog.ProtocolVersion) == stateless
}

func catalogServes(catalog executionstore.MCPServerCatalogRecord, statelessOnly bool) bool {
	return catalog.Fetched() && (!statelessOnly || IsStatelessProtocolVersion(catalog.ProtocolVersion))
}

func (m Manager) refreshCatalog(
	ctx context.Context,
	identity executionstore.MCPServerCatalogIdentity,
	statelessOnly bool,
	fetch catalogFetch,
) (executionstore.MCPServerCatalogRecord, error) {
	return m.refreshCatalogUntil(ctx, identity, statelessOnly, func(current executionstore.MCPServerCatalogRecord) bool {
		return catalogServes(current, statelessOnly) && current.ToolsFreshAt(m.now())
	}, fetch)
}

func alwaysFetchCatalog(executionstore.MCPServerCatalogRecord) bool { return false }

func (m Manager) refreshCatalogUntil(
	ctx context.Context,
	identity executionstore.MCPServerCatalogIdentity,
	statelessOnly bool,
	refreshed func(executionstore.MCPServerCatalogRecord) bool,
	fetch catalogFetch,
) (executionstore.MCPServerCatalogRecord, error) {
	maxWaits := m.CatalogRefreshMaxWaits
	if maxWaits <= 0 {
		maxWaits = defaultCatalogRefreshMaxWaits
	}
	leaseTTL := m.CatalogRefreshLeaseTTL
	if leaseTTL <= 0 {
		leaseTTL = defaultCatalogRefreshLeaseTTL
	}
	for attempt := 0; ; attempt++ {
		leaseAttemptStarted := time.Now()
		owner := uuid.New()
		current, acquired, err := m.Execution.AcquireMCPServerCatalogRefreshLease(
			ctx,
			executionstore.AcquireMCPServerCatalogRefreshLeaseInput{
				Identity:   identity,
				OwnerToken: owner,
				TTL:        leaseTTL,
			},
		)
		if err != nil {
			return executionstore.MCPServerCatalogRecord{}, fmt.Errorf(
				"acquire mcp catalog refresh lease for %s: %w",
				identity.EndpointURL,
				err,
			)
		}
		if acquired {
			if refreshed(current) {
				_ = m.Execution.ReleaseMCPServerCatalogRefreshLease(
					context.WithoutCancel(ctx),
					identity.OrgID,
					current.ID,
					owner,
				)
				return current, nil
			}
			ownerTimeout := leaseTTL - time.Since(leaseAttemptStarted) - catalogRefreshOwnerHeadroom
			return m.fetchCatalogAsLeaseOwner(ctx, identity, current, owner, ownerTimeout, fetch)
		}
		if current.RefreshError != "" {
			return executionstore.MCPServerCatalogRecord{}, errors.New(current.RefreshError)
		}
		if catalogServes(current, statelessOnly) {
			return current, nil
		}
		if attempt >= maxWaits {
			return executionstore.MCPServerCatalogRecord{}, fmt.Errorf(
				"mcp catalog refresh lease for %s is busy",
				identity.EndpointURL,
			)
		}
		wait := min(defaultCatalogRefreshWait*time.Duration(attempt+1), maxDefaultCatalogRefreshWait)
		if m.CatalogRefreshWait != nil {
			wait = m.CatalogRefreshWait(attempt)
		}
		if err := sleepBackoff(ctx, wait); err != nil {
			return executionstore.MCPServerCatalogRecord{}, err
		}
	}
}

func (m Manager) fetchCatalogAsLeaseOwner(
	callerCtx context.Context,
	identity executionstore.MCPServerCatalogIdentity,
	current executionstore.MCPServerCatalogRecord,
	owner storage.ID,
	timeout time.Duration,
	fetch catalogFetch,
) (executionstore.MCPServerCatalogRecord, error) {
	defer func() {
		_ = m.Execution.ReleaseMCPServerCatalogRefreshLease(
			context.WithoutCancel(callerCtx),
			identity.OrgID,
			current.ID,
			owner,
		)
	}()
	if timeout <= 0 {
		return executionstore.MCPServerCatalogRecord{}, fmt.Errorf(
			"mcp catalog refresh lease for %s has insufficient remaining time",
			identity.EndpointURL,
		)
	}
	if err := callerCtx.Err(); err != nil {
		return executionstore.MCPServerCatalogRecord{}, err
	}
	ctx, cancel := context.WithTimeout(callerCtx, timeout)
	defer cancel()
	contents, err := fetch(ctx, current)
	if err != nil {
		return executionstore.MCPServerCatalogRecord{}, errors.Join(
			err,
			m.markCatalogRefreshFailed(callerCtx, identity, current, owner, err),
		)
	}
	tools := dropToolsWithInvalidHeaders(ctx, contents.Listing.Tools)
	snapshot, err := json.Marshal(tools)
	if err != nil {
		return executionstore.MCPServerCatalogRecord{}, fmt.Errorf(
			"marshal mcp tools snapshot for %s: %w",
			identity.EndpointURL,
			err,
		)
	}
	fetched, err := m.Execution.MarkMCPServerCatalogFetched(ctx, executionstore.MarkMCPServerCatalogFetchedInput{
		OrgID:              identity.OrgID,
		ID:                 current.ID,
		OwnerToken:         owner,
		ProtocolVersion:    contents.ProtocolVersion,
		ServerCapabilities: contents.ServerCapabilities,
		ServerInfo:         contents.ServerInfo,
		Instructions:       contents.Instructions,
		Discover:           catalogCacheHint(contents.Discover),
		DiscoverFreshFor:   contents.DiscoverFreshFor,
		ToolsSnapshot:      snapshot,
		Tools:              catalogCacheHint(contents.Listing.Cache),
		ToolsFreshFor:      m.catalogFreshFor(contents.Listing.Cache),
	})
	if err != nil {
		return executionstore.MCPServerCatalogRecord{}, fmt.Errorf("store mcp catalog for %s: %w", identity.EndpointURL, err)
	}
	return fetched, nil
}

func (m Manager) markCatalogRefreshFailed(
	ctx context.Context,
	identity executionstore.MCPServerCatalogIdentity,
	current executionstore.MCPServerCatalogRecord,
	owner storage.ID,
	cause error,
) error {
	ctx, cancel := failureRecordContext(ctx)
	defer cancel()
	return m.Execution.MarkMCPServerCatalogRefreshFailed(
		ctx, identity.OrgID, current.ID, owner, sanitizeInitializationError(cause.Error()),
	)
}

func (m Manager) refreshReadyCatalog(
	ctx context.Context,
	orgID, projectID, agentID storage.ID,
	conn executionstore.MCPConnectionRecord,
	server agentconfig.RuntimeMCPServer,
) (ConnectionResult, error) {
	session, identity, err := m.Connection(ctx, orgID, projectID, conn, server, conn.MCPSessionID, conn.ProtocolVersion)
	if err != nil {
		return ConnectionResult{}, err
	}
	catalog, _, err := m.Execution.GetMCPServerCatalog(ctx, identity)
	if err != nil {
		return ConnectionResult{}, err
	}
	stateless := session.Stateless() || catalogUsable(catalog, true)
	if !catalogUsable(catalog, stateless) || !catalog.ToolsFreshAt(m.now()) {
		wireConn := session.withoutSession()
		fetch := m.statelessCatalogFetch(wireConn, nil)
		if !stateless {
			fetch = m.negotiatedCatalogFetch(wireConn, m.legacyCatalogFetch(legacySession{
				Conn:          session,
				Label:         conn.ServerKey,
				NextRequestID: m.connectionRequestIDs(projectID, agentID, conn.ID),
			}))
		}
		refreshed, err := m.refreshCatalog(ctx, identity, stateless, fetch)
		if err != nil {
			return ConnectionResult{}, err
		}
		catalog = refreshed
	}
	mcpSessionID, protocolVersion := boundSession(catalog, conn.MCPSessionID, conn.ProtocolVersion)
	if conn.UsesCatalog() && *conn.CatalogID == catalog.ID &&
		conn.MCPSessionID == mcpSessionID && conn.ProtocolVersion == protocolVersion {
		return ConnectionResult{Conn: conn, Ready: true}, nil
	}
	return m.rebindCatalog(ctx, orgID, projectID, agentID, conn, server, catalog)
}

func (m Manager) refreshCatalogNow(
	ctx context.Context,
	orgID, projectID, agentID storage.ID,
	conn executionstore.MCPConnectionRecord,
	server agentconfig.RuntimeMCPServer,
) (ConnectionResult, error) {
	if conn.State != executionstore.MCPConnectionStateReady || !IsStatelessProtocolVersion(conn.ProtocolVersion) {
		return ConnectionResult{}, fmt.Errorf("mcp connection %q is not a ready stateless connection", conn.ServerKey)
	}
	wireConn, identity, err := m.Connection(ctx, orgID, projectID, conn, server, "", conn.ProtocolVersion)
	if err != nil {
		return ConnectionResult{}, err
	}
	sameCatalog := func(catalog executionstore.MCPServerCatalogRecord) bool {
		return conn.CatalogID != nil && *conn.CatalogID == catalog.ID
	}
	catalog, err := m.refreshCatalogUntil(ctx, identity, true, func(current executionstore.MCPServerCatalogRecord) bool {
		if sameCatalog(current) {
			return current.Revision > conn.CatalogRevision
		}
		return catalogServes(current, true) && current.ToolsFreshAt(m.now())
	}, m.statelessCatalogFetch(wireConn, nil))
	if err != nil {
		return ConnectionResult{}, err
	}
	if sameCatalog(catalog) {
		if catalog.Revision <= conn.CatalogRevision {
			return ConnectionResult{}, fmt.Errorf("mcp catalog for %q was not refreshed", conn.ServerKey)
		}
		refreshed, found, err := m.Execution.GetMCPConnectionByID(ctx, projectID, agentID, conn.ID)
		if err != nil {
			return ConnectionResult{}, err
		}
		if !found {
			return ConnectionResult{}, storeerr.ErrNotFound
		}
		return ConnectionResult{Conn: refreshed, Ready: refreshed.State == executionstore.MCPConnectionStateReady}, nil
	}
	return m.rebindCatalog(ctx, orgID, projectID, agentID, conn, server, catalog)
}

func (m Manager) rebindCatalog(
	ctx context.Context,
	orgID, projectID, agentID storage.ID,
	conn executionstore.MCPConnectionRecord,
	server agentconfig.RuntimeMCPServer,
	catalog executionstore.MCPServerCatalogRecord,
) (ConnectionResult, error) {
	tools, err := decodeToolsSnapshot(catalog.ToolsSnapshot)
	if err != nil {
		return ConnectionResult{}, fmt.Errorf("decode cached mcp tools for %q: %w", conn.ServerKey, err)
	}
	if err := validateDiscoveredTools(server, tools); err != nil {
		return m.refreshExpired(ctx, orgID, projectID, agentID, conn, server)
	}
	if IsStatelessProtocolVersion(conn.ProtocolVersion) && !IsStatelessProtocolVersion(catalog.ProtocolVersion) {
		return ConnectionResult{}, errors.New("mcp: refusing to downgrade a stateless connection to legacy")
	}
	mcpSessionID, protocolVersion := boundSession(catalog, conn.MCPSessionID, conn.ProtocolVersion)
	updated, err := m.Execution.SetMCPConnectionCatalog(ctx, executionstore.SetMCPConnectionCatalogInput{
		ProjectID:          projectID,
		AgentID:            agentID,
		ID:                 conn.ID,
		GenerationObserved: conn.Generation,
		MCPSessionID:       mcpSessionID,
		ProtocolVersion:    protocolVersion,
		CatalogID:          catalog.ID,
	})
	if errors.Is(err, storeerr.ErrStateTransitionConflict) {
		ready, err := m.readyConnectionByID(ctx, projectID, agentID, conn.ID)
		return ConnectionResult{Conn: ready, Ready: true}, err
	}
	if err != nil {
		return ConnectionResult{}, err
	}
	return ConnectionResult{Conn: updated, Changed: true, Ready: true}, nil
}

func decodeToolsSnapshot(snapshot json.RawMessage) ([]*sdkmcp.Tool, error) {
	var tools []*sdkmcp.Tool
	if len(snapshot) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(snapshot, &tools); err != nil {
		return nil, err
	}
	return tools, nil
}

func dropToolsWithInvalidHeaders(ctx context.Context, tools []*sdkmcp.Tool) []*sdkmcp.Tool {
	kept := make([]*sdkmcp.Tool, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			mlog.LoggerFromContext(ctx).WarnContext(ctx, "mcp: dropping null tool")
			continue
		}
		if _, err := ToolHeaders(tool); err != nil {
			mlog.LoggerFromContext(ctx).WarnContext(ctx, "mcp: dropping tool with invalid header annotations",
				"tool", tool.Name,
				"err", err,
			)
			continue
		}
		kept = append(kept, tool)
	}
	return kept
}

func catalogCacheHint(hint CacheHint) executionstore.MCPServerCatalogCacheHint {
	return executionstore.MCPServerCatalogCacheHint{Scope: hint.CacheScope, TTLMs: int32(min(hint.TTLMs, math.MaxInt32))}
}

func (m Manager) catalogFreshFor(hint CacheHint) time.Duration {
	floor := m.CatalogMinFreshness
	if floor <= 0 {
		floor = DefaultCatalogMinFreshness
	}
	return max(time.Duration(hint.TTLMs)*time.Millisecond, floor)
}

func (m Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}
