package appstore

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
	ErrAppSelectionReserved = errors.New("app conversation reserved by another receipt")
	ErrAppSelectionSettled  = errors.New("app conversation already selected; replan")
)

type AppSelectionReservationError struct {
	ReceiptID uuid.UUID
	State     AppInboxState
}

func (e *AppSelectionReservationError) Error() string {
	return fmt.Sprintf("%s: receipt %s (%s)", ErrAppSelectionReserved, e.ReceiptID, e.State)
}

func (e *AppSelectionReservationError) Unwrap() error { return ErrAppSelectionReserved }

// InboxAppSelection reserves all slots at an app/address so concurrent plans cannot
// split the initial conversation membership.
type InboxAppSelection struct {
	AppID   uuid.UUID           `json:"app_id"`
	Address ConversationAddress `json:"address"`
	Slot    string              `json:"slot"`
}

type appSelectionIdentity struct {
	AppID   uuid.UUID           `json:"app_id"`
	Address ConversationAddress `json:"address"`
}

func (w *AppInboxLeaseTx) CheckNoUnsettledAppSelection(ctx context.Context, address ConversationAddress) error {
	// Zero-recipient events must wait for accepted choices and first-launch plans,
	// or follow-ups could be dropped before their agent exists.
	if err := w.checkLease(ctx); err != nil {
		return err
	}
	if err := LockConversationTx(ctx, w.tx, w.record.ProjectID, w.record.AppID, address); err != nil {
		return err
	}
	if err := w.checkLease(ctx); err != nil {
		return err
	}
	if err := w.checkUnplannedAppProfileChoice(ctx, address); err != nil {
		return err
	}
	selection, err := json.Marshal(appSelectionIdentity{AppID: w.record.AppID, Address: address})
	if err != nil {
		return err
	}
	owners, err := w.q.FindInboxSelectionReservations(ctx, dbsqlc.FindInboxSelectionReservationsParams{
		ProjectID: w.record.ProjectID,
		AppID:     w.record.AppID,
		ReceiptID: w.record.ID,
		Selection: selection,
	})
	if err != nil {
		return err
	}
	if len(owners) != 0 {
		return &AppSelectionReservationError{ReceiptID: owners[0].ID, State: AppInboxState(owners[0].State)}
	}
	return nil
}

func (w *AppInboxLeaseTx) reserveAppSelections(ctx context.Context, plan json.RawMessage) error {
	identities, err := inboxSelectionIdentities(plan, w.record.AppID)
	if err != nil {
		return err
	}
	if err := lockInboxSelectionConversations(
		ctx,
		w.tx,
		w.record.ProjectID,
		w.record.AppID,
		identities,
	); err != nil {
		return err
	}
	if len(identities) == 0 && w.record.Source != AppInboxSourceScheduled {
		return nil
	}
	app, err := getProjectApp(ctx, w.q, w.record.ProjectID, w.record.AppID)
	if err != nil {
		return fmt.Errorf("load selected app: %w", err)
	}
	if app.State != ProjectAppStateActive {
		return storeerr.ErrUnauthorized
	}
	if w.record.Source == AppInboxSourceScheduled {
		if err := w.record.ValidateScheduledPlan(app, plan); err != nil {
			return err
		}
	} else if app.Settings.Launcher == nil {
		return storeerr.ErrUnauthorized
	}
	// Lookup in separate statements after acquiring the gate, so a waiter sees
	// the previous planner's committed reservation at READ COMMITTED.
	for identity := range identities {
		if len(w.record.Events) == 0 {
			if err := w.checkUnplannedAppProfileChoice(ctx, identity.Address); err != nil {
				return err
			}
		}
		selection, err := json.Marshal(identity)
		if err != nil {
			return err
		}
		owners, err := w.q.FindInboxSelectionReservations(ctx, dbsqlc.FindInboxSelectionReservationsParams{
			ProjectID: w.record.ProjectID,
			AppID:     identity.AppID,
			ReceiptID: w.record.ID,
			Selection: selection,
		})
		if err != nil {
			return err
		}
		if len(owners) != 0 {
			return &AppSelectionReservationError{ReceiptID: owners[0].ID, State: AppInboxState(owners[0].State)}
		}
		targets, err := w.q.ListConversationSelections(ctx, dbsqlc.ListConversationSelectionsParams{
			ProjectID: w.record.ProjectID,
			AppID:     identity.AppID,
			Kind:      identity.Address.Kind,
			Ref:       identity.Address.Ref,
		})
		if err != nil {
			return err
		}
		if len(targets) != 0 {
			return ErrAppSelectionSettled
		}
	}
	return nil
}

func (w *AppInboxLeaseTx) checkUnplannedAppProfileChoice(
	ctx context.Context, address ConversationAddress,
) error {
	owner, err := w.q.FindUnplannedAppProfileChoiceReservation(ctx,
		dbsqlc.FindUnplannedAppProfileChoiceReservationParams{
			ProjectID: w.record.ProjectID, AppID: w.record.AppID,
			AddressKind: address.Kind, AddressRef: address.Ref,
			ReceiptID: w.record.ID,
		})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return &AppSelectionReservationError{ReceiptID: owner.ID, State: AppInboxState(owner.State)}
}

func inboxSelectionIdentities(
	plan json.RawMessage,
	appID uuid.UUID,
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
		if selection.AppID == uuid.Nil || selection.AppID != appID ||
			selection.Slot == "" || len(selection.Slot) > 64 || strings.TrimSpace(selection.Slot) != selection.Slot {
			return nil, inboxInvalid("selection requires an app, stable slot and the receipt app")
		}
		if err := selection.Address.Validate(); err != nil {
			return nil, err
		}
		identity := appSelectionIdentity{AppID: selection.AppID, Address: selection.Address}
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

func lockInboxSelectionConversations(ctx context.Context, tx pgx.Tx, projectID, appID uuid.UUID,
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
		if err := LockConversationTx(ctx, tx, projectID, appID, address); err != nil {
			return err
		}
	}
	return nil
}
