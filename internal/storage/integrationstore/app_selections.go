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
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// A settled selection asks the planner to re-read listeners. A reserved one
// belongs to another immutable plan; failed owners need explicit recovery.
var (
	ErrAppSelectionReserved = errors.New("app conversation reserved by another receipt")
	ErrAppSelectionSettled  = errors.New("app conversation already selected; replan")
)

type AppSelectionReservationError struct {
	ReceiptID uuid.UUID
	State     IntegrationInboxState
}

func (e *AppSelectionReservationError) Error() string {
	return fmt.Sprintf("%s: receipt %s (%s)", ErrAppSelectionReserved, e.ReceiptID, e.State)
}

func (e *AppSelectionReservationError) Unwrap() error { return ErrAppSelectionReserved }

// InboxAppSelection is the common envelope inside an initial-launch plan slot.
// The rest of the slot remains owned and validated by its admission workflow.
// Reservation queries omit Slot: one plan freezes all slots for this app/address.
type InboxAppSelection struct {
	AppID        uuid.UUID           `json:"app_id"`
	ConnectionID uuid.UUID           `json:"connection_id"`
	Address      ConversationAddress `json:"address"`
	Slot         string              `json:"slot"`
}

type appSelectionIdentity struct {
	AppID        uuid.UUID           `json:"app_id"`
	ConnectionID uuid.UUID           `json:"connection_id"`
	Address      ConversationAddress `json:"address"`
}

// CheckNoUnsettledAppSelection protects a zero-recipient event from being
// discarded while another receipt is still preparing the conversation's first
// agent. The containment probe deliberately omits app_id: ordinary follow-ups
// need not trigger any app. Failed reservations do not delay plain follow-ups;
// they still block replacement launches until explicit operator recovery.
// A chosen chat menu has accepted work before its plan exists. This app-storage
// bridge also checks that handoff so post-selection replies cannot freeze empty;
// unchosen menus never reserve agent execution or hold these gates for a human.
// Call after acquiring the sorted conversation union and before freezing a plan.
func (w *IntegrationInboxLeaseTx) CheckNoUnsettledAppSelection(ctx context.Context, address ConversationAddress) error {
	if err := w.checkLease(ctx); err != nil {
		return err
	}
	if err := LockConversationTx(ctx, w.tx, w.record.ProjectID, w.record.ConnectionID, address); err != nil {
		return err
	}
	if err := w.checkLease(ctx); err != nil {
		return err
	}
	if err := w.checkUnplannedAppProfileChoice(ctx, address, uuid.Nil); err != nil {
		return err
	}
	selection, err := json.Marshal(struct {
		ConnectionID uuid.UUID           `json:"connection_id"`
		Address      ConversationAddress `json:"address"`
	}{w.record.ConnectionID, address})
	if err != nil {
		return err
	}
	owners, err := w.q.FindInboxSelectionReservations(ctx, dbsqlc.FindInboxSelectionReservationsParams{
		ProjectID:     w.record.ProjectID,
		ConnectionID:  w.record.ConnectionID,
		ReceiptID:     w.record.ID,
		Selection:     selection,
		IncludeFailed: false,
	})
	if err != nil {
		return err
	}
	if len(owners) != 0 {
		return &AppSelectionReservationError{ReceiptID: owners[0].ID, State: IntegrationInboxState(owners[0].State)}
	}
	return nil
}

func (w *IntegrationInboxLeaseTx) reserveAppSelections(ctx context.Context, plan json.RawMessage) error {
	identities, err := inboxSelectionIdentities(plan, w.record.ConnectionID)
	if err != nil {
		return err
	}
	if err := lockInboxSelectionConversations(
		ctx,
		w.tx,
		w.record.ProjectID,
		w.record.ConnectionID,
		identities,
	); err != nil {
		return err
	}
	// Lookup in separate statements after acquiring the gate, so a waiter sees
	// the previous planner's committed reservation at READ COMMITTED.
	for identity := range identities {
		app, err := w.q.GetProjectApp(
			ctx,
			dbsqlc.GetProjectAppParams{ProjectID: w.record.ProjectID, ID: identity.AppID},
		)
		if err != nil {
			return fmt.Errorf("load selected app: %w", err)
		}
		if !app.Enabled || app.LaunchConnectionID == nil || *app.LaunchConnectionID != identity.ConnectionID {
			return storeerr.ErrUnauthorized
		}
		// Undecided ingress waits for accepted choices. Decided receipts instead
		// compete for the frozen plan below, so an operator retry cannot make two
		// accepted, unplanned choices reserve against each other indefinitely.
		if len(w.record.Events) == 0 {
			if err := w.checkUnplannedAppProfileChoice(ctx, identity.Address, identity.AppID); err != nil {
				return err
			}
		}
		selection, err := json.Marshal(identity)
		if err != nil {
			return err
		}
		owners, err := w.q.FindInboxSelectionReservations(ctx, dbsqlc.FindInboxSelectionReservationsParams{
			ProjectID:     w.record.ProjectID,
			ConnectionID:  identity.ConnectionID,
			ReceiptID:     w.record.ID,
			Selection:     selection,
			IncludeFailed: true,
		})
		if err != nil {
			return err
		}
		if len(owners) != 0 {
			return &AppSelectionReservationError{ReceiptID: owners[0].ID, State: IntegrationInboxState(owners[0].State)}
		}
		targets, err := w.q.ListConversationSelections(ctx, dbsqlc.ListConversationSelectionsParams{
			ProjectID:    w.record.ProjectID,
			ConnectionID: identity.ConnectionID,
			Kind:         identity.Address.Kind,
			Ref:          identity.Address.Ref,
		})
		if err != nil {
			return err
		}
		for _, target := range targets {
			if target.AppID != nil && *target.AppID == identity.AppID {
				return ErrAppSelectionSettled
			}
		}
	}
	return nil
}

// Choice commit precedes inbox planning. Bridge only that gap; once a plan is
// frozen, the existing plan reservation and retained target own continuation.
func (w *IntegrationInboxLeaseTx) checkUnplannedAppProfileChoice(
	ctx context.Context, address ConversationAddress, appID uuid.UUID,
) error {
	owner, err := w.q.FindUnplannedAppProfileChoiceReservation(ctx,
		dbsqlc.FindUnplannedAppProfileChoiceReservationParams{
			ProjectID: w.record.ProjectID, ConnectionID: w.record.ConnectionID,
			AddressKind: address.Kind, AddressRef: address.Ref, AppID: storeutil.IDFromNil(appID),
			ReceiptID: w.record.ID,
		})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return &AppSelectionReservationError{ReceiptID: owner.ID, State: IntegrationInboxState(owner.State)}
}

// Parse only the common selection envelope; admission owns all other slot fields.
func inboxSelectionIdentities(
	plan json.RawMessage,
	connectionID uuid.UUID,
) (map[appSelectionIdentity]map[string]bool, error) {
	if len(plan) == 0 {
		return map[appSelectionIdentity]map[string]bool{}, nil
	}
	slots, err := inboxSlots(plan)
	if err != nil {
		return nil, err
	}
	identities := map[appSelectionIdentity]map[string]bool{}
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
		var selection InboxAppSelection
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
		if selection.AppID == uuid.Nil || selection.ConnectionID != connectionID ||
			selection.Slot == "" || len(selection.Slot) > 64 || strings.TrimSpace(selection.Slot) != selection.Slot {
			return nil, inboxInvalid("selection requires an app, stable slot and the receipt connection")
		}
		if err := selection.Address.Validate(); err != nil {
			return nil, err
		}
		identity := appSelectionIdentity{selection.AppID, selection.ConnectionID, selection.Address}
		if identities[identity] == nil {
			identities[identity] = map[string]bool{}
		}
		if identities[identity][selection.Slot] {
			return nil, inboxInvalid("duplicate app selection slot in plan")
		}
		identities[identity][selection.Slot] = true
	}
	return identities, nil
}

func lockInboxSelectionConversations(ctx context.Context, tx pgx.Tx, projectID, connectionID uuid.UUID,
	identities map[appSelectionIdentity]map[string]bool) error {
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
		if err := LockConversationTx(ctx, tx, projectID, connectionID, address); err != nil {
			return err
		}
	}
	return nil
}
