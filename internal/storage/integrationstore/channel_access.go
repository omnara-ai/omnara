package integrationstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// ChannelAccess is a current authorization view, never a reusable access token.
// Private address fields stay between core and its authorized connector.
type ChannelAccess struct {
	IntegrationKind      IntegrationKind
	ProjectID            uuid.UUID
	AgentID              uuid.UUID
	ChannelID            uuid.UUID
	ParentChannelID      uuid.UUID
	IntegrationInstallID uuid.UUID
	IntegrationAppID     uuid.UUID
	DefinitionID         uuid.UUID
	ConnectorKey         string
	Provider             string
	ImplementationKey    string
	Kind                 ChannelKind
	Name                 string
	Description          string
	Active               bool
	ReceiveAllowed       bool
	Capabilities         ChannelCapabilities
	SendParamsSchema     json.RawMessage
	ProviderRef          string
	ProviderRefKind      string
	ProviderMetadata     json.RawMessage
}

func (s *Store) GetAgentChannelAccess(
	ctx context.Context,
	projectID, agentID, channelID uuid.UUID,
) (ChannelAccess, error) {
	return getAgentChannelAccess(ctx, s.q, projectID, agentID, channelID)
}

// GetAgentChannelAccessTx retains the current definition through commit. Callers
// acquire their scope/agent/binding authority before this read; use the unlocked
// GetAgentChannelAccess for discovery before taking those locks. Definition
// publication takes installation/app locks before updating the definition.
func (s *Store) GetAgentChannelAccessTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID, channelID uuid.UUID,
) (ChannelAccess, error) {
	if tx == nil {
		return ChannelAccess{}, errors.New("transaction is required")
	}
	q := s.q.WithTx(tx)
	access, err := getAgentChannelAccess(ctx, q, projectID, agentID, channelID)
	if err != nil {
		return ChannelAccess{}, err
	}
	if _, err := q.LockChannelDefinition(ctx, dbsqlc.LockChannelDefinitionParams{
		ProjectID: projectID, IntegrationInstallID: access.IntegrationInstallID, ID: access.DefinitionID,
	}); err != nil {
		return ChannelAccess{}, integrationChannelReadError("lock channel access definition", err)
	}
	// A publication may have committed while we waited for the shared lock.
	// Reread under that lock rather than returning the pre-lock schema/capabilities.
	return getAgentChannelAccess(ctx, q, projectID, agentID, channelID)
}

func getAgentChannelAccess(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID, channelID uuid.UUID,
) (ChannelAccess, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || channelID == uuid.Nil {
		return ChannelAccess{}, storeerr.InvalidRequest(errors.New("project, agent, and channel are required"))
	}
	row, err := q.GetAgentChannelAccess(ctx, dbsqlc.GetAgentChannelAccessParams{
		ProjectID: projectID, AgentID: agentID, ChannelID: channelID,
	})
	if err != nil {
		return ChannelAccess{}, integrationChannelReadError("get agent channel", err)
	}
	var implemented ChannelCapabilities
	if err := json.Unmarshal(row.Capabilities, &implemented); err != nil {
		return ChannelAccess{}, fmt.Errorf("decode channel capabilities: %w", err)
	}
	return ChannelAccess{
		ProjectID: projectID, AgentID: agentID, ChannelID: row.ID,
		ParentChannelID: storeutil.IDFromPtr(row.ParentChannelID), IntegrationInstallID: row.IntegrationInstallID,
		IntegrationAppID: storeutil.IDFromPtr(row.IntegrationAppID), DefinitionID: row.DefinitionID,
		IntegrationKind: IntegrationKind(row.IntegrationKind),
		ConnectorKey:    stringFromPtr(row.ConnectorKey), Provider: stringFromPtr(row.Provider),
		ImplementationKey: row.ImplementationKey,
		Kind:              ChannelKind(row.Kind), Name: row.DisplayName, Description: row.Description,
		Active: row.Active, ReceiveAllowed: row.Active && row.ReceiveAllowed,
		Capabilities: effectiveChannelCapabilities(
			implemented, row.Active && row.ReadAllowed, row.Active && row.SendAllowed,
			row.Active && row.ReplyChannelAllowed,
		),
		SendParamsSchema: row.SendParamsSchema,
		ProviderRef:      row.ProviderRef, ProviderRefKind: row.ProviderRefKind, ProviderMetadata: row.ProviderMetadata,
	}, nil
}

func effectiveChannelCapabilities(
	implemented ChannelCapabilities,
	canRead, canSend, canCreateReplyChannel bool,
) ChannelCapabilities {
	implemented.Read = implemented.Read && canRead
	implemented.Send = implemented.Send && canSend
	implemented.Text = implemented.Text && implemented.Send
	implemented.Artifacts = implemented.Artifacts && implemented.Send
	implemented.Permissions = implemented.Permissions && canSend
	implemented.Questions = implemented.Questions && canSend
	implemented.CreatesReplyChannel = implemented.CreatesReplyChannel && implemented.Send && canCreateReplyChannel
	return implemented
}
