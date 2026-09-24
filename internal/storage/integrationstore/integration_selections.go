package integrationstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

var (
	ErrIntegrationSelectionReserved = errors.New("integration conversation reserved by another receipt")
	ErrIntegrationSelectionSettled  = errors.New("integration conversation already selected; replan")
)

type IntegrationSelectionReservationError struct {
	ReceiptID uuid.UUID
	State     IntegrationInboxState
}

func (e *IntegrationSelectionReservationError) Error() string {
	return fmt.Sprintf("%s: receipt %s (%s)", ErrIntegrationSelectionReserved, e.ReceiptID, e.State)
}

func (e *IntegrationSelectionReservationError) Unwrap() error { return ErrIntegrationSelectionReserved }

// InboxIntegrationSelection reserves all slots at an integration/address so concurrent plans cannot
// split the initial conversation membership.
type InboxIntegrationSelection struct {
	IntegrationID uuid.UUID           `json:"integration_id"`
	Address       ConversationAddress `json:"address"`
	Slot          string              `json:"slot"`
}

type integrationSelectionIdentity struct {
	IntegrationID uuid.UUID           `json:"integration_id"`
	Address       ConversationAddress `json:"address"`
}

func (w *IntegrationInboxLeaseTx) CheckNoUnsettledIntegrationSelection(
	ctx context.Context,
	address ConversationAddress,
) error {
	// Zero-recipient events must wait for accepted choices and first-launch plans,
	// or follow-ups could be dropped before their agent exists.
	if err := w.CheckLease(ctx); err != nil {
		return err
	}
	if err := LockConversationTx(ctx, w.tx, w.record.ProjectID, w.record.IntegrationID, address); err != nil {
		return err
	}
	if err := w.CheckLease(ctx); err != nil {
		return err
	}
	if err := w.checkUnplannedIntegrationProfileChoice(ctx, address); err != nil {
		return err
	}
	selection, err := json.Marshal(integrationSelectionIdentity{IntegrationID: w.record.IntegrationID, Address: address})
	if err != nil {
		return err
	}
	owners, err := w.q.FindInboxSelectionReservations(ctx, dbsqlc.FindInboxSelectionReservationsParams{
		ProjectID:     w.record.ProjectID,
		IntegrationID: w.record.IntegrationID,
		ReceiptID:     w.record.ID,
		Selection:     selection,
	})
	if err != nil {
		return err
	}
	if len(owners) != 0 {
		return &IntegrationSelectionReservationError{ReceiptID: owners[0].ID, State: IntegrationInboxState(owners[0].State)}
	}
	return nil
}

func (w *IntegrationInboxLeaseTx) reserveIntegrationSelections(ctx context.Context, plan json.RawMessage) error {
	identities, err := inboxSelectionIdentities(plan, w.record.IntegrationID)
	if err != nil {
		return err
	}
	if err := lockInboxSelectionConversations(
		ctx,
		w.tx,
		w.record.ProjectID,
		w.record.IntegrationID,
		identities,
	); err != nil {
		return err
	}
	if len(identities) == 0 && w.record.Source != IntegrationInboxSourceScheduled {
		return nil
	}
	integration, err := getProjectIntegration(ctx, w.q, w.record.ProjectID, w.record.IntegrationID)
	if err != nil {
		return fmt.Errorf("load selected integration: %w", err)
	}
	if integration.State != ProjectIntegrationStateActive {
		return storeerr.ErrUnauthorized
	}
	if w.record.Source == IntegrationInboxSourceScheduled {
		if err := w.record.ValidateScheduledPlan(integration, plan); err != nil {
			return err
		}
	} else if integration.Settings.Launcher == nil {
		return storeerr.ErrUnauthorized
	}
	// Lookup in separate statements after acquiring the gate, so a waiter sees
	// the previous planner's committed reservation at READ COMMITTED.
	for identity := range identities {
		if len(w.record.Events) == 0 {
			if err := w.checkUnplannedIntegrationProfileChoice(ctx, identity.Address); err != nil {
				return err
			}
		}
		selection, err := json.Marshal(identity)
		if err != nil {
			return err
		}
		owners, err := w.q.FindInboxSelectionReservations(ctx, dbsqlc.FindInboxSelectionReservationsParams{
			ProjectID:     w.record.ProjectID,
			IntegrationID: identity.IntegrationID,
			ReceiptID:     w.record.ID,
			Selection:     selection,
		})
		if err != nil {
			return err
		}
		if len(owners) != 0 {
			return &IntegrationSelectionReservationError{ReceiptID: owners[0].ID, State: IntegrationInboxState(owners[0].State)}
		}
		targets, err := w.q.ListConversationSelections(ctx, dbsqlc.ListConversationSelectionsParams{
			ProjectID:     w.record.ProjectID,
			IntegrationID: identity.IntegrationID,
			Kind:          identity.Address.Kind,
			Ref:           identity.Address.Ref,
		})
		if err != nil {
			return err
		}
		if len(targets) != 0 {
			return ErrIntegrationSelectionSettled
		}
	}
	return nil
}

func (w *IntegrationInboxLeaseTx) checkUnplannedIntegrationProfileChoice(
	ctx context.Context, address ConversationAddress,
) error {
	owner, err := w.q.FindUnplannedIntegrationProfileChoiceReservation(ctx,
		dbsqlc.FindUnplannedIntegrationProfileChoiceReservationParams{
			ProjectID: w.record.ProjectID, IntegrationID: w.record.IntegrationID,
			AddressKind: address.Kind, AddressRef: address.Ref,
			ReceiptID: w.record.ID,
		})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return &IntegrationSelectionReservationError{ReceiptID: owner.ID, State: IntegrationInboxState(owner.State)}
}

func inboxSelectionIdentities(
	plan json.RawMessage,
	integrationID uuid.UUID,
) (map[integrationSelectionIdentity]map[string]bool, error) {
	if len(plan) == 0 {
		return map[integrationSelectionIdentity]map[string]bool{}, nil
	}
	slots, err := inboxSlots(plan)
	if err != nil {
		return nil, err
	}
	identities := map[integrationSelectionIdentity]map[string]bool{}
	for _, raw := range slots {
		var envelope struct {
			Selection json.RawMessage `json:"selection"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, inboxInvalid("invalid selection envelope")
		}
		if len(envelope.Selection) == 0 {
			continue
		}
		var selection InboxIntegrationSelection
		decoder := json.NewDecoder(bytes.NewReader(envelope.Selection))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&selection); err != nil {
			return nil, inboxInvalid("invalid selection envelope")
		}
		canonical, err := json.Marshal(selection)
		if err != nil {
			return nil, err
		}
		if !jsoncanonical.Equal(canonical, envelope.Selection) {
			return nil, inboxInvalid("selection identities must use their canonical encoding")
		}
		if selection.IntegrationID == uuid.Nil || selection.IntegrationID != integrationID ||
			selection.Slot == "" || len(selection.Slot) > 64 || strings.TrimSpace(selection.Slot) != selection.Slot {
			return nil, inboxInvalid("selection requires an integration, stable slot and the receipt integration")
		}
		if err := selection.Address.Validate(); err != nil {
			return nil, err
		}
		identity := integrationSelectionIdentity{IntegrationID: selection.IntegrationID, Address: selection.Address}
		if identities[identity] == nil {
			identities[identity] = map[string]bool{}
		}
		if identities[identity][selection.Slot] {
			return nil, inboxInvalid("duplicate integration selection slot in plan")
		}
		identities[identity][selection.Slot] = true
	}
	return identities, nil
}

func lockInboxSelectionConversations(ctx context.Context, tx pgx.Tx, projectID, integrationID uuid.UUID,
	identities map[integrationSelectionIdentity]map[string]bool) error {
	addresses := map[ConversationAddress]bool{}
	for identity := range identities {
		addresses[identity.Address] = true
	}
	ordered := make([]ConversationAddress, 0, len(addresses))
	for address := range addresses {
		ordered = append(ordered, address)
	}
	slices.SortFunc(ordered, func(a, b ConversationAddress) int {
		if order := strings.Compare(a.Kind, b.Kind); order != 0 {
			return order
		}
		return strings.Compare(a.Ref, b.Ref)
	})
	for _, address := range ordered {
		if err := LockConversationTx(ctx, tx, projectID, integrationID, address); err != nil {
			return err
		}
	}
	return nil
}
