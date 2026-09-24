package executionstore

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func validateInboxSubscriptionTx(ctx context.Context, tx pgx.Tx, slot InboxInputSlot) error {
	q := dbsqlc.New(tx)
	for _, address := range slot.Subscription.Alternatives {
		allowed, err := q.HasIntegrationSubscription(ctx, dbsqlc.HasIntegrationSubscriptionParams{
			ProjectID: slot.Input.ProjectID, AgentID: slot.AgentID, IntegrationID: slot.Input.Origin.IntegrationID,
			ScopeKind: address.Kind, ScopeRef: address.Ref,
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
