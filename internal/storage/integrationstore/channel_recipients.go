package integrationstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// ChannelBindingIdentity is historical ownership, not permission to admit input.
type ChannelBindingIdentity struct {
	ID                   uuid.UUID
	ProjectID            uuid.UUID
	AgentID              uuid.UUID
	IntegrationInstallID uuid.UUID
	IntegrationTargetID  uuid.UUID
}

// ChannelReceiptRouting observes existing routing facts in one database snapshot.
// ChannelID is uuid.Nil when the provider address has no current registered channel.
// Receive history includes revoked grants, but not send/read-only grants.
type ChannelReceiptRouting struct {
	ChannelID                uuid.UUID
	HasReceiveBindingHistory bool
	WorkflowStarted          bool
}

// LookupChannelReceiptRouting observes this receipt's workflow progress only for
// the addressed channel, using the accepted input's immutable origin. It neither
// authorizes new work nor validates a receipt lease.
func (s *Store) LookupChannelReceiptRouting(
	ctx context.Context,
	projectID, integrationInstallID uuid.UUID,
	providerRef string,
	receiptID uuid.UUID,
) (ChannelReceiptRouting, error) {
	if projectID == uuid.Nil || integrationInstallID == uuid.Nil || receiptID == uuid.Nil ||
		providerRef == "" || len(providerRef) > 2048 {
		return ChannelReceiptRouting{}, storeerr.InvalidRequest(
			errors.New("project, installation, receipt, and bounded provider reference are required"))
	}
	if err := dbsafe.Text(providerRef); err != nil {
		return ChannelReceiptRouting{}, storeerr.InvalidRequest(err)
	}
	row, err := s.q.LookupChannelReceiptRouting(ctx, dbsqlc.LookupChannelReceiptRoutingParams{
		ProjectID: projectID, IntegrationInstallID: integrationInstallID, ReceiptID: receiptID, ProviderRef: providerRef,
	})
	if err != nil {
		return ChannelReceiptRouting{}, integrationChannelReadError("lookup channel receipt routing", err)
	}
	return ChannelReceiptRouting{
		ChannelID:                storeutil.IDFromPtr(row.ChannelID),
		HasReceiveBindingHistory: row.HasReceiveBindingHistory, WorkflowStarted: row.WorkflowStarted,
	}, nil
}

// GetChannelBindingIdentity preserves replay scope after revocation or retirement.
// New input must separately recheck this exact binding with GetActiveReceiveBindingTx.
func (s *Store) GetChannelBindingIdentity(
	ctx context.Context,
	projectID, integrationInstallID, bindingID uuid.UUID,
) (ChannelBindingIdentity, error) {
	if projectID == uuid.Nil || integrationInstallID == uuid.Nil || bindingID == uuid.Nil {
		return ChannelBindingIdentity{}, storeerr.InvalidRequest(
			errors.New("project, installation, and binding are required"))
	}
	row, err := s.q.GetChannelBindingIdentity(ctx, dbsqlc.GetChannelBindingIdentityParams{
		ProjectID: projectID, IntegrationInstallID: integrationInstallID, ID: bindingID,
	})
	if err != nil {
		return ChannelBindingIdentity{}, integrationChannelReadError("get channel binding identity", err)
	}
	return ChannelBindingIdentity{
		ID: row.ID, ProjectID: row.ProjectID, AgentID: row.AgentID,
		IntegrationInstallID: row.IntegrationInstallID, IntegrationTargetID: row.IntegrationTargetID,
	}, nil
}

// ListChannelReceiveBindings returns one live receive binding per active agent,
// ordered by agent ID then binding ID. Deduplication precedes the row limit.
// afterAgentID may be uuid.Nil for the first page; the caller owns limit+1/cursors.
// This discovery read grants nothing: admission must recheck the exact binding.
func (s *Store) ListChannelReceiveBindings(
	ctx context.Context,
	projectID, integrationInstallID, integrationTargetID, afterAgentID uuid.UUID,
	limit int32,
) ([]IntegrationTargetBindingRecord, error) {
	if projectID == uuid.Nil || integrationInstallID == uuid.Nil || integrationTargetID == uuid.Nil || limit < 1 {
		return nil, storeerr.InvalidRequest(errors.New("project, installation, channel, and positive limit are required"))
	}
	rows, err := s.q.ListChannelReceiveBindings(ctx, dbsqlc.ListChannelReceiveBindingsParams{
		ProjectID: projectID, IntegrationInstallID: integrationInstallID, IntegrationTargetID: integrationTargetID,
		AfterAgentID: afterAgentID, RowLimit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list channel receive bindings: %w", err)
	}
	out := make([]IntegrationTargetBindingRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, integrationTargetBindingRecordFromSQLC(row))
	}
	return out, nil
}
