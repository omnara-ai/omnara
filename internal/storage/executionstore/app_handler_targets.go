package executionstore

import (
	"context"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

type appConversation struct {
	appID   uuid.UUID
	address integrationstore.ConversationAddress
}

// Launch locks only concrete origins, after app gates and before agent locks.
// Configured handlers are discoverable without materializing attribution targets.
func lockAppConversationsTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID uuid.UUID,
	origins ...AgentInputOrigin,
) error {
	conversations := make([]appConversation, 0, len(origins))
	for _, origin := range origins {
		conversations = append(conversations, appConversation{origin.AppID, origin.Address})
	}
	slices.SortFunc(conversations, func(a, b appConversation) int {
		if order := slices.Compare(a.appID[:], b.appID[:]); order != 0 {
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
			conversation.appID,
			conversation.address,
		); err != nil {
			return err
		}
	}
	return nil
}
