//go:build integration

package executionstore_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestArchiveAgentDoesNotLockUnchangedActorRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"actor-lock-contention@example.com",
		"Actor Lock Contention",
	)
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t,
		ctx,
		store,
		"actor-lock-contention",
		"Actor Lock Contention",
		`
instruction: Investigate.
model:
  provider_config: openai-prod
  name: gpt-test
`,
	)
	launchAgent := func(key string) uuid.UUID {
		launch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: key,
		})
		require.NoError(t, err, "launch agent %s", key)
		return launch.Agent.ID
	}
	first := launchAgent("actor-lock-contention-1")
	second := launchAgent("actor-lock-contention-2")

	actors, err := store.Execution().ListActors(
		ctx,
		executionstore.ListActorsInput{ProjectID: testProjectID},
	)
	require.NoError(t, err, "list actors")
	require.Len(t, actors, 1, "launches must share one actor identity")

	_, err = pool.Exec(ctx, `UPDATE actors SET display_name = 'stale' WHERE id = $1`, actors[0].ID)
	require.NoError(t, err, "make stored actor display name stale")
	_, _, err = store.Execution().ArchiveAgent(ctx, testProjectID, first, userPrincipal(user.ID))
	require.NoError(t, err, "archive first agent")
	refreshed, err := store.Execution().GetActor(ctx, testProjectID, actors[0].ID)
	require.NoError(t, err, "get refreshed actor")
	require.Equal(t, user.DisplayName, refreshed.DisplayName, "stale actor display name must be rewritten")

	holder := integrationdb.BeginTx(t, ctx, pool)
	var lockedActorID uuid.UUID
	require.NoError(
		t,
		holder.QueryRow(
			ctx,
			`SELECT id FROM actors WHERE id = $1 FOR NO KEY UPDATE`,
			actors[0].ID,
		).Scan(&lockedActorID),
		"hold shared actor row lock",
	)

	archived := integrationdb.RunAsyncError(func() error {
		_, _, archiveErr := store.Execution().ArchiveAgent(
			ctx,
			testProjectID,
			second,
			userPrincipal(user.ID),
		)
		return archiveErr
	})
	require.NoError(
		t,
		integrationdb.Await(t, archived, "archive while the shared actor row is locked"),
		"archive must resolve an unchanged actor without waiting on its row lock",
	)
}
