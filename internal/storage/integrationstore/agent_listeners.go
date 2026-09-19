package integrationstore

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/resourceguard"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// LockAppConnectionsTx validates all concrete connection authorities before the
// caller takes profile, machine-source or agent locks. An inbox admission locks
// the union of these connections and its receipt connection in UUID order before
// locking the receipt. Instance deletion does not revoke already compiled configs.
func LockAppConnectionsTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID uuid.UUID,
	resources map[string]agentconfig.AppResourceCompiled,
	additional ...uuid.UUID,
) error {
	connections := map[uuid.UUID]string{}
	for _, id := range additional {
		if id != uuid.Nil {
			connections[id] = ""
		}
	}
	for _, resource := range resources {
		if !resource.Enabled || resource.ConnectionID == "" {
			continue
		}
		if err := resource.Validate(); err != nil {
			return storeerr.InvalidRequest(err)
		}
		id, err := publicid.Decode(publicid.KindIntegrationConnection, resource.ConnectionID)
		if err != nil {
			return err
		}
		definition, _ := appdefinition.Lookup(resource.Definition)
		if provider, found := connections[id]; found && provider != "" && provider != definition.Provider {
			return storeerr.ErrUnauthorized
		}
		connections[id] = definition.Provider
	}
	ids := slices.Collect(maps.Keys(connections))
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	q := dbsqlc.New(tx)
	for _, id := range ids {
		if err := q.LockIntegrationConnectionLifecycleShared(
			ctx,
			dbsqlc.LockIntegrationConnectionLifecycleSharedParams{ConnectionID: id},
		); err != nil {
			return err
		}
		connection, err := getIntegrationConnection(ctx, q, projectID, id)
		if err != nil {
			return err
		}
		if connection.State != IntegrationConnectionStateActive ||
			(connections[id] != "" && connections[id] != connection.Provider) {
			return storeerr.ErrUnauthorized
		}
	}
	return nil
}

type ReconcileAgentListenersInput struct {
	OrgID, ProjectID, AgentID, ConfigID uuid.UUID
	Previous, Next                      map[string]agentconfig.AppResourceCompiled
}

// ReconcileAgentListenersTx runs inside config activation after connection and
// agent locks. A saved profile alone creates no subscriptions. Unrelated config
// edits retain exact follows; removing their resource authority disables them.
func ReconcileAgentListenersTx(ctx context.Context, tx pgx.Tx, input ReconcileAgentListenersInput) error {
	q := dbsqlc.New(tx)
	existing, err := q.ListActiveAgentListeners(
		ctx,
		dbsqlc.ListActiveAgentListenersParams{ProjectID: input.ProjectID, AgentID: input.AgentID},
	)
	if err != nil {
		return err
	}
	if len(existing) == 0 && len(input.Next) == 0 {
		return nil
	}
	if err := q.DeactivateAgentListeners(
		ctx,
		dbsqlc.DeactivateAgentListenersParams{ProjectID: input.ProjectID, AgentID: input.AgentID},
	); err != nil {
		return err
	}
	for _, key := range slices.Sorted(maps.Keys(input.Next)) {
		resource := input.Next[key]
		if !resource.Enabled || resource.Listener == nil {
			continue
		}
		if err := resource.Validate(); err != nil {
			return storeerr.InvalidRequest(err)
		}
		connectionID, err := publicid.Decode(publicid.KindIntegrationConnection, resource.ConnectionID)
		if err != nil {
			return err
		}
		kind, ref, err := resource.Scope.Conversation()
		if err != nil {
			return err
		}
		if _, err := q.UpsertAgentListener(ctx, dbsqlc.UpsertAgentListenerParams{
			ProjectID:      input.ProjectID,
			AgentID:        input.AgentID,
			ConnectionID:   connectionID,
			ResourceKey:    key,
			ScopeKind:      kind,
			ScopeRef:       ref,
			Events:         resource.Listener.Events,
			SourceConfigID: input.ConfigID,
		}); err != nil {
			return fmt.Errorf("materialize app listener %q: %w", key, err)
		}
	}
	for _, listener := range existing {
		if listener.ToolCallID == nil ||
			!retainsFollow(input.Previous[listener.ResourceKey], input.Next[listener.ResourceKey], listener) {
			continue
		}
		if _, err := q.UpsertAgentListener(ctx, dbsqlc.UpsertAgentListenerParams{
			ProjectID: input.ProjectID, AgentID: input.AgentID, ConnectionID: listener.ConnectionID,
			ResourceKey: listener.ResourceKey, ScopeKind: listener.ScopeKind, ScopeRef: listener.ScopeRef,
			Events: listener.Events, SourceConfigID: input.ConfigID, ToolCallID: listener.ToolCallID,
		}); err != nil {
			return fmt.Errorf("retain app follow: %w", err)
		}
	}
	limits, err := resourceguard.ResolveLimits(ctx, q, input.OrgID)
	if err != nil {
		return err
	}
	count, err := q.CountActiveAgentListeners(
		ctx,
		dbsqlc.CountActiveAgentListenersParams{ProjectID: input.ProjectID, AgentID: input.AgentID},
	)
	if err != nil {
		return err
	}
	if count > limits.MaxActiveAppListenersPerAgent {
		return fmt.Errorf(
			"app listeners limit of %d reached: %w",
			limits.MaxActiveAppListenersPerAgent,
			storeerr.ErrConflict,
		)
	}
	return nil
}

func retainsFollow(previous, next agentconfig.AppResourceCompiled, listener dbsqlc.AgentListener) bool {
	if !previous.Enabled || !next.Enabled || next.Follow == nil || !next.Follow.Replies ||
		previous.Follow == nil || !previous.Follow.Replies ||
		previous.Definition != next.Definition || previous.ConnectionID != next.ConnectionID ||
		previous.Scope == nil || next.Scope == nil {
		return false
	}
	connectionID, err := publicid.Decode(publicid.KindIntegrationConnection, next.ConnectionID)
	if err != nil || connectionID != listener.ConnectionID {
		return false
	}
	// Keeping or widening the prior scope preserves its confirmed follows. When
	// narrowing to a single conversation, preserve only that exact conversation.
	if next.Scope.Contains(*previous.Scope) {
		return true
	}
	kind, ref, err := next.Scope.Conversation()
	return err == nil && kind == listener.ScopeKind && ref == listener.ScopeRef
}
