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
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
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
		if target.IntegrationState != string(integrationstore.ProjectIntegrationStateActive) ||
			handler.integrationID != target.IntegrationID {
			continue
		}
		destination, err := resolveInteractionHandlerDestination(ctx, tx, projectID, agentID, key, handler)
		if err != nil {
			return InteractionSelection{}, err
		}
		if destination == nil || destination.IntegrationTargetID != originTargetID {
			continue
		}

		matches++
		selection = InteractionSelection{
			IntegrationTargetID: originTargetID,
			HandlerKey:          key,
		}
	}
	if matches != 1 {
		selection = InteractionSelection{}
	}
	return selection, writeInteractionSelection(ctx, q, projectID, agentID, selection)
}

func (s *Store) ReconcileInteractionSelectionTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID uuid.UUID,
) (InteractionSelection, error) {
	q := dbsqlc.New(tx)
	selection, contract, err := loadInteractionContract(ctx, q, projectID, agentID)
	if err != nil || selection.HandlerKey == "" {
		return selection, err
	}
	destination, err := selectedInteractionDestination(
		ctx,
		tx,
		projectID,
		agentID,
		selection,
		contract,
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
	selection, contract, err := loadInteractionContract(ctx, q, projectID, agentID)
	if err != nil {
		return nil, err
	}
	return selectedInteractionDestination(ctx, tx, projectID, agentID, selection, contract)
}

func (r *ToolCallReader) ListInteractionHandlers(
	ctx context.Context,
	cursor string,
	limit int,
) (agentconfig.InteractionHandlerPage, error) {
	t := r.transaction
	return listInteractionHandlers(ctx, t.tx, t.input.ProjectID, t.input.AgentID, cursor, limit)
}

func listInteractionHandlers(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID uuid.UUID,
	cursor string,
	limit int,
) (agentconfig.InteractionHandlerPage, error) {
	q := dbsqlc.New(tx)
	if _, err := agentconfig.ListInteractionHandlers(nil, nil, cursor, limit); err != nil {
		return agentconfig.InteractionHandlerPage{}, storeerr.InvalidRequest(err)
	}
	selection, handlers, err := loadInteractionHandlers(ctx, q, projectID, agentID)
	if err != nil {
		return agentconfig.InteractionHandlerPage{}, err
	}
	entries := make(map[string]agentconfig.InteractionHandlerEntry, len(handlers))
	var current *agentconfig.HandlerSelection
	for key, handler := range handlers {
		destination, err := resolveInteractionHandlerDestination(ctx, tx, projectID, agentID, key, handler)
		if err != nil {
			return agentconfig.InteractionHandlerPage{}, err
		}
		if destination == nil {
			continue
		}
		scope, err := integrationdefinition.ParseConversation(
			handler.definition.Provider,
			destination.Address.Kind,
			destination.Address.Ref,
		)
		if err != nil {
			return agentconfig.InteractionHandlerPage{}, err
		}
		entries[key] = agentconfig.InteractionHandlerEntry{
			Description: handler.prepared.Description,
			InputSchema: handler.prepared.InputSchema,
			Destination: scope,
		}
		if selection.HandlerKey == key && selection.IntegrationTargetID == destination.IntegrationTargetID {
			integrationID, err := publicid.Encode(publicid.KindProjectIntegration, destination.IntegrationID)
			if err != nil {
				return agentconfig.InteractionHandlerPage{}, err
			}
			current = &agentconfig.HandlerSelection{
				Handler:       key,
				IntegrationID: integrationID,
				Args:          json.RawMessage(`{}`),
				Destination:   scope,
			}
		}
	}
	return agentconfig.ListInteractionHandlers(entries, current, cursor, limit)
}

type SelectInteractionHandlerInput struct {
	HandlerKey string
	Args       json.RawMessage
}

func SetInteractionHandlerForToolCall(
	input SelectInteractionHandlerInput,
	completion ToolCallCompletionInput,
) ToolCallCommand {
	return toolCallCommandFunc(func(ctx context.Context, t *toolCallTransaction) (any, error) {
		selected := InteractionSelection{HandlerKey: input.HandlerKey}
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
			if err := integrationstore.LockIntegrationsTx(
				ctx,
				t.tx,
				t.input.ProjectID,
				[]uuid.UUID{capability.IntegrationID},
			); err != nil {
				return nil, err
			}
			handlers, err := prepareInteractionHandlers(
				ctx,
				t.q,
				t.input.ProjectID,
				map[string]agentconfig.IntegrationCapabilityCompiled{selected.HandlerKey: capability},
			)
			if err != nil {
				return nil, err
			}
			var ok bool
			handler, ok = handlers[selected.HandlerKey]
			if !ok {
				return nil, storeerr.ErrUnauthorized
			}
		} else {
			if len(input.Args) > 0 &&
				!jsoncanonical.Equal(input.Args, json.RawMessage(`{}`)) {
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
			// A replacement handler integration would require an earlier lock class than the held agent lock.
			integrations := map[uuid.UUID]agentconfig.IntegrationResolution{
				handler.prepared.IntegrationID: {
					IntegrationID:   handler.prepared.IntegrationID,
					IntegrationType: handler.definition.IntegrationType,
				},
			}
			_, err := agentconfig.ResolveInteractionHandlerAuthority(
				original,
				current, selected.HandlerKey, integrations)
			if err != nil {
				return nil, storeerr.ErrUnauthorized
			}
			if err := validateInteractionObject(input.Args, InteractionDestinationMaxBytes); err != nil {
				return nil, storeerr.InvalidRequest(err)
			}
			if err := handler.definition.InteractionHandler.ValidateArgs(input.Args); err != nil {
				return nil, storeerr.InvalidRequest(err)
			}
			destination, err := resolveInteractionHandlerDestination(
				ctx,
				t.tx,
				t.input.ProjectID,
				t.input.AgentID,
				selected.HandlerKey,
				handler,
			)
			if err != nil {
				return nil, err
			}
			if destination == nil {
				return nil, storeerr.ErrUnauthorized
			}
			selected.IntegrationTargetID = destination.IntegrationTargetID
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
	return InteractionSelection{
		IntegrationTargetID: storeutil.IDFromPtr(row.IntegrationTargetID),
		HandlerKey:          row.HandlerKey,
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
	integrationID uuid.UUID
	definition    integrationdefinition.Definition
	prepared      agentconfig.PreparedIntegrationInteractionHandler
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

func prepareInteractionHandlers(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID uuid.UUID,
	configured map[string]agentconfig.IntegrationCapabilityCompiled,
) (map[string]resolvedInteractionHandler, error) {
	integrations := map[uuid.UUID]dbsqlc.ProjectIntegration{}
	result := map[string]resolvedInteractionHandler{}
	for key, capability := range configured {
		id := capability.IntegrationID
		integration, loaded := integrations[id]
		if !loaded {
			var err error
			integration, err = q.GetProjectIntegration(
				ctx,
				dbsqlc.GetProjectIntegrationParams{ProjectID: projectID, ID: id},
			)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
			integrations[id] = integration
		}
		if integration.State != string(integrationstore.ProjectIntegrationStateActive) {
			continue
		}
		definition, ok := integrationdefinition.Lookup(integrationdefinition.Type(integration.IntegrationType))
		if !ok || definition.InteractionHandler == nil {
			continue
		}
		prepared, err := definition.InteractionHandler.Prepare()
		if err != nil {
			continue
		}
		result[key] = resolvedInteractionHandler{
			integrationID: id,
			definition:    definition,
			prepared: agentconfig.PreparedIntegrationInteractionHandler{
				IntegrationID:              capability.IntegrationID,
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
	changed, err := q.SetInteractionSelection(ctx, dbsqlc.SetInteractionSelectionParams{
		ProjectID:  projectID,
		AgentID:    agentID,
		TargetID:   storeutil.IDFromNil(selection.IntegrationTargetID),
		HandlerKey: storeutil.TextFromEmpty(selection.HandlerKey),
	})
	if err != nil {
		return err
	}
	if changed != 1 {
		return storeerr.ErrStateTransitionConflict
	}
	return nil
}

func resolveInteractionHandlerDestination(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID uuid.UUID,
	key string,
	handler resolvedInteractionHandler,
) (*InteractionDestination, error) {
	target, found, err := integrationstore.GetAgentIntegrationConversationTargetTx(
		ctx,
		tx,
		projectID,
		agentID,
		handler.integrationID,
	)
	if err != nil || !found {
		return nil, err
	}
	if _, err := integrationdefinition.ParseConversation(
		handler.definition.Provider, target.Address.Kind,
		target.Address.Ref,
	); err != nil {
		return nil, nil //nolint:nilerr,nilnil // An invalid assignment cannot authorize a handler.
	}
	return &InteractionDestination{
		HandlerKey:          key,
		IntegrationID:       handler.integrationID,
		IntegrationType:     handler.definition.IntegrationType,
		IntegrationTargetID: target.ID,
		Address:             target.Address,
	}, nil
}

func selectedInteractionDestination(
	ctx context.Context, tx pgx.Tx, projectID, agentID uuid.UUID,
	selection InteractionSelection, contract agentconfig.RuntimeContract,
) (*InteractionDestination, error) {
	capability, ok := contract.InteractionHandlers[selection.HandlerKey]
	if !ok || selection.IntegrationTargetID == uuid.Nil {
		return nil, nil //nolint:nilnil // Dashboard-only selection.
	}
	handlers, err := prepareInteractionHandlers(ctx, dbsqlc.New(tx), projectID,
		map[string]agentconfig.IntegrationCapabilityCompiled{selection.HandlerKey: capability})
	if err != nil {
		return nil, err
	}
	handler, ok := handlers[selection.HandlerKey]
	if !ok {
		return nil, nil //nolint:nilnil // Handler unavailable.
	}
	destination, err := resolveInteractionHandlerDestination(ctx, tx, projectID, agentID, selection.HandlerKey, handler)
	if err != nil || destination == nil {
		return nil, err
	}
	if destination.IntegrationTargetID != selection.IntegrationTargetID {
		return nil, nil //nolint:nilnil // Selection no longer eligible.
	}
	return destination, nil
}

// Taking an integration gate under the held agent lock would invert lock order;
// presentation and callbacks recheck live authority afterward.
func captureInteractionDestinationTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID uuid.UUID,
) (json.RawMessage, error) {
	q := dbsqlc.New(tx)
	selection, contract, err := loadInteractionContract(ctx, q, projectID, agentID)
	if err != nil {
		return nil, err
	}
	destination, err := selectedInteractionDestination(
		ctx,
		tx,
		projectID,
		agentID,
		selection,
		contract,
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
	IntegrationID       uuid.UUID
	IntegrationType     integrationdefinition.Type
	Address             integrationstore.ConversationAddress
	SourceSetupRevision int64
}

func (s *Store) GetInteractionCallbackIntegrationID(ctx context.Context, id uuid.UUID) (uuid.UUID, error) {
	// Cross-project routing hint only; callers must verify the callback against the owning integration.
	integrationID, err := s.q.GetInteractionCallbackIntegrationID(ctx, dbsqlc.GetInteractionCallbackIntegrationIDParams{
		ID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, storeerr.ErrNotFound
	}
	return integrationID, err
}

func (s *Store) GetInteractionForHandlerCallback(
	ctx context.Context, projectID, integrationID, id uuid.UUID,
) (AgentInteractionRecord, error) {
	agentID, err := s.q.GetInteractionCallbackAgent(ctx, dbsqlc.GetInteractionCallbackAgentParams{
		ProjectID: projectID, InteractionID: id, IntegrationID: integrationID.String(),
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
		integration, err := dbsqlc.New(tx).GetProjectIntegration(ctx, dbsqlc.GetProjectIntegrationParams{
			ProjectID: input.ProjectID, ID: destination.IntegrationID,
		})
		if err != nil {
			return AgentInteractionRecord{}, err
		}
		if integration.SetupRevision != input.SourceSetupRevision {
			return AgentInteractionRecord{}, storeerr.ErrUnauthorized
		}
	}
	if destination.IntegrationID != input.IntegrationID ||
		destination.IntegrationType != input.IntegrationType ||
		destination.Address != input.Address ||
		(input.IntegrationTargetID != uuid.Nil && input.IntegrationTargetID != destination.IntegrationTargetID) {
		return AgentInteractionRecord{}, storeerr.ErrUnauthorized
	}
	if err := validateIntegrationInputActor(destination.IntegrationID, input.Actor); err != nil {
		return AgentInteractionRecord{}, err
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
	if err := q.LockProjectIntegrationLifecycleShared(ctx, dbsqlc.LockProjectIntegrationLifecycleSharedParams{
		IntegrationID: destination.IntegrationID,
	}); err != nil {
		return AgentInteractionRecord{}, nil, err
	}
	if _, err := q.LockAgentInProject(ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: projectID, ID: agentID,
	}); err != nil {
		return AgentInteractionRecord{}, nil, err
	}
	_, contract, err := loadInteractionContract(ctx, q, projectID, agentID)
	if err != nil {
		return AgentInteractionRecord{}, nil, err
	}
	selection := InteractionSelection{
		IntegrationTargetID: destination.IntegrationTargetID,
		HandlerKey:          destination.HandlerKey,
	}
	current, err := selectedInteractionDestination(ctx, tx, projectID, agentID, selection, contract)
	if err != nil {
		return AgentInteractionRecord{}, nil, err
	}
	if current == nil || *current != *destination {
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
	if destination == nil || *destination != input.Destination {
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
