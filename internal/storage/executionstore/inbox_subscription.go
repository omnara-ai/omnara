package executionstore

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// Caller holds the agent lifecycle gate, so detach cannot remove a subscription
// between this check and input admission. A fresh matching attachment may
// authorize previously frozen work; subscription IDs are not ingress generations.
func validateInboxSubscriptionTx(ctx context.Context, tx pgx.Tx, slot InboxInputSlot) error {
	q := dbsqlc.New(tx)
	for _, reference := range slot.Subscription.Alternatives {
		allowed, err := q.HasAppSubscription(ctx, dbsqlc.HasAppSubscriptionParams{
			ProjectID: slot.Input.ProjectID, AgentID: slot.AgentID, AppID: slot.Input.Origin.AppID,
			SubscriptionType: reference.Type, ScopeKind: reference.Address.Kind,
			ScopeRef: reference.Address.Ref, Event: slot.Subscription.Event,
		})
		if err != nil {
			return err
		}
		if allowed {
			return nil
		}
	}
	return storeerr.ErrUnauthorized
}
