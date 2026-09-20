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
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type ListenerOrigin string

const (
	ListenerConfigured ListenerOrigin = "configured"
	ListenerRuntime    ListenerOrigin = "runtime"
)

// LockAppsTx precedes profile, machine-source and agent locks. Include the
// receipt's app in the same sorted set when admitting an inbox event. Compiled
// references remain valid metadata after disconnection/deletion; only additional
// apps authorize the current operation and must be active.
func LockAppsTx(ctx context.Context, tx pgx.Tx, projectID uuid.UUID, refs []string, additional ...uuid.UUID) error {
	referenced := make([]uuid.UUID, 0, len(refs))
	for _, ref := range refs {
		id, err := publicid.Decode(publicid.KindProjectApp, ref)
		if err != nil {
			return storeerr.InvalidRequest(err)
		}
		referenced = append(referenced, id)
	}
	return lockProjectAppsTx(ctx, tx, projectID, referenced, additional)
}

// Both compiled references and inbox admission use this sorted gate set. Only
// required apps authorize the operation; references may be disconnected/deleted.
func lockProjectAppsTx(
	ctx context.Context, tx pgx.Tx, projectID uuid.UUID, referenced, required []uuid.UUID,
) error {
	ids := make(map[uuid.UUID]bool, len(referenced)+len(required))
	for _, id := range referenced {
		if id != uuid.Nil {
			ids[id] = false
		}
	}
	for _, id := range required {
		if id != uuid.Nil {
			ids[id] = true
		}
	}
	ordered := slices.Collect(maps.Keys(ids))
	slices.SortFunc(ordered, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	q := dbsqlc.New(tx)
	for _, id := range ordered {
		if err := q.LockProjectAppLifecycleShared(ctx, dbsqlc.LockProjectAppLifecycleSharedParams{AppID: id}); err != nil {
			return err
		}
	}
	if len(ordered) == 0 {
		return nil
	}
	apps, err := q.ListProjectAppMetadataByIDs(ctx, dbsqlc.ListProjectAppMetadataByIDsParams{
		ProjectID: projectID, Ids: ordered,
	})
	if err != nil {
		return err
	}
	if len(apps) != len(ordered) {
		return storeerr.ErrNotFound // Missing and foreign-project IDs grant no authority.
	}
	for _, app := range apps {
		if !ids[app.ID] {
			continue
		}
		if app.DeletedAt != nil {
			return storeerr.ErrNotFound
		}
		if app.State != string(ProjectAppStateActive) {
			return storeerr.ErrUnauthorized
		}
	}
	return nil
}

type ReconcileAgentListenersInput struct {
	OrgID, ProjectID, AgentID, ConfigID uuid.UUID
	Next                                map[string]agentconfig.AppCapabilityCompiled
}

// ReconcileAgentListenersTx runs in config activation after app and agent
// locks. The listener, never a sending tool, owns all of its subscriptions.
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
		capability := input.Next[key]
		appID, prepared, err := prepareListenerTx(ctx, q, input.ProjectID, key, capability, false)
		if err != nil {
			return err
		}
		if appID == uuid.Nil {
			continue // A tombstoned app cannot materialize or retain subscriptions.
		}
		write := func(address ConversationAddress, origin ListenerOrigin, toolCallID *uuid.UUID) error {
			_, err := q.UpsertAgentListener(ctx, dbsqlc.UpsertAgentListenerParams{
				ProjectID: input.ProjectID, AgentID: input.AgentID, AppID: appID, ListenerKey: key,
				ScopeKind: address.Kind, ScopeRef: address.Ref, Events: prepared.Events, SourceConfigID: input.ConfigID,
				Origin: string(origin), ToolCallID: toolCallID,
			})
			return err
		}
		for _, conversation := range prepared.Conversations {
			kind, ref, err := conversation.Conversation()
			if err != nil {
				return err
			}
			if err := write(ConversationAddress{Kind: kind, Ref: ref}, ListenerConfigured, nil); err != nil {
				return fmt.Errorf("materialize listener %q: %w", key, err)
			}
		}
		for _, prior := range existing {
			// Changing a listener's initial list does not discard runtime follows.
			// Its current event selection applies to both origins.
			if prior.Origin != string(ListenerRuntime) || prior.ListenerKey != key || prior.AppID != appID {
				continue
			}
			if err := write(
				ConversationAddress{Kind: prior.ScopeKind, Ref: prior.ScopeRef},
				ListenerRuntime,
				prior.ToolCallID,
			); err != nil {
				return fmt.Errorf("retain listener %q subscription: %w", key, err)
			}
		}
	}
	return checkAgentListenerLimit(ctx, q, input.OrgID, input.ProjectID, input.AgentID)
}

// A nil ID without error means a configured listener's app is tombstoned.
func prepareListenerTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID uuid.UUID,
	key string,
	capability agentconfig.AppCapabilityCompiled,
	requireActive bool,
) (uuid.UUID, appdefinition.PreparedListener, error) {
	name, listener, ok := toolcatalog.SplitAppListenerName(key)
	if !ok {
		return uuid.Nil, appdefinition.PreparedListener{}, storeerr.InvalidRequest(fmt.Errorf("invalid listener key %q", key))
	}
	id, err := publicid.Decode(publicid.KindProjectApp, capability.AppID)
	if err != nil {
		return uuid.Nil, appdefinition.PreparedListener{}, err
	}
	apps, err := q.ListProjectAppMetadataByIDs(ctx, dbsqlc.ListProjectAppMetadataByIDsParams{
		ProjectID: projectID, Ids: []uuid.UUID{id},
	})
	if err != nil {
		return uuid.Nil, appdefinition.PreparedListener{}, err
	}
	if len(apps) != 1 {
		return uuid.Nil, appdefinition.PreparedListener{}, storeerr.ErrNotFound
	}
	app := apps[0]
	if app.Name != name {
		return uuid.Nil, appdefinition.PreparedListener{}, storeerr.ErrUnauthorized
	}
	if app.DeletedAt != nil {
		if requireActive {
			return uuid.Nil, appdefinition.PreparedListener{}, storeerr.ErrNotFound
		}
		return uuid.Nil, appdefinition.PreparedListener{}, nil
	}
	if requireActive && app.State != string(ProjectAppStateActive) {
		return uuid.Nil, appdefinition.PreparedListener{}, storeerr.ErrUnauthorized
	}
	definition, ok := appdefinition.Lookup(app.DefinitionID)
	if !ok {
		return uuid.Nil, appdefinition.PreparedListener{}, storeerr.ErrUnauthorized
	}
	impl, ok := definition.Listeners[listener]
	if !ok {
		return uuid.Nil, appdefinition.PreparedListener{}, storeerr.InvalidRequest(
			fmt.Errorf("app does not export listener %q", listener),
		)
	}
	prepared, err := impl.Prepare(capability.Config)
	return id, prepared, err
}

func checkAgentListenerLimit(ctx context.Context, q *dbsqlc.Queries, orgID, projectID, agentID uuid.UUID) error {
	limits, err := resourceguard.ResolveLimits(ctx, q, orgID)
	if err != nil {
		return err
	}
	count, err := q.CountActiveAgentListeners(
		ctx,
		dbsqlc.CountActiveAgentListenersParams{ProjectID: projectID, AgentID: agentID},
	)
	if err != nil {
		return err
	}
	if count > limits.MaxActiveAppListenersPerAgent {
		return fmt.Errorf("app listeners limit of %d reached: %w", limits.MaxActiveAppListenersPerAgent, storeerr.ErrConflict)
	}
	return nil
}

// RegisterRuntimeListenerInput describes a conversation under an existing configured listener.
type RegisterRuntimeListenerInput struct {
	OrgID, ProjectID, AgentID, ConfigID, AppID uuid.UUID
	ListenerKey                                string
	Capability                                 agentconfig.AppCapabilityCompiled
	Address                                    ConversationAddress
	ToolCallID                                 *uuid.UUID
}

// RegisterRuntimeListenerTx adds a conversation under an existing configured
// listener. The caller holds app/conversation/agent gates and supplies that
// agent's current immutable config; this cannot add a missing listener.
func RegisterRuntimeListenerTx(ctx context.Context, tx pgx.Tx, input RegisterRuntimeListenerInput) error {
	if err := input.Address.Validate(); err != nil {
		return err
	}
	q := dbsqlc.New(tx)
	appID, prepared, err := prepareListenerTx(ctx, q, input.ProjectID, input.ListenerKey, input.Capability, true)
	if err != nil {
		return err
	}
	if appID != input.AppID {
		return storeerr.ErrUnauthorized
	}
	// Recheck the persisted config association at the mutation boundary.
	agent, err := q.GetAgentInProject(ctx, dbsqlc.GetAgentInProjectParams{ProjectID: input.ProjectID, ID: input.AgentID})
	if err != nil {
		return err
	}
	if agent.State != "active" || agent.CurrentConfigID != input.ConfigID {
		return storeerr.ErrStateTransitionConflict
	}
	_, err = q.UpsertAgentListener(ctx, dbsqlc.UpsertAgentListenerParams{
		ProjectID: input.ProjectID, AgentID: input.AgentID, AppID: input.AppID, ListenerKey: input.ListenerKey,
		ScopeKind: input.Address.Kind, ScopeRef: input.Address.Ref, Events: prepared.Events,
		SourceConfigID: input.ConfigID, Origin: string(ListenerRuntime), ToolCallID: input.ToolCallID,
	})
	if err != nil {
		return err
	}
	return checkAgentListenerLimit(ctx, q, input.OrgID, input.ProjectID, input.AgentID)
}
