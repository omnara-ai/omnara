//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/crontrigger"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func cronAppFixture(t *testing.T) (appInteractionFixture, executionstore.CronTriggerRecord) {
	t.Helper()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := createIntegrationProjectAdmin(t, ctx, store, "cron-app@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "cron-app")
	f := appInteractionFixture{ctx: ctx, store: store, user: user, profile: profile}
	f.app = f.createApp(t, "cron-chat")
	record, err := store.Execution().CreateCronTrigger(ctx, cronAppInput(t, f, "Daily"))
	require.NoError(t, err)
	return f, record
}

func cronAppInput(t *testing.T, f appInteractionFixture, name string) executionstore.CreateCronTriggerInput {
	t.Helper()
	return executionstore.CreateCronTriggerInput{
		ProjectID: testProjectID, Name: name, Enabled: true,
		CronExpression: "0 9 * * *", Timezone: "America/Los_Angeles", IdempotencyKey: name,
		Target: executionstore.CronTriggerTarget{
			Kind: executionstore.CronTriggerTargetApp, ID: f.app.ID,
			Settings: cronAppSettings(t, f.profile.ID, "C123", `Daily {{.trigger.local_date}}`,
				`Task {{.trigger.name}} {{.trigger.local_date}}`),
		},
	}
}

func cronAppSettings(t *testing.T, profileID uuid.UUID, channel, opening, message string) json.RawMessage {
	t.Helper()
	profile, err := publicid.Encode(publicid.KindAgentProfile, profileID)
	require.NoError(t, err)
	raw, err := json.Marshal(map[string]string{
		"agent_profile_id": profile, "channel_id": channel,
		"opening_message_template": opening, "message_template": message,
	})
	require.NoError(t, err)
	return raw
}

func claimCronApp(t *testing.T, f appInteractionFixture, id uuid.UUID) executionstore.ClaimedCronTrigger {
	t.Helper()
	_, err := f.store.pool.Exec(
		f.ctx,
		`UPDATE cron_triggers SET next_fire_after='2026-03-09T01:30:00Z' WHERE id=$1`,
		id,
	)
	require.NoError(t, err)
	claimed, err := f.store.Execution().ClaimDueCronTriggers(f.ctx, 100)
	require.NoError(t, err)
	for _, c := range claimed.Claimed {
		if c.TriggerID == id {
			return c
		}
	}
	t.Fatal("cron occurrence was not claimed")
	return executionstore.ClaimedCronTrigger{}
}

func cronAppReceipt(
	t *testing.T,
	f appInteractionFixture,
	id uuid.UUID,
) (uuid.UUID, integrationstore.ScheduledAppEvent) {
	t.Helper()
	var receipt uuid.UUID
	var raw []byte
	require.NoError(
		t,
		f.store.pool.QueryRow(f.ctx,
			`SELECT inbox.id, inbox.payload FROM cron_triggers cron
  JOIN integration_inbox inbox ON inbox.id=cron.last_app_receipt_id WHERE cron.id=$1`, id).
			Scan(&receipt, &raw),
	)
	event, err := (integrationstore.IntegrationInboxRecord{
		Source: integrationstore.IntegrationInboxSourceScheduled, Payload: raw,
	}).ScheduledEvent()
	require.NoError(t, err)
	return receipt, event
}

func TestCronAppEventCurrentSnapshotReplayAndStats(t *testing.T) {
	t.Parallel()
	f, record := cronAppFixture(t)
	lastFired := time.Date(2026, 3, 7, 17, 0, 0, 0, time.UTC)
	_, err := f.store.pool.Exec(f.ctx, `UPDATE cron_triggers SET last_fired_at=$2 WHERE id=$1`, record.ID, lastFired)
	require.NoError(t, err)
	claimed := claimCronApp(t, f, record.ID)
	// App-owned profile settings may change even after a cron claim. The handoff
	// copies current settings, without resolving or freezing profile config.
	other := createIntegrationTestProfile(t, f.ctx, f.store, "other-cron-profile")
	target := record.Target
	target.Settings = cronAppSettings(t, other.ID, "G456",
		`Edited {{.trigger.name}} {{.trigger.local_date}}`, `Current {{.trigger.local_date}}`)
	name := "Changed"
	_, err = f.store.Execution().UpdateCronTrigger(f.ctx, executionstore.UpdateCronTriggerInput{
		ProjectID: testProjectID, TriggerID: record.ID, Target: &target, Name: &name,
	})
	require.NoError(t, err)
	queued, err := f.store.Execution().CreateCronTriggerAppEvent(f.ctx, claimed)
	require.NoError(t, err)
	require.True(t, queued)
	receipt, event := cronAppReceipt(t, f, record.ID)
	require.Equal(t, record.ID, event.TriggerID)
	require.Equal(t, "Changed", event.Occurrence.Name)
	require.Equal(t, record.Timezone, event.Occurrence.Timezone)
	require.True(t, event.Occurrence.DueAt.Equal(claimed.DueAt))
	require.True(t, event.Occurrence.FiredAt.Equal(claimed.FiredAt))
	require.NotNil(t, event.Occurrence.LastFiredAt)
	require.True(t, event.Occurrence.LastFiredAt.Equal(lastFired))
	require.JSONEq(t, string(target.Settings), string(event.Settings))
	var key, source string
	require.NoError(
		t,
		f.store.pool.QueryRow(f.ctx, `SELECT receipt_key,source FROM integration_inbox WHERE id=$1`, receipt).
			Scan(&key, &source),
	)
	require.Equal(t, string(integrationstore.IntegrationInboxSourceScheduled), source)
	require.Equal(t, "cron_trigger:"+record.ID.String()+":"+claimed.DueAt.UTC().Format(time.RFC3339), key)
	queued, err = f.store.Execution().CreateCronTriggerAppEvent(f.ctx, claimed)
	require.NoError(t, err)
	require.False(t, queued)
	current, err := f.store.Execution().GetCronTrigger(f.ctx, testProjectID, record.ID)
	require.NoError(t, err)
	require.NotNil(t, current.LastFiredAt)
	require.NotNil(t, current.LastRun)
	require.Equal(t, executionstore.CronTriggerLastRunQueued, current.LastRun.State)
	// Later edits and disable do not rewrite the accepted snapshot.
	disabled := false
	target.Settings = cronAppSettings(t, f.profile.ID, "C123", "Future opening", "Future task")
	_, err = f.store.Execution().
		UpdateCronTrigger(f.ctx, executionstore.UpdateCronTriggerInput{
			ProjectID: testProjectID,
			TriggerID: record.ID,
			Enabled:   &disabled,
			Target:    &target,
		})
	require.NoError(t, err)
	_, again := cronAppReceipt(t, f, record.ID)
	require.Equal(t, event, again)
	second, err := f.store.Execution().CreateCronTrigger(f.ctx, cronAppInput(t, f, "Second"))
	require.NoError(t, err)
	_, err = f.store.pool.Exec(
		f.ctx,
		`UPDATE cron_triggers SET next_fire_after=now()-interval '1 minute' WHERE id=$1`,
		second.ID,
	)
	require.NoError(t, err)
	stats, err := crontrigger.NewService(f.store.Execution(), nil, slog.Default()).FireDueTriggers(f.ctx)
	require.NoError(t, err)
	require.Equal(t, 1, stats.Queued)
	require.Zero(t, stats.Launched)
	require.Zero(t, stats.Failures)
}

func TestCronAppEventClaimFencingAndEdits(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"disable", "reschedule", "token", "expired", "deleted"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f, record := cronAppFixture(t)
			claimed := claimCronApp(t, f, record.ID)
			switch scenario {
			case "disable":
				disabled := false
				_, err := f.store.Execution().
					UpdateCronTrigger(f.ctx, executionstore.UpdateCronTriggerInput{
						ProjectID: testProjectID,
						TriggerID: record.ID,
						Enabled:   &disabled,
					})
				require.NoError(t, err)
			case "reschedule":
				schedule := "0 10 * * *"
				_, err := f.store.Execution().
					UpdateCronTrigger(f.ctx, executionstore.UpdateCronTriggerInput{
						ProjectID:      testProjectID,
						TriggerID:      record.ID,
						CronExpression: &schedule,
					})
				require.NoError(t, err)
			case "token":
				_, err := f.store.pool.Exec(
					f.ctx,
					`UPDATE cron_triggers SET claim_token=$2 WHERE id=$1`,
					record.ID,
					uuid.New(),
				)
				require.NoError(t, err)
			case "expired":
				_, err := f.store.pool.Exec(
					f.ctx,
					`UPDATE cron_triggers SET claimed_until=now()-interval '1 second' WHERE id=$1`,
					record.ID,
				)
				require.NoError(t, err)
			case "deleted":
				require.NoError(t, f.store.Execution().DeleteCronTrigger(f.ctx, testProjectID, record.ID))
			}
			var beforeNext, afterNext *time.Time
			var beforeToken, afterToken *uuid.UUID
			require.NoError(
				t,
				f.store.pool.QueryRow(f.ctx, `SELECT next_fire_after,claim_token FROM cron_triggers WHERE id=$1`, record.ID).
					Scan(&beforeNext, &beforeToken),
			)
			queued, err := f.store.Execution().CreateCronTriggerAppEvent(f.ctx, claimed)
			require.NoError(t, err)
			require.False(t, queued)
			require.NoError(
				t,
				f.store.pool.QueryRow(f.ctx, `SELECT next_fire_after,claim_token FROM cron_triggers WHERE id=$1`, record.ID).
					Scan(&afterNext, &afterToken),
			)
			require.Equal(t, beforeNext, afterNext)
			if scenario == "disable" || scenario == "reschedule" {
				require.Nil(t, afterToken)
			} else {
				require.Equal(t, beforeToken, afterToken)
			}
			var count int
			require.NoError(
				t,
				f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM integration_inbox WHERE app_id=$1`, f.app.ID).
					Scan(&count),
			)
			require.Zero(t, count)
		})
	}
}

func TestCronAppEventLifecycle(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"disconnect", "delete_app", "delete_profile"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f, record := cronAppFixture(t)
			claimed := claimCronApp(t, f, record.ID)
			switch scenario {
			case "disconnect":
				_, err := f.store.Integrations().
					DisconnectProjectApp(f.ctx, integrationstore.DisconnectProjectAppInput{ProjectID: testProjectID, AppID: f.app.ID})
				require.NoError(t, err)
			case "delete_app":
				require.NoError(t, f.store.Integrations().DeleteProjectApp(f.ctx, testOrgID, testProjectID, f.app.ID))
			case "delete_profile":
				require.NoError(t, f.store.Execution().DeleteAgentProfile(f.ctx, testProjectID, f.profile.ID))
			}
			queued, err := f.store.Execution().CreateCronTriggerAppEvent(f.ctx, claimed)
			require.NoError(t, err)
			current, err := f.store.Execution().GetCronTrigger(f.ctx, testProjectID, record.ID)
			if scenario == "delete_app" {
				require.False(t, queued)
				require.ErrorIs(t, err, storeerr.ErrNotFound)
				return
			}
			require.NoError(t, err)
			if scenario == "delete_profile" {
				// Profile references are app inputs, not cron ownership. The app
				// reports unavailable profiles when it handles this accepted event.
				require.True(t, queued)
				require.True(t, current.Enabled)
				require.Nil(t, current.FailureReport)
				_, event := cronAppReceipt(t, f, record.ID)
				require.JSONEq(t, string(record.Target.Settings), string(event.Settings))
				_, err = f.store.Execution().CreateCronTrigger(f.ctx, cronAppInput(t, f, "deleted-profile"))
				require.NoError(t, err)
				return
			}
			require.False(t, queued)
			require.NotNil(t, current.FailureReport)
			require.False(t, current.FailureReport.WillRetry)
			require.True(t, current.NextFireAfter.After(claimed.DueAt))
			var token *uuid.UUID
			require.NoError(
				t,
				f.store.pool.QueryRow(f.ctx, `SELECT claim_token FROM cron_triggers WHERE id=$1`, record.ID).
					Scan(&token),
			)
			require.Nil(t, token)
		})
	}
}

func TestCronAppEventValidationListAndLastRun(t *testing.T) {
	t.Parallel()
	f, record := cronAppFixture(t)
	replay, err := f.store.Execution().CreateCronTrigger(f.ctx, cronAppInput(t, f, "Daily"))
	require.NoError(t, err)
	require.Equal(t, record.ID, replay.ID)
	require.False(t, replay.Created)
	input := cronAppInput(t, f, "Daily")
	var settings map[string]any
	require.NoError(t, json.Unmarshal(input.Target.Settings, &settings))
	input.Target.Settings, err = json.MarshalIndent(settings, "", "  ")
	require.NoError(t, err)
	replay, err = f.store.Execution().CreateCronTrigger(f.ctx, input)
	require.NoError(t, err)
	require.Equal(t, record.ID, replay.ID, "JSON formatting does not change idempotency")
	input.Target.Settings = cronAppSettings(t, uuid.New(), "C123", "Opening", "Task")
	_, err = f.store.Execution().CreateCronTrigger(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
	for i, settings := range []string{
		`{}`, `null`, `[]`,
		string(cronAppSettings(t, f.profile.ID, "D123", "Opening", "Task")),
		string(cronAppSettings(t, f.profile.ID, "C123", "{{.missing}}", "Task")),
	} {
		input := cronAppInput(t, f, fmt.Sprintf("invalid%d", i))
		input.Target.Settings = json.RawMessage(settings)
		_, err := f.store.Execution().CreateCronTrigger(f.ctx, input)
		require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	}
	target := record.Target
	target.ID = uuid.New()
	_, err = f.store.Execution().UpdateCronTrigger(f.ctx, executionstore.UpdateCronTriggerInput{
		ProjectID: testProjectID, TriggerID: record.ID, Target: &target,
	})
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	claimed := claimCronApp(t, f, record.ID)
	queued, err := f.store.Execution().CreateCronTriggerAppEvent(f.ctx, claimed)
	require.NoError(t, err)
	require.True(t, queued)
	receipt, _ := cronAppReceipt(t, f, record.ID)
	page, err := f.store.Execution().
		ListCronTriggersForProject(f.ctx, executionstore.ListCronTriggersForProjectInput{
			ProjectID: testProjectID,
			Limit:     10,
			Filters:   executionstore.CronTriggerListFilters{AppID: f.app.ID},
		})
	require.NoError(t, err)
	require.Len(t, page.Triggers, 1)
	require.Equal(t, executionstore.CronTriggerLastRunQueued, page.Triggers[0].LastRun.State)
	page, err = f.store.Execution().
		ListCronTriggersForProject(f.ctx, executionstore.ListCronTriggersForProjectInput{
			ProjectID: testProjectID,
			Limit:     10,
			Filters:   executionstore.CronTriggerListFilters{AppID: uuid.New()},
		})
	require.NoError(t, err)
	require.Empty(t, page.Triggers)
	for _, state := range []string{"processing", "completed", "failed"} {
		_, err := f.store.pool.Exec(
			f.ctx,
			`UPDATE integration_inbox SET state=$2,
  claim_token=CASE WHEN $2='processing' THEN uuidv7() ELSE NULL END,
  claim_expires_at=CASE WHEN $2='processing' THEN now()+interval '1 minute' ELSE NULL END,
  completed_at=CASE WHEN $2 IN ('completed','failed') THEN now() ELSE NULL END,
  last_error='private provider error' WHERE id=$1`,
			receipt,
			state,
		)
		require.NoError(t, err)
		current, err := f.store.Execution().GetCronTrigger(f.ctx, testProjectID, record.ID)
		require.NoError(t, err)
		require.Equal(t, state, string(current.LastRun.State))
		if state == "failed" {
			require.Equal(t, "Scheduled app action failed.", *current.LastRun.FailureMessage)
		} else {
			require.Nil(t, current.LastRun.FailureMessage)
		}
	}
	// A surviving older failure must never replace expired exact diagnostics.
	_, err = f.store.pool.Exec(
		f.ctx,
		`INSERT INTO integration_inbox(project_id,app_id,receipt_key,payload,source,state,completed_at)
  VALUES ($1,$2,'older',convert_to('{}','UTF8'),'scheduled','failed',now())`,
		testProjectID,
		f.app.ID,
	)
	require.NoError(t, err)
	_, err = f.store.pool.Exec(f.ctx, `DELETE FROM integration_inbox WHERE id=$1`, receipt)
	require.NoError(t, err)
	current, err := f.store.Execution().GetCronTrigger(f.ctx, testProjectID, record.ID)
	require.NoError(t, err)
	require.Nil(t, current.LastRun)
	// Wrong app and provider source cannot masquerade as the pointed event.
	other := f.createApp(t, "other-app")
	for _, appID := range []uuid.UUID{other.ID, f.app.ID} {
		var wrong uuid.UUID
		require.NoError(
			t,
			f.store.pool.QueryRow(f.ctx,
				`INSERT INTO integration_inbox(project_id,app_id,receipt_key,payload)
  VALUES ($1,$2,$3,convert_to('{}','UTF8')) RETURNING id`, testProjectID, appID, uuid.NewString()).
				Scan(&wrong),
		)
		if appID != f.app.ID {
			_, err = f.store.pool.Exec(
				f.ctx,
				`UPDATE integration_inbox SET source='scheduled' WHERE id=$1`,
				wrong,
			)
			require.NoError(t, err)
		}
		_, err = f.store.pool.Exec(
			f.ctx,
			`UPDATE cron_triggers SET last_app_receipt_id=$2 WHERE id=$1`,
			record.ID,
			wrong,
		)
		require.NoError(t, err)
		current, err = f.store.Execution().GetCronTrigger(f.ctx, testProjectID, record.ID)
		require.NoError(t, err)
		require.Nil(t, current.LastRun)
	}
}

func TestCronAppEventLeaseExpiresDuringHandoff(t *testing.T) {
	t.Parallel()
	f, record := cronAppFixture(t)
	claimed := claimCronApp(t, f, record.ID)
	// Expire the lease after insertion, before its diagnostic pointer and cron
	// completion. Rollback must remove both the receipt and this injected expiry.
	_, err := f.store.pool.Exec(
		f.ctx,
		`CREATE FUNCTION expire_app_handoff() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE cron_triggers SET claimed_until=clock_timestamp()-interval '1 second' WHERE id=(convert_from(NEW.payload,'UTF8')::jsonb->>'trigger_id')::uuid; RETURN NEW; END $$; CREATE TRIGGER expire_app_handoff AFTER INSERT ON integration_inbox FOR EACH ROW EXECUTE FUNCTION expire_app_handoff()`,
	)
	require.NoError(t, err)
	queued, err := f.store.Execution().CreateCronTriggerAppEvent(f.ctx, claimed)
	require.NoError(t, err)
	require.False(t, queued)
	var count int
	require.NoError(
		t,
		f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM integration_inbox WHERE app_id=$1`, f.app.ID).Scan(&count),
	)
	require.Zero(t, count)
	current, err := f.store.Execution().GetCronTrigger(f.ctx, testProjectID, record.ID)
	require.NoError(t, err)
	require.Nil(t, current.LastRun)
	require.Nil(t, current.LastFiredAt)
	require.Nil(t, current.FailureReport)
	require.True(t, current.NextFireAfter.Equal(claimed.DueAt))
}

func TestCronAppEventInvalidTimezoneAfterClaimDoesNotRetry(t *testing.T) {
	t.Parallel()
	f, record := cronAppFixture(t)
	claimed := claimCronApp(t, f, record.ID)
	// API edits and claiming validate timezone. Exercise the defensive fallback
	// for a stored value becoming invalid after the claim, without changing due.
	_, err := f.store.pool.Exec(f.ctx, `UPDATE cron_triggers SET timezone='Invalid/Timezone' WHERE id=$1`, record.ID)
	require.NoError(t, err)
	queued, err := f.store.Execution().CreateCronTriggerAppEvent(f.ctx, claimed)
	require.NoError(t, err)
	require.False(t, queued)
	current, err := f.store.Execution().GetCronTrigger(f.ctx, testProjectID, record.ID)
	require.NoError(t, err)
	require.NotNil(t, current.FailureReport)
	require.Equal(t, "Scheduled app action skipped: invalid timezone.", current.FailureReport.Message)
	require.False(t, current.FailureReport.WillRetry)
	require.False(t, current.Enabled)
	require.Nil(t, current.NextFireAfter)
	require.Nil(t, current.LastFiredAt)
	var claimToken *uuid.UUID
	require.NoError(t, f.store.pool.QueryRow(f.ctx,
		`SELECT claim_token FROM cron_triggers WHERE id=$1`, record.ID).Scan(&claimToken))
	require.Nil(t, claimToken)
	var count int
	require.NoError(t, f.store.pool.QueryRow(f.ctx,
		`SELECT count(*) FROM integration_inbox WHERE app_id=$1`, f.app.ID).Scan(&count))
	require.Zero(t, count)
}

func TestCronAppEventProjectScopeAndProfileIndependence(t *testing.T) {
	t.Parallel()
	f, record := cronAppFixture(t)
	input := cronAppInput(t, f, "foreign-app")
	input.ProjectID = seedAdditionalProjectForTest(t, f.ctx, f.store.pool, "foreign-cron-app")
	_, err := f.store.Execution().CreateCronTrigger(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrNotFound)

	// Saving and handing off app settings validates their syntax, not referenced
	// resource availability. Runtime app handling owns that check.
	input = cronAppInput(t, f, "unavailable-profile")
	input.Target.Settings = cronAppSettings(t, uuid.New(), "C123", "Opening", "Task")
	created, err := f.store.Execution().CreateCronTrigger(f.ctx, input)
	require.NoError(t, err)
	claimed := claimCronApp(t, f, created.ID)
	queued, err := f.store.Execution().CreateCronTriggerAppEvent(f.ctx, claimed)
	require.NoError(t, err)
	require.True(t, queued)

	// A concurrent profile edit must not block either create or handoff.
	claimed = claimCronApp(t, f, record.ID)
	tx, err := f.store.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(f.ctx) }()
	_, err = tx.Exec(f.ctx, `SELECT id FROM agent_profiles WHERE id=$1 FOR UPDATE`, f.profile.ID)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(f.ctx, 3*time.Second)
	defer cancel()
	_, err = f.store.Execution().CreateCronTrigger(ctx, cronAppInput(t, f, "profile-being-edited"))
	require.NoError(t, err)
	queued, err = f.store.Execution().CreateCronTriggerAppEvent(ctx, claimed)
	require.NoError(t, err)
	require.True(t, queued)
}

func TestCronTriggerAppAndNormalContentContracts(t *testing.T) {
	t.Parallel()
	f, appTrigger := cronAppFixture(t)
	require.Empty(t, appTrigger.MessageTemplate)
	var profileID, agentID *uuid.UUID
	var message *string
	var settings []byte
	require.NoError(t, f.store.pool.QueryRow(f.ctx,
		`SELECT agent_profile_id,agent_id,message_template,app_settings FROM cron_triggers WHERE id=$1`,
		appTrigger.ID).Scan(&profileID, &agentID, &message, &settings))
	require.Nil(t, profileID)
	require.Nil(t, agentID)
	require.Nil(t, message)
	require.JSONEq(t, string(appTrigger.Target.Settings), string(settings))

	input := cronAppInput(t, f, "invalid-app-message")
	input.MessageTemplate = "Task outside settings"
	_, err := f.store.Execution().CreateCronTrigger(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	_, err = f.store.Execution().UpdateCronTrigger(f.ctx, executionstore.UpdateCronTriggerInput{
		ProjectID: testProjectID, TriggerID: appTrigger.ID, MessageTemplate: &input.MessageTemplate,
	})
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)

	input = executionstore.CreateCronTriggerInput{
		ProjectID: testProjectID, Name: "ordinary-profile", Enabled: true,
		CronExpression: "0 9 * * *", MessageTemplate: "Task {{.trigger.name}}",
		Target: executionstore.CronTriggerTarget{Kind: executionstore.CronTriggerTargetAgentProfile, ID: f.profile.ID},
	}
	normal, err := f.store.Execution().CreateCronTrigger(f.ctx, input)
	require.NoError(t, err)
	require.Equal(t, input.MessageTemplate, normal.MessageTemplate)
	require.Nil(t, normal.Target.Settings)
	input.Target.Settings = appTrigger.Target.Settings
	_, err = f.store.Execution().CreateCronTrigger(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	_, err = f.store.Execution().UpdateCronTrigger(f.ctx, executionstore.UpdateCronTriggerInput{
		ProjectID: testProjectID, TriggerID: normal.ID, Target: &input.Target,
	})
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	page, err := f.store.Execution().ListCronTriggersForProject(f.ctx, executionstore.ListCronTriggersForProjectInput{
		ProjectID: testProjectID, Limit: 10,
		Filters: executionstore.CronTriggerListFilters{AgentProfileID: f.profile.ID},
	})
	require.NoError(t, err)
	require.Len(t, page.Triggers, 1, "app settings do not make the schedule a profile-owned target")
	require.Equal(t, normal.ID, page.Triggers[0].ID)

	// The migration enforces the same ownership/content boundaries for all writers,
	// while retaining enabled/next-fire and ordinary profile delivery constraints.
	for _, test := range []struct {
		name string
		id   uuid.UUID
		sql  string
	}{
		{"app with profile", appTrigger.ID, `UPDATE cron_triggers SET agent_profile_id=$2 WHERE id=$1`},
		{"app with message", appTrigger.ID, `UPDATE cron_triggers SET message_template='task' WHERE id=$1`},
		{"app without settings", appTrigger.ID, `UPDATE cron_triggers SET app_settings=NULL WHERE id=$1`},
		{"app with JSON null", appTrigger.ID, `UPDATE cron_triggers SET app_settings='null' WHERE id=$1`},
		{"app with array", appTrigger.ID, `UPDATE cron_triggers SET app_settings='[]' WHERE id=$1`},
		{"app steering", appTrigger.ID, `UPDATE cron_triggers SET delivery_mode='steering' WHERE id=$1`},
		{"app without due time", appTrigger.ID, `UPDATE cron_triggers SET next_fire_after=NULL WHERE id=$1`},
		{"profile without message", normal.ID, `UPDATE cron_triggers SET message_template=NULL WHERE id=$1`},
		{"profile empty message", normal.ID, `UPDATE cron_triggers SET message_template='' WHERE id=$1`},
		{"profile app settings", normal.ID, `UPDATE cron_triggers SET app_settings='{}' WHERE id=$1`},
		{"profile app receipt", normal.ID, `UPDATE cron_triggers SET last_app_receipt_id=uuidv7() WHERE id=$1`},
		{"profile steering", normal.ID, `UPDATE cron_triggers SET delivery_mode='steering' WHERE id=$1`},
		{"profile without due time", normal.ID, `UPDATE cron_triggers SET next_fire_after=NULL WHERE id=$1`},
		{"no target", normal.ID, `UPDATE cron_triggers SET agent_profile_id=NULL WHERE id=$1`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			args := []any{test.id}
			if test.name == "app with profile" {
				args = append(args, f.profile.ID)
			}
			_, err := f.store.pool.Exec(f.ctx, test.sql, args...)
			var constraint *pgconn.PgError
			require.ErrorAs(t, err, &constraint)
			require.Equal(t, "23514", constraint.Code)
		})
	}
}
