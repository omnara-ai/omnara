package executionstore

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func validateInboxSubscriptionTx(ctx context.Context, tx pgx.Tx, slot InboxInputSlot) error {
	q := dbsqlc.New(tx)
	for _, reference := range slot.Subscription.Alternatives {
		allowed, err := q.HasIntegrationSubscription(ctx, dbsqlc.HasIntegrationSubscriptionParams{
			ProjectID: slot.Input.ProjectID, AgentID: slot.AgentID, IntegrationID: slot.Input.Origin.IntegrationID,
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
