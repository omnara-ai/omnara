//go:build integration

package executionstore_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestLaunchProfileLocks(t *testing.T) {
	for _, operation := range []string{"launch", "retarget"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			pool := openIntegrationDB(t, ctx)
			seedMigratedDB(t, ctx, pool)
			store := newIntegrationStore(pool)
			user := mustCreateProjectDeveloperUser(t, ctx, store, "profile-lock@example.com", "Profile lock")
			profile := mustCreateConfigAndProfileBookmarkFromYAML(
				t, ctx, store, "profile-lock", "Profile lock", subagentParentYAML)
			config := mustCreateAgentConfigFromYAML(t, ctx, store,
				strings.Replace(subagentParentYAML, "Coordinate helpers.", "New helpers.", 1))
			held, release := make(chan struct{}), make(chan struct{})
			var seen atomic.Bool
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			trace := &integrationdb.QueryTrace{OnQuery: func(sql string) {
				if strings.HasPrefix(sql, "-- name: GetAgentProfile ") && seen.CompareAndSwap(false, true) {
					close(held)
					select {
					case <-release:
					case <-ctx.Done():
					}
				}
			}}
			poolConfig := pool.Config()
			poolConfig.ConnConfig.Tracer = trace
			traced, err := pgxpool.NewWithConfig(ctx, poolConfig)
			require.NoError(t, err)
			defer traced.Close()
			defer unblock()
			launching := newIntegrationStore(traced)
			input := executionstore.LaunchAgentInput{ProjectID: testProjectID, ProfileID: profile.ID,
				AgentConfigID: profile.CurrentConfigID, LaunchedBy: userPrincipal(user.ID), IdempotencyKey: "first"}
			first := integrationdb.RunAsync(func() (executionstore.LaunchAgentResult, error) {
				return launching.Execution().LaunchAgent(ctx, input)
			})
			select {
			case <-held:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if operation == "launch" {
				secondCtx, stop := context.WithTimeout(ctx, 2*time.Second)
				defer stop()
				secondInput := input
				secondInput.IdempotencyKey = "second"
				second, err := store.Execution().LaunchAgent(secondCtx, secondInput)
				require.NoError(t, err)
				require.True(t, second.Created)
				unblock()
				require.NoError(t, integrationdb.Await(t, first, "first launch").Err)
				return
			}
			retarget := integrationdb.RunAsync(func() (executionstore.AgentProfileRecord, error) {
				return store.Execution().RetargetAgentProfile(ctx, executionstore.RetargetAgentProfileInput{
					ProjectID: testProjectID, ProfileID: profile.ID,
					ExpectedCurrentConfigID: profile.CurrentConfigID, ConfigID: config.ID})
			})
			integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockAgentProfile", 1)
			unblock()
			launched := integrationdb.Await(t, first, "launch before retarget")
			require.NoError(t, launched.Err)
			require.Equal(t, profile.CurrentConfigID, launched.Value.Agent.CurrentConfigID)
			updated := integrationdb.Await(t, retarget, "retarget after launch")
			require.NoError(t, updated.Err)
			input.AgentConfigID = updated.Value.CurrentConfigID
			input.IdempotencyKey = "after-retarget"
			after, err := store.Execution().LaunchAgent(ctx, input)
			require.NoError(t, err)
			require.Equal(t, config.ID, after.Agent.CurrentConfigID)
			require.NoError(t, store.Execution().DeleteAgentProfile(ctx, testProjectID, profile.ID))
			input.IdempotencyKey = "after-delete"
			_, err = store.Execution().LaunchAgent(ctx, input)
			require.ErrorIs(t, err, storeerr.ErrNotFound)
		})
	}
}
