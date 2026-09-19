package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) GetInteractionSelection(
	ctx context.Context, projectID, agentID uuid.UUID,
) (InteractionSelection, error) {
	row, err := s.q.GetInteractionSelection(ctx, dbsqlc.GetInteractionSelectionParams{
		ProjectID: projectID, AgentID: agentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return InteractionSelection{}, storeerr.ErrNotFound
	}
	return interactionSelectionFromRow(row), err
}

// SelectInteractionDestinationForOriginTx is called only for a newly accepted
// explicit-origin input, after deduplication. Replays must skip it. The caller
// holds the agent lock (or has just inserted the agent). A nil origin preserves
// selection. This helper never acquires an earlier connection lifecycle gate.
func (s *Store) SelectInteractionDestinationForOriginTx(
	ctx context.Context, tx pgx.Tx, projectID, agentID, originTargetID uuid.UUID,
) (InteractionSelection, error) {
	q := dbsqlc.New(tx)
	if originTargetID == uuid.Nil {
		row, err := q.GetInteractionSelection(ctx, dbsqlc.GetInteractionSelectionParams{
			ProjectID: projectID, AgentID: agentID,
		})
		return interactionSelectionFromRow(row), err
	}
	_, resources, err := loadInteractionResources(ctx, q, projectID, agentID)
	if err != nil {
		return InteractionSelection{}, err
	}
	target, err := q.GetInteractionDestinationTarget(ctx, dbsqlc.GetInteractionDestinationTargetParams{
		ProjectID: projectID, AgentID: agentID, TargetID: originTargetID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return InteractionSelection{}, storeerr.ErrNotFound
	}
	if err != nil {
		return InteractionSelection{}, err
	}
	var selection InteractionSelection
	if matches := interactionTargetDestinations(resources, target); len(matches) == 1 {
		selection = InteractionSelection{IntegrationTargetID: originTargetID, ResourceKey: matches[0].ResourceKey}
	}
	return selection, writeInteractionSelection(ctx, q, projectID, agentID, selection)
}

// ReconcileInteractionSelectionTx runs after current-config activation while
// holding the agent lock. It clears revoked choices without selecting a fallback
// or changing any existing interaction's snapshot. Connection reads do not lock.
func (s *Store) ReconcileInteractionSelectionTx(
	ctx context.Context, tx pgx.Tx, projectID, agentID uuid.UUID,
) (InteractionSelection, error) {
	q := dbsqlc.New(tx)
	selection, resources, err := loadInteractionResources(ctx, q, projectID, agentID)
	if err != nil || selection == (InteractionSelection{}) {
		return selection, err
	}
	destination, err := selectedInteractionDestination(ctx, q, projectID, agentID, selection, resources)
	if err != nil || destination != nil {
		return selection, err
	}
	return InteractionSelection{}, writeInteractionSelection(ctx, q, projectID, agentID, InteractionSelection{})
}

func (s *Store) ListInteractionDestinations(
	ctx context.Context, projectID, agentID uuid.UUID,
) (InteractionDestinations, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return InteractionDestinations{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	return listInteractionDestinations(ctx, dbsqlc.New(tx), projectID, agentID)
}

// ListInteractionDestinations is advisory. The command revalidates under the agent lock.
func (r *ToolCallReader) ListInteractionDestinations(ctx context.Context) (InteractionDestinations, error) {
	t := r.transaction
	return listInteractionDestinations(ctx, t.q, t.input.ProjectID, t.input.AgentID)
}

func listInteractionDestinations(
	ctx context.Context, q *dbsqlc.Queries, projectID, agentID uuid.UUID,
) (InteractionDestinations, error) {
	selection, resources, err := loadInteractionResources(ctx, q, projectID, agentID)
	if err != nil {
		return InteractionDestinations{}, err
	}
	targets, err := q.ListInteractionDestinationTargets(ctx, dbsqlc.ListInteractionDestinationTargetsParams{
		ProjectID: projectID, AgentID: agentID,
	})
	if err != nil {
		return InteractionDestinations{}, err
	}
	result := InteractionDestinations{Current: selection, Destinations: []InteractionDestinationOption{}}
	for _, target := range targets {
		matches := interactionTargetDestinations(resources, dbsqlc.GetInteractionDestinationTargetRow(target))
		for _, destination := range matches {
			result.Destinations = append(result.Destinations, InteractionDestinationOption{
				Destination: destination, TargetRef: target.TargetRef, DisplayName: target.DisplayName,
			})
		}
	}
	return result, nil
}

// SetInteractionDestinationForToolCall changes future prompt routing together
// with the destination tool's completion. Omitting the key requires exactly one
// eligible handler; the zero selection explicitly chooses the dashboard.
func SetInteractionDestinationForToolCall(
	selection InteractionSelection, completion ToolCallCompletionInput,
) ToolCallCommand {
	return toolCallCommandFunc(func(ctx context.Context, t *toolCallTransaction) (any, error) {
		if err := t.lockForMutation(ctx); err != nil {
			return nil, err
		}
		selected := selection
		if selected.IntegrationTargetID == uuid.Nil {
			if selected.ResourceKey != "" {
				return nil, storeerr.InvalidRequest(errors.New("resource key requires an interaction target"))
			}
		} else {
			_, resources, err := loadInteractionResources(ctx, t.q, t.input.ProjectID, t.input.AgentID)
			if err != nil {
				return nil, err
			}
			target, err := t.q.GetInteractionDestinationTarget(ctx, dbsqlc.GetInteractionDestinationTargetParams{
				ProjectID: t.input.ProjectID, AgentID: t.input.AgentID, TargetID: selected.IntegrationTargetID,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, storeerr.ErrNotFound
			}
			if err != nil {
				return nil, err
			}
			matches := interactionTargetDestinations(resources, target)
			if selected.ResourceKey == "" {
				if len(matches) != 1 {
					return nil, storeerr.ErrConflict
				}
				selected.ResourceKey = matches[0].ResourceKey
			}
			found := false
			for _, match := range matches {
				found = found || match.ResourceKey == selected.ResourceKey
			}
			if !found {
				return nil, storeerr.ErrUnauthorized
			}
		}
		if err := writeInteractionSelection(ctx, t.q, t.input.ProjectID, t.input.AgentID, selected); err != nil {
			return nil, err
		}
		if _, err := t.completeToolCall(ctx, completion); err != nil {
			return nil, err
		}
		return selected, nil
	})
}

func interactionSelectionFromRow(row dbsqlc.GetInteractionSelectionRow) InteractionSelection {
	// A target alone is attribution, not a selected interaction handler.
	if row.ResourceKey == "" || row.IntegrationTargetID == nil {
		return InteractionSelection{}
	}
	return InteractionSelection{
		IntegrationTargetID: storeutil.IDFromPtr(row.IntegrationTargetID), ResourceKey: row.ResourceKey,
	}
}

func loadInteractionResources(
	ctx context.Context, q *dbsqlc.Queries, projectID, agentID uuid.UUID,
) (InteractionSelection, map[string]agentconfig.AppResourceCompiled, error) {
	row, err := q.GetInteractionSelection(ctx, dbsqlc.GetInteractionSelectionParams{
		ProjectID: projectID, AgentID: agentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return InteractionSelection{}, nil, storeerr.ErrNotFound
	}
	if err != nil {
		return InteractionSelection{}, nil, err
	}
	config, err := loadAgentConfigTx(ctx, q, projectID, row.CurrentConfigID)
	if err != nil {
		return InteractionSelection{}, nil, err
	}
	contract, err := launchableRuntimeContract(config)
	return interactionSelectionFromRow(row), contract.AppResources, err
}

func writeInteractionSelection(
	ctx context.Context, q *dbsqlc.Queries, projectID, agentID uuid.UUID, selection InteractionSelection,
) error {
	changed, err := q.SetInteractionSelection(ctx, dbsqlc.SetInteractionSelectionParams{
		ProjectID: projectID, AgentID: agentID,
		TargetID:    storeutil.IDFromNil(selection.IntegrationTargetID),
		ResourceKey: storeutil.TextFromEmpty(selection.ResourceKey),
	})
	if err != nil {
		return err
	}
	if changed != 1 {
		return storeerr.ErrStateTransitionConflict
	}
	return nil
}

func interactionTargetDestinations(
	resources map[string]agentconfig.AppResourceCompiled, target dbsqlc.GetInteractionDestinationTargetRow,
) []InteractionDestination {
	if target.ConnectionState != string(integrationstore.IntegrationConnectionStateActive) {
		return nil
	}
	return matchingInteractionDestinations(resources, target.ID, target.ConnectionID, target.Provider,
		integrationstore.ConversationAddress{Kind: target.ProviderRefKind, Ref: target.ProviderRef})
}

func selectedInteractionDestination(
	ctx context.Context, q *dbsqlc.Queries, projectID, agentID uuid.UUID,
	selection InteractionSelection, resources map[string]agentconfig.AppResourceCompiled,
) (*InteractionDestination, error) {
	if selection.ResourceKey == "" || selection.IntegrationTargetID == uuid.Nil {
		return nil, nil //nolint:nilnil // Dashboard-only selection has no external destination.
	}
	target, err := q.GetInteractionDestinationTarget(ctx, dbsqlc.GetInteractionDestinationTargetParams{
		ProjectID: projectID, AgentID: agentID, TargetID: selection.IntegrationTargetID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil //nolint:nilnil // A removed target leaves dashboard-only presentation.
	}
	if err != nil {
		return nil, err
	}
	for _, destination := range interactionTargetDestinations(resources, target) {
		if destination.ResourceKey == selection.ResourceKey {
			return &destination, nil
		}
	}
	return nil, nil //nolint:nilnil // Revoked config or connection leaves dashboard-only presentation.
}

// captureInteractionDestinationTx is deliberately read-only under the existing
// agent lock. It must not acquire connection gates after that lock. The snapshot
// is identity, so presentation and hosted responses recheck live authority.
func captureInteractionDestinationTx(
	ctx context.Context, q *dbsqlc.Queries, projectID, agentID uuid.UUID,
) (json.RawMessage, error) {
	selection, resources, err := loadInteractionResources(ctx, q, projectID, agentID)
	if err != nil {
		return nil, err
	}
	destination, err := selectedInteractionDestination(ctx, q, projectID, agentID, selection, resources)
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
	ConnectionID      uuid.UUID
	HandlerDefinition string
	Address           integrationstore.ConversationAddress
	// The verified connection revision is fenced with the actual resolution.
	SourceConnectionUpdatedAt time.Time
}

// GetInteractionForHandlerCallback locates an immutable captured interaction
// within a verified connection. The result is identity only; resolution still
// goes through ResolveAgentInteractionFromHandler's fenced authority check.
func (s *Store) GetInteractionForHandlerCallback(
	ctx context.Context, projectID, connectionID, id uuid.UUID,
) (AgentInteractionRecord, error) {
	agentID, err := s.q.GetInteractionCallbackAgent(ctx, dbsqlc.GetInteractionCallbackAgentParams{
		ProjectID: projectID, InteractionID: id, ConnectionID: connectionID.String(),
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
	_, destination, err := lockAuthorizedInteractionDestination(ctx, tx, input.ProjectID, input.AgentID, input.ID)
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	if !input.SourceConnectionUpdatedAt.IsZero() {
		connection, err := dbsqlc.New(tx).GetIntegrationConnection(ctx, dbsqlc.GetIntegrationConnectionParams{
			ProjectID: input.ProjectID, ID: destination.ConnectionID,
		})
		if err != nil {
			return AgentInteractionRecord{}, err
		}
		if !connection.UpdatedAt.Equal(input.SourceConnectionUpdatedAt) {
			return AgentInteractionRecord{}, storeerr.ErrUnauthorized
		}
	}
	if destination.ConnectionID != input.ConnectionID || destination.HandlerDefinition != input.HandlerDefinition ||
		destination.Address != input.Address ||
		(input.IntegrationTargetID != uuid.Nil && input.IntegrationTargetID != destination.IntegrationTargetID) {
		return AgentInteractionRecord{}, storeerr.ErrUnauthorized
	}
	input.IntegrationTargetID = destination.IntegrationTargetID
	notifications := s.newTxNotifications()
	record, err := resolveAgentInteractionTx(ctx, notifications, tx, dbsqlc.New(tx), input.ResolveAgentInteractionInput)
	if err != nil {
		return AgentInteractionRecord{}, err
	}
	if err := s.commitTxWithNotifications(ctx, tx, notifications, "resolve interaction from handler"); err != nil {
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
	// The snapshot is immutable, so its connection can be gated before the
	// agent lock without acquiring additional gates after a config change.
	if err := q.LockIntegrationConnectionLifecycleShared(ctx, dbsqlc.LockIntegrationConnectionLifecycleSharedParams{
		ConnectionID: destination.ConnectionID,
	}); err != nil {
		return AgentInteractionRecord{}, nil, err
	}
	if _, err := q.LockAgentInProject(ctx, dbsqlc.LockAgentInProjectParams{
		ProjectID: projectID, ID: agentID,
	}); err != nil {
		return AgentInteractionRecord{}, nil, err
	}
	_, resources, err := loadInteractionResources(ctx, q, projectID, agentID)
	if err != nil {
		return AgentInteractionRecord{}, nil, err
	}
	selection := InteractionSelection{
		IntegrationTargetID: destination.IntegrationTargetID, ResourceKey: destination.ResourceKey,
	}
	current, err := selectedInteractionDestination(ctx, q, projectID, agentID, selection, resources)
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
	params := dbsqlc.GetAgentInteractionParams{ProjectID: input.ProjectID, AgentID: input.AgentID, ID: input.ID}
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
		return AgentInteractionRecord{}, fmt.Errorf("record interaction presentation receipt: %w", err)
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
