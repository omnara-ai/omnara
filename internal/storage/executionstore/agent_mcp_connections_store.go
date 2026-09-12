package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type MCPConnectionState string

const (
	MCPConnectionStateInitializing MCPConnectionState = "initializing"
	MCPConnectionStateReady        MCPConnectionState = "ready"
	MCPConnectionStateFailed       MCPConnectionState = "failed"
	MCPConnectionStateExpired      MCPConnectionState = "expired"
)

type MCPConnectionRecord struct {
	ID                 ID                 `json:"id"`
	AgentID            ID                 `json:"agent_id"`
	ServerKey          string             `json:"server_key"`
	EndpointURL        string             `json:"endpoint_url"`
	ConfigHash         string             `json:"config_hash"`
	State              MCPConnectionState `json:"state"`
	ProtocolVersion    string             `json:"protocol_version"`
	MCPSessionID       string             `json:"mcp_session_id"`
	ServerCapabilities json.RawMessage    `json:"server_capabilities"`
	ServerInfo         json.RawMessage    `json:"server_info"`
	ToolsSnapshot      json.RawMessage    `json:"tools_snapshot"`
	Instructions       string             `json:"instructions"`
	InitializeError    string             `json:"initialize_error"`
	Generation         int64              `json:"generation"`
	RequestSequence    int64              `json:"request_sequence"`
	CatalogID          *ID                `json:"catalog_id,omitempty"`
	CatalogRevision    int64              `json:"catalog_revision"`
	CreatedAt          time.Time          `json:"created_at"`
	UpdatedAt          time.Time          `json:"updated_at"`
}

func (r MCPConnectionRecord) UsesCatalog() bool { return r.CatalogID != nil }

func (r MCPConnectionRecord) withCatalog(catalogs map[ID]MCPServerCatalogRecord) MCPConnectionRecord {
	if r.CatalogID == nil {
		return r
	}
	catalog, found := catalogs[*r.CatalogID]
	if !found {
		return r
	}
	if catalog.RefreshError != "" {
		r.State = MCPConnectionStateFailed
		r.InitializeError = catalog.RefreshError
		r.ToolsSnapshot = json.RawMessage(`[]`)
		return r
	}
	r.ServerCapabilities = catalog.ServerCapabilities
	r.ServerInfo = catalog.ServerInfo
	r.ToolsSnapshot = catalog.ToolsSnapshot
	r.Instructions = catalog.Instructions
	r.CatalogRevision = catalog.Revision
	return r
}

func (s *Store) attachMCPConnectionCatalogs(
	ctx context.Context,
	projectID, agentID ID,
	connections []MCPConnectionRecord,
) ([]MCPConnectionRecord, error) {
	needsCatalog := false
	for _, connection := range connections {
		if connection.UsesCatalog() {
			needsCatalog = true
			break
		}
	}
	if !needsCatalog {
		return connections, nil
	}
	catalogs, err := s.listAgentMCPConnectionCatalogs(ctx, projectID, agentID)
	if err != nil {
		return nil, err
	}
	out := make([]MCPConnectionRecord, 0, len(connections))
	for _, connection := range connections {
		out = append(out, connection.withCatalog(catalogs))
	}
	return out, nil
}

func (s *Store) attachMCPConnectionCatalog(
	ctx context.Context,
	projectID, agentID ID,
	connection MCPConnectionRecord,
) (MCPConnectionRecord, error) {
	attached, err := s.attachMCPConnectionCatalogs(ctx, projectID, agentID, []MCPConnectionRecord{connection})
	if err != nil {
		return MCPConnectionRecord{}, err
	}
	return attached[0], nil
}

func (s *Store) ReconcileAgentMCPConnections(
	ctx context.Context,
	projectID, agentID ID,
	servers []agentconfig.RuntimeMCPServer,
) ([]MCPConnectionRecord, error) {
	if isNilID(projectID) || isNilID(agentID) {
		return nil, errors.New("project and agent are required")
	}
	desired := make(map[string]string, len(servers))
	serverKeys := make([]string, 0, len(servers))
	for _, server := range servers {
		configHash, err := mcpServerConfigHash(server)
		if err != nil {
			return nil, fmt.Errorf("hash mcp server %q config: %w", server.ServerKey, err)
		}
		desired[server.ServerKey] = configHash
		serverKeys = append(serverKeys, server.ServerKey)
	}
	current, err := s.ListAgentMCPConnections(ctx, projectID, agentID)
	if err != nil {
		return nil, err
	}
	connections := make([]MCPConnectionRecord, 0, len(servers))
	reconciled := true
	for _, connection := range current {
		configHash, configured := desired[connection.ServerKey]
		if configured {
			if connection.ConfigHash == configHash {
				connections = append(connections, connection)
			} else {
				reconciled = false
			}
			continue
		}
		if connection.State != MCPConnectionStateExpired {
			reconciled = false
		}
	}
	if reconciled && len(connections) == len(servers) && len(current) > 0 {
		return s.attachMCPConnectionCatalogs(ctx, projectID, agentID, connections)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin reconcile agent mcp connections: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	project, err := loadProjectTx(ctx, qtx, projectID)
	if err != nil {
		return nil, err
	}
	if err := lifecyclelock.EnterActiveProject(ctx, tx, project.OrgID, projectID); err != nil {
		return nil, err
	}
	if err := lifecyclelock.Agents(ctx, tx, []lifecyclelock.AgentRef{{
		ProjectID: projectID,
		AgentID:   agentID,
	}}); err != nil {
		return nil, err
	}
	agent, err := qtx.GetAgentInProject(ctx, dbsqlc.GetAgentInProjectParams{
		ProjectID: projectID,
		ID:        agentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, storeerr.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("revalidate mcp connection agent: %w", err)
	}
	if AgentState(agent.State) != AgentStateActive {
		return nil, storeerr.ErrStateTransitionConflict
	}
	connections, err = createAgentMCPConnectionsTx(ctx, qtx, projectID, agentID, servers)
	if err != nil {
		return nil, err
	}
	if err := qtx.ExpireRemovedMCPConnections(ctx, dbsqlc.ExpireRemovedMCPConnectionsParams{
		ProjectID:  projectID,
		AgentID:    agentID,
		ServerKeys: serverKeys,
	}); err != nil {
		return nil, fmt.Errorf("expire removed mcp connections: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit agent mcp reconciliation: %w", err)
	}
	return s.attachMCPConnectionCatalogs(ctx, projectID, agentID, connections)
}

func (s *Store) GetMCPConnection(
	ctx context.Context,
	projectID, agentID ID,
	serverKey string,
) (MCPConnectionRecord, bool, error) {
	if isNilID(projectID) || isNilID(agentID) || serverKey == "" {
		return MCPConnectionRecord{}, false, errors.New(
			"project, agent, and server key are required",
		)
	}
	row, err := s.q.GetMCPConnection(
		ctx,
		dbsqlc.GetMCPConnectionParams{ProjectID: projectID, AgentID: agentID, ServerKey: serverKey},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return MCPConnectionRecord{}, false, nil
	}
	if err != nil {
		return MCPConnectionRecord{}, false, fmt.Errorf("get mcp connection: %w", err)
	}
	record, err := s.attachMCPConnectionCatalog(ctx, projectID, agentID, mcpConnectionRecordFromSQLC(row))
	if err != nil {
		return MCPConnectionRecord{}, false, err
	}
	return record, true, nil
}

func (s *Store) ListAgentMCPConnections(
	ctx context.Context,
	projectID, agentID ID,
) ([]MCPConnectionRecord, error) {
	if isNilID(projectID) || isNilID(agentID) {
		return nil, errors.New("project and agent are required")
	}
	rows, err := s.q.ListAgentMCPConnections(
		ctx,
		dbsqlc.ListAgentMCPConnectionsParams{ProjectID: projectID, AgentID: agentID},
	)
	if err != nil {
		return nil, fmt.Errorf("list agent mcp connections: %w", err)
	}
	out := make([]MCPConnectionRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, mcpConnectionRecordFromSQLC(row))
	}
	return s.attachMCPConnectionCatalogs(ctx, projectID, agentID, out)
}

func (s *Store) GetMCPConnectionByID(
	ctx context.Context,
	projectID, agentID, id ID,
) (MCPConnectionRecord, bool, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(id) {
		return MCPConnectionRecord{}, false, errors.New(
			"project, agent, and connection id are required",
		)
	}
	row, err := s.q.GetMCPConnectionByID(
		ctx,
		dbsqlc.GetMCPConnectionByIDParams{ProjectID: projectID, AgentID: agentID, ID: id},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return MCPConnectionRecord{}, false, nil
	}
	if err != nil {
		return MCPConnectionRecord{}, false, fmt.Errorf("get mcp connection by id: %w", err)
	}
	record, err := s.attachMCPConnectionCatalog(ctx, projectID, agentID, mcpConnectionRecordFromSQLC(row))
	if err != nil {
		return MCPConnectionRecord{}, false, err
	}
	return record, true, nil
}

type MarkMCPConnectionReadyInput struct {
	ProjectID          ID
	AgentID            ID
	ID                 ID
	GenerationObserved int64
	MCPSessionID       string
	ProtocolVersion    string
	CatalogID          ID
}

func (s *Store) MarkMCPConnectionReady(
	ctx context.Context,
	input MarkMCPConnectionReadyInput,
) (MCPConnectionRecord, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.ID) {
		return MCPConnectionRecord{}, errors.New("project, agent, and connection id are required")
	}
	if input.GenerationObserved <= 0 {
		return MCPConnectionRecord{}, errors.New("observed generation must be positive")
	}
	if isNilID(input.CatalogID) || input.ProtocolVersion == "" {
		return MCPConnectionRecord{}, errors.New("catalog id and protocol version are required")
	}
	row, err := s.q.MarkMCPConnectionReady(ctx, dbsqlc.MarkMCPConnectionReadyParams{
		ProjectID:          input.ProjectID,
		AgentID:            input.AgentID,
		ID:                 input.ID,
		GenerationObserved: input.GenerationObserved,
		McpSessionID:       input.MCPSessionID,
		ProtocolVersion:    input.ProtocolVersion,
		CatalogID:          input.CatalogID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return MCPConnectionRecord{}, storeerr.ErrStateTransitionConflict
	}
	if err != nil {
		return MCPConnectionRecord{}, fmt.Errorf("mark mcp connection ready: %w", err)
	}
	return s.attachMCPConnectionCatalog(ctx, input.ProjectID, input.AgentID, mcpConnectionRecordFromSQLC(row))
}

type SetMCPConnectionCatalogInput struct {
	ProjectID          ID
	AgentID            ID
	ID                 ID
	GenerationObserved int64
	MCPSessionID       string
	ProtocolVersion    string
	CatalogID          ID
}

func (s *Store) SetMCPConnectionCatalog(
	ctx context.Context,
	input SetMCPConnectionCatalogInput,
) (MCPConnectionRecord, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.ID) || isNilID(input.CatalogID) {
		return MCPConnectionRecord{}, errors.New("project, agent, connection id, and catalog id are required")
	}
	if input.GenerationObserved <= 0 {
		return MCPConnectionRecord{}, errors.New("observed generation must be positive")
	}
	if input.ProtocolVersion == "" {
		return MCPConnectionRecord{}, errors.New("protocol version is required")
	}
	row, err := s.q.SetMCPConnectionCatalog(ctx, dbsqlc.SetMCPConnectionCatalogParams{
		ProtocolVersion:    input.ProtocolVersion,
		McpSessionID:       input.MCPSessionID,
		CatalogID:          input.CatalogID,
		ProjectID:          input.ProjectID,
		AgentID:            input.AgentID,
		ID:                 input.ID,
		GenerationObserved: input.GenerationObserved,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return MCPConnectionRecord{}, storeerr.ErrStateTransitionConflict
	}
	if err != nil {
		return MCPConnectionRecord{}, fmt.Errorf("set mcp connection catalog: %w", err)
	}
	return s.attachMCPConnectionCatalog(ctx, input.ProjectID, input.AgentID, mcpConnectionRecordFromSQLC(row))
}

func (s *Store) BeginMCPConnectionInitialization(
	ctx context.Context,
	projectID, agentID, id ID,
) (MCPConnectionRecord, bool, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(id) {
		return MCPConnectionRecord{}, false, errors.New(
			"project, agent, and connection id are required",
		)
	}
	row, err := s.q.BeginMCPConnectionInitialization(
		ctx,
		dbsqlc.BeginMCPConnectionInitializationParams{
			ProjectID: projectID,
			AgentID:   agentID,
			ID:        id,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return MCPConnectionRecord{}, false, nil
	}
	if err != nil {
		return MCPConnectionRecord{}, false, fmt.Errorf(
			"begin mcp connection initialization: %w",
			err,
		)
	}
	return mcpConnectionRecordFromSQLC(row), true, nil
}

func (s *Store) MarkMCPConnectionFailed(
	ctx context.Context,
	projectID, agentID, id ID,
	generationObserved int64,
	initializeError string,
) (MCPConnectionRecord, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(id) {
		return MCPConnectionRecord{}, errors.New("project, agent, and connection id are required")
	}
	if generationObserved <= 0 {
		return MCPConnectionRecord{}, errors.New("observed generation must be positive")
	}
	row, err := s.q.MarkMCPConnectionFailed(
		ctx,
		dbsqlc.MarkMCPConnectionFailedParams{
			ProjectID:          projectID,
			AgentID:            agentID,
			ID:                 id,
			GenerationObserved: generationObserved,
			InitializeError:    initializeError,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return MCPConnectionRecord{}, storeerr.ErrStateTransitionConflict
	}
	if err != nil {
		return MCPConnectionRecord{}, fmt.Errorf("mark mcp connection failed: %w", err)
	}
	return mcpConnectionRecordFromSQLC(row), nil
}

func (s *Store) MarkMCPConnectionExpired(
	ctx context.Context,
	projectID, agentID, id ID,
	generationObserved int64,
) (MCPConnectionRecord, bool, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(id) {
		return MCPConnectionRecord{}, false, errors.New(
			"project, agent, and connection id are required",
		)
	}
	if generationObserved <= 0 {
		return MCPConnectionRecord{}, false, errors.New("observed generation must be positive")
	}
	row, err := s.q.MarkMCPConnectionExpired(
		ctx,
		dbsqlc.MarkMCPConnectionExpiredParams{
			ProjectID:          projectID,
			AgentID:            agentID,
			ID:                 id,
			GenerationObserved: generationObserved,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return MCPConnectionRecord{}, false, nil
	}
	if err != nil {
		return MCPConnectionRecord{}, false, fmt.Errorf("mark mcp connection expired: %w", err)
	}
	return mcpConnectionRecordFromSQLC(row), true, nil
}

func (s *Store) NextMCPRequestSequence(
	ctx context.Context,
	projectID, agentID, id ID,
) (int64, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(id) {
		return 0, errors.New("project, agent, and connection id are required")
	}
	seq, err := s.q.NextMCPRequestSequence(
		ctx,
		dbsqlc.NextMCPRequestSequenceParams{
			ProjectID: projectID,
			AgentID:   agentID,
			ID:        id,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, storeerr.ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("next mcp request sequence: %w", err)
	}
	return seq, nil
}

func mcpConnectionRecordFromSQLC(row dbsqlc.AgentMcpConnection) MCPConnectionRecord {
	return MCPConnectionRecord{
		ID:                 row.ID,
		AgentID:            row.AgentID,
		ServerKey:          row.ServerKey,
		EndpointURL:        row.EndpointUrl,
		ConfigHash:         row.ConfigHash,
		State:              MCPConnectionState(row.State),
		ProtocolVersion:    row.ProtocolVersion,
		MCPSessionID:       row.McpSessionID,
		ServerCapabilities: normalizedJSON(row.ServerCapabilities),
		ServerInfo:         normalizedJSON(row.ServerInfo),
		ToolsSnapshot:      normalizedJSONArray(row.ToolsSnapshot),
		InitializeError:    row.InitializeError,
		Generation:         row.Generation,
		RequestSequence:    row.RequestSequence,
		CatalogID:          row.CatalogID,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}
}
