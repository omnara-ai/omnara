package executionstore

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// Caller holds the agent lifecycle gate, so config activation cannot revoke a
// subscription between this check and input admission. No earlier lock class is
// entered here. Unrelated config edits may retain/re-authorize the same listener.
func validateInboxListenerTx(ctx context.Context, tx pgx.Tx, slot InboxInputSlot) error {
	q := dbsqlc.New(tx)
	agent, err := q.GetAgentInProject(
		ctx,
		dbsqlc.GetAgentInProjectParams{ProjectID: slot.Input.ProjectID, ID: slot.AgentID},
	)
	if err != nil {
		return err
	}
	listeners, err := q.ListActiveAgentListeners(
		ctx,
		dbsqlc.ListActiveAgentListenersParams{ProjectID: slot.Input.ProjectID, AgentID: slot.AgentID},
	)
	if err != nil {
		return err
	}
	for _, listener := range listeners {
		if listener.SourceConfigID != agent.CurrentConfigID ||
			listener.ConnectionID != slot.Input.Origin.ConnectionID ||
			!slices.Contains(listener.Events, slot.Listener.Event) {
			continue
		}
		// Another post can refresh a follow's tool-call provenance without
		// changing its receive authority. Distinguish followed and declared
		// listeners, but do not pin admission to a particular post.
		for _, reference := range slot.Listener.Alternatives {
			if reference.ResourceKey == listener.ResourceKey && reference.Address.Kind == listener.ScopeKind &&
				reference.Address.Ref == listener.ScopeRef &&
				reference.Followed == (listener.ToolCallID != nil) {
				return nil
			}
		}
	}
	return storeerr.ErrUnauthorized
}
