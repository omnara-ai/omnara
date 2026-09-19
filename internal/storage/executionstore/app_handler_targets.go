package executionstore

import (
	"context"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

type appConversation struct {
	connectionID uuid.UUID
	address      integrationstore.ConversationAddress
}

func fixedHandlerConversations(resources map[string]agentconfig.AppResourceCompiled) ([]appConversation, error) {
	var conversations []appConversation
	for _, resource := range resources {
		if !resource.Enabled || resource.InteractionHandler == nil {
			continue
		}
		if err := resource.Validate(); err != nil {
			return nil, err
		}
		id, err := publicid.Decode(publicid.KindIntegrationConnection, resource.ConnectionID)
		if err != nil {
			return nil, err
		}
		kind, ref, err := resource.Scope.Conversation()
		if err != nil {
			return nil, err
		}
		conversations = append(
			conversations,
			appConversation{id, integrationstore.ConversationAddress{Kind: kind, Ref: ref}},
		)
	}
	return conversations, nil
}

// Lock every conversation used by activation before profile/source/agent locks.
// Inbox launch must acquire this same union before nesting launchAgentTx, so an
// initial origin cannot invert the order against another fixed handler scope.
func lockAppConversationsTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID uuid.UUID,
	resources map[string]agentconfig.AppResourceCompiled,
	origins ...AgentInputOrigin,
) error {
	conversations, err := fixedHandlerConversations(resources)
	if err != nil {
		return err
	}
	for _, origin := range origins {
		conversations = append(conversations, appConversation{origin.ConnectionID, origin.Address})
	}
	slices.SortFunc(conversations, func(a, b appConversation) int {
		if order := slices.Compare(a.connectionID[:], b.connectionID[:]); order != 0 {
			return order
		}
		if order := strings.Compare(a.address.Kind, b.address.Kind); order != 0 {
			return order
		}
		return strings.Compare(a.address.Ref, b.address.Ref)
	})
	for _, conversation := range slices.Compact(conversations) {
		if err := integrationstore.LockConversationTx(
			ctx,
			tx,
			projectID,
			conversation.connectionID,
			conversation.address,
		); err != nil {
			return err
		}
	}
	return nil
}

// Fixed scopes are discoverable without a prior input or subscription. Targets
// remain attribution only. Removed handlers leave history intact and selection
// reconciliation clears revoked choices without falling back to another handler.
func (s *Store) activateHandlerTargetsTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID uuid.UUID,
	resources map[string]agentconfig.AppResourceCompiled,
) error {
	conversations, err := fixedHandlerConversations(resources)
	if err != nil {
		return err
	}
	for _, conversation := range conversations {
		if _, err := s.integrations.EnsureConversationTargetTx(ctx, tx, integrationstore.EnsureConversationTargetInput{
			ProjectID: projectID, AgentID: agentID, ConnectionID: conversation.connectionID,
			Address: conversation.address, Role: integrationstore.TargetAttribution,
		}); err != nil {
			return err
		}
	}
	_, err = s.ReconcileInteractionSelectionTx(ctx, tx, projectID, agentID)
	return err
}
