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

// ScheduledLaunchPreparation records publication attempts and confirmed roots.
// An attempt without a root is uncertain, even after lease expiry. Only a definite
// provider rejection permits clearing it. Operator retry never clears evidence.
type ScheduledLaunchPreparation struct {
	AttemptedAt *time.Time           `json:"attempted_at,omitempty"`
	Root        *appdefinition.Scope `json:"root,omitempty"`
}

func (r IntegrationInboxRecord) ScheduledPreparation() (ScheduledLaunchPreparation, error) {
	var state ScheduledLaunchPreparation
	if r.Source != IntegrationInboxSourceScheduledLaunch {
		return state, storeerr.ErrUnauthorized
	}
	if err := json.Unmarshal(r.Preparation, &state); err != nil {
		return state, err
	}
	if state.Root != nil && state.AttemptedAt == nil {
		return state, inboxInvalid("scheduled root has no publication attempt")
	}
	return state, nil
}

func (w *IntegrationInboxLeaseTx) BeginScheduledPublication(
	ctx context.Context,
) (ScheduledLaunchPreparation, bool, error) {
	if err := w.checkLease(ctx); err != nil {
		return ScheduledLaunchPreparation{}, false, err
	}
	state, err := w.record.ScheduledPreparation()
	if err != nil {
		return state, false, err
	}
	if state.AttemptedAt != nil {
		return state, false, nil
	}
	now, err := w.q.DBNow(ctx)
	if err != nil {
		return state, false, err
	}
	state.AttemptedAt = &now
	if err := w.writeScheduledPreparation(ctx, state); err != nil {
		return state, false, err
	}
	return state, true, nil
}

// RecordScheduledNonDelivery is called only for a definite provider rejection
// before delivery, never a timeout, 5xx, missing response or negative history lookup.
func (w *IntegrationInboxLeaseTx) RecordScheduledNonDelivery(ctx context.Context) error {
	if err := w.checkLease(ctx); err != nil {
		return err
	}
	state, err := w.record.ScheduledPreparation()
	if err != nil {
		return err
	}
	if state.Root != nil {
		return storeerr.ErrStateTransitionConflict
	}
	return w.writeScheduledPreparation(ctx, ScheduledLaunchPreparation{})
}

func (w *IntegrationInboxLeaseTx) RecordScheduledRoot(ctx context.Context, root appdefinition.Scope) error {
	if err := w.checkLease(ctx); err != nil {
		return err
	}
	state, err := w.record.ScheduledPreparation()
	if err != nil {
		return err
	}
	if state.AttemptedAt == nil {
		return inboxInvalid("scheduled publication has not started")
	}
	launch, err := w.record.ScheduledLaunch()
	if err != nil {
		return err
	}
	app, err := getProjectApp(ctx, w.q, w.record.ProjectID, w.record.AppID)
	if err != nil {
		return err
	}
	if err := validateScheduledRoot(app.Provider, launch.Destination, root); err != nil {
		return err
	}
	if state.Root != nil {
		previous, err := json.Marshal(state.Root)
		if err != nil {
			return fmt.Errorf("marshal recorded scheduled root: %w", err)
		}
		next, err := json.Marshal(root)
		if err != nil {
			return fmt.Errorf("marshal scheduled root: %w", err)
		}
		if !jsoncanonical.Equal(previous, next) {
			return storeerr.ErrIdempotencyConflict
		}
		return nil
	}
	state.Root = &root
	return w.writeScheduledPreparation(ctx, state)
}

func validateScheduledRoot(provider string, destination json.RawMessage, root appdefinition.Scope) error {
	if err := root.Validate(provider); err != nil {
		return storeerr.InvalidRequest(err)
	}
	parent, err := appdefinition.ResolveDestination(provider, destination, nil)
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

// Callers refresh the locked receipt before choosing a state transition. The
// UPDATE fences lease expiry again at the moment of the write.
func (w *IntegrationInboxLeaseTx) writeScheduledPreparation(
	ctx context.Context,
	state ScheduledLaunchPreparation,
) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	rows, err := w.q.UpdateScheduledLaunchPreparation(ctx, dbsqlc.UpdateScheduledLaunchPreparationParams{
		ProjectID: w.lease.ProjectID, ID: w.lease.ReceiptID, ClaimToken: w.lease.Token, Preparation: raw,
	})
	if err := inboxLeaseMutation("record scheduled publication", rows, err); err != nil {
		return err
	}
	w.record.Preparation = raw
	return nil
}
