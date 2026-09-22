package integrationstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/cronschedule"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// ScheduledAppEvent is the immutable handoff from a claimed cron occurrence.
// The app owns settings and their runtime interpretation; cron owns occurrence identity.
type ScheduledAppEvent struct {
	TriggerID  uuid.UUID               `json:"trigger_id"`
	Occurrence cronschedule.Occurrence `json:"occurrence"`
	Settings   json.RawMessage         `json:"settings"`
}

type AcceptScheduledAppEventInput struct {
	ProjectID  uuid.UUID
	AppID      uuid.UUID
	ReceiptKey string
	Event      ScheduledAppEvent
}

// AcceptScheduledAppEventTx is composed only by executionstore's cron handoff,
// under project/app/cron gates. The caller must roll back on any error.
// It cannot complete the firing on its own and performs no external I/O.
func (s *Store) AcceptScheduledAppEventTx(
	ctx context.Context,
	tx pgx.Tx,
	input AcceptScheduledAppEventInput,
) (IntegrationInboxRecord, bool, error) {
	payload, err := json.Marshal(input.Event)
	if err != nil {
		return IntegrationInboxRecord{}, false, err
	}
	if err := validateIntegrationReceipt(VerifiedIntegrationReceipt{
		ProjectID: input.ProjectID, AppID: input.AppID, ReceiptKey: input.ReceiptKey, Payload: payload,
	}); err != nil {
		return IntegrationInboxRecord{}, false, err
	}
	if err := input.Event.validate(); err != nil {
		return IntegrationInboxRecord{}, false, err
	}
	q := dbsqlc.New(tx)
	app, err := getProjectApp(ctx, q, input.ProjectID, input.AppID)
	if err != nil {
		return IntegrationInboxRecord{}, false, err
	}
	if app.State != ProjectAppStateActive {
		return IntegrationInboxRecord{}, false, storeerr.ErrUnauthorized
	}
	if _, err := appdefinition.ValidateScheduleSettings(app.AppType, input.Event.Settings); err != nil {
		return IntegrationInboxRecord{}, false, storeerr.InvalidRequest(err)
	}
	row, err := q.InsertScheduledAppEventReceipt(ctx, dbsqlc.InsertScheduledAppEventReceiptParams{
		ProjectID: input.ProjectID, AppID: input.AppID, ReceiptKey: input.ReceiptKey, Payload: payload,
	})
	created := !errors.Is(err, pgx.ErrNoRows)
	if !created {
		row, err = q.GetIntegrationInboxReceiptByKey(ctx, dbsqlc.GetIntegrationInboxReceiptByKeyParams{
			ProjectID: input.ProjectID, AppID: input.AppID, ReceiptKey: input.ReceiptKey,
		})
		if err == nil && (row.Source != string(IntegrationInboxSourceScheduled) ||
			!jsoncanonical.Equal(row.Payload, payload)) {
			return IntegrationInboxRecord{}, false, storeerr.ErrIdempotencyConflict
		}
	}
	if err != nil {
		return IntegrationInboxRecord{}, false, err
	}
	return inboxRecord(row), created, nil
}

func (s ScheduledAppEvent) validate() error {
	if s.TriggerID == uuid.Nil || s.Occurrence.DueAt.IsZero() || s.Occurrence.FiredAt.IsZero() ||
		strings.TrimSpace(s.Occurrence.Name) == "" {
		return inboxInvalid("scheduled event requires an occurrence")
	}
	if _, err := s.Occurrence.MessageData(); err != nil {
		return storeerr.InvalidRequest(err)
	}
	return nil
}

func (r IntegrationInboxRecord) ScheduledEvent() (ScheduledAppEvent, error) {
	var event ScheduledAppEvent
	if r.Source != IntegrationInboxSourceScheduled {
		return event, storeerr.ErrUnauthorized
	}
	if err := json.Unmarshal(r.Payload, &event); err != nil {
		return event, fmt.Errorf("decode scheduled event: %w", err)
	}
	return event, event.validate()
}

// ValidateScheduledPlan checks app-defined behavior without handing application
// code a transaction. Storage retains project, selection and receipt authority.
func (r IntegrationInboxRecord) ValidateScheduledPlan(app ProjectAppRecord, plan json.RawMessage) error {
	event, err := r.ScheduledEvent()
	if err != nil {
		return err
	}
	if app.ID != r.AppID || app.ProjectID != r.ProjectID {
		return storeerr.ErrUnauthorized
	}
	// The immutable receipt was validated at admission. Check planned authority
	// below without compiling its schema or rendering sample templates under locks.
	var slots map[string]struct {
		Scope     appdefinition.Scope `json:"scope"`
		Selection *InboxAppSelection  `json:"selection"`
		Launch    *struct {
			ProfileID    uuid.UUID
			InitialInput *struct {
				ContentBlocks json.RawMessage `json:"content_blocks"`
			}
		} `json:"launch"`
	}
	if err := json.Unmarshal(plan, &slots); err != nil || slots == nil {
		return inboxInvalid("invalid scheduled plan")
	}
	facts := appdefinition.SchedulePlan{AppName: app.Name, Occurrence: event.Occurrence, Settings: event.Settings}
	for key, slot := range slots {
		fact := appdefinition.ScheduleSlot{Key: key, Scope: slot.Scope}
		if slot.Launch != nil {
			if slot.Selection == nil {
				return inboxInvalid("scheduled launch requires a selection")
			}
			fact.ProfileID = slot.Launch.ProfileID
			if slot.Launch.InitialInput != nil {
				fact.Content = slot.Launch.InitialInput.ContentBlocks
			}
		}
		if slot.Selection != nil {
			kind, ref, err := slot.Scope.Conversation()
			if err != nil {
				return storeerr.InvalidRequest(err)
			}
			if slot.Selection.AppID != r.AppID || slot.Selection.Slot != key ||
				slot.Selection.Address != (ConversationAddress{Kind: kind, Ref: ref}) {
				return storeerr.ErrUnauthorized
			}
		}
		facts.Slots = append(facts.Slots, fact)
	}
	if err := appdefinition.ValidateSchedulePlan(app.AppType, facts); err != nil {
		return storeerr.InvalidRequest(err)
	}
	return nil
}
