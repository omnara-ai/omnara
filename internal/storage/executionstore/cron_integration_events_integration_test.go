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

func cronIntegrationFixture(t *testing.T) (integrationInteractionFixture, executionstore.CronTriggerRecord) {
	t.Helper()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := createIntegrationProjectAdmin(t, ctx, store, "cron-integration@example.com")
	profile := createIntegrationTestProfile(t, ctx, store, "cron-integration")
	f := integrationInteractionFixture{ctx: ctx, store: store, user: user, profile: profile}
	f.integration = f.createIntegration(t, "cron-chat")
	record, err := store.Execution().CreateCronTrigger(ctx, cronIntegrationInput(t, f, "Daily"))
	require.NoError(t, err)
	return f, record
}

func cronIntegrationInput(
	t *testing.T,
	f integrationInteractionFixture,
	name string,
) executionstore.CreateCronTriggerInput {
	t.Helper()
	return executionstore.CreateCronTriggerInput{
		ProjectID: testProjectID, Name: name, Enabled: true,
		CronExpression: "0 9 * * *", Timezone: "America/Los_Angeles", IdempotencyKey: name,
		Target: executionstore.CronTriggerTarget{
			Kind: executionstore.CronTriggerTargetIntegration, ID: f.integration.ID,
			Settings: cronIntegrationSettings(t, f.profile.ID, "C123", `Daily {{.trigger.local_date}}`,
				`Task {{.trigger.name}} {{.trigger.local_date}}`),
		},
	}
}

func cronIntegrationSettings(t *testing.T, profileID uuid.UUID, channel, opening, message string) json.RawMessage {
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

func claimCronIntegration(
	t *testing.T,
	f integrationInteractionFixture,
	id uuid.UUID,
) executionstore.ClaimedCronTrigger {
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

func cronIntegrationReceipt(
	t *testing.T,
	f integrationInteractionFixture,
	id uuid.UUID,
) (uuid.UUID, integrationstore.ScheduledIntegrationEvent) {
	t.Helper()
	var receipt uuid.UUID
	var raw []byte
	require.NoError(
		t,
		f.store.pool.QueryRow(f.ctx,
			`SELECT inbox.id, inbox.payload FROM cron_triggers cron
  JOIN integration_inbox inbox ON inbox.id=cron.last_integration_receipt_id WHERE cron.id=$1`, id).
			Scan(&receipt, &raw),
	)
	event, err := (integrationstore.IntegrationInboxRecord{
		Source: integrationstore.IntegrationInboxSourceScheduled, Payload: raw,
	}).ScheduledEvent()
	require.NoError(t, err)
	return receipt, event
}

func TestCronIntegrationEventCurrentSnapshotReplayAndStats(t *testing.T) {
	t.Parallel()
	f, record := cronIntegrationFixture(t)
	lastFired := time.Date(2026, 3, 7, 17, 0, 0, 0, time.UTC)
	_, err := f.store.pool.Exec(f.ctx, `UPDATE cron_triggers SET last_fired_at=$2 WHERE id=$1`, record.ID, lastFired)
	require.NoError(t, err)
	claimed := claimCronIntegration(t, f, record.ID)
	other := createIntegrationTestProfile(t, f.ctx, f.store, "other-cron-profile")
	target := record.Target
	target.Settings = cronIntegrationSettings(t, other.ID, "G456",
		`Edited {{.trigger.name}} {{.trigger.local_date}}`, `Current {{.trigger.local_date}}`)
	name := "Changed"
	_, err = f.store.Execution().UpdateCronTrigger(f.ctx, executionstore.UpdateCronTriggerInput{
		ProjectID: testProjectID, TriggerID: record.ID, Target: &target, Name: &name,
	})
	require.NoError(t, err)
	queued, err := f.store.Execution().CreateCronTriggerIntegrationEvent(f.ctx, claimed)
	require.NoError(t, err)
	require.True(t, queued)
	receipt, event := cronIntegrationReceipt(t, f, record.ID)
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
	queued, err = f.store.Execution().CreateCronTriggerIntegrationEvent(f.ctx, claimed)
	require.NoError(t, err)
	require.False(t, queued)
	current, err := f.store.Execution().GetCronTrigger(f.ctx, testProjectID, record.ID)
	require.NoError(t, err)
	require.NotNil(t, current.LastFiredAt)
	require.NotNil(t, current.LastRun)
	require.Equal(t, executionstore.CronTriggerLastRunQueued, current.LastRun.State)
	disabled := false
	target.Settings = cronIntegrationSettings(t, f.profile.ID, "C123", "Future opening", "Future task")
	_, err = f.store.Execution().
		UpdateCronTrigger(f.ctx, executionstore.UpdateCronTriggerInput{
			ProjectID: testProjectID,
			TriggerID: record.ID,
			Enabled:   &disabled,
			Target:    &target,
		})
	require.NoError(t, err)
	_, again := cronIntegrationReceipt(t, f, record.ID)
	require.Equal(t, event, again)
	second, err := f.store.Execution().CreateCronTrigger(f.ctx, cronIntegrationInput(t, f, "Second"))
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

func TestCronIntegrationEventClaimFencingAndEdits(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"disable", "reschedule", "token", "expired", "deleted"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f, record := cronIntegrationFixture(t)
			claimed := claimCronIntegration(t, f, record.ID)
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
			queued, err := f.store.Execution().CreateCronTriggerIntegrationEvent(f.ctx, claimed)
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
				f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM integration_inbox WHERE integration_id=$1`, f.integration.ID).
					Scan(&count),
			)
			require.Zero(t, count)
		})
	}
}

func TestCronIntegrationEventLifecycle(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"disconnect", "delete_integration", "delete_profile"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f, record := cronIntegrationFixture(t)
			claimed := claimCronIntegration(t, f, record.ID)
			switch scenario {
			case "disconnect":
				_, err := f.store.Integrations().
					DisconnectProjectIntegration(
						f.ctx,
						integrationstore.DisconnectProjectIntegrationInput{ProjectID: testProjectID, IntegrationID: f.integration.ID},
					)
				require.NoError(t, err)
			case "delete_integration":
				require.NoError(
					t,
					f.store.Integrations().DeleteProjectIntegration(f.ctx, testOrgID, testProjectID, f.integration.ID),
				)
			case "delete_profile":
				require.NoError(t, f.store.Execution().DeleteAgentProfile(f.ctx, testProjectID, f.profile.ID))
			}
			queued, err := f.store.Execution().CreateCronTriggerIntegrationEvent(f.ctx, claimed)
			require.NoError(t, err)
			current, err := f.store.Execution().GetCronTrigger(f.ctx, testProjectID, record.ID)
			if scenario == "delete_integration" {
				require.False(t, queued)
				require.ErrorIs(t, err, storeerr.ErrNotFound)
				return
			}
			require.NoError(t, err)
			if scenario == "delete_profile" {
				require.True(t, queued)
				require.True(t, current.Enabled)
				require.Nil(t, current.FailureReport)
				_, event := cronIntegrationReceipt(t, f, record.ID)
				require.JSONEq(t, string(record.Target.Settings), string(event.Settings))
				_, err = f.store.Execution().CreateCronTrigger(f.ctx, cronIntegrationInput(t, f, "deleted-profile"))
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

func TestCronIntegrationEventValidationListAndLastRun(t *testing.T) {
	t.Parallel()
	f, record := cronIntegrationFixture(t)
	replay, err := f.store.Execution().CreateCronTrigger(f.ctx, cronIntegrationInput(t, f, "Daily"))
	require.NoError(t, err)
	require.Equal(t, record.ID, replay.ID)
	require.False(t, replay.Created)
	input := cronIntegrationInput(t, f, "Daily")
	var settings map[string]any
	require.NoError(t, json.Unmarshal(input.Target.Settings, &settings))
	input.Target.Settings, err = json.MarshalIndent(settings, "", "  ")
	require.NoError(t, err)
	replay, err = f.store.Execution().CreateCronTrigger(f.ctx, input)
	require.NoError(t, err)
	require.Equal(t, record.ID, replay.ID, "JSON formatting does not change idempotency")
	input.Target.Settings = cronIntegrationSettings(t, uuid.New(), "C123", "Opening", "Task")
	_, err = f.store.Execution().CreateCronTrigger(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
	for i, settings := range []string{
		`{}`, `null`, `[]`,
		string(cronIntegrationSettings(t, f.profile.ID, "D123", "Opening", "Task")),
		string(cronIntegrationSettings(t, f.profile.ID, "C123", "{{.missing}}", "Task")),
	} {
		input := cronIntegrationInput(t, f, fmt.Sprintf("invalid%d", i))
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
	claimed := claimCronIntegration(t, f, record.ID)
	queued, err := f.store.Execution().CreateCronTriggerIntegrationEvent(f.ctx, claimed)
	require.NoError(t, err)
	require.True(t, queued)
	receipt, _ := cronIntegrationReceipt(t, f, record.ID)
	page, err := f.store.Execution().
		ListCronTriggersForProject(f.ctx, executionstore.ListCronTriggersForProjectInput{
			ProjectID: testProjectID,
			Limit:     10,
			Filters:   executionstore.CronTriggerListFilters{IntegrationID: f.integration.ID},
		})
	require.NoError(t, err)
	require.Len(t, page.Triggers, 1)
	require.Equal(t, executionstore.CronTriggerLastRunQueued, page.Triggers[0].LastRun.State)
	page, err = f.store.Execution().
		ListCronTriggersForProject(f.ctx, executionstore.ListCronTriggersForProjectInput{
			ProjectID: testProjectID,
			Limit:     10,
			Filters:   executionstore.CronTriggerListFilters{IntegrationID: uuid.New()},
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
			require.Equal(t, "Scheduled integration action failed.", *current.LastRun.FailureMessage)
		} else {
			require.Nil(t, current.LastRun.FailureMessage)
		}
	}
	_, err = f.store.pool.Exec(
		f.ctx,
		`INSERT INTO integration_inbox(project_id,integration_id,receipt_key,payload,source,state,completed_at)
  VALUES ($1,$2,'older',convert_to('{}','UTF8'),'scheduled','failed',now())`,
		testProjectID,
		f.integration.ID,
	)
	require.NoError(t, err)
	_, err = f.store.pool.Exec(f.ctx, `DELETE FROM integration_inbox WHERE id=$1`, receipt)
	require.NoError(t, err)
	current, err := f.store.Execution().GetCronTrigger(f.ctx, testProjectID, record.ID)
	require.NoError(t, err)
	require.Nil(t, current.LastRun)
	other := f.createIntegration(t, "other-integration")
	for _, integrationID := range []uuid.UUID{other.ID, f.integration.ID} {
		var wrong uuid.UUID
		require.NoError(
			t,
			f.store.pool.QueryRow(f.ctx,
				`INSERT INTO integration_inbox(project_id,integration_id,receipt_key,payload)
  VALUES ($1,$2,$3,convert_to('{}','UTF8')) RETURNING id`, testProjectID, integrationID, uuid.NewString()).
				Scan(&wrong),
		)
		if integrationID != f.integration.ID {
			_, err = f.store.pool.Exec(
				f.ctx,
				`UPDATE integration_inbox SET source='scheduled' WHERE id=$1`,
				wrong,
			)
			require.NoError(t, err)
		}
		_, err = f.store.pool.Exec(
			f.ctx,
			`UPDATE cron_triggers SET last_integration_receipt_id=$2 WHERE id=$1`,
			record.ID,
			wrong,
		)
		require.NoError(t, err)
		current, err = f.store.Execution().GetCronTrigger(f.ctx, testProjectID, record.ID)
		require.NoError(t, err)
		require.Nil(t, current.LastRun)
	}
}

func TestCronIntegrationEventLeaseExpiresDuringHandoff(t *testing.T) {
	t.Parallel()
	f, record := cronIntegrationFixture(t)
	claimed := claimCronIntegration(t, f, record.ID)
	_, err := f.store.pool.Exec(
		f.ctx,
		`CREATE FUNCTION expire_integration_handoff() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE cron_triggers SET claimed_until=clock_timestamp()-interval '1 second' WHERE id=(convert_from(NEW.payload,'UTF8')::jsonb->>'trigger_id')::uuid; RETURN NEW; END $$; CREATE TRIGGER expire_integration_handoff AFTER INSERT ON integration_inbox FOR EACH ROW EXECUTE FUNCTION expire_integration_handoff()`,
	)
	require.NoError(t, err)
	queued, err := f.store.Execution().CreateCronTriggerIntegrationEvent(f.ctx, claimed)
	require.NoError(t, err)
	require.False(t, queued)
	var count int
	require.NoError(
		t,
		f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM integration_inbox WHERE integration_id=$1`, f.integration.ID).Scan(
			&count,
		),
	)
	require.Zero(t, count)
	current, err := f.store.Execution().GetCronTrigger(f.ctx, testProjectID, record.ID)
	require.NoError(t, err)
	require.Nil(t, current.LastRun)
	require.Nil(t, current.LastFiredAt)
	require.Nil(t, current.FailureReport)
	require.True(t, current.NextFireAfter.Equal(claimed.DueAt))
}

func TestCronIntegrationEventInvalidTimezoneAfterClaimDoesNotRetry(t *testing.T) {
	t.Parallel()
	f, record := cronIntegrationFixture(t)
	claimed := claimCronIntegration(t, f, record.ID)
	_, err := f.store.pool.Exec(f.ctx, `UPDATE cron_triggers SET timezone='Invalid/Timezone' WHERE id=$1`, record.ID)
	require.NoError(t, err)
	queued, err := f.store.Execution().CreateCronTriggerIntegrationEvent(f.ctx, claimed)
	require.NoError(t, err)
	require.False(t, queued)
	current, err := f.store.Execution().GetCronTrigger(f.ctx, testProjectID, record.ID)
	require.NoError(t, err)
	require.NotNil(t, current.FailureReport)
	require.Equal(t, "Scheduled integration action skipped: invalid timezone.", current.FailureReport.Message)
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
		`SELECT count(*) FROM integration_inbox WHERE integration_id=$1`, f.integration.ID).Scan(&count))
	require.Zero(t, count)
}

func TestCronIntegrationEventProjectScopeAndProfileIndependence(t *testing.T) {
	t.Parallel()
	f, record := cronIntegrationFixture(t)
	input := cronIntegrationInput(t, f, "foreign-integration")
	input.ProjectID = seedAdditionalProjectForTest(t, f.ctx, f.store.pool, "foreign-cron-integration")
	_, err := f.store.Execution().CreateCronTrigger(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrNotFound)

	input = cronIntegrationInput(t, f, "unavailable-profile")
	input.Target.Settings = cronIntegrationSettings(t, uuid.New(), "C123", "Opening", "Task")
	created, err := f.store.Execution().CreateCronTrigger(f.ctx, input)
	require.NoError(t, err)
	claimed := claimCronIntegration(t, f, created.ID)
	queued, err := f.store.Execution().CreateCronTriggerIntegrationEvent(f.ctx, claimed)
	require.NoError(t, err)
	require.True(t, queued)

	claimed = claimCronIntegration(t, f, record.ID)
	tx, err := f.store.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(f.ctx) }()
	_, err = tx.Exec(f.ctx, `SELECT id FROM agent_profiles WHERE id=$1 FOR UPDATE`, f.profile.ID)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(f.ctx, 3*time.Second)
	defer cancel()
	_, err = f.store.Execution().CreateCronTrigger(ctx, cronIntegrationInput(t, f, "profile-being-edited"))
	require.NoError(t, err)
	queued, err = f.store.Execution().CreateCronTriggerIntegrationEvent(ctx, claimed)
	require.NoError(t, err)
	require.True(t, queued)
}

func TestCronTriggerIntegrationAndNormalContentContracts(t *testing.T) {
	t.Parallel()
	f, integrationTrigger := cronIntegrationFixture(t)
	require.Empty(t, integrationTrigger.MessageTemplate)
	var profileID, agentID *uuid.UUID
	var message *string
	var settings []byte
	require.NoError(t, f.store.pool.QueryRow(f.ctx,
		`SELECT agent_profile_id,agent_id,message_template,integration_settings FROM cron_triggers WHERE id=$1`,
		integrationTrigger.ID).Scan(&profileID, &agentID, &message, &settings))
	require.Nil(t, profileID)
	require.Nil(t, agentID)
	require.Nil(t, message)
	require.JSONEq(t, string(integrationTrigger.Target.Settings), string(settings))

	input := cronIntegrationInput(t, f, "invalid-integration-message")
	input.MessageTemplate = "Task outside settings"
	_, err := f.store.Execution().CreateCronTrigger(f.ctx, input)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	_, err = f.store.Execution().UpdateCronTrigger(f.ctx, executionstore.UpdateCronTriggerInput{
		ProjectID: testProjectID, TriggerID: integrationTrigger.ID, MessageTemplate: &input.MessageTemplate,
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
	input.Target.Settings = integrationTrigger.Target.Settings
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
	require.Len(t, page.Triggers, 1, "integration settings do not make the schedule a profile-owned target")
	require.Equal(t, normal.ID, page.Triggers[0].ID)

	for _, test := range []struct {
		name string
		id   uuid.UUID
		sql  string
	}{
		{"integration with profile", integrationTrigger.ID, `UPDATE cron_triggers SET agent_profile_id=$2 WHERE id=$1`},
		{"integration with message", integrationTrigger.ID, `UPDATE cron_triggers SET message_template='task' WHERE id=$1`},
		{
			"integration without settings",
			integrationTrigger.ID,
			`UPDATE cron_triggers SET integration_settings=NULL WHERE id=$1`,
		},
		{
			"integration with JSON null",
			integrationTrigger.ID,
			`UPDATE cron_triggers SET integration_settings='null' WHERE id=$1`,
		},
		{"integration with array", integrationTrigger.ID, `UPDATE cron_triggers SET integration_settings='[]' WHERE id=$1`},
		{"integration steering", integrationTrigger.ID, `UPDATE cron_triggers SET delivery_mode='steering' WHERE id=$1`},
		{"integration without due time", integrationTrigger.ID, `UPDATE cron_triggers SET next_fire_after=NULL WHERE id=$1`},
		{"profile without message", normal.ID, `UPDATE cron_triggers SET message_template=NULL WHERE id=$1`},
		{"profile empty message", normal.ID, `UPDATE cron_triggers SET message_template='' WHERE id=$1`},
		{"profile integration settings", normal.ID, `UPDATE cron_triggers SET integration_settings='{}' WHERE id=$1`},
		{
			"profile integration receipt",
			normal.ID,
			`UPDATE cron_triggers SET last_integration_receipt_id=uuidv7() WHERE id=$1`,
		},
		{"profile steering", normal.ID, `UPDATE cron_triggers SET delivery_mode='steering' WHERE id=$1`},
		{"profile without due time", normal.ID, `UPDATE cron_triggers SET next_fire_after=NULL WHERE id=$1`},
		{"no target", normal.ID, `UPDATE cron_triggers SET agent_profile_id=NULL WHERE id=$1`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			args := []any{test.id}
			if test.name == "integration with profile" {
				args = append(args, f.profile.ID)
			}
			_, err := f.store.pool.Exec(f.ctx, test.sql, args...)
			var constraint *pgconn.PgError
			require.ErrorAs(t, err, &constraint)
			require.Equal(t, "23514", constraint.Code)
		})
	}
}
