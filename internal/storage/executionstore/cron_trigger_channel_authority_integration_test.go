//go:build integration

package executionstore_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/crontrigger"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestCronDisabledChannelAuthorityFailsOneFiringAndCanRecover(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"app", "install"} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			f := newChannelAuthorityFixture(t, ctx, "cron-disabled-"+scope)
			var profileID uuid.UUID
			require.NoError(t, f.Store.pool.QueryRow(ctx,
				`SELECT agent_profile_id FROM agents WHERE id=$1`, f.AgentID).Scan(&profileID))
			created, err := f.Store.Execution().CreateCronTrigger(ctx, executionstore.CreateCronTriggerInput{
				ProjectID: testProjectID, Name: "Cron with managed channel", Enabled: true,
				Target:         executionstore.CronTriggerTarget{Kind: executionstore.CronTriggerTargetAgentProfile, ID: profileID},
				CronExpression: "0 9 * * *", Timezone: "UTC", MessageTemplate: "Scheduled message.",
				IdempotencyKey: "disabled-authority-cron",
				ChannelBindings: []executionstore.LaunchChannelBinding{{
					ChannelID: f.Target.ID, Grants: integrationstore.ChannelGrants{SendAllowed: true},
				}},
			})
			require.NoError(t, err)
			_, err = f.Store.pool.Exec(ctx, `UPDATE cron_triggers
SET next_fire_after=transaction_timestamp()-interval '1 minute' WHERE id=$1`, created.ID)
			require.NoError(t, err)
			stateQuery, authorityID := `UPDATE integration_apps SET state=$2 WHERE id=$1`, f.AppID
			if scope == "install" {
				stateQuery, authorityID = `UPDATE integration_installs SET state=$2 WHERE id=$1`, f.InstallID
			}
			_, err = f.Store.pool.Exec(ctx, stateQuery, authorityID, "disabled")
			require.NoError(t, err)
			service := crontrigger.NewService(f.Store.Execution(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
			stats, err := service.FireDueTriggers(ctx)
			require.NoError(t, err)
			require.Equal(t, crontrigger.FireStats{Claimed: 1, Failures: 1}, stats)
			failed, err := f.Store.Execution().GetCronTrigger(ctx, testProjectID, created.ID)
			require.NoError(t, err)
			require.NotNil(t, failed.FailureReport)
			require.False(t, failed.FailureReport.WillRetry, "disabled authority is permanent for this firing")
			require.Contains(t, failed.FailureReport.Message, storeerr.ErrNotFound.Error())
			require.Nil(t, failed.LastFiredAt)
			var released, advanced bool
			require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT claim_token IS NULL AND claimed_until IS NULL,
next_fire_after > transaction_timestamp() FROM cron_triggers WHERE id=$1`, created.ID).Scan(&released, &advanced))
			require.True(t, released)
			require.True(t, advanced)
			var launches, grants int
			require.NoError(t, f.Store.pool.QueryRow(ctx,
				`SELECT count(*) FROM agents WHERE id<>$1`, f.AgentID).Scan(&launches))
			require.NoError(t, f.Store.pool.QueryRow(ctx,
				`SELECT count(*) FROM integration_target_bindings WHERE integration_target_id=$1`, f.Target.ID).Scan(&grants))
			require.Zero(t, launches)
			require.Zero(t, grants)
			stats, err = service.FireDueTriggers(ctx)
			require.NoError(t, err)
			require.Zero(t, stats.Claimed, "the failed occurrence is not queued for another attempt")
			_, err = f.Store.pool.Exec(ctx, stateQuery, authorityID, "active")
			require.NoError(t, err)
			_, err = f.Store.pool.Exec(ctx, `UPDATE cron_triggers
SET next_fire_after=transaction_timestamp()-interval '1 minute' WHERE id=$1`, created.ID)
			require.NoError(t, err)
			stats, err = service.FireDueTriggers(ctx)
			require.NoError(t, err)
			require.Equal(t, crontrigger.FireStats{Claimed: 1, Launched: 1}, stats)
			completed, err := f.Store.Execution().GetCronTrigger(ctx, testProjectID, created.ID)
			require.NoError(t, err)
			require.NotNil(t, completed.LastFiredAt)
			require.Nil(t, completed.FailureReport)
		})
	}
}
