package executionstore

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func validateInboxSubscriptionTx(ctx context.Context, tx pgx.Tx, recipient InboxInputRecipient) error {
	q := dbsqlc.New(tx)
	for _, address := range recipient.Subscription.Alternatives {
		allowed, err := q.HasIntegrationSubscription(ctx, dbsqlc.HasIntegrationSubscriptionParams{
			ProjectID: recipient.Input.ProjectID, AgentID: recipient.AgentID,
			IntegrationID: recipient.Input.Origin.IntegrationID,
			ScopeKind:     address.Kind, ScopeRef: address.Ref,
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
