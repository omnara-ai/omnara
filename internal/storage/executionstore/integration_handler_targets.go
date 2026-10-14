package executionstore

import (
	"context"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

type integrationConversation struct {
	integrationID uuid.UUID
	address       integrationstore.ConversationAddress
}

func lockIntegrationConversationsTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID uuid.UUID,
	origins ...AgentInputOrigin,
) error {
	conversations := make([]integrationConversation, 0, len(origins))
	for _, origin := range origins {
		conversations = append(conversations, integrationConversation{origin.IntegrationID, origin.Address})
	}
	slices.SortFunc(conversations, func(a, b integrationConversation) int {
		if order := slices.Compare(a.integrationID[:], b.integrationID[:]); order != 0 {
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
			conversation.integrationID,
			conversation.address,
		); err != nil {
			return err
		}
	}
	return nil
}
