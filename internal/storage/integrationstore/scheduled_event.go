package integrationstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/cronschedule"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ScheduledIntegrationEvent struct {
	TriggerID  uuid.UUID               `json:"trigger_id"`
	Occurrence cronschedule.Occurrence `json:"occurrence"`
	Settings   json.RawMessage         `json:"settings"`
}

type AcceptScheduledIntegrationEventInput struct {
	ProjectID     uuid.UUID
	IntegrationID uuid.UUID
	ReceiptKey    string
	Event         ScheduledIntegrationEvent
}

func (s *Store) AcceptScheduledIntegrationEventTx(
	ctx context.Context,
	tx pgx.Tx,
	input AcceptScheduledIntegrationEventInput,
) (IntegrationInboxRecord, bool, error) {
	payload, err := json.Marshal(input.Event)
	if err != nil {
		return IntegrationInboxRecord{}, false, err
	}
	if err := validateIntegrationReceipt(VerifiedIntegrationReceipt{
		ProjectID: input.ProjectID, IntegrationID: input.IntegrationID, ReceiptKey: input.ReceiptKey, Payload: payload,
	}); err != nil {
		return IntegrationInboxRecord{}, false, err
	}
	if err := input.Event.validate(); err != nil {
		return IntegrationInboxRecord{}, false, err
	}
	q := dbsqlc.New(tx)
	integration, err := getProjectIntegration(ctx, q, input.ProjectID, input.IntegrationID)
	if err != nil {
		return IntegrationInboxRecord{}, false, err
	}
	if integration.State != ProjectIntegrationStateActive {
		return IntegrationInboxRecord{}, false, storeerr.ErrUnauthorized
	}
	if _, err := integrationdefinition.ValidateScheduleSettings(
		integration.IntegrationType, input.Event.Settings,
	); err != nil {
		return IntegrationInboxRecord{}, false, storeerr.InvalidRequest(err)
	}
	row, err := q.InsertScheduledIntegrationEventReceipt(ctx, dbsqlc.InsertScheduledIntegrationEventReceiptParams{
		ProjectID: input.ProjectID, IntegrationID: input.IntegrationID, ReceiptKey: input.ReceiptKey, Payload: payload,
	})
	created := !errors.Is(err, pgx.ErrNoRows)
	if !created {
		row, err = q.GetIntegrationInboxReceiptByKey(ctx, dbsqlc.GetIntegrationInboxReceiptByKeyParams{
			ProjectID: input.ProjectID, IntegrationID: input.IntegrationID, ReceiptKey: input.ReceiptKey,
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

func (s ScheduledIntegrationEvent) validate() error {
	if s.TriggerID == uuid.Nil || s.Occurrence.DueAt.IsZero() || s.Occurrence.FiredAt.IsZero() ||
		strings.TrimSpace(s.Occurrence.Name) == "" {
		return inboxInvalid("scheduled event requires an occurrence")
	}
	if _, err := s.Occurrence.MessageData(); err != nil {
		return storeerr.InvalidRequest(err)
	}
	return nil
}

func (r IntegrationInboxRecord) ScheduledEvent() (ScheduledIntegrationEvent, error) {
	var event ScheduledIntegrationEvent
	if r.Source != IntegrationInboxSourceScheduled {
		return event, storeerr.ErrUnauthorized
	}
	if err := json.Unmarshal(r.Payload, &event); err != nil {
		return event, fmt.Errorf("decode scheduled event: %w", err)
	}
	return event, event.validate()
}

func (r IntegrationInboxRecord) ValidateScheduledPlan(
	integration ProjectIntegrationRecord,
	plan json.RawMessage,
) error {
	event, err := r.ScheduledEvent()
	if err != nil {
		return err
	}
	if integration.ID != r.IntegrationID || integration.ProjectID != r.ProjectID {
		return storeerr.ErrUnauthorized
	}
	// Admission validated the immutable receipt; avoid schema compilation under locks.
	var slots map[string]struct {
		Scope     integrationdefinition.Scope `json:"scope"`
		Selection *InboxIntegrationSelection  `json:"selection"`
		Launch    *struct {
			ProfileID    uuid.UUID `json:"profile_id"`
			InitialInput *struct {
				ContentBlocks json.RawMessage `json:"content_blocks"`
			} `json:"initial_input"`
		} `json:"launch"`
	}
	if err := json.Unmarshal(plan, &slots); err != nil || slots == nil {
		return inboxInvalid("invalid scheduled plan")
	}
	facts := integrationdefinition.SchedulePlan{
		IntegrationName: integration.Name, Occurrence: event.Occurrence, Settings: event.Settings,
	}
	for key, slot := range slots {
		fact := integrationdefinition.ScheduleSlot{Key: key, Scope: slot.Scope}
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
			if slot.Selection.IntegrationID != r.IntegrationID || slot.Selection.Slot != key ||
				slot.Selection.Address != (ConversationAddress{Kind: kind, Ref: ref}) {
				return storeerr.ErrUnauthorized
			}
		}
		facts.Slots = append(facts.Slots, fact)
	}
	if err := integrationdefinition.ValidateSchedulePlan(integration.IntegrationType, facts); err != nil {
		return storeerr.InvalidRequest(err)
	}
	return nil
}
