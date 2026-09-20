package executionstore

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// Caller holds the agent lifecycle gate, so config activation cannot revoke a
// subscription between this check and input admission. No earlier lock class is
// entered here. Unrelated config edits may retain/re-authorize the same listener.
func validateInboxListenerTx(ctx context.Context, tx pgx.Tx, slot InboxInputSlot) error {
	q := dbsqlc.New(tx)
	for _, reference := range slot.Listener.Alternatives {
		allowed, err := q.HasActiveAgentListener(ctx, dbsqlc.HasActiveAgentListenerParams{
			ProjectID: slot.Input.ProjectID, AgentID: slot.AgentID, AppID: slot.Input.Origin.AppID,
			ListenerKey: reference.ListenerKey, ScopeKind: reference.Address.Kind,
			ScopeRef: reference.Address.Ref, Event: slot.Listener.Event,
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
