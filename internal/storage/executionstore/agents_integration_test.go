//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestAgentLaunchRequiresConfigAndCanRecordProfile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool)
	now := time.Date(2026, 4, 29, 15, 0, 0, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{Email: "agent-profile-launch@example.com", DisplayName: "Agent Profile Launch"},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "agent-profile-launch", "Launch Profile", `
name: Launch Profile
instruction: Start with the saved profile config.
model:
  provider_config: openai-prod
  name: profile-launch
`, now)

	launch, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		Message:        "start",
		IdempotencyKey: "idem-profile-launch",
	})
	if err != nil {
		t.Fatalf("launch with profile and config: %v", err)
	}
	if !launch.Created || launch.Agent.CurrentConfigID != profile.CurrentConfigID {
		t.Fatalf("unexpected launched agent/config: agent=%+v profile=%+v", launch.Agent, profile)
	}
	if launch.Agent.ProfileID != profile.ID {
		t.Fatalf("launched agent profile = %s, want %s", launch.Agent.ProfileID, profile.ID)
	}
	if launch.Agent.State != "active" {
		t.Fatalf("launched agent state = %s, want active", launch.Agent.State)
	}
	if launch.ConfigChange.AgentInput.InputKind != "config_change" || launch.ConfigChange.AgentInput.State != "resolved" ||
		launch.ConfigChange.AgentInput.AgentConfigID != launch.AgentConfig.ID {
		t.Fatalf("initial config change should be resolved with launch config: %+v", launch.ConfigChange.AgentInput)
	}
	events, err := store.Execution().ListAgentEventsForRead(ctx, testProjectID, launch.Agent.ID, 0, 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 || events[0].EventKind != "agent_input" {
		t.Fatalf("launch should append exactly initial config_change event, got %+v", events)
	}
	replayed, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		Message:        "changed request body",
		IdempotencyKey: "idem-profile-launch",
	})
	if err != nil {
		t.Fatalf("replay launch: %v", err)
	}
	requireCurrentAgentLaunchReplay(t, replayed, launch.Agent)
	if err := store.Execution().DeleteAgentProfile(ctx, testProjectID, profile.ID); err != nil {
		t.Fatalf("delete profile after launch: %v", err)
	}
	replayedAfterProfileDelete, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		Message:        "start",
		IdempotencyKey: "idem-profile-launch",
	})
	if err != nil {
		t.Fatalf("replay launch after profile delete: %v", err)
	}
	requireCurrentAgentLaunchReplay(t, replayedAfterProfileDelete, launch.Agent)
	configOnly, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "idem-config-only-launch",
	})
	if err != nil {
		t.Fatalf("launch config only: %v", err)
	}
	if configOnly.Agent.CurrentConfigID != profile.CurrentConfigID {
		t.Fatalf("config-only launch current config = %s, want %s", configOnly.Agent.CurrentConfigID, profile.CurrentConfigID)
	}
	if configOnly.Agent.ProfileID != executionstore.NilID {
		t.Fatalf("config-only launch profile = %s, want nil", configOnly.Agent.ProfileID)
	}
	if _, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "idem-missing-config-launch",
	}); err == nil || !strings.Contains(err.Error(), "agent config") {
		t.Fatalf("launch without config error = %v, want agent config required", err)
	}
}

func TestProjectScopedAgentStorageHidesCrossProjectResources(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	agentID := mustCreateAgent(t, ctx, store, now)
	otherProjectID := testID("cross_project_agent_storage")

	if _, err := store.Execution().ListAgentEventsForRead(ctx, otherProjectID, agentID, 0, 10); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("list cross-project events error = %v, want ErrNotFound", err)
	}
	if _, _, _, err := store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
		ProjectID:      otherProjectID,
		AgentID:        agentID,
		ContentBlocks:  json.RawMessage(`[{"type":"text","text":"cross-project input"}]`),
		IdempotencyKey: "cross-project-input",
	}); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("create cross-project agent input error = %v, want ErrNotFound", err)
	}
}

func TestSystemConfigChangeHasNoActor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool)
	now := time.Date(2026, 4, 29, 15, 15, 0, 0, time.UTC)
	configID := mustCreateAgentConfig(t, ctx, store, testProjectID, "system-config-sender", now)
	agent, err := store.Execution().CreateAgentFixture(ctx, executionstore.AgentFixtureInput{
		ProjectID:       testProjectID,
		CurrentConfigID: configID,
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	events, err := store.Execution().ListAgentEventsForRead(ctx, testProjectID, agent.ID, 0, 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 || events[0].EventKind != "agent_input" {
		t.Fatalf("create agent should append one config_change event, got %+v", events)
	}
	readEvents, err := store.Execution().ListAgentEventsForRead(ctx, testProjectID, agent.ID, 0, 10)
	if err != nil {
		t.Fatalf("list read events: %v", err)
	}
	if len(readEvents) != 1 || readEvents[0].ActorID != NilID {
		t.Fatalf("system config_change read record = %+v, want actorless", readEvents)
	}
}

func TestAgentIdentityIsImmutable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := NewStore(pool)
	agentID := mustCreateAgent(t, ctx, store, time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC))
	for _, column := range []string{"id", "org_id", "project_id"} {
		_, err := pool.Exec(
			ctx,
			fmt.Sprintf("UPDATE agents SET %s = $1 WHERE project_id = $2 AND id = $3", column),
			testID("immutable_agent_"+column),
			testProjectID,
			agentID,
		)
		var databaseError *pgconn.PgError
		if !errors.As(err, &databaseError) || databaseError.Code != "25006" {
			t.Fatalf("update agent %s error = %v, want immutable-identity SQLSTATE 25006", column, err)
		}
	}
	for _, update := range []string{
		"idempotency_key = 'changed'",
		"created_at = created_at + interval '1 second'",
	} {
		_, err := pool.Exec(
			ctx,
			fmt.Sprintf("UPDATE agents SET %s WHERE project_id = $1 AND id = $2", update),
			testProjectID,
			agentID,
		)
		var databaseError *pgconn.PgError
		if !errors.As(err, &databaseError) || databaseError.Code != "25006" {
			t.Fatalf("update agent with %q error = %v, want immutable-identity SQLSTATE 25006", update, err)
		}
	}

	var retainedID, retainedOrgID, retainedProjectID ID
	if err := pool.QueryRow(
		ctx,
		`SELECT id, org_id, project_id FROM agents WHERE id = $1`,
		agentID,
	).Scan(&retainedID, &retainedOrgID, &retainedProjectID); err != nil {
		t.Fatalf("read retained agent scope: %v", err)
	}
	if retainedID != agentID || retainedOrgID != testOrgID || retainedProjectID != testProjectID {
		t.Fatalf(
			"agent scope after rejected updates = (%s, %s, %s), want (%s, %s, %s)",
			retainedID,
			retainedOrgID,
			retainedProjectID,
			agentID,
			testOrgID,
			testProjectID,
		)
	}
}

func TestAgentConfigHistoryTablesAreImmutable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool)
	now := time.Date(2026, 4, 29, 15, 20, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "history-immutable@example.com", "History Immutable")
	profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "history-immutable", "History Immutable", `
name: History Immutable
instruction: Initial immutable history config.
model:
  provider_config: openai-prod
  name: history-immutable
`, now)
	launch, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-history-immutable-launch",
		},
	)
	if err != nil {
		t.Fatalf("launch agent: %v", err)
	}
	alternateConfig := mustCreateAgentConfigFromYAML(t, ctx, store, "history-immutable-alternate", `
name: History Immutable
instruction: Alternate immutable history config.
model:
  provider_config: openai-prod
  name: history-immutable
`, now.Add(2*time.Second))
	pinnedConfig, found, err := store.Execution().GetAgentConfig(ctx, testProjectID, profile.CurrentConfigID)
	if err != nil || !found {
		t.Fatalf("load pinned agent config: found=%v err=%v", found, err)
	}

	if _, err := pool.Exec(
		ctx,
		`UPDATE agent_configs SET source = source || 'mutated' WHERE project_id = $1 AND id = $2`,
		testProjectID,
		profile.CurrentConfigID,
	); !isPgCode(
		err,
		"25006",
	) {
		t.Fatalf("mutate agent config error = %v, want SQLSTATE 25006", err)
	}
	if _, err := pool.Exec(
		ctx,
		`DELETE FROM agent_configs WHERE project_id = $1 AND id = $2`,
		testProjectID,
		profile.CurrentConfigID,
	); !isPgCode(
		err,
		"25006",
	) {
		t.Fatalf("delete agent config error = %v, want SQLSTATE 25006", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE agent_profile_versions SET reason = 'mutated' WHERE project_id = $1 AND profile_id = $2 AND generation = 1`,
		testProjectID,
		profile.ID,
	); !isPgCode(
		err,
		"25006",
	) {
		t.Fatalf("mutate agent profile version error = %v, want SQLSTATE 25006", err)
	}
	if _, err := pool.Exec(
		ctx,
		`DELETE FROM agent_profile_versions WHERE project_id = $1 AND profile_id = $2 AND generation = 1`,
		testProjectID,
		profile.ID,
	); !isPgCode(
		err,
		"25006",
	) {
		t.Fatalf("direct delete agent profile version error = %v, want SQLSTATE 25006", err)
	}
	otherProfile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID:       testProjectID,
		Name:            "Other Immutable Profile",
		CurrentConfigID: alternateConfig.ID,
		IdempotencyKey:  "other-immutable-profile",
	})
	if err != nil {
		t.Fatalf("create other profile: %v", err)
	}
	var currentVersionID, otherVersionID ID
	if err := pool.QueryRow(
		ctx,
		`SELECT current_version_id FROM agent_profiles WHERE project_id = $1 AND id = $2`,
		testProjectID,
		profile.ID,
	).Scan(&currentVersionID); err != nil {
		t.Fatalf("read current profile version: %v", err)
	}
	if err := pool.QueryRow(
		ctx,
		`SELECT current_version_id FROM agent_profiles WHERE project_id = $1 AND id = $2`,
		testProjectID,
		otherProfile.ID,
	).Scan(&otherVersionID); err != nil {
		t.Fatalf("read other profile version: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE agent_profile_versions SET deleted_at = statement_timestamp()
		 WHERE project_id = $1 AND profile_id = $2 AND id = $3`,
		testProjectID,
		profile.ID,
		currentVersionID,
	); !isPgCode(err, "25006") {
		t.Fatalf("soft-delete current live profile version error = %v, want SQLSTATE 25006", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE agent_profiles SET current_version_id = $1 WHERE project_id = $2 AND id = $3`,
		otherVersionID,
		testProjectID,
		profile.ID,
	); !isPgCode(err, "23503") {
		t.Fatalf("point profile at another profile version error = %v, want foreign-key violation", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE agent_inputs SET agent_config_id = $1 WHERE project_id = $2 AND agent_id = $3 AND id = $4`,
		alternateConfig.ID,
		testProjectID,
		launch.Agent.ID,
		launch.ConfigChange.AgentInput.ID,
	); !isPgCode(
		err,
		"25006",
	) {
		t.Fatalf("mutate config-change input config error = %v, want SQLSTATE 25006", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE agent_inputs SET admitted_event_id = NULL WHERE project_id = $1 AND agent_id = $2 AND id = $3`,
		testProjectID,
		launch.Agent.ID,
		launch.ConfigChange.AgentInput.ID,
	); !isPgCode(
		err,
		"25006",
	) {
		t.Fatalf("mutate resolved config-change input event error = %v, want SQLSTATE 25006", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE agent_inputs SET input_rank = input_rank + 1 WHERE project_id = $1 AND agent_id = $2 AND id = $3`,
		testProjectID,
		launch.Agent.ID,
		launch.ConfigChange.AgentInput.ID,
	); !isPgCode(err, "25006") {
		t.Fatalf("mutate resolved config-change input rank error = %v, want SQLSTATE 25006", err)
	}
	pinnedConfiguredModel, err := store.Models().GetConfiguredModel(ctx, testOrgID, pinnedConfig.ConfiguredModelID)
	if err != nil {
		t.Fatalf("load pinned configured model: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE configured_model_revisions SET provider_model_slug = provider_model_slug || '-mutated' WHERE org_id = $1 AND configured_model_id = $2 AND id = $3`,
		testOrgID,
		pinnedConfiguredModel.ID,
		pinnedConfiguredModel.CurrentRevisionID,
	); !isPgCode(
		err,
		"25006",
	) {
		t.Fatalf("mutate configured model revision error = %v, want SQLSTATE 25006", err)
	}
	if _, err := pool.Exec(
		ctx,
		`DELETE FROM configured_model_revisions WHERE org_id = $1 AND configured_model_id = $2 AND id = $3`,
		testOrgID,
		pinnedConfiguredModel.ID,
		pinnedConfiguredModel.CurrentRevisionID,
	); !isPgCode(
		err,
		"25006",
	) {
		t.Fatalf("delete configured model revision error = %v, want SQLSTATE 25006", err)
	}
}

func TestDeleteAgentProfileCascadesVersionsButKeepsConfigsAndAgents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool)
	now := time.Date(2026, 4, 29, 15, 28, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "delete-profile@example.com", "Delete Profile")
	profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "delete-profile", "Delete Profile", `
name: Delete Profile
instruction: Deletable helper profile.
model:
  provider_config: openai-prod
  name: delete-profile
`, now)
	launch, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-delete-profile-launch",
		},
	)
	if err != nil {
		t.Fatalf("launch agent: %v", err)
	}
	if err := store.Execution().DeleteAgentProfile(ctx, testProjectID, profile.ID); err != nil {
		t.Fatalf("delete profile: %v", err)
	}
	if _, err := store.Execution().GetAgentProfile(ctx, testProjectID, profile.ID); !storeerr.IsNotFound(err) {
		t.Fatalf("deleted profile lookup error = %v, want not found", err)
	}
	var versionCount, deletedVersionCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*), count(*) FILTER (WHERE deleted_at IS NOT NULL)
FROM agent_profile_versions
WHERE project_id = $1 AND profile_id = $2
`, testProjectID, profile.ID).
		Scan(&versionCount, &deletedVersionCount); err != nil {
		t.Fatalf("count profile versions: %v", err)
	}
	if versionCount == 0 || deletedVersionCount != versionCount {
		t.Fatalf("profile versions were not soft-deleted: total=%d deleted=%d", versionCount, deletedVersionCount)
	}
	if _, err := store.Execution().GetAgentInProject(ctx, testProjectID, launch.Agent.ID); err != nil {
		t.Fatalf("launched agent should survive profile deletion: %v", err)
	}
	if _, found, err := store.Execution().GetAgentConfig(ctx, testProjectID, profile.CurrentConfigID); err != nil || !found {
		t.Fatalf("profile config should survive profile deletion: %v", err)
	}
	if err := store.Execution().DeleteAgentProfile(ctx, testProjectID, profile.ID); !storeerr.IsNotFound(err) {
		t.Fatalf("second delete profile error = %v, want not found", err)
	}
}

func TestDeleteAgentProfileSerializesWithRetarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 4, 29, 15, 29, 0, 0, time.UTC)
	profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "delete-retarget", "Delete Retarget", `
name: Delete Retarget
instruction: Initial profile.
model:
  provider_config: openai-prod
  name: delete-retarget
`, now)
	retargetConfig := mustCreateAgentConfigFromYAML(t, ctx, store, "delete-retarget-next", `
name: Delete Retarget
instruction: Retargeted profile.
model:
  provider_config: openai-prod
  name: delete-retarget
`, now.Add(time.Second))

	blockingTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin profile retarget blocker: %v", err)
	}
	defer func() { _ = blockingTx.Rollback(ctx) }()
	var blockingPID int32
	if err := blockingTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockingPID); err != nil {
		t.Fatalf("get profile retarget blocker backend: %v", err)
	}
	qtx := dbsqlc.New(blockingTx)
	if _, err := qtx.LockAgentProfile(
		ctx,
		dbsqlc.LockAgentProfileParams{ProjectID: testProjectID, ProfileID: profile.ID},
	); err != nil {
		t.Fatalf("lock profile for retarget: %v", err)
	}
	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- store.Execution().DeleteAgentProfile(context.Background(), testProjectID, profile.ID)
	}()
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, pool, "-- name: LockAgentProfile", blockingPID)

	if _, err := qtx.RetargetAgentProfile(ctx, dbsqlc.RetargetAgentProfileParams{
		CurrentConfigID:         retargetConfig.ID,
		ExpectedCurrentConfigID: profile.CurrentConfigID,
		ProjectID:               testProjectID,
		ProfileID:               profile.ID,
		Reason:                  "retarget",
	}); err != nil {
		t.Fatalf("retarget profile while delete waits: %v", err)
	}
	var releaseFloor time.Time
	if err := blockingTx.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&releaseFloor); err != nil {
		t.Fatalf("read profile lock release floor: %v", err)
	}
	if err := blockingTx.Commit(ctx); err != nil {
		t.Fatalf("commit profile retarget blocker: %v", err)
	}
	if err := <-deleteDone; err != nil {
		t.Fatalf("delete profile after retarget: %v", err)
	}

	var liveVersions int
	var deletedAt time.Time
	if err := pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE version.deleted_at IS NULL), profile.deleted_at
FROM agent_profiles profile
JOIN agent_profile_versions version
  ON version.project_id = profile.project_id AND version.profile_id = profile.id
WHERE profile.project_id = $1 AND profile.id = $2
GROUP BY profile.deleted_at
`, testProjectID, profile.ID).Scan(&liveVersions, &deletedAt); err != nil {
		t.Fatalf("read deleted profile lineage: %v", err)
	}
	if liveVersions != 0 {
		t.Fatalf("live profile versions after delete = %d, want 0", liveVersions)
	}
	if deletedAt.Before(releaseFloor) {
		t.Fatalf("profile deleted_at = %s, want at or after lock release floor %s", deletedAt, releaseFloor)
	}
}

func TestAgentLaunchSerializesWithProfileDeletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"launch-profile-delete@example.com",
		"Launch Profile Delete",
	)
	profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "launch-profile-delete", "Launch Profile Delete", `
name: Launch Profile Delete
instruction: Launch only from a live profile.
model:
  provider_config: openai-prod
  name: launch-profile-delete
`, now)

	blockingTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin profile-version blocker: %v", err)
	}
	defer func() { _ = blockingTx.Rollback(ctx) }()
	if _, err := blockingTx.Exec(ctx, `
SELECT id
FROM agent_profile_versions
WHERE project_id = $1 AND profile_id = $2 AND deleted_at IS NULL
FOR UPDATE
`, testProjectID, profile.ID); err != nil {
		t.Fatalf("lock profile version: %v", err)
	}
	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- store.Execution().DeleteAgentProfile(context.Background(), testProjectID, profile.ID)
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "DeleteAgentProfileVersions", 1)

	launchDone := make(chan error, 1)
	go func() {
		_, launchErr := store.Execution().LaunchAgent(context.Background(), executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "launch-profile-delete-race",
		})
		launchDone <- launchErr
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockAgentProfile", 1)

	if err := blockingTx.Commit(ctx); err != nil {
		t.Fatalf("release profile-version blocker: %v", err)
	}
	if err := <-deleteDone; err != nil {
		t.Fatalf("delete profile: %v", err)
	}
	if err := <-launchDone; !storeerr.IsNotFound(err) {
		t.Fatalf("launch after concurrent profile deletion error = %v, want not found", err)
	}
	var agents int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM agents WHERE project_id = $1 AND idempotency_key = $2`,
		testProjectID,
		"launch-profile-delete-race",
	).Scan(&agents); err != nil {
		t.Fatalf("count raced launches: %v", err)
	}
	if agents != 0 {
		t.Fatalf("agents created from deleted profile = %d, want 0", agents)
	}
}

func TestCreateAgentProfileIdempotentReplayReturnsExistingProfile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool)
	now := time.Date(2026, 4, 29, 15, 30, 0, 0, time.UTC)
	sourceYAML := `
name: Replay Profile
instruction: Profile creation should replay cleanly.
model:
  provider_config: openai-prod
  name: profile-replay
`
	config := mustCreateAgentConfigFromYAML(t, ctx, store, "profile-replay", sourceYAML, now)
	input := executionstore.CreateAgentProfileInput{
		ProjectID:       testProjectID,
		Name:            "Replay Profile",
		CurrentConfigID: config.ID,
		IdempotencyKey:  "profile-replay",
	}
	first, err := store.Execution().CreateAgentProfile(ctx, input)
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	replayed, err := store.Execution().CreateAgentProfile(ctx, input)
	if err != nil {
		t.Fatalf("replay create profile: %v", err)
	}
	if replayed.Created {
		t.Fatalf("replayed profile should not be marked created")
	}
	if replayed.ID != first.ID || replayed.CurrentConfigID != first.CurrentConfigID ||
		replayed.CurrentConfig.ID != first.CurrentConfig.ID {
		t.Fatalf("unexpected replayed profile: first=%+v replayed=%+v", first, replayed)
	}
	duplicateNameInput := input
	duplicateNameInput.IdempotencyKey = "profile-replay-duplicate-name"
	if _, err := store.Execution().CreateAgentProfile(ctx, duplicateNameInput); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("duplicate profile name error = %v, want ErrConflict", err)
	}
	retargetYAML := `
name: Replay Profile
instruction: Retargeted profile head.
model:
  provider_config: openai-prod
  name: profile-replay
`
	retargetConfig := mustCreateAgentConfigFromYAML(
		t,
		ctx,
		store,
		"profile-replay-retarget",
		retargetYAML,
		now.Add(2*time.Second),
	)
	retargeted, err := store.Execution().RetargetAgentProfile(ctx, executionstore.RetargetAgentProfileInput{
		ProjectID:               testProjectID,
		ProfileID:               first.ID,
		ExpectedCurrentConfigID: first.CurrentConfigID,
		IdempotencyKey:          "idem-retarget-profile-replay",
		ConfigID:                retargetConfig.ID,
	})
	if err != nil {
		t.Fatalf("retarget profile before create replay: %v", err)
	}
	replayedRetarget, err := store.Execution().RetargetAgentProfile(ctx, executionstore.RetargetAgentProfileInput{
		ProjectID:               testProjectID,
		ProfileID:               first.ID,
		ExpectedCurrentConfigID: first.CurrentConfigID,
		IdempotencyKey:          "idem-retarget-profile-replay",
		ConfigID:                retargetConfig.ID,
	})
	if err != nil {
		t.Fatalf("replay retarget profile: %v", err)
	}
	if replayedRetarget.ID != retargeted.ID || replayedRetarget.CurrentConfigID != retargeted.CurrentConfigID ||
		replayedRetarget.CurrentGeneration != retargeted.CurrentGeneration {
		t.Fatalf("unexpected replayed retarget: retargeted=%+v replayed=%+v", retargeted, replayedRetarget)
	}
	conflictingRetargetYAML := `
name: Replay Profile
instruction: Conflicting retargeted profile head.
model:
  provider_config: openai-prod
  name: profile-replay
`
	conflictingRetargetConfig := mustCreateAgentConfigFromYAML(
		t,
		ctx,
		store,
		"profile-replay-conflict",
		conflictingRetargetYAML,
		now.Add(2750*time.Millisecond),
	)
	if _, err := store.Execution().RetargetAgentProfile(ctx, executionstore.RetargetAgentProfileInput{
		ProjectID:               testProjectID,
		ProfileID:               first.ID,
		ExpectedCurrentConfigID: first.CurrentConfigID,
		IdempotencyKey:          "idem-retarget-profile-replay",
		ConfigID:                conflictingRetargetConfig.ID,
	}); !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf("conflicting retarget replay should conflict, got %v", err)
	}
	replayedAfterRetarget, err := store.Execution().CreateAgentProfile(ctx, input)
	if err != nil {
		t.Fatalf("replay create profile after retarget: %v", err)
	}
	if replayedAfterRetarget.ID != first.ID || replayedAfterRetarget.CurrentConfigID != retargeted.CurrentConfigID ||
		replayedAfterRetarget.CurrentConfig.ID != retargeted.CurrentConfigID {
		t.Fatalf(
			"replay after retarget should return existing profile head: retargeted=%+v replayed=%+v",
			retargeted,
			replayedAfterRetarget,
		)
	}
}

func TestChangeAgentConfigCreatesConfigChangeEventAndIsIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool)
	now := time.Date(2026, 4, 29, 16, 0, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "agent-config-change@example.com", "Agent Config Change")
	otherUser := mustCreateProjectDeveloperUser(
		t,
		ctx,
		store,
		"agent-config-change-other@example.com",
		"Agent Config Change Other")

	operator := mustCreateProjectOperatorUser(
		t,
		ctx,
		store,
		"agent-config-change-operator@example.com",
		"Agent Config Change Operator")

	profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "agent-config-change", "Change Profile", `
name: Change Profile
instruction: Original instruction.
model:
  provider_config: openai-prod
  name: config-change
`, now)
	launch, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-config-change-launch",
		},
	)
	if err != nil {
		t.Fatalf("launch with config: %v", err)
	}
	if _, err := store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: changeInputFromRecord(launch.AgentConfig),
		AgentID:                launch.Agent.ID,
		ActorType:              identitystore.PrincipalTypeUser,
		ActorID:                operator.ID,
		Reason:                 "launch",
		IdempotencyKey:         "idem-config-change-operator-launch-reason",
	}); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("operator config change with launch audit reason should be unauthorized, got %v", err)
	}
	updatedYAML := `
name: Change Profile
instruction: Updated instruction.
model:
  provider_config: openai-prod
  name: config-change
`
	compiled := mustCompileAgentYAMLResolved(t, ctx, store, updatedYAML, now.Add(2*time.Second))
	change, err := store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: executionstore.CreateAgentConfigInput{
			ProjectID:               testProjectID,
			Definition:              json.RawMessage(compiled.CanonicalJSON),
			Source:                  updatedYAML,
			ConfiguredModelID:       parseConfiguredModelID(t, compiled),
			CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
			CompilerVersion:         agentconfig.CompilerVersion,
			EffectiveDefinitionHash: compiled.Hash,
		},
		AgentID:        launch.Agent.ID,
		ActorType:      identitystore.PrincipalTypeUser,
		ActorID:        user.ID,
		Reason:         "user_update",
		IdempotencyKey: "idem-config-change",
	})
	if err != nil {
		t.Fatalf("change config: %v", err)
	}
	if change.AgentConfig.ID == launch.Agent.CurrentConfigID {
		t.Fatalf("config change reused initial config unexpectedly: %s", change.AgentConfig.ID)
	}
	loaded, err := store.Execution().GetAgentInProject(ctx, testProjectID, launch.Agent.ID)
	if err != nil {
		t.Fatalf("load changed agent: %v", err)
	}
	if loaded.CurrentConfigID != change.AgentConfig.ID {
		t.Fatalf("agent current config not advanced: agent=%+v change=%+v", loaded, change)
	}
	replayed, err := store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: changeInputFromRecord(change.AgentConfig),
		AgentID:                launch.Agent.ID,
		ActorType:              identitystore.PrincipalTypeUser,
		ActorID:                user.ID,
		Reason:                 "user_update",
		IdempotencyKey:         "idem-config-change",
	})
	if err != nil {
		t.Fatalf("replay config change: %v", err)
	}
	if replayed.AgentConfig.ID != change.AgentConfig.ID ||
		replayed.ConfigChange.AgentInput.ID != change.ConfigChange.AgentInput.ID ||
		replayed.ConfigChange.Event.ID != change.ConfigChange.Event.ID {
		t.Fatalf("unexpected replayed change: original=%+v replay=%+v", change, replayed)
	}
	if _, err := store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: changeInputFromRecord(change.AgentConfig),
		AgentID:                launch.Agent.ID,
		ActorType:              identitystore.PrincipalTypeUser,
		ActorID:                otherUser.ID,
		Reason:                 "user_update",
		IdempotencyKey:         "idem-config-change",
	}); !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf("config change replay by a different actor should conflict, got %v", err)
	}
	secondYAML := `
name: Change Profile
instruction: Second updated instruction.
model:
  provider_config: openai-prod
  name: config-change
`
	secondCompiled := mustCompileAgentYAMLResolved(t, ctx, store, secondYAML, now.Add(4*time.Second))
	secondChange, err := store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: executionstore.CreateAgentConfigInput{
			ProjectID:               testProjectID,
			Definition:              json.RawMessage(secondCompiled.CanonicalJSON),
			Source:                  secondYAML,
			ConfiguredModelID:       parseConfiguredModelID(t, secondCompiled),
			CompiledDefinition:      json.RawMessage(secondCompiled.CanonicalJSON),
			CompilerVersion:         agentconfig.CompilerVersion,
			EffectiveDefinitionHash: secondCompiled.Hash,
		},
		AgentID:        launch.Agent.ID,
		ActorType:      identitystore.PrincipalTypeUser,
		ActorID:        user.ID,
		Reason:         "user_update",
		IdempotencyKey: "idem-config-change-second",
	})
	if err != nil {
		t.Fatalf("second config change: %v", err)
	}
	replayedOld, err := store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: changeInputFromRecord(change.AgentConfig),
		AgentID:                launch.Agent.ID,
		ActorType:              identitystore.PrincipalTypeUser,
		ActorID:                user.ID,
		Reason:                 "user_update",
		IdempotencyKey:         "idem-config-change",
	})
	if err != nil {
		t.Fatalf("replay old config change after second change: %v", err)
	}
	if replayedOld.ConfigChange.AgentInput.ID != change.ConfigChange.AgentInput.ID ||
		replayedOld.ConfigChange.Event.ID != change.ConfigChange.Event.ID {
		t.Fatalf(
			"old replay should return original config change: original=%+v replay=%+v",
			change.ConfigChange,
			replayedOld.ConfigChange,
		)
	}
	loadedAfterOldReplay, err := store.Execution().GetAgentInProject(ctx, testProjectID, launch.Agent.ID)
	if err != nil {
		t.Fatalf("load agent after old replay: %v", err)
	}
	if loadedAfterOldReplay.CurrentConfigID != secondChange.AgentConfig.ID {
		t.Fatalf("old replay rolled back current config: agent=%+v second=%+v", loadedAfterOldReplay, secondChange)
	}
	conflictInput := changeInputFromRecord(secondChange.AgentConfig)
	if _, err := store.Execution().ChangeAgentConfig(
		ctx,
		executionstore.ChangeAgentConfigInput{
			CreateAgentConfigInput: conflictInput,
			AgentID:                launch.Agent.ID,
			ActorType:              identitystore.PrincipalTypeUser,
			ActorID:                user.ID,
			Reason:                 "user_update",
			IdempotencyKey:         "idem-config-change",
		},
	); !errors.Is(
		err,
		storeerr.ErrIdempotencyConflict,
	) {
		t.Fatalf("expected config change idempotency conflict, got %v", err)
	}
}

func TestCaptureAgentConfigForModelContextSeesEventsCommittedBeforeLock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool)
	now := time.Date(2026, 4, 29, 16, 20, 0, 0, time.UTC)
	user, err := store.Identity().CreateVerifiedUser(
		ctx,
		CreateVerifiedUserInput{Email: "agent-config-capture@example.com", DisplayName: "Agent Config Capture"},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "agent-config-capture", "Capture Profile", `
name: Capture Profile
instruction: Original instruction.
model:
  provider_config: openai-prod
  name: config-capture
`, now)
	launch, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-config-capture-launch",
		},
	)
	if err != nil {
		t.Fatalf("launch with config: %v", err)
	}
	actorID := mustEnsureOmnaraActor(t, ctx, store, testOrgID, testProjectID, user.ID)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var locked ID
	if err := tx.QueryRow(ctx, `
SELECT id
FROM agents
WHERE project_id = $1 AND id = $2
FOR UPDATE
`, testProjectID, launch.Agent.ID).
		Scan(&locked); err != nil {
		t.Fatalf("lock agent: %v", err)
	}

	type captureResult struct {
		snapshot executionstore.AgentConfigSnapshotRecord
		err      error
	}
	started := make(chan struct{})
	resultCh := make(chan captureResult, 1)
	go func() {
		close(started)
		snapshot, err := store.Execution().CaptureAgentConfigForModelContext(ctx, testProjectID, launch.Agent.ID)
		resultCh <- captureResult{snapshot: snapshot, err: err}
	}()
	<-started
	integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockAgentInProject", 1)
	select {
	case result := <-resultCh:
		t.Fatalf("capture returned before the held agent lock was released: snapshot=%+v err=%v", result.snapshot, result.err)
	default:
	}

	var sequence int64
	if err := tx.QueryRow(ctx, `
SELECT next_event_sequence
FROM agents
WHERE project_id = $1 AND id = $2
`, testProjectID, launch.Agent.ID).
		Scan(&sequence); err != nil {
		t.Fatalf("read locked agent sequence: %v", err)
	}
	eventAt := now.Add(2 * time.Second)
	var inputID ID
	if err := tx.QueryRow(ctx, `
INSERT INTO agent_inputs(
	id, project_id, agent_id, state, input_rank, actor_id,
	input_kind, delivery_mode, queued_at, metadata
)
VALUES (uuidv7(), $1, $2, 'received', 1024, $3, 'content', 'queued', $4, '{}'::jsonb)
RETURNING id`, testProjectID, launch.Agent.ID, actorID, eventAt).Scan(&inputID); err != nil {
		t.Fatalf("insert blocked input: %v", err)
	}
	var turnID ID
	if err := tx.QueryRow(ctx, `SELECT uuidv7()`).Scan(&turnID); err != nil {
		t.Fatalf("generate blocked turn id: %v", err)
	}
	var eventID ID
	if err := tx.QueryRow(ctx, `
INSERT INTO agent_events(id, agent_id, turn_id, sequence, event_kind, idempotency_key, agent_input_id, is_opening_event, created_at)
VALUES (uuidv7(), $1, $2, $3, 'agent_input', $4, $5, true, $6)
RETURNING id`, launch.Agent.ID, turnID, sequence, "agent_input:"+inputID.String(), inputID, eventAt).Scan(&eventID); err != nil {
		t.Fatalf("insert blocked event: %v", err)
	}
	if _, err := tx.Exec(
		ctx,
		`
UPDATE agent_inputs
SET state = 'resolved', admitted_event_id = $1, admitted_at = $2, resolved_at = $2
WHERE project_id = $3 AND agent_id = $4 AND id = $5
`,
		eventID,
		eventAt,
		testProjectID,
		launch.Agent.ID,
		inputID,
	); err != nil {
		t.Fatalf("resolve blocked input: %v", err)
	}
	var turnSequence int64
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(turn.turn_sequence), 0) + 1 FROM agent_turns turn JOIN agents agent ON agent.id = turn.agent_id WHERE agent.project_id = $1 AND turn.agent_id = $2`, testProjectID, launch.Agent.ID).
		Scan(&turnSequence); err != nil {
		t.Fatalf("next blocked turn sequence: %v", err)
	}
	if _, err := tx.Exec(ctx, `
	INSERT INTO agent_turns(id, agent_id, turn_sequence, latest_event_id, latest_semantic_event_id)
	VALUES ($1, $2, $3, $4, $4)
	`, turnID, launch.Agent.ID, turnSequence, eventID); err != nil {
		t.Fatalf("insert blocked turn: %v", err)
	}
	if _, err := tx.Exec(
		ctx,
		`UPDATE agents SET next_event_sequence = next_event_sequence + 1, updated_at = $1 WHERE project_id = $2 AND id = $3`,
		eventAt,
		testProjectID,
		launch.Agent.ID,
	); err != nil {
		t.Fatalf("advance event sequence: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit lock tx: %v", err)
	}

	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Fatalf("capture after lock release: %v", result.err)
		}
		if result.snapshot.InputEventSequence != sequence {
			t.Fatalf("capture watermark = %d, want committed event sequence %d", result.snapshot.InputEventSequence, sequence)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("capture did not complete after releasing the agent lock")
	}
}

func TestCaptureAgentConfigForEventWatermarkUsesConfigActiveAtSequence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool)
	now := time.Date(2026, 4, 29, 16, 45, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "watermark-config@example.com", "Watermark Config")
	profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "watermark-config", "Watermark Config", `
name: Watermark Config
instruction: First watermark config.
model:
  provider_config: openai-prod
  name: watermark
`, now)
	launch, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-watermark-launch",
		},
	)
	if err != nil {
		t.Fatalf("launch agent: %v", err)
	}
	changed, err := store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: changeInputFromRecord(
			mustCreateAgentConfigFromYAML(t, ctx, store, "watermark-config-second", `
name: Watermark Config
instruction: Second watermark config.
model:
  provider_config: openai-prod
  name: watermark
`, now.Add(2*time.Second))),
		AgentID:        launch.Agent.ID,
		ActorType:      identitystore.PrincipalTypeUser,
		ActorID:        user.ID,
		IdempotencyKey: "idem-watermark-change",
	})
	if err != nil {
		t.Fatalf("change agent config: %v", err)
	}

	before, err := store.Execution().CaptureAgentConfigForEventWatermark(
		ctx,
		testProjectID,
		launch.Agent.ID,
		changed.ConfigChange.Event.Sequence-1,
	)
	if err != nil {
		t.Fatalf("capture before config change: %v", err)
	}
	if before.AgentConfig.ID != launch.AgentConfig.ID {
		t.Fatalf("before boundary snapshot = %+v, want launch config %s", before, launch.AgentConfig.ID)
	}
	at, err := store.Execution().CaptureAgentConfigForEventWatermark(
		ctx,
		testProjectID,
		launch.Agent.ID,
		changed.ConfigChange.Event.Sequence,
	)
	if err != nil {
		t.Fatalf("capture at config change: %v", err)
	}
	if at.AgentConfig.ID != changed.AgentConfig.ID {
		t.Fatalf("at boundary snapshot = %+v, want changed config %s", at, changed.AgentConfig.ID)
	}
}

func isPgCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}

func TestChangeAgentConfigAcceptsLiveMCPDiffs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool)
	now := time.Date(2026, 4, 29, 16, 30, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "agent-config-policy@example.com", "Agent Config Policy")
	profile := mustCreateConfigAndProfileBookmarkFromYAML(t, ctx, store, "agent-config-policy", "Policy Profile", `
name: Policy Profile
instruction: Original instruction.
model:
  provider_config: openai-prod
  name: config-policy
`, now)
	launch, err := store.Execution().LaunchAgent(
		ctx,
		executionstore.LaunchAgentInput{
			ProjectID:      testProjectID,
			ProfileID:      profile.ID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     userPrincipal(user.ID),
			IdempotencyKey: "idem-config-policy-launch",
		},
	)
	if err != nil {
		t.Fatalf("launch with config: %v", err)
	}
	yaml := `
name: Policy Profile
instruction: Add MCP.
model:
  provider_config: openai-prod
  name: config-policy
mcp:
  docs:
    url: https://mcp.example.com
`
	compiled := mustCompileAgentYAMLResolved(t, ctx, store, yaml, now.Add(2*time.Second))
	changed, err := store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: executionstore.CreateAgentConfigInput{
			ProjectID:               testProjectID,
			Definition:              json.RawMessage(compiled.CanonicalJSON),
			Source:                  yaml,
			ConfiguredModelID:       parseConfiguredModelID(t, compiled),
			CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
			CompilerVersion:         agentconfig.CompilerVersion,
			EffectiveDefinitionHash: compiled.Hash,
		},
		AgentID:        launch.Agent.ID,
		ActorType:      identitystore.PrincipalTypeUser,
		ActorID:        user.ID,
		Reason:         "policy-test",
		IdempotencyKey: "idem-config-policy-mcp",
	})
	if err != nil {
		t.Fatalf("change config: %v", err)
	}
	loaded, err := store.Execution().GetAgentInProject(ctx, testProjectID, launch.Agent.ID)
	if err != nil {
		t.Fatalf("load agent after change: %v", err)
	}
	if loaded.CurrentConfigID != changed.AgentConfig.ID {
		t.Fatalf("current config = %s, want %s", loaded.CurrentConfigID, changed.AgentConfig.ID)
	}
	currentConfig, found, err := store.Execution().GetAgentConfig(ctx, testProjectID, loaded.CurrentConfigID)
	if err != nil {
		t.Fatalf("load current config: %v", err)
	}
	if !found {
		t.Fatal("current config not found")
	}
	contract, err := agentconfig.RuntimeContractFromCompiled(
		currentConfig.CompiledDefinition,
		currentConfig.CompilerVersion,
		currentConfig.EffectiveDefinitionHash,
	)
	if err != nil {
		t.Fatalf("load current runtime contract: %v", err)
	}
	if len(contract.MCPServers) != 1 || contract.MCPServers[0].ServerKey != "docs" ||
		contract.MCPServers[0].URL != "https://mcp.example.com" {
		t.Fatalf("current MCP servers = %+v, want docs at https://mcp.example.com", contract.MCPServers)
	}
}

func TestChangeAgentConfigReconcilesExplicitMachineSources(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	publisher := &recordingPostCommitPublisher{}
	store := newIntegrationStore(pool, WithPostCommitPublisher(publisher))
	now := time.Date(2026, 4, 29, 16, 35, 0, 0, time.UTC)
	user := mustCreateProjectDeveloperUser(t, ctx, store, "agent-config-machines@example.com", "Agent Config Machines")
	secret, _, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID:          testOrgID,
		OwnerKind:      secretstore.SecretOwnerProject,
		OwnerProjectID: testProjectID,
		Name:           "agent-config-machine-secret",
		Material:       secrets.GenericMaterial{Value: "secret-value"},
		Actor:          userPrincipal(user.ID),
	})
	if err != nil {
		t.Fatalf("create machine source secret: %v", err)
	}
	firstMachine, err := store.Execution().CreateDaemonMachine(ctx, executionstore.CreateDaemonMachineInput{
		OrgID:          testOrgID,
		DisplayName:    "Live Explicit First",
		Env:            json.RawMessage(`{"Base":"machine"}`),
		IdempotencyKey: "idem-live-explicit-first",
	})
	if err != nil {
		t.Fatalf("create first machine: %v", err)
	}
	secondMachine, err := store.Execution().CreateDaemonMachine(ctx, executionstore.CreateDaemonMachineInput{
		OrgID:          testOrgID,
		DisplayName:    "Live Explicit Second",
		IdempotencyKey: "idem-live-explicit-second",
	})
	if err != nil {
		t.Fatalf("create second machine: %v", err)
	}
	for index, machine := range []executionstore.MachineRecord{firstMachine, secondMachine} {
		if _, _, err := store.Execution().CreateProjectMachineGrant(ctx, executionstore.CreateProjectMachineGrantInput{
			OrgID:          testOrgID,
			ProjectID:      testProjectID,
			MachineID:      machine.ID,
			IdempotencyKey: fmt.Sprintf("idem-live-explicit-grant-%d", index),
		}); err != nil {
			t.Fatalf("grant machine %d: %v", index, err)
		}
	}
	initialYAML := `
name: Live Explicit Sources
instruction: Use explicit machines.
model:
  provider_config: openai-prod
  name: live-explicit
machine_sources:
  - machine_name: ` + firstMachine.DisplayName + `
    cwd: /initial
    env_overlay:
      APP: initial
tools:
  run_command: {}
`
	profile := mustCreateConfigAndProfileBookmarkFromYAML(
		t,
		ctx,
		store,
		"live-explicit",
		"Live Explicit Sources",
		initialYAML,
		now,
	)
	launchInput := executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "idem-live-explicit-launch",
	}
	launch, err := store.Execution().LaunchAgent(ctx, launchInput)
	if err != nil {
		t.Fatalf("launch first agent: %v", err)
	}
	shared, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      profile.ID,
		AgentConfigID:  profile.CurrentConfigID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "idem-live-explicit-shared-launch",
	})
	if err != nil {
		t.Fatalf("launch second agent on shared machine: %v", err)
	}
	if shared.MachineBindings[0].MachineID != firstMachine.ID ||
		shared.MachineBindings[0].ID == launch.MachineBindings[0].ID {
		t.Fatalf("shared machine bindings = first %+v second %+v", launch.MachineBindings, shared.MachineBindings)
	}
	invalidYAML := `
name: Live Explicit Sources
instruction: Use explicit machines.
model:
  provider_config: openai-prod
  name: live-explicit
machine_sources:
  - machine_name: ` + firstMachine.DisplayName + `
    secret_env_overlay:
      BASE: ` + secretPublicIDForTest(t, secret.ID) + `
tools:
  run_command: {}
`
	invalidConfig := mustCreateAgentConfigFromYAML(t, ctx, store, "live-explicit-invalid", invalidYAML, now.Add(3*time.Second))
	if err := store.Execution().ValidateAgentConfigMachineSources(
		ctx,
		testProjectID,
		invalidConfig.CompiledDefinition,
		invalidConfig.CompilerVersion,
		invalidConfig.EffectiveDefinitionHash,
	); err == nil || !strings.Contains(err.Error(), "env and secret_env cannot both set key BASE") {
		t.Fatalf("invalid machine source validation error = %v", err)
	}
	invalidProfile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID:       testProjectID,
		Name:            "Invalid Live Explicit Sources",
		CurrentConfigID: invalidConfig.ID,
		IdempotencyKey:  "profile-live-explicit-invalid",
	})
	if err != nil {
		t.Fatalf("create invalid machine source profile: %v", err)
	}
	if _, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:      testProjectID,
		ProfileID:      invalidProfile.ID,
		AgentConfigID:  invalidConfig.ID,
		LaunchedBy:     userPrincipal(user.ID),
		IdempotencyKey: "idem-live-explicit-invalid-launch",
	}); err == nil || !strings.Contains(err.Error(), "env and secret_env cannot both set key BASE") {
		t.Fatalf("invalid machine source launch error = %v", err)
	}
	if _, err := store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: changeInputFromRecord(invalidConfig),
		AgentID:                launch.Agent.ID,
		ActorType:              identitystore.PrincipalTypeUser,
		ActorID:                user.ID,
		IdempotencyKey:         "idem-live-explicit-invalid",
	}); err == nil || !strings.Contains(err.Error(), "env and secret_env cannot both set key BASE") {
		t.Fatalf("invalid baseline overlay change error = %v", err)
	}
	afterInvalid, err := store.Execution().GetAgentInProject(ctx, testProjectID, launch.Agent.ID)
	if err != nil {
		t.Fatalf("load agent after invalid change: %v", err)
	}
	if afterInvalid.CurrentConfigID != launch.AgentConfig.ID {
		t.Fatalf("invalid reconciliation advanced config to %s", afterInvalid.CurrentConfigID)
	}
	validYAML := `
name: Live Explicit Sources
instruction: Use explicit machines.
model:
  provider_config: openai-prod
  name: live-explicit
machine_sources:
  - machine_name: ` + firstMachine.DisplayName + `
    cwd: /changed
    description: changed first
    env_overlay:
      Base: null
      UNUSED: null
      APP: changed
    secret_env_overlay:
      BASE: ` + secretPublicIDForTest(t, secret.ID) + `
  - machine_name: ` + secondMachine.DisplayName + `
    cwd: /second
tools:
  run_command: {}
`
	changed := changeAgentConfigFromYAMLForTest(
		t,
		ctx,
		store,
		launch.Agent.ID,
		user.ID,
		"live-explicit-valid",
		validYAML,
		"idem-live-explicit-valid",
		now.Add(4*time.Second),
	)
	if len(changed.DeleteMachines) != 0 {
		t.Fatalf("explicit change deleted machines: %+v", changed.DeleteMachines)
	}
	bindingForMachine := func(machineID ID) executionstore.AgentMachineBindingRecord {
		t.Helper()
		row, err := store.q.GetAgentMachineBindingByMachine(ctx, dbsqlc.GetAgentMachineBindingByMachineParams{
			ProjectID:   testProjectID,
			AgentID:     launch.Agent.ID,
			MachineID:   machineID,
			BindingKind: string(executionstore.MachineBindingKindExplicit),
		})
		if err != nil {
			t.Fatalf("load active binding for %s: %v", machineID, err)
		}
		return executionstore.IntegrationAgentMachineBindingRecordFromSQLC(row)
	}
	firstBinding := bindingForMachine(firstMachine.ID)
	secondBinding := bindingForMachine(secondMachine.ID)
	assertCurrentAgentReplay := func(stage string) {
		t.Helper()
		current, err := store.Execution().GetAgentInProject(ctx, testProjectID, launch.Agent.ID)
		if err != nil {
			t.Fatalf("load current agent %s: %v", stage, err)
		}
		replayed, err := store.Execution().LaunchAgent(ctx, launchInput)
		if err != nil {
			t.Fatalf("replay launch %s: %v", stage, err)
		}
		requireCurrentAgentLaunchReplay(t, replayed, current)
	}
	if firstBinding.ID != launch.MachineBindings[0].ID || firstBinding.Cwd != "/changed" ||
		firstBinding.Description != "changed first" || secondBinding.Cwd != "/second" {
		t.Fatalf("reconciled bindings = first %+v second %+v", firstBinding, secondBinding)
	}
	if !sameJSON(firstBinding.EnvOverlay, json.RawMessage(`{"APP":"changed","Base":null,"UNUSED":null}`)) ||
		!sameJSON(firstBinding.SecretEnvOverlay, json.RawMessage(`{"BASE":"`+secretPublicIDForTest(t, secret.ID)+`"}`)) {
		t.Fatalf("reconciled first binding environment = %s / %s", firstBinding.EnvOverlay, firstBinding.SecretEnvOverlay)
	}
	reorderedYAML := `
name: Live Explicit Sources
instruction: Use explicit machines.
model:
  provider_config: openai-prod
  name: live-explicit
machine_sources:
  - machine_name: ` + secondMachine.DisplayName + `
    cwd: /second
  - machine_name: ` + firstMachine.DisplayName + `
    cwd: /changed
    description: changed first
    env_overlay:
      Base: null
      UNUSED: null
      APP: changed
    secret_env_overlay:
      BASE: ` + secretPublicIDForTest(t, secret.ID) + `
tools:
  run_command: {}
`
	changeAgentConfigFromYAMLForTest(
		t,
		ctx,
		store,
		launch.Agent.ID,
		user.ID,
		"live-explicit-reordered",
		reorderedYAML,
		"idem-live-explicit-reordered",
		now.Add(5*time.Second),
	)
	if reorderedFirst := bindingForMachine(firstMachine.ID); !reorderedFirst.UpdatedAt.Equal(firstBinding.UpdatedAt) {
		t.Fatalf("source reorder updated first binding at %v, want %v", reorderedFirst.UpdatedAt, firstBinding.UpdatedAt)
	}
	if reorderedSecond := bindingForMachine(secondMachine.ID); !reorderedSecond.UpdatedAt.Equal(secondBinding.UpdatedAt) {
		t.Fatalf("source reorder updated second binding at %v, want %v", reorderedSecond.UpdatedAt, secondBinding.UpdatedAt)
	}
	token, err := store.Execution().CreateBYOMachineDaemonToken(ctx, executionstore.CreateBYOMachineDaemonTokenInput{
		OrgID:     testOrgID,
		MachineID: firstMachine.ID,
		Name:      "live source removal",
		Token:     "token-live-explicit-source-removal",
	})
	if err != nil {
		t.Fatalf("create live source removal daemon token: %v", err)
	}
	runtime, err := store.Execution().RegisterDaemonRuntime(ctx, executionstore.RegisterDaemonRuntimeInput{
		OrgID:            testOrgID,
		MachineID:        firstMachine.ID,
		DaemonTokenID:    token.ID,
		DaemonInstanceID: testID("daemon-live-explicit-source-removal"),
		DaemonVersion:    "1.0.0",
		LeaseTimeout:     time.Hour,
	})
	if err != nil {
		t.Fatalf("register live source removal daemon runtime: %v", err)
	}
	firstLock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		launch.Agent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire first live source removal runtime lock: %v", err)
	}
	sharedLock, err := store.Execution().AcquireAgentRuntimeLock(
		ctx,
		testProjectID,
		shared.Agent.ID,
		testWorkerProcessID,
		testAgentRuntimeLockLeaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire shared live source removal runtime lock: %v", err)
	}
	startRunningProcess := func(testName string, agentID, bindingID ID, lock executionstore.AgentRuntimeLockRecord) executionstore.ProcessRecord {
		t.Helper()
		processAt := now.Add(5500 * time.Millisecond)
		fixture := processDaemonFixture{
			Store:     store,
			OrgID:     testOrgID,
			AgentID:   agentID,
			MachineID: firstMachine.ID,
			BindingID: bindingID,
			TokenID:   token.ID,
			RuntimeID: runtime.ID,
			DaemonID:  testID("daemon-live-explicit-source-removal"),
			UserID:    user.ID,
			Lock:      lock,
			Now:       processAt.Add(-10 * time.Second),
		}
		process, err := startProcessForTest(ctx, store, executionstore.ExecuteToolCallInput{
			ProjectID:     testProjectID,
			AgentID:       agentID,
			ToolCallID:    createToolCallForProcessTest(t, ctx, fixture, testName, "run_command"),
			RuntimeLockID: lock.ID,
		}, executionstore.CreateProcessInput{
			AgentMachineBindingID: bindingID,
			Command:               "sleep 3600",
			ShellSelector:         "sh",
		})
		if err != nil {
			t.Fatalf("start %s process: %v", testName, err)
		}
		if _, found, err := acceptDaemonProcessForTest(
			ctx,
			store,
			testOrgID,
			firstMachine.ID,
			runtime.ID,
			process.ID,
		); err != nil || !found {
			t.Fatalf("accept %s process found=%v err=%v", testName, found, err)
		}
		markProcessStartedForTest(t, ctx, fixture, process, processAt.Add(200*time.Millisecond))
		return process
	}
	firstProcess := startRunningProcess("live_explicit_source_removed", launch.Agent.ID, firstBinding.ID, firstLock)
	sharedProcess := startRunningProcess(
		"live_explicit_source_shared",
		shared.Agent.ID,
		shared.MachineBindings[0].ID,
		sharedLock,
	)
	secondOnlyYAML := `
name: Live Explicit Sources
instruction: Use explicit machines.
model:
  provider_config: openai-prod
  name: live-explicit
machine_sources:
  - machine_name: ` + secondMachine.DisplayName + `
    cwd: /second
tools:
  run_command: {}
`
	changeAgentConfigFromYAMLForTest(
		t,
		ctx,
		store,
		launch.Agent.ID,
		user.ID,
		"live-explicit-remove",
		secondOnlyYAML,
		"idem-live-explicit-remove",
		now.Add(6*time.Second),
	)
	released := getAgentMachineBindingForTest(t, ctx, store, testProjectID, launch.Agent.ID, firstBinding.ID)
	if released.State != "released" {
		t.Fatalf("removed explicit binding state = %s, want released", released.State)
	}
	assertCurrentAgentReplay("after release")
	removedProcess, err := store.Execution().GetProcess(ctx, testProjectID, launch.Agent.ID, firstProcess.ID)
	if err != nil {
		t.Fatalf("load removed explicit source process: %v", err)
	}
	if removedProcess.State != executionstore.ProcessStateUnknown ||
		removedProcess.StateReasonCode != "agent_config_machine_source_removed" ||
		removedProcess.StateChangedAt.IsZero() {
		t.Fatalf("removed explicit source process = %+v", removedProcess)
	}
	if !publisher.hasProcessTermination(firstMachine.ID, firstProcess.ID) {
		t.Fatalf("removed explicit source did not publish termination for process %s", firstProcess.ID)
	}
	remainingProcess, err := store.Execution().GetProcess(ctx, testProjectID, shared.Agent.ID, sharedProcess.ID)
	if err != nil {
		t.Fatalf("load shared explicit source process: %v", err)
	}
	if remainingProcess.State != executionstore.ProcessStateRunning || remainingProcess.SourceEndedAt != nil {
		t.Fatalf("shared explicit source process changed: %+v", remainingProcess)
	}
	if publisher.hasProcessTermination(firstMachine.ID, sharedProcess.ID) {
		t.Fatalf("removed explicit source terminated shared process %s", sharedProcess.ID)
	}
	changeAgentConfigFromYAMLForTest(
		t,
		ctx,
		store,
		launch.Agent.ID,
		user.ID,
		"live-explicit-readd",
		reorderedYAML,
		"idem-live-explicit-readd",
		now.Add(7*time.Second),
	)
	rebound := bindingForMachine(firstMachine.ID)
	if rebound.ID == firstBinding.ID || rebound.MachineRef == firstBinding.MachineRef {
		t.Fatalf("re-added explicit source reused binding history: old=%+v new=%+v", firstBinding, rebound)
	}
	assertCurrentAgentReplay("after reattachment")
	if updated, err := store.q.ReleaseExplicitAgentMachineBinding(
		ctx,
		dbsqlc.ReleaseExplicitAgentMachineBindingParams{
			ProjectID: testProjectID,
			AgentID:   launch.Agent.ID,
			MachineID: firstMachine.ID,
		},
	); err != nil || updated != 1 {
		t.Fatalf("release binding before source removal: updated=%d err=%v", updated, err)
	}
	changeAgentConfigFromYAMLForTest(
		t,
		ctx,
		store,
		launch.Agent.ID,
		user.ID,
		"live-explicit-remove-released",
		secondOnlyYAML,
		"idem-live-explicit-remove-released",
		now.Add(9*time.Second),
	)
}

func TestAgentConfigDedupesOnlyEquivalentAuthoredConfigs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)

	store := newIntegrationStore(pool)
	now := time.Date(2026, 4, 29, 17, 0, 0, 0, time.UTC)
	first := mustCreateAgentConfig(t, ctx, store, testProjectID, "dedupe-first", now)
	second := mustCreateAgentConfig(t, ctx, store, testProjectID, "dedupe-second", now.Add(time.Second))
	if first != second {
		t.Fatalf("equivalent test configs should dedupe by hash: first=%s second=%s", first, second)
	}
	sourceA := `
name: Equivalent Source
instruction: test
model:
  provider_config: openai-prod
  name: equivalent
`
	sourceB := `
model:
  provider_config: openai-prod
  name: equivalent
instruction: test
name: Equivalent Source
`
	compiledA := mustCompileAgentYAMLResolved(t, ctx, store, sourceA, now.Add(time.Second))
	compiledB := mustCompileAgentYAMLResolved(t, ctx, store, sourceB, now.Add(time.Second))
	if compiledA.Hash != compiledB.Hash || string(compiledA.CanonicalJSON) != string(compiledB.CanonicalJSON) {
		t.Fatalf(
			"test sources should compile to equivalent config: hash %q/%q json %s/%s",
			compiledA.Hash,
			compiledB.Hash,
			compiledA.CanonicalJSON,
			compiledB.CanonicalJSON,
		)
	}
	equivalentModelID := parseConfiguredModelID(t, compiledA)
	equivalentA, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               testProjectID,
		Definition:              json.RawMessage(compiledA.CanonicalJSON),
		Source:                  sourceA,
		ConfiguredModelID:       equivalentModelID,
		CompiledDefinition:      json.RawMessage(compiledA.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiledA.Hash,
	})
	if err != nil {
		t.Fatalf("create equivalent source A config: %v", err)
	}
	equivalentB, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               testProjectID,
		Definition:              json.RawMessage(compiledB.CanonicalJSON),
		Source:                  sourceB,
		ConfiguredModelID:       equivalentModelID,
		CompiledDefinition:      json.RawMessage(compiledB.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiledB.Hash,
	})
	if err != nil {
		t.Fatalf("create equivalent source B config: %v", err)
	}
	if equivalentA.ID == equivalentB.ID {
		t.Fatalf("same behavior with different authored source should keep distinct config rows: %s", equivalentA.ID)
	}
	if equivalentA.EffectiveDefinitionHash != equivalentB.EffectiveDefinitionHash {
		t.Fatalf(
			"distinct authored config rows should still share behavior hash: %q/%q",
			equivalentA.EffectiveDefinitionHash,
			equivalentB.EffectiveDefinitionHash,
		)
	}
	replayedA, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               testProjectID,
		Definition:              json.RawMessage(compiledA.CanonicalJSON),
		Source:                  sourceA,
		ConfiguredModelID:       equivalentModelID,
		CompiledDefinition:      json.RawMessage(compiledA.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiledA.Hash,
	})
	if err != nil {
		t.Fatalf("replay equivalent source A config: %v", err)
	}
	if replayedA.ID != equivalentA.ID || replayedA.Source != sourceA {
		t.Fatalf(
			"identical authored config should dedupe to source-preserving row: original=%+v replay=%+v",
			equivalentA,
			replayedA,
		)
	}
}

func mustCreateConfigAndProfileBookmarkFromYAML(
	t *testing.T,
	ctx context.Context,
	store *Store,
	key, name, sourceYAML string,
	now time.Time,
) executionstore.AgentProfileRecord {
	t.Helper()
	compiled := mustCompileAgentYAMLResolved(t, ctx, store, sourceYAML, now)
	config := mustCreateAgentConfigFromCompiled(t, ctx, store, key, sourceYAML, compiled, now)
	profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID:       testProjectID,
		Name:            name,
		CurrentConfigID: config.ID,
		IdempotencyKey:  "profile-" + key,
	})
	if err != nil {
		t.Fatalf("create agent profile %s: %v", key, err)
	}
	return profile
}

func mustCreateAgentConfigFromYAML(
	t *testing.T,
	ctx context.Context,
	store *Store,
	key, sourceYAML string,
	now time.Time,
) executionstore.AgentConfigRecord {
	t.Helper()
	compiled := mustCompileAgentYAMLResolved(t, ctx, store, sourceYAML, now)
	return mustCreateAgentConfigFromCompiled(t, ctx, store, key, sourceYAML, compiled, now)
}

func mustCreateAgentConfigFromCompiled(
	t *testing.T,
	ctx context.Context,
	store *Store,
	key, source string,
	compiled agentconfig.Result,
	now time.Time,
) executionstore.AgentConfigRecord {
	t.Helper()
	config, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               testProjectID,
		Definition:              json.RawMessage(compiled.CanonicalJSON),
		Source:                  source,
		ConfiguredModelID:       parseConfiguredModelID(t, compiled),
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiled.Hash,
	})
	if err != nil {
		t.Fatalf("create agent config %s: %v", key, err)
	}
	return config
}

func mustCompileAgentYAML(t *testing.T, sourceYAML string) agentconfig.Result {
	t.Helper()
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(sourceYAML), agentconfig.CompileOptions{})
	if err != nil {
		t.Fatalf("compile agent yaml: %v", err)
	}
	return compiled
}

func mustCompileAgentYAMLWithMachineSourceResolvers(
	t *testing.T,
	ctx context.Context,
	store *Store,
	sourceYAML string,
) agentconfig.Result {
	t.Helper()
	return mustCompileAgentYAMLResolved(t, ctx, store, sourceYAML, time.Date(2026, 5, 21, 8, 0, 0, 0, time.UTC))
}

func changeInputFromRecord(record executionstore.AgentConfigRecord) executionstore.CreateAgentConfigInput {
	return executionstore.CreateAgentConfigInput{
		ProjectID:               record.ProjectID,
		Definition:              record.Definition,
		Source:                  record.Source,
		ConfiguredModelID:       record.ConfiguredModelID,
		CompiledDefinition:      record.CompiledDefinition,
		CompilerVersion:         record.CompilerVersion,
		EffectiveDefinitionHash: record.EffectiveDefinitionHash,
	}
}

func changeAgentConfigFromYAMLForTest(
	t *testing.T,
	ctx context.Context,
	store *Store,
	agentID, actorID ID,
	key, sourceYAML, idempotencyKey string,
	now time.Time,
) executionstore.ChangeAgentConfigResult {
	t.Helper()
	config := mustCreateAgentConfigFromYAML(t, ctx, store, key, sourceYAML, now)
	result, err := store.Execution().ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput: changeInputFromRecord(config),
		AgentID:                agentID,
		ActorType:              identitystore.PrincipalTypeUser,
		ActorID:                actorID,
		IdempotencyKey:         idempotencyKey,
	})
	if err != nil {
		t.Fatalf("change agent config %s: %v", key, err)
	}
	return result
}
