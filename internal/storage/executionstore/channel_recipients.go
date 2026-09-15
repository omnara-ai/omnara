package executionstore

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type LookupChannelRecipientsInput struct {
	ProjectID            uuid.UUID
	IntegrationInstallID uuid.UUID
	ProviderRef          string
	Receipt              ChannelEventLease
	InputKeys            []string
	AfterAgentID         uuid.UUID
	Limit                int32
	Capabilities         []channelconnector.Capability
}

type ChannelRecipient struct {
	AgentID   uuid.UUID
	BindingID uuid.UUID
	InputKeys []string
}

type LookupChannelRecipientsResult struct {
	ChannelID                uuid.UUID
	HasReceiveBindingHistory bool
	WorkflowStarted          bool
	Recipients               []ChannelRecipient
	HasMore                  bool
}

// LookupChannelRecipients observes existing grants for provider behavior to
// select. Paging never creates a subscription. Each later admission must recheck
// the exact binding, semantic precondition and receipt lease in its transaction.
func (s *Store) LookupChannelRecipients(
	ctx context.Context, input LookupChannelRecipientsInput,
) (LookupChannelRecipientsResult, error) {
	if input.ProjectID == uuid.Nil || input.IntegrationInstallID == uuid.Nil ||
		input.Receipt.ReceiptID == uuid.Nil || input.Receipt.LeaseToken == uuid.Nil || input.Receipt.LeaseGeneration <= 0 ||
		strings.TrimSpace(input.ProviderRef) == "" || len(input.ProviderRef) > 512 || input.Limit < 1 || input.Limit > 100 {
		return LookupChannelRecipientsResult{}, storeerr.InvalidRequest(
			errors.New("project, installation, current receipt lease, bounded provider reference and limit are required"))
	}
	if err := dbsafe.Text(input.ProviderRef); err != nil {
		return LookupChannelRecipientsResult{}, storeerr.InvalidRequest(err)
	}
	if err := validateChannelLookupKeys(input.InputKeys); err != nil {
		return LookupChannelRecipientsResult{}, err
	}
	install, err := s.integrations.GetIntegrationInstall(ctx, input.ProjectID, input.IntegrationInstallID)
	if err != nil {
		return LookupChannelRecipientsResult{}, err
	}
	if install.State != integrationstore.IntegrationInstallStateActive {
		return LookupChannelRecipientsResult{}, storeerr.ErrNotFound
	}
	if _, err := s.integrations.GetConnectorIntegrationApp(ctx, install.IntegrationAppID, input.Capabilities); err != nil {
		return LookupChannelRecipientsResult{}, err
	}
	if err := checkChannelEventLease(ctx, s.q, input.ProjectID, input.IntegrationInstallID, input.Receipt); err != nil {
		return LookupChannelRecipientsResult{}, err
	}
	routing, err := s.integrations.LookupChannelReceiptRouting(
		ctx, input.ProjectID, input.IntegrationInstallID, input.ProviderRef, input.Receipt.ReceiptID)
	if err != nil {
		return LookupChannelRecipientsResult{}, err
	}
	result := LookupChannelRecipientsResult{
		ChannelID: routing.ChannelID, HasReceiveBindingHistory: routing.HasReceiveBindingHistory,
		WorkflowStarted: routing.WorkflowStarted, Recipients: []ChannelRecipient{},
	}
	if routing.ChannelID == uuid.Nil {
		return result, nil
	}
	bindings, err := s.integrations.ListChannelReceiveBindings(ctx, input.ProjectID, input.IntegrationInstallID,
		routing.ChannelID, input.AfterAgentID, input.Limit+1)
	if err != nil {
		return LookupChannelRecipientsResult{}, err
	}
	result.HasMore = len(bindings) > int(input.Limit)
	if result.HasMore {
		bindings = bindings[:input.Limit]
	}
	agentIDs := make([]uuid.UUID, len(bindings))
	for i, binding := range bindings {
		agentIDs[i] = binding.AgentID
	}
	keys, err := s.q.GetExistingChannelInputKeysForAgents(ctx, dbsqlc.GetExistingChannelInputKeysForAgentsParams{
		ProjectID: input.ProjectID, AgentIds: agentIDs, IdempotencyScope: integrationstore.IdempotencyScope(install),
		InputKeys: input.InputKeys,
	})
	if err != nil {
		return LookupChannelRecipientsResult{}, err
	}
	byAgent := make(map[uuid.UUID][]string, len(bindings))
	for _, key := range keys {
		byAgent[key.AgentID] = append(byAgent[key.AgentID], key.InputIdempotencyKey)
	}
	for _, binding := range bindings {
		inputKeys := byAgent[binding.AgentID]
		if inputKeys == nil {
			inputKeys = []string{}
		}
		result.Recipients = append(result.Recipients, ChannelRecipient{
			AgentID: binding.AgentID, BindingID: binding.ID, InputKeys: inputKeys,
		})
	}
	return result, nil
}

// ErrChannelRecipientsChanged asks the behavior to repeat recipient selection;
// it must not retry a stale decision to launch into an established conversation.
var ErrChannelRecipientsChanged = errors.New("channel recipients changed; repeat recipient lookup")

func checkChannelWorkflowInitialBinding(
	ctx context.Context, q *dbsqlc.Queries, identity ChannelWorkflowIdentity,
	lease ChannelEventLease, target integrationstore.IntegrationTargetRecord,
) error {
	// Binding creators use this same lock. Read routing facts in the subsequent
	// statement so READ COMMITTED sees a creator that committed while we waited.
	if _, err := q.LockIntegrationTargetForBinding(ctx, dbsqlc.LockIntegrationTargetForBindingParams{
		ProjectID: identity.ProjectID, IntegrationInstallID: identity.IntegrationInstallID, IntegrationTargetID: target.ID,
	}); err != nil {
		return err
	}
	routing, err := q.LookupChannelReceiptRouting(ctx, dbsqlc.LookupChannelReceiptRoutingParams{
		ProjectID: identity.ProjectID, IntegrationInstallID: identity.IntegrationInstallID,
		ReceiptID: lease.ReceiptID, ProviderRef: target.ProviderRef,
	})
	if err != nil {
		return err
	}
	if routing.HasReceiveBindingHistory && !routing.WorkflowStarted {
		return ErrChannelRecipientsChanged
	}
	return nil
}
