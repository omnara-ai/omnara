package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func (s *Store) GetInteractionSelection(
	ctx context.Context,
	projectID, agentID uuid.UUID,
) (InteractionSelection, error) {
	row, err := s.q.GetInteractionSelection(
		ctx,
		dbsqlc.GetInteractionSelectionParams{ProjectID: projectID, AgentID: agentID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return InteractionSelection{}, storeerr.ErrNotFound
	}
	return interactionSelectionFromRow(row), err
}

// SelectInteractionDestinationForOriginTx runs only after deduplication of a new
// explicit-origin input, under the agent lock. Replays skip it; originless inputs
// preserve selection. Reads never acquire an earlier app lifecycle gate.
func (s *Store) SelectInteractionDestinationForOriginTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID, originTargetID uuid.UUID,
) (InteractionSelection, error) {
	q := dbsqlc.New(tx)
	if originTargetID == uuid.Nil {
		row, err := q.GetInteractionSelection(
			ctx,
			dbsqlc.GetInteractionSelectionParams{ProjectID: projectID, AgentID: agentID},
		)
		return interactionSelectionFromRow(row), err
	}
	_, handlers, err := loadInteractionHandlers(ctx, q, projectID, agentID)
	if err != nil {
		return InteractionSelection{}, err
	}
	target, err := q.GetInteractionDestinationTarget(
		ctx,
		dbsqlc.GetInteractionDestinationTargetParams{
			ProjectID: projectID,
			AgentID:   agentID,
			TargetID:  originTargetID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return InteractionSelection{}, storeerr.ErrNotFound
	}
	if err != nil {
		return InteractionSelection{}, err
	}
	var selection InteractionSelection
	matches := 0
	for key, handler := range handlers {
		if target.AppState != string(integrationstore.ProjectAppStateActive) ||
			handler.appID != target.AppID ||
			handler.definition.Provider != target.Provider {
			continue
		}
		args, ok := interactionArgsForOrigin(
			*handler.definition.InteractionHandler,
			handler.prepared.Config,
			integrationstore.ConversationAddress{
				Kind: target.ProviderRefKind,
				Ref:  target.ProviderRef,
			},
		)
		if !ok {
			continue
		}
		matches++
		selection = InteractionSelection{
			IntegrationTargetID: originTargetID,
			HandlerKey:          key,
			Args:                args,
		}
	}
	if matches != 1 {
		selection = InteractionSelection{}
	}
	return selection, writeInteractionSelection(ctx, q, projectID, agentID, selection)
}

// ReconcileInteractionSelectionTx affects future prompts only. Captured prompts retain their
// snapshot and separately recheck effective config and live app authority.
func (s *Store) ReconcileInteractionSelectionTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID uuid.UUID,
) (InteractionSelection, error) {
	q := dbsqlc.New(tx)
	selection, handlers, err := loadInteractionHandlers(ctx, q, projectID, agentID)
	if err != nil || selection.HandlerKey == "" {
		return selection, err
	}
	destination, err := selectedInteractionDestination(
		ctx,
		q,
		projectID,
		agentID,
		selection,
		handlers,
	)
	if err != nil || destination != nil {
		return selection, err
	}
	return InteractionSelection{}, writeInteractionSelection(
		ctx,
		q,
		projectID,
		agentID,
		InteractionSelection{},
	)
}

func (s *Store) ListInteractionHandlers(
	ctx context.Context,
	projectID, agentID uuid.UUID,
	cursor string,
	limit int,
) (agentconfig.InteractionHandlerPage, error) {
	tx, err := s.pool.BeginTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
	)
	if err != nil {
		return agentconfig.InteractionHandlerPage{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	return listInteractionHandlers(ctx, dbsqlc.New(tx), projectID, agentID, cursor, limit)
}

// GetSelectedInteractionDestination resolves current routing for runtime messages.
// Prompt presentation must use its immutable captured destination instead.
func (s *Store) GetSelectedInteractionDestination(
	ctx context.Context,
	projectID, agentID uuid.UUID,
) (*InteractionDestination, error) {
	tx, err := s.pool.BeginTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	selection, handlers, err := loadInteractionHandlers(ctx, q, projectID, agentID)
	if err != nil {
		return nil, err
	}
	return selectedInteractionDestination(ctx, q, projectID, agentID, selection, handlers)
}

// ListInteractionHandlers is advisory; selection revalidates authority under the agent lock.
func (r *ToolCallReader) ListInteractionHandlers(
	ctx context.Context,
	cursor string,
	limit int,
) (agentconfig.InteractionHandlerPage, error) {
	t := r.transaction
	return listInteractionHandlers(ctx, t.q, t.input.ProjectID, t.input.AgentID, cursor, limit)
}

func listInteractionHandlers(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID uuid.UUID,
	cursor string,
	limit int,
) (agentconfig.InteractionHandlerPage, error) {
	// Reject invalid bounds/cursors before reading app metadata.
	if _, err := agentconfig.ListInteractionHandlers(nil, nil, cursor, limit); err != nil {
		return agentconfig.InteractionHandlerPage{}, storeerr.InvalidRequest(err)
	}
	selection, handlers, err := loadInteractionHandlers(ctx, q, projectID, agentID)
	if err != nil {
		return agentconfig.InteractionHandlerPage{}, err
	}
	prepared := make(map[string]agentconfig.PreparedAppInteractionHandler, len(handlers))
	for key, handler := range handlers {
		prepared[key] = handler.prepared
	}
	var current *agentconfig.HandlerSelection
	destination, err := selectedInteractionDestination(
		ctx,
		q,
		projectID,
		agentID,
		selection,
		handlers,
	)
	if err != nil {
		return agentconfig.InteractionHandlerPage{}, err
	}
	if destination != nil {
		handler := handlers[destination.HandlerKey]
		scope, err := handler.definition.InteractionHandler.ResolveArgs(
			destination.Config,
			destination.Args,
		)
		if err != nil {
			return agentconfig.InteractionHandlerPage{}, err
		}
		current = &agentconfig.HandlerSelection{
			Handler:     destination.HandlerKey,
			AppID:       handler.prepared.AppID,
			Args:        destination.Args,
			Destination: scope,
		}
	}
	return agentconfig.ListInteractionHandlers(prepared, current, cursor, limit)
}

// SetInteractionHandlerForToolCall resolves the saved model-call config before
// locking. Project/app/conversation gates precede the agent lock. Current config
// is then compared under that lock, so a pending call cannot gain changed hidden
// settings or a replacement app through a reused handler name.
func SetInteractionHandlerForToolCall(
	selection InteractionSelection,
	completion ToolCallCompletionInput,
) ToolCallCommand {
	return toolCallCommandFunc(func(ctx context.Context, t *toolCallTransaction) (any, error) {
		selected := selection
		if selected.IntegrationTargetID != uuid.Nil {
			return nil, storeerr.InvalidRequest(
				errors.New("handler selection takes args, not a target ID"),
			)
		}
		reader := &ToolCallReader{transaction: t}
		call, err := reader.GetToolCall(ctx)
		if err != nil {
			return nil, err
		}
		original, _, err := reader.RuntimeContract(ctx, call.ModelCallContextID)
		if err != nil {
			return nil, err
		}
		var handler resolvedInteractionHandler
		var address integrationstore.ConversationAddress
		if selected.HandlerKey != "" {
			capability, found := original.InteractionHandlers[selected.HandlerKey]
			if !found {
				return nil, storeerr.ErrUnauthorized
			}
			project, err := loadProjectTx(ctx, t.q, t.input.ProjectID)
			if err != nil {
				return nil, err
			}
			if err := lifecyclelock.EnterActiveProject(
				ctx,
				t.tx,
				project.OrgID,
				t.input.ProjectID,
			); err != nil {
				return nil, err
			}
			if err := integrationstore.LockAppsTx(
				ctx,
				t.tx,
				t.input.ProjectID,
				[]string{capability.AppID},
			); err != nil {
				return nil, err
			}
			handlers, err := prepareInteractionHandlers(
				ctx,
				t.q,
				t.input.ProjectID,
				map[string]agentconfig.AppCapabilityCompiled{selected.HandlerKey: capability},
			)
			if err != nil {
				return nil, err
			}
			var ok bool
			handler, ok = handlers[selected.HandlerKey]
			if !ok {
				return nil, storeerr.ErrUnauthorized
			}
			if err := validateInteractionObject(
				selected.Args,
				InteractionDestinationMaxBytes,
			); err != nil {
				return nil, storeerr.InvalidRequest(err)
			}
			scope, err := handler.definition.InteractionHandler.ResolveArgs(
				handler.prepared.Config,
				selected.Args,
			)
			if err != nil {
				return nil, storeerr.InvalidRequest(err)
			}
			kind, ref, err := scope.Conversation()
			if err != nil {
				return nil, storeerr.InvalidRequest(err)
			}
			address = integrationstore.ConversationAddress{Kind: kind, Ref: ref}
			selected.Args, err = jsoncanonical.Normalize(selected.Args)
			if err != nil {
				return nil, storeerr.InvalidRequest(err)
			}
			if err := integrationstore.LockConversationTx(
				ctx,
				t.tx,
				t.input.ProjectID,
				handler.appID,
				address,
			); err != nil {
				return nil, err
			}
		} else {
			if len(selected.Args) > 0 &&
				!jsoncanonical.Equal(selected.Args, json.RawMessage(`{}`)) {
				return nil, storeerr.InvalidRequest(
					errors.New("dashboard-only selection requires empty args"),
				)
			}
			selected = InteractionSelection{}
		}
		if err := t.lockForMutation(ctx); err != nil {
			return nil, err
		}
		_, current, err := loadInteractionContract(ctx, t.q, t.input.ProjectID, t.input.AgentID)
		if err != nil {
			return nil, err
		}
		if !interactionSelectionToolAuthorized(original, current) {
			return nil, storeerr.ErrUnauthorized
		}
		if selected.HandlerKey != "" {
			// Pure authority comparison uses only the app already gated above. Do not
			// acquire another app gate if activation replaced the handler's app ID.
			apps := map[string]agentconfig.AppResolution{
				handler.prepared.AppID: {
					AppID:      handler.prepared.AppID,
					Definition: handler.definition.ID,
				},
			}
			_, err := agentconfig.ResolveInteractionHandlerAuthority(
				original,
				current, selected.HandlerKey, apps)
			if err != nil {
				return nil, storeerr.ErrUnauthorized
			}
			target, err := t.store.integrations.EnsureConversationTargetTx(
				ctx,
				t.tx,
				integrationstore.EnsureConversationTargetInput{
					ProjectID: t.input.ProjectID,
					AgentID:   t.input.AgentID,
					AppID:     handler.appID,
					Address:   address,
					Role:      integrationstore.TargetAttribution,
				},
			)
			if err != nil {
				return nil, err
			}
			selected.IntegrationTargetID = target.ID
		}
		if err := writeInteractionSelection(
			ctx,
			t.q,
			t.input.ProjectID,
			t.input.AgentID,
			selected,
		); err != nil {
			return nil, err
		}
		if _, err := t.completeToolCall(ctx, completion); err != nil {
			return nil, err
		}
		return selected, nil
	})
}

func interactionSelectionToolAuthorized(original, current agentconfig.RuntimeContract) bool {
	for _, before := range original.Tools {
		if before.Name != toolcatalog.ToolNameSetInteractionHandler {
			continue
		}
		for _, after := range current.Tools {
			if after.Name == before.Name {
				return after.Permission.Mode != toolpermission.ModeAlwaysDeny &&
					reflect.DeepEqual(before.Permission, after.Permission)
			}
		}
	}
	return false
}

func interactionSelectionFromRow(row dbsqlc.GetInteractionSelectionRow) InteractionSelection {
	var args json.RawMessage
	if row.HandlerArgs != nil {
		args = *row.HandlerArgs
	}
	return InteractionSelection{
		IntegrationTargetID: storeutil.IDFromPtr(row.IntegrationTargetID),
		HandlerKey:          row.HandlerKey,
		Args:                args,
	}
}

func loadInteractionContract(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID uuid.UUID,
) (InteractionSelection, agentconfig.RuntimeContract, error) {
	row, err := q.GetInteractionSelection(
		ctx,
		dbsqlc.GetInteractionSelectionParams{ProjectID: projectID, AgentID: agentID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return InteractionSelection{}, agentconfig.RuntimeContract{}, storeerr.ErrNotFound
	}
	if err != nil {
		return InteractionSelection{}, agentconfig.RuntimeContract{}, err
	}
	config, err := loadAgentConfigTx(ctx, q, projectID, row.CurrentConfigID)
	if err != nil {
		return InteractionSelection{}, agentconfig.RuntimeContract{}, err
	}
	contract, err := launchableRuntimeContract(config)
	return interactionSelectionFromRow(row), contract, err
}

type resolvedInteractionHandler struct {
	appID      uuid.UUID
	definition appdefinition.Definition
	prepared   agentconfig.PreparedAppInteractionHandler
}

func loadInteractionHandlers(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID uuid.UUID,
) (InteractionSelection, map[string]resolvedInteractionHandler, error) {
	selection, contract, err := loadInteractionContract(ctx, q, projectID, agentID)
	if err != nil {
		return InteractionSelection{}, nil, err
	}
	handlers, err := prepareInteractionHandlers(ctx, q, projectID, contract.InteractionHandlers)
	return selection, handlers, err
}

// Read each distinct app once. Missing/disconnected apps omit only their own
// capability; database failures still propagate rather than hiding outages.
func prepareInteractionHandlers(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID uuid.UUID,
	configured map[string]agentconfig.AppCapabilityCompiled,
) (map[string]resolvedInteractionHandler, error) {
	apps := map[uuid.UUID]dbsqlc.ProjectApp{}
	result := map[string]resolvedInteractionHandler{}
	for key, capability := range configured {
		id, err := publicid.Decode(publicid.KindProjectApp, capability.AppID)
		if err != nil {
			continue
		}
		app, loaded := apps[id]
		if !loaded {
			app, err = q.GetProjectApp(
				ctx,
				dbsqlc.GetProjectAppParams{ProjectID: projectID, ID: id},
			)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
			apps[id] = app
		}
		if app.State != string(integrationstore.ProjectAppStateActive) {
			continue
		}
		definition, ok := appdefinition.Lookup(app.DefinitionID)
		if !ok || definition.Provider != app.Provider || definition.InteractionHandler == nil {
			continue
		}
		prepared, err := definition.InteractionHandler.Prepare(capability.Config)
		if err != nil {
			continue
		}
		result[key] = resolvedInteractionHandler{
			appID:      id,
			definition: definition,
			prepared: agentconfig.PreparedAppInteractionHandler{
				AppID:                      capability.AppID,
				PreparedInteractionHandler: prepared,
			},
		}
	}
	return result, nil
}

func writeInteractionSelection(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID uuid.UUID,
	selection InteractionSelection,
) error {
	var args *json.RawMessage
	if selection.HandlerKey != "" {
		args = &selection.Args
	}
	changed, err := q.SetInteractionSelection(ctx, dbsqlc.SetInteractionSelectionParams{
		ProjectID:   projectID,
		AgentID:     agentID,
		TargetID:    storeutil.IDFromNil(selection.IntegrationTargetID),
		HandlerKey:  storeutil.TextFromEmpty(selection.HandlerKey),
		HandlerArgs: args,
	})
	if err != nil {
		return err
	}
	if changed != 1 {
		return storeerr.ErrStateTransitionConflict
	}
	return nil
}

func selectedInteractionDestination(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID uuid.UUID,
	selection InteractionSelection,
	handlers map[string]resolvedInteractionHandler,
) (*InteractionDestination, error) {
	handler, ok := handlers[selection.HandlerKey]
	if !ok || selection.IntegrationTargetID == uuid.Nil {
		return nil, nil //nolint:nilnil // Unavailable selection means dashboard only.
	}
	target, err := q.GetInteractionDestinationTarget(
		ctx,
		dbsqlc.GetInteractionDestinationTargetParams{
			ProjectID: projectID,
			AgentID:   agentID,
			TargetID:  selection.IntegrationTargetID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil //nolint:nilnil // Removed target.
	}
	if err != nil {
		return nil, err
	}
	if target.AppID != handler.appID ||
		target.AppState != string(integrationstore.ProjectAppStateActive) ||
		target.Provider != handler.definition.Provider {
		return nil, nil //nolint:nilnil // Revoked app.
	}
	destination := &InteractionDestination{
		HandlerKey:          selection.HandlerKey,
		AppID:               handler.appID,
		HandlerDefinition:   handler.definition.ID,
		Config:              handler.prepared.Config,
		Args:                selection.Args,
		IntegrationTargetID: target.ID,
		Address: integrationstore.ConversationAddress{
			Kind: target.ProviderRefKind,
			Ref:  target.ProviderRef,
		},
	}
	if destination.validate() != nil {
		return nil, nil //nolint:nilerr,nilnil // Invalidated selection falls back to dashboard presentation.
	}
	return destination, nil
}

// Capture is read-only under the existing agent lock. It must not acquire an
// earlier app gate; presentation and hosted callbacks recheck live authority.
func captureInteractionDestinationTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID uuid.UUID,
) (json.RawMessage, error) {
	selection, handlers, err := loadInteractionHandlers(ctx, q, projectID, agentID)
	if err != nil {
		return nil, err
	}
	destination, err := selectedInteractionDestination(
		ctx,
		q,
		projectID,
		agentID,
		selection,
		handlers,
	)
	if err != nil || destination == nil {
		return nil, err
	}
	raw, err := json.Marshal(destination)
	if err != nil {
		return nil, err
	}
	return raw, validateInteractionObject(raw, InteractionDestinationMaxBytes)
}

type ResolveAgentInteractionFromHandlerInput struct {
	ResolveAgentInteractionInput
	AppID             uuid.UUID
	HandlerDefinition string
	Address           integrationstore.ConversationAddress
	// The verified app revision is fenced with the actual resolution.
	SourceSetupRevision int64
}

// GetInteractionCallbackAppID identifies the setup that must verify a callback.
// This is not authorization; resolution rechecks the captured handler and origin.
func (s *Store) GetInteractionCallbackAppID(ctx context.Context, id uuid.UUID) (uuid.UUID, error) {
	appID, err := s.q.GetInteractionCallbackAppID(ctx, dbsqlc.GetInteractionCallbackAppIDParams{ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, storeerr.ErrNotFound
	}
	return appID, err
}

// GetInteractionForHandlerCallback locates an immutable captured interaction
// within a verified app. The result is identity only; resolution still
// goes through ResolveAgentInteractionFromHandler's fenced authority check.
func (s *Store) GetInteractionForHandlerCallback(
	ctx context.Context, projectID, appID, id uuid.UUID,
) (AgentInteractionRecord, error) {
	agentID, err := s.q.GetInteractionCallbackAgent(ctx, dbsqlc.GetInteractionCallbackAgentParams{
		ProjectID: projectID, InteractionID: id, AppID: appID.String(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentInteractionRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	record, found, err := s.GetAgentInteraction(ctx, projectID, agentID, id)
	if err == nil && !found {
		err = storeerr.ErrNotFound
	}
	return record, err
}

// ResolveAgentInteractionFromHandler accepts a provider-verified conversation
// participant. The caller verifies callback authenticity/address, not an
// approver ACL. Project-authorized dashboard/API resolution uses the existing
// ResolveAgentInteraction method and does not depend on mirror authority.
func (s *Store) ResolveAgentInteractionFromHandler(
	ctx context.Context, input ResolveAgentInteractionFromHandlerInput,
) (AgentInteractionRecord, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, destination, err := lockAuthorizedInteractionDestination(
		ctx,
		tx,
		input.ProjectID,
		input.AgentID,
		input.ID,
	)
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	if input.SourceSetupRevision != 0 {
		app, err := dbsqlc.New(tx).GetProjectApp(ctx, dbsqlc.GetProjectAppParams{
			ProjectID: input.ProjectID, ID: destination.AppID,
		})
		if err != nil {
			return AgentInteractionRecord{}, err
		}
		if app.SetupRevision != input.SourceSetupRevision {
			return AgentInteractionRecord{}, storeerr.ErrUnauthorized
		}
	}
	if destination.AppID != input.AppID ||
		destination.HandlerDefinition != input.HandlerDefinition ||
		destination.Address != input.Address ||
		(input.IntegrationTargetID != uuid.Nil && input.IntegrationTargetID != destination.IntegrationTargetID) {
		return AgentInteractionRecord{}, storeerr.ErrUnauthorized
	}
	input.IntegrationTargetID = destination.IntegrationTargetID
	notifications := s.newTxNotifications()
	record, err := resolveAgentInteractionTx(
		ctx,
		notifications,
		tx,
		dbsqlc.New(tx),
		input.ResolveAgentInteractionInput,
	)
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	if err := s.commitTxWithNotifications(
		ctx,
		tx,
		notifications,
		"resolve interaction from handler",
	); err != nil {
		return AgentInteractionRecord{}, err
	}
	return record, nil
}

// GetAgentInteractionForPresentation revalidates the captured destination, never
// the mutable current selection. Call immediately before provider I/O and inspect
// State: only open interactions may be posted; terminal receipts may be dismissed.
// The returned snapshot cannot reserve provider authority across a network call.
func (s *Store) GetAgentInteractionForPresentation(
	ctx context.Context, projectID, agentID, id uuid.UUID,
) (AgentInteractionRecord, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	record, _, err := lockAuthorizedInteractionDestination(ctx, tx, projectID, agentID, id)
	return record, err
}

func lockAuthorizedInteractionDestination(
	ctx context.Context, tx pgx.Tx, projectID, agentID, id uuid.UUID,
) (AgentInteractionRecord, *InteractionDestination, error) {
	q := dbsqlc.New(tx)
	params := dbsqlc.GetAgentInteractionParams{ProjectID: projectID, AgentID: agentID, ID: id}
	row, err := q.GetAgentInteraction(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentInteractionRecord{}, nil, storeerr.ErrNotFound
	}
	if err != nil {
		return AgentInteractionRecord{}, nil, err
	}
	destination, err := agentInteractionRecordFromSQLC(row).CapturedDestination()
	if err != nil {
		return AgentInteractionRecord{}, nil, err
	}
	if destination == nil {
		return AgentInteractionRecord{}, nil, storeerr.ErrUnauthorized
	}
	project, err := loadProjectTx(ctx, q, projectID)
	if err != nil {
		return AgentInteractionRecord{}, nil, err
	}
	if err := lifecyclelock.EnterActiveProject(ctx, tx, project.OrgID, projectID); err != nil {
		return AgentInteractionRecord{}, nil, err
	}
	// The snapshot is immutable, so its app can be gated before the
	// agent lock without acquiring additional gates after a config change.
	if err := q.LockProjectAppLifecycleShared(ctx, dbsqlc.LockProjectAppLifecycleSharedParams{
		AppID: destination.AppID,
	}); err != nil {
		return AgentInteractionRecord{}, nil, err
	}
	if _, err := q.LockAgentInProject(ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: projectID, ID: agentID,
	}); err != nil {
		return AgentInteractionRecord{}, nil, err
	}
	_, handlers, err := loadInteractionHandlers(ctx, q, projectID, agentID)
	if err != nil {
		return AgentInteractionRecord{}, nil, err
	}
	selection := InteractionSelection{
		IntegrationTargetID: destination.IntegrationTargetID,
		HandlerKey:          destination.HandlerKey,
		Args:                destination.Args,
	}
	current, err := selectedInteractionDestination(ctx, q, projectID, agentID, selection, handlers)
	if err != nil {
		return AgentInteractionRecord{}, nil, err
	}
	if current == nil || !sameInteractionDestination(*current, *destination) {
		return AgentInteractionRecord{}, nil, storeerr.ErrUnauthorized
	}
	row, err = q.GetAgentInteraction(ctx, params)
	return agentInteractionRecordFromSQLC(row), destination, err
}

type RecordInteractionPresentationReceiptInput struct {
	ProjectID, AgentID, ID uuid.UUID
	Destination            InteractionDestination
	Receipt                json.RawMessage
}

// RecordInteractionPresentationReceipt stores one confirmed send, including a
// late confirmation after cancellation or revocation. It grants no authority and
// changes no lifecycle fields. Identical replays succeed; replacement conflicts.
// Presentation attempts/retries belong to the presenter, not this receipt.
func (s *Store) RecordInteractionPresentationReceipt(
	ctx context.Context, input RecordInteractionPresentationReceiptInput,
) (AgentInteractionRecord, error) {
	if err := input.Destination.validate(); err != nil {
		return AgentInteractionRecord{}, storeerr.InvalidRequest(err)
	}
	if err := validateInteractionObject(input.Receipt, InteractionReceiptMaxBytes); err != nil {
		return AgentInteractionRecord{}, storeerr.InvalidRequest(err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	if _, err := q.LockAgentInProject(ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: input.ProjectID, ID: input.AgentID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AgentInteractionRecord{}, storeerr.ErrNotFound
		}
		return AgentInteractionRecord{}, err
	}
	params := dbsqlc.GetAgentInteractionParams{
		ProjectID: input.ProjectID,
		AgentID:   input.AgentID,
		ID:        input.ID,
	}
	row, err := q.GetAgentInteraction(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentInteractionRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	record := agentInteractionRecordFromSQLC(row)
	destination, err := record.CapturedDestination()
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	if destination == nil || !sameInteractionDestination(*destination, input.Destination) {
		return AgentInteractionRecord{}, storeerr.ErrUnauthorized
	}
	if len(record.PresentationReceipt) != 0 {
		if !sameJSON(record.PresentationReceipt, input.Receipt) {
			return AgentInteractionRecord{}, storeerr.ErrIdempotencyConflict
		}
		return record, nil
	}
	receiptParams := dbsqlc.RecordAgentInteractionPresentationReceiptParams{
		ProjectID: input.ProjectID, AgentID: input.AgentID, ID: input.ID,
		Destination: record.Destination, Receipt: input.Receipt,
	}
	changed, err := q.RecordAgentInteractionPresentationReceipt(ctx, receiptParams)
	if err != nil {
		return AgentInteractionRecord{}, fmt.Errorf(
			"record interaction presentation receipt: %w",
			err,
		)
	}
	if changed != 1 {
		return AgentInteractionRecord{}, storeerr.ErrIdempotencyConflict
	}
	row, err = q.GetAgentInteraction(ctx, params)
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AgentInteractionRecord{}, err
	}
	return agentInteractionRecordFromSQLC(row), nil
}
