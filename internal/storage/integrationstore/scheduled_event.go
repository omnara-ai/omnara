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
	integration, err := getIntegration(ctx, q, input.ProjectID, input.IntegrationID)
	if err != nil {
		return IntegrationInboxRecord{}, false, err
	}
	if integration.State != IntegrationStateActive {
		return IntegrationInboxRecord{}, false, storeerr.ErrUnauthorized
	}
	if _, err := integrationdefinition.ValidateScheduleSettings(
		integration.IntegrationKind, input.Event.Settings,
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
	integration IntegrationRecord,
	plan json.RawMessage,
) error {
	event, err := r.ScheduledEvent()
	if err != nil {
		return err
	}
	if integration.ID != r.IntegrationID || integration.ProjectID != r.ProjectID {
		return storeerr.ErrUnauthorized
	}
	var frozen struct {
		Message struct {
			Scope         integrationdefinition.Scope `json:"scope"`
			ContentBlocks json.RawMessage             `json:"content_blocks"`
		} `json:"message"`
		Recipients map[string]struct {
			LaunchClaim *InboxLaunchClaim `json:"launch_claim"`
			Launch      *struct {
				ProfileID uuid.UUID `json:"profile_id"`
			} `json:"launch"`
		} `json:"recipients"`
	}
	if err := json.Unmarshal(plan, &frozen); err != nil || frozen.Recipients == nil {
		return inboxInvalid("invalid scheduled plan")
	}
	facts := integrationdefinition.SchedulePlan{
		IntegrationName: integration.Name, Occurrence: event.Occurrence, Settings: event.Settings,
	}
	for key, recipient := range frozen.Recipients {
		fact := integrationdefinition.ScheduleLaunch{Key: key, Scope: frozen.Message.Scope}
		if recipient.Launch != nil {
			if recipient.LaunchClaim == nil {
				return inboxInvalid("scheduled launch requires a launch claim")
			}
			fact.ProfileID = recipient.Launch.ProfileID
			fact.Content = frozen.Message.ContentBlocks
		}
		if recipient.LaunchClaim != nil {
			kind, ref, err := frozen.Message.Scope.Conversation()
			if err != nil {
				return storeerr.InvalidRequest(err)
			}
			if recipient.LaunchClaim.IntegrationID != r.IntegrationID || recipient.LaunchClaim.LaunchKey != key ||
				recipient.LaunchClaim.Address != (ConversationAddress{Kind: kind, Ref: ref}) {
				return storeerr.ErrUnauthorized
			}
		}
		facts.Launches = append(facts.Launches, fact)
	}
	if err := integrationdefinition.ValidateSchedulePlan(integration.IntegrationKind, facts); err != nil {
		return storeerr.InvalidRequest(err)
	}
	return nil
}
