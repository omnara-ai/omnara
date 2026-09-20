//go:build integration

package executionstore_test

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/crontrigger"
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
	record, err := store.Execution().CreateCronTrigger(ctx, cronAppInput(f, "Daily"))
	require.NoError(t, err)
	return f, record
}

func cronAppInput(f appInteractionFixture, name string) executionstore.CreateCronTriggerInput {
	return executionstore.CreateCronTriggerInput{
		ProjectID:       testProjectID,
		Name:            name,
		Enabled:         true,
		CronExpression:  "0 9 * * *",
		Timezone:        "America/Los_Angeles",
		MessageTemplate: `Task {{.trigger.name}} {{.trigger.local_date}}`,
		IdempotencyKey:  name,
		Target: executionstore.CronTriggerTarget{
			Kind: executionstore.CronTriggerTargetAppLaunch,
			ID:   f.app.ID,
			AppLaunch: &executionstore.CronAppLaunchTarget{
				ProfileID:              f.profile.ID,
				Destination:            json.RawMessage(`{"channel_id":"C123"}`),
				OpeningMessageTemplate: `Daily {{.trigger.local_date}}`,
			},
		},
	}
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
) (uuid.UUID, integrationstore.ScheduledAppLaunch) {
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
	var launch integrationstore.ScheduledAppLaunch
	require.NoError(t, json.Unmarshal(raw, &launch))
	return receipt, launch
}

func TestCronAppLaunchCurrentSnapshotReplayAndStats(t *testing.T) {
	t.Parallel()
	f, record := cronAppFixture(t)
	claimed := claimCronApp(t, f, record.ID)
	target := record.Target
	settings := *target.AppLaunch
	settings.Destination = json.RawMessage(`{"channel_id":"G456"}`)
	settings.OpeningMessageTemplate = `Edited {{.trigger.name}} {{.trigger.local_date}}`
	target.AppLaunch = &settings
	name, message := "Changed", `Current {{.trigger.local_date}}`
	_, err := f.store.Execution().
		UpdateCronTrigger(f.ctx, executionstore.UpdateCronTriggerInput{
			ProjectID:       testProjectID,
			TriggerID:       record.ID,
			Target:          &target,
			Name:            &name,
			MessageTemplate: &message,
		})
	require.NoError(t, err)
	other := createLaunchTestAgent(t, f.ctx, f.store, "new-profile-config", testAgentConfigYAML())
	_, err = f.store.Execution().
		RetargetAgentProfile(f.ctx, executionstore.RetargetAgentProfileInput{
			ProjectID:               testProjectID,
			ProfileID:               f.profile.ID,
			ExpectedCurrentConfigID: f.profile.CurrentConfigID,
			ConfigID:                other.CurrentConfigID,
		})
	require.NoError(t, err)
	queued, err := f.store.Execution().CreateCronTriggerAppLaunch(f.ctx, claimed)
	require.NoError(t, err)
	require.True(t, queued)
	receipt, launch := cronAppReceipt(t, f, record.ID)
	require.Equal(t, "Changed", launch.TriggerName)
	require.Equal(t, "Edited Changed 2026-03-08", launch.OpeningMessage)
	require.Equal(t, "Current 2026-03-08", launch.Message)
	require.Equal(t, other.CurrentConfigID, launch.ConfigID)
	require.JSONEq(t, `{"channel_id":"G456"}`, string(launch.Destination))
	require.True(t, launch.DueAt.Equal(claimed.DueAt))
	var key, source string
	require.NoError(
		t,
		f.store.pool.QueryRow(f.ctx, `SELECT receipt_key,source FROM integration_inbox WHERE id=$1`, receipt).
			Scan(&key, &source),
	)
	require.Equal(t, "scheduled_launch", source)
	require.Equal(t, "cron_trigger:"+record.ID.String()+":"+claimed.DueAt.UTC().Format(time.RFC3339), key)
	queued, err = f.store.Execution().CreateCronTriggerAppLaunch(f.ctx, claimed)
	require.NoError(t, err)
	require.False(t, queued)
	current, err := f.store.Execution().GetCronTrigger(f.ctx, testProjectID, record.ID)
	require.NoError(t, err)
	require.NotNil(t, current.LastFiredAt)
	require.NotNil(t, current.LastRun)
	require.Equal(t, executionstore.CronTriggerLastRunQueued, current.LastRun.State)
	// Later edits and disable do not rewrite the accepted snapshot.
	disabled := false
	_, err = f.store.Execution().
		UpdateCronTrigger(f.ctx, executionstore.UpdateCronTriggerInput{
			ProjectID: testProjectID,
			TriggerID: record.ID,
			Enabled:   &disabled,
		})
	require.NoError(t, err)
	_, again := cronAppReceipt(t, f, record.ID)
	require.Equal(t, launch, again)
	second, err := f.store.Execution().CreateCronTrigger(f.ctx, cronAppInput(f, "Second"))
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

func TestCronAppLaunchClaimFencingAndEdits(t *testing.T) {
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
			queued, err := f.store.Execution().CreateCronTriggerAppLaunch(f.ctx, claimed)
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

func TestCronAppLaunchRollback(t *testing.T) {
	t.Parallel()
	f, record := cronAppFixture(t)
	claimed := claimCronApp(t, f, record.ID)
	_, err := f.store.pool.Exec(
		f.ctx,
		`CREATE FUNCTION fail_app_handoff() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.last_app_receipt_id IS NOT NULL THEN RAISE EXCEPTION 'injected handoff failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_app_handoff BEFORE UPDATE ON cron_triggers FOR EACH ROW EXECUTE FUNCTION fail_app_handoff()`,
	)
	require.NoError(t, err)
	queued, err := f.store.Execution().CreateCronTriggerAppLaunch(f.ctx, claimed)
	require.Error(t, err)
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
	_, err = f.store.pool.Exec(f.ctx, `DROP TRIGGER fail_app_handoff ON cron_triggers`)
	require.NoError(t, err)
	queued, err = f.store.Execution().CreateCronTriggerAppLaunch(f.ctx, claimed)
	require.NoError(t, err)
	require.True(t, queued)
}

func TestCronAppLaunchLifecycle(t *testing.T) {
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
			queued, err := f.store.Execution().CreateCronTriggerAppLaunch(f.ctx, claimed)
			require.NoError(t, err)
			require.False(t, queued)
			current, err := f.store.Execution().GetCronTrigger(f.ctx, testProjectID, record.ID)
			if scenario != "disconnect" {
				require.ErrorIs(t, err, storeerr.ErrNotFound)
				return
			}
			require.NoError(t, err)
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

func TestCronAppLaunchValidationListAndLastRun(t *testing.T) {
	t.Parallel()
	f, record := cronAppFixture(t)
	replay, err := f.store.Execution().CreateCronTrigger(f.ctx, cronAppInput(f, "Daily"))
	require.NoError(t, err)
	require.Equal(t, record.ID, replay.ID)
	require.False(t, replay.Created)
	for i, destination := range []string{`{}`, `{"channel_id":"D123"}`, `{"channel_id":"C123","thread_ts":"1.2"}`} {
		input := cronAppInput(f, fmt.Sprintf("invalid%d", i))
		input.Target.AppLaunch.Destination = json.RawMessage(destination)
		_, err := f.store.Execution().CreateCronTrigger(f.ctx, input)
		require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	}
	target := record.Target
	settings := *target.AppLaunch
	settings.ProfileID = uuid.New()
	target.AppLaunch = &settings
	_, err = f.store.Execution().
		UpdateCronTrigger(f.ctx, executionstore.UpdateCronTriggerInput{
			ProjectID: testProjectID,
			TriggerID: record.ID,
			Target:    &target,
		})
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	claimed := claimCronApp(t, f, record.ID)
	queued, err := f.store.Execution().CreateCronTriggerAppLaunch(f.ctx, claimed)
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
	for _, state := range []string{"processing", "completed", "failed", "discarded"} {
		_, err := f.store.pool.Exec(
			f.ctx,
			`UPDATE integration_inbox SET state=$2, claim_token=CASE WHEN $2='processing' THEN uuidv7() ELSE NULL END, claim_expires_at=CASE WHEN $2='processing' THEN now()+interval '1 minute' ELSE NULL END, completed_at=CASE WHEN $2 IN ('completed','discarded') THEN now() ELSE NULL END, last_error='private provider error' WHERE id=$1`,
			receipt,
			state,
		)
		require.NoError(t, err)
		current, err := f.store.Execution().GetCronTrigger(f.ctx, testProjectID, record.ID)
		require.NoError(t, err)
		want := map[string]string{
			"processing": "preparing", "completed": "launched", "failed": "failed", "discarded": "discarded",
		}[state]
		require.Equal(t, want, string(current.LastRun.State))
		if state == "failed" {
			require.Equal(t, "Scheduled app launch failed.", *current.LastRun.FailureMessage)
		} else {
			require.Nil(t, current.LastRun.FailureMessage)
		}
	}
	// A surviving older failure must never replace expired exact diagnostics.
	_, err = f.store.pool.Exec(
		f.ctx,
		`INSERT INTO integration_inbox(project_id,app_id,receipt_key,payload,source,state) VALUES ($1,$2,'older',convert_to('{}','UTF8'),'scheduled_launch','failed')`,
		testProjectID,
		f.app.ID,
	)
	require.NoError(t, err)
	_, err = f.store.pool.Exec(f.ctx, `DELETE FROM integration_inbox WHERE id=$1`, receipt)
	require.NoError(t, err)
	current, err := f.store.Execution().GetCronTrigger(f.ctx, testProjectID, record.ID)
	require.NoError(t, err)
	require.Nil(t, current.LastRun)
	// Wrong app and provider source cannot masquerade as the pointed launch.
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
				`UPDATE integration_inbox SET source='scheduled_launch' WHERE id=$1`,
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

func TestCronAppLaunchLeaseExpiresDuringHandoff(t *testing.T) {
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
	queued, err := f.store.Execution().CreateCronTriggerAppLaunch(f.ctx, claimed)
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

func TestCronAppLaunchInvalidTimezoneAfterClaimDoesNotRetry(t *testing.T) {
	t.Parallel()
	f, record := cronAppFixture(t)
	claimed := claimCronApp(t, f, record.ID)
	// API edits and claiming validate timezone. Exercise the defensive fallback
	// for a stored value becoming invalid after the claim, without changing due.
	_, err := f.store.pool.Exec(f.ctx, `UPDATE cron_triggers SET timezone='Invalid/Timezone' WHERE id=$1`, record.ID)
	require.NoError(t, err)
	queued, err := f.store.Execution().CreateCronTriggerAppLaunch(f.ctx, claimed)
	require.NoError(t, err)
	require.False(t, queued)
	current, err := f.store.Execution().GetCronTrigger(f.ctx, testProjectID, record.ID)
	require.NoError(t, err)
	require.NotNil(t, current.FailureReport)
	require.Equal(t, "Scheduled app launch skipped: invalid timezone.", current.FailureReport.Message)
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
