package integrationstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// ScheduledAppLaunch is the immutable handoff from a claimed cron occurrence.
// Credentials and mention-launcher settings are deliberately not snapshotted.
type ScheduledAppLaunch struct {
	TriggerID      uuid.UUID       `json:"trigger_id"`
	ProfileID      uuid.UUID       `json:"profile_id"`
	ConfigID       uuid.UUID       `json:"config_id"`
	TriggerName    string          `json:"trigger_name"`
	DueAt          time.Time       `json:"due_at"`
	Destination    json.RawMessage `json:"destination"`
	OpeningMessage string          `json:"opening_message"`
	Message        string          `json:"message"`
}

type AcceptScheduledAppLaunchInput struct {
	ProjectID  uuid.UUID
	AppID      uuid.UUID
	ReceiptKey string
	Launch     ScheduledAppLaunch
}

// AcceptScheduledAppLaunchTx is composed only by executionstore's cron handoff,
// under project/app/profile/cron gates. The caller must roll back on any error.
// It cannot complete the firing on its own and performs no external I/O.
func (s *Store) AcceptScheduledAppLaunchTx(
	ctx context.Context,
	tx pgx.Tx,
	input AcceptScheduledAppLaunchInput,
) (IntegrationInboxRecord, bool, error) {
	payload, err := json.Marshal(input.Launch)
	if err != nil {
		return IntegrationInboxRecord{}, false, err
	}
	if err := validateIntegrationReceipt(VerifiedIntegrationReceipt{
		ProjectID: input.ProjectID, AppID: input.AppID, ReceiptKey: input.ReceiptKey, Payload: payload,
	}); err != nil {
		return IntegrationInboxRecord{}, false, err
	}
	if err := input.Launch.validate(); err != nil {
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
	if _, err := appdefinition.CanonicalScheduledDestination(app.Provider, input.Launch.Destination); err != nil {
		return IntegrationInboxRecord{}, false, storeerr.InvalidRequest(err)
	}
	row, err := q.InsertScheduledAppLaunchReceipt(ctx, dbsqlc.InsertScheduledAppLaunchReceiptParams{
		ProjectID: input.ProjectID, AppID: input.AppID, ReceiptKey: input.ReceiptKey, Payload: payload,
	})
	created := !errors.Is(err, pgx.ErrNoRows)
	if !created {
		row, err = q.GetIntegrationInboxReceiptByKey(ctx, dbsqlc.GetIntegrationInboxReceiptByKeyParams{
			ProjectID: input.ProjectID, AppID: input.AppID, ReceiptKey: input.ReceiptKey,
		})
		if err == nil && (row.Source != string(IntegrationInboxSourceScheduledLaunch) ||
			!jsoncanonical.Equal(row.Payload, payload)) {
			return IntegrationInboxRecord{}, false, storeerr.ErrIdempotencyConflict
		}
	}
	if err != nil {
		return IntegrationInboxRecord{}, false, err
	}
	return inboxRecord(row), created, nil
}

func (s ScheduledAppLaunch) validate() error {
	if s.TriggerID == uuid.Nil || s.ProfileID == uuid.Nil || s.ConfigID == uuid.Nil || s.DueAt.IsZero() ||
		strings.TrimSpace(s.TriggerName) == "" || strings.TrimSpace(s.OpeningMessage) == "" ||
		strings.TrimSpace(s.Message) == "" {
		return inboxInvalid("scheduled launch requires an occurrence, profile config, destination and messages")
	}
	return nil
}

func (r IntegrationInboxRecord) ScheduledLaunch() (ScheduledAppLaunch, error) {
	var launch ScheduledAppLaunch
	if r.Source != IntegrationInboxSourceScheduledLaunch {
		return launch, storeerr.ErrUnauthorized
	}
	if err := json.Unmarshal(r.Payload, &launch); err != nil {
		return launch, fmt.Errorf("decode scheduled launch: %w", err)
	}
	return launch, launch.validate()
}

// ScheduledRoot reads the confirmed destination from the immutable launch plan.
// A scheduled receipt authorizes one thread beneath its accepted parent channel.
func (r IntegrationInboxRecord) ScheduledRoot(provider string, plan json.RawMessage) (appdefinition.Scope, error) {
	launch, err := r.ScheduledLaunch()
	if err != nil {
		return appdefinition.Scope{}, err
	}
	var slots map[string]struct {
		Scope     appdefinition.Scope `json:"scope"`
		Selection InboxAppSelection   `json:"selection"`
	}
	if err := json.Unmarshal(plan, &slots); err != nil {
		return appdefinition.Scope{}, inboxInvalid("invalid scheduled launch plan")
	}
	slot, ok := slots["scheduled"]
	if !ok || len(slots) != 1 || slot.Selection.AppID != r.AppID || slot.Selection.Slot != "scheduled" {
		return appdefinition.Scope{}, storeerr.ErrUnauthorized
	}
	if err := validateScheduledRoot(provider, launch.Destination, slot.Scope); err != nil {
		return appdefinition.Scope{}, err
	}
	kind, ref, err := slot.Scope.Conversation()
	if err != nil {
		return appdefinition.Scope{}, err
	}
	if slot.Selection.Address != (ConversationAddress{Kind: kind, Ref: ref}) {
		return appdefinition.Scope{}, storeerr.ErrUnauthorized
	}
	return slot.Scope, nil
}

func validateScheduledRoot(provider string, destination json.RawMessage, root appdefinition.Scope) error {
	if err := root.Validate(provider); err != nil {
		return storeerr.InvalidRequest(err)
	}
	parent, err := appdefinition.ResolveDestination(provider, destination)
	if err != nil {
		return storeerr.InvalidRequest(err)
	}
	switch provider {
	case appdefinition.ProviderSlack:
		if root.Slack.ThreadTS != "" && root.Slack.ChannelID == parent.Slack.ChannelID {
			return nil
		}
	case appdefinition.ProviderDiscord:
		if root.Discord.ThreadID != "" && root.Discord.GuildID != "" &&
			root.Discord.ChannelID == parent.Discord.ChannelID &&
			(parent.Discord.GuildID == "" || root.Discord.GuildID == parent.Discord.GuildID) {
			return nil
		}
	}
	return inboxInvalid("scheduled root differs from its configured parent")
}
