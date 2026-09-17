//go:build integration

package executionstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestChannelBehaviorProfileDeletionRace(t *testing.T) {
	t.Parallel()
	for _, registrationFirst := range []bool{false, true} {
		name := "deletion first"
		if registrationFirst {
			name = "registration first"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			pool := openIntegrationDB(t, ctx)
			seedMigratedDB(t, ctx, pool)
			store := newSecretIntegrationStore(pool)
			_, agent, _, install := createChannelLifecycleFixture(t, ctx, store, "behavior-profile-race")
			blocker, err := pool.BeginTx(ctx, pgx.TxOptions{})
			require.NoError(t, err)
			defer func() { _ = blocker.Rollback(context.WithoutCancel(ctx)) }()
			_, err = blocker.Exec(ctx, `SELECT id FROM agent_profiles WHERE id = $1 FOR UPDATE`, agent.AgentProfileID)
			require.NoError(t, err)
			registration := make(chan error, 1)
			deletion := make(chan error, 1)
			register := func() {
				_, err := store.Integrations().CreateIntegrationRoute(ctx, integrationstore.CreateIntegrationRouteInput{
					ProjectID: testProjectID, IntegrationInstallID: install.ID, AgentProfileID: agent.AgentProfileID,
					DeploymentKey: "conversation", BehaviorKey: "conversation",
				})
				registration <- err
			}
			remove := func() {
				deletion <- store.Execution().DeleteAgentProfile(ctx, testProjectID, agent.AgentProfileID)
			}
			if registrationFirst {
				go register()
			} else {
				go remove()
			}
			integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockAgentProfile", 1)
			if registrationFirst {
				go remove()
			} else {
				go register()
			}
			integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockAgentProfile", 2)
			require.NoError(t, blocker.Commit(ctx))
			registrationErr, deletionErr := <-registration, <-deletion
			if registrationFirst {
				require.NoError(t, registrationErr)
				require.ErrorIs(t, deletionErr, storeerr.ErrConflict)
				_, err := pool.Exec(ctx, `UPDATE integration_installs SET state = 'disabled' WHERE id = $1`, install.ID)
				require.NoError(t, err)
				require.ErrorIs(t, store.Execution().DeleteAgentProfile(
					ctx, testProjectID, agent.AgentProfileID), storeerr.ErrConflict,
					"retained route profile must remain valid when the installation reconnects")
			} else {
				require.NoError(t, deletionErr)
				require.ErrorIs(t, registrationErr, storeerr.ErrNotFound)
			}
		})
	}
}
