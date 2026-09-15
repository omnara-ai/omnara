package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type MCPServerCatalogCredential struct {
	SecretID        uuid.UUID
	SecretVersionID uuid.UUID
	AWSRegion       string
	AWSService      string
}

type MCPServerCatalogIdentity struct {
	OrgID       uuid.UUID
	EndpointURL string
	Credential  *MCPServerCatalogCredential
}

func (identity MCPServerCatalogIdentity) validate() error {
	if identity.OrgID == uuid.Nil || identity.EndpointURL == "" {
		return errors.New("org and endpoint url are required")
	}
	if identity.Credential != nil &&
		(identity.Credential.SecretID == uuid.Nil || identity.Credential.SecretVersionID == uuid.Nil) {
		return errors.New("credential secret and secret version are required")
	}
	return nil
}

func (identity MCPServerCatalogIdentity) secretID() *uuid.UUID {
	if identity.Credential == nil {
		return nil
	}
	id := identity.Credential.SecretID
	return &id
}

func (identity MCPServerCatalogIdentity) secretVersionID() *uuid.UUID {
	if identity.Credential == nil {
		return nil
	}
	id := identity.Credential.SecretVersionID
	return &id
}

func (identity MCPServerCatalogIdentity) awsRegion() string {
	if identity.Credential == nil {
		return ""
	}
	return identity.Credential.AWSRegion
}

func (identity MCPServerCatalogIdentity) awsService() string {
	if identity.Credential == nil {
		return ""
	}
	return identity.Credential.AWSService
}

type MCPServerCatalogCacheHint struct {
	Scope string
	TTLMs int32
}

type MCPServerCatalogRecord struct {
	ID                    uuid.UUID                   `json:"id"`
	OrgID                 uuid.UUID                   `json:"org_id"`
	EndpointURL           string                      `json:"endpoint_url"`
	Credential            *MCPServerCatalogCredential `json:"credential,omitempty"`
	Revision              int64                       `json:"revision"`
	ProtocolVersion       string                      `json:"protocol_version"`
	ServerCapabilities    json.RawMessage             `json:"server_capabilities"`
	ServerInfo            json.RawMessage             `json:"server_info"`
	Instructions          string                      `json:"instructions"`
	Discover              MCPServerCatalogCacheHint   `json:"discover"`
	DiscoverExpiresAt     *time.Time                  `json:"discover_expires_at,omitempty"`
	ToolsSnapshot         json.RawMessage             `json:"tools_snapshot"`
	Tools                 MCPServerCatalogCacheHint   `json:"tools"`
	ToolsExpiresAt        *time.Time                  `json:"tools_expires_at,omitempty"`
	FetchedAt             *time.Time                  `json:"fetched_at,omitempty"`
	RefreshOwnerToken     *uuid.UUID                  `json:"refresh_owner_token,omitempty"`
	RefreshLeaseExpiresAt *time.Time                  `json:"refresh_lease_expires_at,omitempty"`
	RefreshError          string                      `json:"refresh_error"`
	CreatedAt             time.Time                   `json:"created_at"`
	UpdatedAt             time.Time                   `json:"updated_at"`
}

func (r MCPServerCatalogRecord) Fetched() bool { return r.FetchedAt != nil }

func (r MCPServerCatalogRecord) ToolsFreshAt(now time.Time) bool {
	return r.RefreshError == "" && r.ToolsExpiresAt != nil && now.Before(*r.ToolsExpiresAt)
}

func (s *Store) GetMCPServerCatalog(
	ctx context.Context,
	identity MCPServerCatalogIdentity,
) (MCPServerCatalogRecord, bool, error) {
	if err := identity.validate(); err != nil {
		return MCPServerCatalogRecord{}, false, err
	}
	row, err := s.q.GetMCPServerCatalog(ctx, dbsqlc.GetMCPServerCatalogParams{
		OrgID:           identity.OrgID,
		EndpointUrl:     identity.EndpointURL,
		SecretID:        identity.secretID(),
		SecretVersionID: identity.secretVersionID(),
		AwsRegion:       identity.awsRegion(),
		AwsService:      identity.awsService(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return MCPServerCatalogRecord{}, false, nil
	}
	if err != nil {
		return MCPServerCatalogRecord{}, false, fmt.Errorf("get mcp server catalog: %w", err)
	}
	return mcpServerCatalogRecordFromSQLC(row), true, nil
}

type AcquireMCPServerCatalogRefreshLeaseInput struct {
	Identity   MCPServerCatalogIdentity
	OwnerToken uuid.UUID
	TTL        time.Duration
}

func (s *Store) AcquireMCPServerCatalogRefreshLease(
	ctx context.Context,
	input AcquireMCPServerCatalogRefreshLeaseInput,
) (MCPServerCatalogRecord, bool, error) {
	if err := input.Identity.validate(); err != nil {
		return MCPServerCatalogRecord{}, false, err
	}
	if input.OwnerToken == uuid.Nil || input.TTL <= 0 {
		return MCPServerCatalogRecord{}, false, errors.New("owner token and positive ttl are required")
	}
	row, err := s.q.AcquireMCPServerCatalogRefreshLease(ctx, dbsqlc.AcquireMCPServerCatalogRefreshLeaseParams{
		OrgID:           input.Identity.OrgID,
		EndpointUrl:     input.Identity.EndpointURL,
		SecretID:        input.Identity.secretID(),
		SecretVersionID: input.Identity.secretVersionID(),
		AwsRegion:       input.Identity.awsRegion(),
		AwsService:      input.Identity.awsService(),
		OwnerToken:      input.OwnerToken,
		TtlMilliseconds: input.TTL.Milliseconds(),
	})
	if err != nil {
		return MCPServerCatalogRecord{}, false, fmt.Errorf("acquire mcp server catalog refresh lease: %w", err)
	}
	record := mcpServerCatalogRecordFromSQLC(row)
	acquired := record.RefreshOwnerToken != nil && *record.RefreshOwnerToken == input.OwnerToken
	return record, acquired, nil
}

type MarkMCPServerCatalogFetchedInput struct {
	OrgID              uuid.UUID
	ID                 uuid.UUID
	OwnerToken         uuid.UUID
	ProtocolVersion    string
	ServerCapabilities json.RawMessage
	ServerInfo         json.RawMessage
	Instructions       string
	Discover           MCPServerCatalogCacheHint
	DiscoverFreshFor   time.Duration
	ToolsSnapshot      json.RawMessage
	Tools              MCPServerCatalogCacheHint
	ToolsFreshFor      time.Duration
}

func (s *Store) MarkMCPServerCatalogFetched(
	ctx context.Context,
	input MarkMCPServerCatalogFetchedInput,
) (MCPServerCatalogRecord, error) {
	if input.OrgID == uuid.Nil || input.ID == uuid.Nil || input.OwnerToken == uuid.Nil {
		return MCPServerCatalogRecord{}, errors.New("org, catalog id, and owner token are required")
	}
	if input.ProtocolVersion == "" {
		return MCPServerCatalogRecord{}, errors.New("protocol version is required")
	}
	if input.Discover.TTLMs < 0 || input.Tools.TTLMs < 0 || input.DiscoverFreshFor < 0 || input.ToolsFreshFor < 0 {
		return MCPServerCatalogRecord{}, errors.New("cache ttls must not be negative")
	}
	row, err := s.q.MarkMCPServerCatalogFetched(ctx, dbsqlc.MarkMCPServerCatalogFetchedParams{
		ProtocolVersion:              input.ProtocolVersion,
		ServerCapabilities:           normalizedJSON(input.ServerCapabilities),
		ServerInfo:                   normalizedJSON(input.ServerInfo),
		Instructions:                 input.Instructions,
		DiscoverCacheScope:           input.Discover.Scope,
		DiscoverTtlMs:                input.Discover.TTLMs,
		DiscoverFreshForMilliseconds: input.DiscoverFreshFor.Milliseconds(),
		ToolsSnapshot:                normalizedJSONArray(input.ToolsSnapshot),
		ToolsCacheScope:              input.Tools.Scope,
		ToolsTtlMs:                   input.Tools.TTLMs,
		ToolsFreshForMilliseconds:    input.ToolsFreshFor.Milliseconds(),
		OrgID:                        input.OrgID,
		ID:                           input.ID,
		OwnerToken:                   input.OwnerToken,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return MCPServerCatalogRecord{}, storeerr.ErrStateTransitionConflict
	}
	if err != nil {
		return MCPServerCatalogRecord{}, fmt.Errorf("mark mcp server catalog fetched: %w", err)
	}
	return mcpServerCatalogRecordFromSQLC(row), nil
}

func (s *Store) ReleaseMCPServerCatalogRefreshLease(
	ctx context.Context,
	orgID, id, ownerToken uuid.UUID,
) error {
	if orgID == uuid.Nil || id == uuid.Nil || ownerToken == uuid.Nil {
		return errors.New("org, catalog id, and owner token are required")
	}
	if err := s.q.ReleaseMCPServerCatalogRefreshLease(ctx, dbsqlc.ReleaseMCPServerCatalogRefreshLeaseParams{
		OrgID:      orgID,
		ID:         id,
		OwnerToken: ownerToken,
	}); err != nil {
		return fmt.Errorf("release mcp server catalog refresh lease: %w", err)
	}
	return nil
}

func (s *Store) listAgentMCPConnectionCatalogs(
	ctx context.Context,
	projectID, agentID uuid.UUID,
) (map[uuid.UUID]MCPServerCatalogRecord, error) {
	rows, err := s.q.ListAgentMCPConnectionCatalogs(
		ctx,
		dbsqlc.ListAgentMCPConnectionCatalogsParams{ProjectID: projectID, AgentID: agentID},
	)
	if err != nil {
		return nil, fmt.Errorf("list agent mcp connection catalogs: %w", err)
	}
	out := make(map[uuid.UUID]MCPServerCatalogRecord, len(rows))
	for _, row := range rows {
		out[row.ID] = mcpServerCatalogRecordFromSQLC(row)
	}
	return out, nil
}

func mcpServerCatalogRecordFromSQLC(row dbsqlc.McpServerCatalog) MCPServerCatalogRecord {
	record := MCPServerCatalogRecord{
		ID:                 row.ID,
		OrgID:              row.OrgID,
		EndpointURL:        row.EndpointUrl,
		Revision:           row.Revision,
		ProtocolVersion:    row.ProtocolVersion,
		ServerCapabilities: normalizedJSON(row.ServerCapabilities),
		ServerInfo:         normalizedJSON(row.ServerInfo),
		Instructions:       row.Instructions,
		Discover: MCPServerCatalogCacheHint{
			Scope: row.DiscoverCacheScope,
			TTLMs: row.DiscoverTtlMs,
		},
		DiscoverExpiresAt: row.DiscoverExpiresAt,
		ToolsSnapshot:     normalizedJSONArray(row.ToolsSnapshot),
		Tools: MCPServerCatalogCacheHint{
			Scope: row.ToolsCacheScope,
			TTLMs: row.ToolsTtlMs,
		},
		ToolsExpiresAt:        row.ToolsExpiresAt,
		FetchedAt:             row.FetchedAt,
		RefreshOwnerToken:     row.RefreshOwnerToken,
		RefreshLeaseExpiresAt: row.RefreshLeaseExpiresAt,
		RefreshError:          row.RefreshError,
		CreatedAt:             row.CreatedAt,
		UpdatedAt:             row.UpdatedAt,
	}
	if row.SecretID != nil && row.SecretVersionID != nil {
		record.Credential = &MCPServerCatalogCredential{
			SecretID:        *row.SecretID,
			SecretVersionID: *row.SecretVersionID,
			AWSRegion:       row.AwsRegion,
			AWSService:      row.AwsService,
		}
	}
	return record
}

func (s *Store) MarkMCPServerCatalogRefreshFailed(
	ctx context.Context,
	orgID, id, ownerToken uuid.UUID,
	message string,
) error {
	count, err := s.q.MarkMCPServerCatalogRefreshFailed(ctx, dbsqlc.MarkMCPServerCatalogRefreshFailedParams{
		OrgID:        orgID,
		ID:           id,
		OwnerToken:   ownerToken,
		RefreshError: message,
	})
	if err != nil {
		return fmt.Errorf("record mcp catalog refresh failure: %w", err)
	}
	if count == 0 {
		return storeerr.ErrStateTransitionConflict
	}
	return nil
}
