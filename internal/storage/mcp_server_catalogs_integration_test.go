//go:build integration

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func acquireCatalogLease(
	t *testing.T,
	ctx context.Context,
	store *Store,
	identity executionstore.MCPServerCatalogIdentity,
	owner executionstore.ID,
) (executionstore.MCPServerCatalogRecord, bool) {
	t.Helper()
	record, acquired, err := store.Execution().AcquireMCPServerCatalogRefreshLease(
		ctx,
		executionstore.AcquireMCPServerCatalogRefreshLeaseInput{Identity: identity, OwnerToken: owner, TTL: time.Minute},
	)
	if err != nil {
		t.Fatalf("acquire catalog lease: %v", err)
	}
	return record, acquired
}

func seedCatalogForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	endpoint string,
	tools string,
) executionstore.MCPServerCatalogRecord {
	t.Helper()
	identity := executionstore.MCPServerCatalogIdentity{OrgID: testOrgID, EndpointURL: endpoint}
	owner := uuid.New()
	record, acquired := acquireCatalogLease(t, ctx, store, identity, owner)
	if !acquired {
		t.Fatalf("seed catalog for %s: lease not acquired", endpoint)
	}
	fetched, err := store.Execution().MarkMCPServerCatalogFetched(ctx, executionstore.MarkMCPServerCatalogFetchedInput{
		OrgID:           testOrgID,
		ID:              record.ID,
		OwnerToken:      owner,
		ProtocolVersion: "2025-11-25",
		ToolsSnapshot:   json.RawMessage(tools),
		ToolsFreshFor:   time.Minute,
	})
	if err != nil {
		t.Fatalf("seed catalog for %s: %v", endpoint, err)
	}
	return fetched
}

func TestMCPServerCatalogLeaseAndFreshness(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)

	identity := executionstore.MCPServerCatalogIdentity{OrgID: testOrgID, EndpointURL: "https://example.com/catalog-lease"}
	if _, found, err := store.Execution().GetMCPServerCatalog(ctx, identity); err != nil || found {
		t.Fatalf("catalog before lease: found=%t err=%v", found, err)
	}

	owner := uuid.New()
	created, acquired := acquireCatalogLease(t, ctx, store, identity, owner)
	if !acquired {
		t.Fatal("first lease was not acquired")
	}
	if created.Fetched() || created.Revision != 0 || created.RefreshOwnerToken == nil ||
		*created.RefreshOwnerToken != owner {
		t.Fatalf("unexpected placeholder catalog: %+v", created)
	}
	competitor := uuid.New()
	busy, acquired := acquireCatalogLease(t, ctx, store, identity, competitor)
	if acquired {
		t.Fatal("competing lease must not be acquired while the first lease is live")
	}
	if busy.ID != created.ID || busy.RefreshOwnerToken == nil || *busy.RefreshOwnerToken != owner {
		t.Fatalf("competing lease returned a different row: %+v", busy)
	}
	if _, err := store.Execution().MarkMCPServerCatalogFetched(ctx, executionstore.MarkMCPServerCatalogFetchedInput{
		OrgID: testOrgID, ID: created.ID, OwnerToken: competitor, ProtocolVersion: "2026-07-28",
	}); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("mark fetched without lease error = %v, want state transition conflict", err)
	}

	tools := json.RawMessage(`[{"name":"greet","description":"say hi","inputSchema":{"type":"object"}}]`)
	fetched, err := store.Execution().MarkMCPServerCatalogFetched(ctx, executionstore.MarkMCPServerCatalogFetchedInput{
		OrgID:              testOrgID,
		ID:                 created.ID,
		OwnerToken:         owner,
		ProtocolVersion:    "2026-07-28",
		ServerCapabilities: json.RawMessage(`{"tools":{}}`),
		ServerInfo:         json.RawMessage(`{"name":"catalog-test"}`),
		Instructions:       "use greet",
		Discover:           executionstore.MCPServerCatalogCacheHint{Scope: "public", TTLMs: 3_600_000},
		DiscoverFreshFor:   time.Hour,
		ToolsSnapshot:      tools,
		Tools:              executionstore.MCPServerCatalogCacheHint{Scope: "private", TTLMs: 0},
		ToolsFreshFor:      5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("mark fetched: %v", err)
	}
	now := time.Now()
	if !fetched.Fetched() || fetched.Revision != 1 || fetched.RefreshOwnerToken != nil ||
		fetched.RefreshLeaseExpiresAt != nil {
		t.Fatalf("unexpected fetched catalog: %+v", fetched)
	}
	if !fetched.ToolsFreshAt(now) || fetched.DiscoverExpiresAt == nil || !now.Before(*fetched.DiscoverExpiresAt) ||
		fetched.ToolsFreshAt(now.Add(6*time.Minute)) {
		t.Fatalf("unexpected freshness windows: tools=%v discover=%v", fetched.ToolsExpiresAt, fetched.DiscoverExpiresAt)
	}
	if fetched.Tools.Scope != "private" || fetched.Tools.TTLMs != 0 || fetched.Discover.TTLMs != 3_600_000 {
		t.Fatalf("unexpected cache hints: %+v %+v", fetched.Tools, fetched.Discover)
	}
	if !jsoncanonical.Equal(fetched.ToolsSnapshot, tools) || fetched.Instructions != "use greet" {
		t.Fatalf("unexpected catalog contents: %+v", fetched)
	}

	loaded, found, err := store.Execution().GetMCPServerCatalog(ctx, identity)
	if err != nil || !found || loaded.ID != created.ID || loaded.Revision != 1 {
		t.Fatalf("reload catalog: found=%t err=%v record=%+v", found, err, loaded)
	}

	second := uuid.New()
	relocked, acquired := acquireCatalogLease(t, ctx, store, identity, second)
	if !acquired || relocked.Revision != 1 || !jsoncanonical.Equal(relocked.ToolsSnapshot, tools) {
		t.Fatalf("relock after fetch: acquired=%t record=%+v", acquired, relocked)
	}
	if _, err := store.Execution().MarkMCPServerCatalogFetched(ctx, executionstore.MarkMCPServerCatalogFetchedInput{
		OrgID: testOrgID, ID: created.ID, OwnerToken: second, ProtocolVersion: "2025-11-25",
	}); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("stateless catalog downgrade error = %v, want conflict", err)
	}
	if err := store.Execution().MarkMCPServerCatalogRefreshFailed(
		ctx, testOrgID, created.ID, owner, "obsolete failure",
	); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("expired owner must not publish an error: %v", err)
	}
	if err := store.Execution().MarkMCPServerCatalogRefreshFailed(
		ctx, testOrgID, created.ID, second, "newer failure",
	); err != nil {
		t.Fatal(err)
	}
	failed, _, err := store.Execution().GetMCPServerCatalog(ctx, identity)
	if err != nil || failed.RefreshError != "newer failure" || failed.ToolsFreshAt(time.Now()) {
		t.Fatalf("newer error did not invalidate freshness: %+v %v", failed, err)
	}
	recoveryOwner := uuid.New()
	if _, acquired := acquireCatalogLease(t, ctx, store, identity, recoveryOwner); !acquired {
		t.Fatal("recovery lease busy")
	}
	recovered, err := store.Execution().MarkMCPServerCatalogFetched(ctx, executionstore.MarkMCPServerCatalogFetchedInput{
		OrgID: testOrgID, ID: created.ID, OwnerToken: recoveryOwner,
		ProtocolVersion: "2026-07-28", ToolsSnapshot: tools, ToolsFreshFor: time.Minute,
	})
	if err != nil || recovered.RefreshError != "" || !recovered.ToolsFreshAt(time.Now()) {
		t.Fatalf("newer success did not clear failure: %+v %v", recovered, err)
	}
	if err := store.Execution().ReleaseMCPServerCatalogRefreshLease(ctx, testOrgID, created.ID, second); err != nil {
		t.Fatalf("release lease: %v", err)
	}
	released, _, err := store.Execution().GetMCPServerCatalog(ctx, identity)
	if err != nil || released.RefreshOwnerToken != nil {
		t.Fatalf("lease not released: err=%v record=%+v", err, released)
	}
}

func TestMCPServerCatalogIdentityFollowsSecretVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	user := createSecretTestUser(t, ctx, store, "MCP Catalog Secret Admin", "admin")
	secret, version, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:     testOrgID,
		OwnerKind: secretstore.SecretOwnerOrg,
		Name:      "mcp-catalog-bearer",
		Material:  secrets.GenericMaterial{Value: "token-v1"},
		Actor:     userPrincipal(user.ID),
	})
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}
	endpoint := "https://example.com/catalog-secret"
	anonymous := executionstore.MCPServerCatalogIdentity{OrgID: testOrgID, EndpointURL: endpoint}
	credentialed := executionstore.MCPServerCatalogIdentity{
		OrgID:       testOrgID,
		EndpointURL: endpoint,
		Credential: &executionstore.MCPServerCatalogCredential{
			SecretID:        secret.ID,
			SecretVersionID: version.ID,
		},
	}
	fetchCatalog := func(
		identity executionstore.MCPServerCatalogIdentity,
		tools string,
	) executionstore.MCPServerCatalogRecord {
		t.Helper()
		owner := uuid.New()
		record, acquired := acquireCatalogLease(t, ctx, store, identity, owner)
		if !acquired {
			t.Fatalf("acquire lease for %+v: not acquired", identity.Credential)
		}
		fetched, err := store.Execution().MarkMCPServerCatalogFetched(ctx, executionstore.MarkMCPServerCatalogFetchedInput{
			OrgID:           testOrgID,
			ID:              record.ID,
			OwnerToken:      owner,
			ProtocolVersion: "2026-07-28",
			ToolsSnapshot:   json.RawMessage(tools),
			ToolsFreshFor:   time.Minute,
		})
		if err != nil {
			t.Fatalf("mark fetched for %+v: %v", identity.Credential, err)
		}
		return fetched
	}
	anonymousCatalog := fetchCatalog(anonymous, `[{"name":"anonymous"}]`)
	credentialedCatalog := fetchCatalog(credentialed, `[{"name":"credentialed"}]`)
	if anonymousCatalog.ID == credentialedCatalog.ID {
		t.Fatal("anonymous and credentialed identities must not share a catalog row")
	}
	if credentialedCatalog.Credential == nil || credentialedCatalog.Credential.SecretVersionID != version.ID {
		t.Fatalf("credentialed catalog lost its credential: %+v", credentialedCatalog)
	}

	_, rotated, err := store.Secrets().CreateSecretVersion(ctx, secretstore.CreateSecretVersionInput{
		OrgID:    testOrgID,
		SecretID: secret.ID,
		Material: secrets.GenericMaterial{Value: "token-v2"},
		Actor:    userPrincipal(user.ID),
	})
	if err != nil {
		t.Fatalf("rotate secret: %v", err)
	}
	if rotated.ID == version.ID {
		t.Fatal("expected a new secret version id")
	}
	rotatedIdentity := credentialed
	rotatedIdentity.Credential = &executionstore.MCPServerCatalogCredential{
		SecretID:        secret.ID,
		SecretVersionID: rotated.ID,
	}
	if _, found, err := store.Execution().GetMCPServerCatalog(ctx, rotatedIdentity); err != nil || found {
		t.Fatalf("rotated identity must start without a catalog: found=%t err=%v", found, err)
	}
	stale, found, err := store.Execution().GetMCPServerCatalog(ctx, credentialed)
	if err != nil || !found || stale.ID != credentialedCatalog.ID {
		t.Fatalf("previous version catalog should remain until its version is destroyed: found=%t err=%v", found, err)
	}

	if _, err := pool.Exec(
		ctx,
		`DELETE FROM secret_versions WHERE secret_id = $1 AND id = $2`,
		secret.ID,
		version.ID,
	); err != nil {
		t.Fatalf("destroy old secret version: %v", err)
	}
	if _, found, err := store.Execution().GetMCPServerCatalog(ctx, credentialed); err != nil || found {
		t.Fatalf("catalog must cascade with its secret version: found=%t err=%v", found, err)
	}
	if _, found, err := store.Execution().GetMCPServerCatalog(ctx, anonymous); err != nil || !found {
		t.Fatalf("anonymous catalog must survive secret rotation: found=%t err=%v", found, err)
	}
}

func TestAgentMCPConnectionReadsToolsFromCatalog(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user, err := store.CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{Email: "mcp-catalog-connection@example.com", DisplayName: "MCP Catalog Connection User"},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	agent := createLaunchTestAgent(t, ctx, store, "idem-mcp-catalog-agent", `
instruction: Use MCP later.
model:
  provider_config: openai-prod
  name: gpt-test
mcp:
  docs:
    url: https://example.com/catalog-connection
    permission:
      mode: always_ask
      parameters: {}
`)
	launch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      agent.ID,
		AgentConfigID:  agent.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "idem-mcp-catalog-agent",
	})
	if err != nil {
		t.Fatalf("launch agent: %v", err)
	}
	conn := launch.MCPConnections[0]
	identity := executionstore.MCPServerCatalogIdentity{
		OrgID:       testOrgID,
		EndpointURL: "https://example.com/catalog-connection",
	}
	owner := uuid.New()
	placeholder, acquired := acquireCatalogLease(t, ctx, store, identity, owner)
	if !acquired {
		t.Fatal("acquire lease: not acquired")
	}
	catalog, err := store.Execution().MarkMCPServerCatalogFetched(ctx, executionstore.MarkMCPServerCatalogFetchedInput{
		OrgID:              testOrgID,
		ID:                 placeholder.ID,
		OwnerToken:         owner,
		ProtocolVersion:    "2026-07-28",
		ServerCapabilities: json.RawMessage(`{"tools":{}}`),
		ServerInfo:         json.RawMessage(`{"name":"catalog"}`),
		Instructions:       "cached instructions",
		ToolsSnapshot:      json.RawMessage(`[{"name":"greet"}]`),
		ToolsFreshFor:      time.Minute,
	})
	if err != nil {
		t.Fatalf("mark fetched: %v", err)
	}

	ready, err := store.Execution().MarkMCPConnectionReady(ctx, executionstore.MarkMCPConnectionReadyInput{
		ProjectID:          testProjectID,
		AgentID:            launch.Agent.ID,
		ID:                 conn.ID,
		GenerationObserved: conn.Generation,
		ProtocolVersion:    "2026-07-28",
		CatalogID:          catalog.ID,
	})
	if err != nil {
		t.Fatalf("mark ready with catalog: %v", err)
	}
	if !ready.UsesCatalog() || *ready.CatalogID != catalog.ID || ready.CatalogRevision != 1 ||
		!jsoncanonical.Equal(ready.ToolsSnapshot, json.RawMessage(`[{"name":"greet"}]`)) ||
		ready.Instructions != "cached instructions" ||
		!jsoncanonical.Equal(ready.ServerInfo, json.RawMessage(`{"name":"catalog"}`)) {
		t.Fatalf("ready connection did not read through the catalog: %+v", ready)
	}
	listed, err := store.Execution().ListAgentMCPConnections(ctx, testProjectID, launch.Agent.ID)
	if err != nil || len(listed) != 1 ||
		!jsoncanonical.Equal(listed[0].ToolsSnapshot, json.RawMessage(`[{"name":"greet"}]`)) {
		t.Fatalf("list connections through catalog: err=%v listed=%+v", err, listed)
	}

	owner = uuid.New()
	if _, acquired := acquireCatalogLease(t, ctx, store, identity, owner); !acquired {
		t.Fatal("acquire refresh lease: not acquired")
	}
	if _, err := store.Execution().MarkMCPServerCatalogFetched(ctx, executionstore.MarkMCPServerCatalogFetchedInput{
		OrgID:           testOrgID,
		ID:              catalog.ID,
		OwnerToken:      owner,
		ProtocolVersion: "2026-07-28",
		ToolsSnapshot:   json.RawMessage(`[{"name":"greet"},{"name":"wave"}]`),
		ToolsFreshFor:   time.Minute,
	}); err != nil {
		t.Fatalf("refresh catalog: %v", err)
	}
	refreshed, found, err := store.Execution().GetMCPConnection(ctx, testProjectID, launch.Agent.ID, "docs")
	if err != nil || !found || refreshed.CatalogRevision != 2 ||
		!jsoncanonical.Equal(refreshed.ToolsSnapshot, json.RawMessage(`[{"name":"greet"},{"name":"wave"}]`)) {
		t.Fatalf("connection did not observe refreshed catalog: found=%t err=%v record=%+v", found, err, refreshed)
	}

	if _, err := store.Execution().SetMCPConnectionCatalog(ctx, executionstore.SetMCPConnectionCatalogInput{
		ProjectID:          testProjectID,
		AgentID:            launch.Agent.ID,
		ID:                 conn.ID,
		GenerationObserved: conn.Generation + 1,
		ProtocolVersion:    "2026-07-28",
		CatalogID:          catalog.ID,
	}); !errors.Is(err, storeerr.ErrStateTransitionConflict) {
		t.Fatalf("stale generation rebind error = %v, want state transition conflict", err)
	}

	expired, changed, err := store.Execution().MarkMCPConnectionExpired(
		ctx, testProjectID, launch.Agent.ID, conn.ID, conn.Generation,
	)
	if err != nil || !changed {
		t.Fatalf("expire connection: changed=%t err=%v", changed, err)
	}
	if expired.UsesCatalog() || string(expired.ToolsSnapshot) != "[]" || expired.CatalogRevision != 0 {
		t.Fatalf("expired connection kept its catalog: %+v", expired)
	}
	if _, found, err := store.Execution().GetMCPServerCatalog(ctx, identity); err != nil || !found {
		t.Fatalf("expiring a connection must not delete the shared catalog: found=%t err=%v", found, err)
	}
}
