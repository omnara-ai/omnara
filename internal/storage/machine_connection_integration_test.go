//go:build integration

package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/bearertoken"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestConnectBYOMachineCreatesMachineTokenAndSelectedGrants(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := NewStore(pool)
	secondProjectID := testID("machine-connection-second-project")
	unselectedProjectID := testID("machine-connection-unselected-project")
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
INSERT INTO projects(id, org_id, name, idempotency_key, created_at, updated_at)
VALUES
  ($1, $3, 'Second Machine Connection Project', 'machine-connection-second-project', $4, $4),
  ($2, $3, 'Unselected Machine Connection Project', 'machine-connection-unselected-project', $4, $4)
`, secondProjectID, unselectedProjectID, testOrgID, now); err != nil {
		t.Fatalf("seed projects: %v", err)
	}
	result, err := store.Execution().ConnectBYOMachine(ctx, executionstore.ConnectBYOMachineInput{
		OrgID:       testOrgID,
		DisplayName: "Connected Machine",
		ProjectIDs:  []ID{secondProjectID, testProjectID},
		TokenName:   "web-console",
	})
	if err != nil {
		t.Fatalf("connect machine: %v", err)
	}
	if !result.Machine.Created || result.Machine.DisplayName != "Connected Machine" {
		t.Fatalf("unexpected machine: %+v", result.Machine)
	}
	if result.DaemonToken.Record.MachineID != result.Machine.ID {
		t.Fatalf("unexpected token record: %+v", result.DaemonToken.Record)
	}
	if err := bearertoken.Validate(result.DaemonToken.Token, bearertoken.KindDaemon); err != nil {
		t.Fatalf("connected machine token is not canonical: %v", err)
	}
	if len(result.ProjectGrants) != 2 {
		t.Fatalf("project grants = %d, want 2", len(result.ProjectGrants))
	}
	for index, projectID := range []ID{secondProjectID, testProjectID} {
		grant := result.ProjectGrants[index]
		if grant.ProjectID != projectID || grant.MachineID != result.Machine.ID || !grant.Created {
			t.Fatalf("unexpected project grant %d: %+v", index, grant)
		}
	}
	if count := countProjectMachineGrantsForMachineForTest(
		t,
		ctx,
		store,
		testOrgID,
		unselectedProjectID,
		result.Machine.ID,
	); count != 0 {
		t.Fatalf("unselected project grant count = %d, want 0", count)
	}
	principal, err := store.Execution().AuthenticateMachineDaemonToken(ctx, result.DaemonToken.Token)
	if err != nil {
		t.Fatalf("authenticate connected machine token: %v", err)
	}
	if principal.ID != result.Machine.ID {
		t.Fatalf("token machine = %s, want %s", principal.ID, result.Machine.ID)
	}
	withoutGrants, err := store.Execution().ConnectBYOMachine(ctx, executionstore.ConnectBYOMachineInput{
		OrgID:       testOrgID,
		DisplayName: "Connected Without Grants",
		ProjectIDs:  []ID{},
		TokenName:   "web-console",
	})
	if err != nil {
		t.Fatalf("connect machine without grants: %v", err)
	}
	if len(withoutGrants.ProjectGrants) != 0 {
		t.Fatalf("project grants without selections = %d, want 0", len(withoutGrants.ProjectGrants))
	}
}

func TestConnectBYOMachineRollsBackEveryResourceWhenGrantCreationFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := NewStore(pool)
	if _, err := pool.Exec(ctx, `
CREATE FUNCTION fail_machine_connection_grant() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'forced machine connection grant failure';
END
$$;
CREATE TRIGGER fail_machine_connection_grant
BEFORE INSERT ON project_machine_grants
FOR EACH ROW EXECUTE FUNCTION fail_machine_connection_grant()
`); err != nil {
		t.Fatalf("install grant failure trigger: %v", err)
	}
	tokenName := "machine-connection-rollback"
	_, err := store.Execution().ConnectBYOMachine(ctx, executionstore.ConnectBYOMachineInput{
		OrgID:       testOrgID,
		DisplayName: "Rolled Back Machine",
		ProjectIDs:  []ID{testProjectID},
		TokenName:   tokenName,
	})
	if err == nil {
		t.Fatal("connect machine succeeded despite forced grant failure")
	}
	var machineCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM machines WHERE org_id = $1 AND display_name = 'Rolled Back Machine'
`, testOrgID).Scan(&machineCount); err != nil {
		t.Fatalf("count rolled back machines: %v", err)
	}
	if machineCount != 0 {
		t.Fatalf("rolled back machine count = %d, want 0", machineCount)
	}
	var tokenCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM machine_daemon_tokens WHERE org_id = $1 AND name = $2
`, testOrgID, tokenName).Scan(&tokenCount); err != nil {
		t.Fatalf("count rolled back tokens: %v", err)
	}
	if tokenCount != 0 {
		t.Fatalf("rolled back token count = %d, want 0", tokenCount)
	}
	var grantCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM project_machine_grants WHERE org_id = $1 AND project_id = $2
`, testOrgID, testProjectID).Scan(&grantCount); err != nil {
		t.Fatalf("count rolled back grants: %v", err)
	}
	if grantCount != 0 {
		t.Fatalf("rolled back grant count = %d, want 0", grantCount)
	}
}

func TestConnectBYOMachineRejectsInvalidProjectSelections(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := NewStore(pool)
	baseInput := executionstore.ConnectBYOMachineInput{
		OrgID:       testOrgID,
		DisplayName: "Invalid Project Connection",
		TokenName:   "web-console",
	}
	duplicateInput := baseInput
	duplicateInput.ProjectIDs = []ID{testProjectID, testProjectID}
	if _, err := store.Execution().ConnectBYOMachine(ctx, duplicateInput); !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("duplicate project IDs error = %v, want ErrInvalidRequest", err)
	}
	deletedProjectID := testID("machine-connection-deleted-project")
	now := time.Date(2026, 8, 3, 13, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
INSERT INTO projects(id, org_id, name, idempotency_key, created_at, updated_at, deleted_at)
VALUES ($1, $2, 'Deleted Machine Connection Project', 'machine-connection-deleted-project', $3, $3, $3)
`, deletedProjectID, testOrgID, now); err != nil {
		t.Fatalf("seed deleted project: %v", err)
	}
	deletedInput := baseInput
	deletedInput.ProjectIDs = []ID{deletedProjectID}
	if _, err := store.Execution().ConnectBYOMachine(ctx, deletedInput); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("deleted project error = %v, want ErrNotFound", err)
	}
	machine, err := store.Execution().CreateDaemonMachine(ctx, executionstore.CreateDaemonMachineInput{
		OrgID:       testOrgID,
		DisplayName: "Deleted Project Grant Machine",
	})
	if err != nil {
		t.Fatalf("create machine for deleted project grant: %v", err)
	}
	if _, _, err := store.Execution().CreateProjectMachineGrant(ctx, executionstore.CreateProjectMachineGrantInput{
		OrgID:     testOrgID,
		ProjectID: deletedProjectID,
		MachineID: machine.ID,
	}); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("grant to deleted project error = %v, want ErrNotFound", err)
	}
	if _, err := store.Execution().DeleteMachine(ctx, executionstore.DeleteMachineInput{
		OrgID:     testOrgID,
		MachineID: machine.ID,
	}); err != nil {
		t.Fatalf("delete project grant machine: %v", err)
	}
	if _, _, err := store.Execution().CreateProjectMachineGrant(ctx, executionstore.CreateProjectMachineGrantInput{
		OrgID:     testOrgID,
		ProjectID: testProjectID,
		MachineID: machine.ID,
	}); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("grant deleted machine error = %v, want ErrNotFound", err)
	}
	if _, _, err := store.Execution().CreateProjectMachineGrant(ctx, executionstore.CreateProjectMachineGrantInput{
		OrgID:     testOrgID,
		ProjectID: testProjectID,
		MachineID: testID("missing-project-grant-machine"),
	}); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("grant missing machine error = %v, want ErrNotFound", err)
	}
	var invalidMachineCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM machines WHERE org_id = $1 AND display_name = 'Invalid Project Connection'
`, testOrgID).Scan(&invalidMachineCount); err != nil {
		t.Fatalf("count invalid-project machines: %v", err)
	}
	if invalidMachineCount != 0 {
		t.Fatalf("invalid-project machine count = %d, want 0", invalidMachineCount)
	}
}

func TestConnectBYOMachineSerializesWithScopeDeletion(t *testing.T) {
	t.Parallel()
	t.Run("connection wins", func(t *testing.T) {
		ctx := context.Background()
		pool := openIntegrationDB(t, ctx)
		seedMigratedDB(t, ctx, pool)
		store := newIntegrationStore(pool)
		user := mustCreateIdentityUser(
			t,
			ctx,
			store,
			"machine-connection-wins@example.com",
			"Machine Connection Wins",
		)
		actor, err := executionstore.OmnaraActorParams(testOrgID, userPrincipal(user.ID))
		if err != nil {
			t.Fatalf("build project deletion actor: %v", err)
		}

		controlTx := integrationdb.BeginTx(t, ctx, pool)
		if err := dbsqlc.New(controlTx).LockResourceCreation(
			ctx,
			dbsqlc.LockResourceCreationParams{
				ResourceKind: "machines",
				Scope:        testOrgID.String(),
			},
		); err != nil {
			t.Fatalf("lock machine creation: %v", err)
		}

		connectDone := integrationdb.RunAsync(func() (executionstore.ConnectBYOMachineResult, error) {
			return store.Execution().ConnectBYOMachine(
				ctx,
				executionstore.ConnectBYOMachineInput{
					OrgID:       testOrgID,
					DisplayName: "Connected Before Project Deletion",
					ProjectIDs:  []ID{testProjectID},
					TokenName:   "connection-wins",
				},
			)
		})
		integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockResourceCreation", 1)

		deleteDone := integrationdb.RunAsyncError(func() error {
			_, deleteErr := store.Organizations().DeleteProjectOnceForIntegration(
				ctx,
				testOrgID,
				testProjectID,
				actor,
			)
			return deleteErr
		})
		integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockProjectLifecycleExclusive", 1)
		if err := controlTx.Commit(ctx); err != nil {
			t.Fatalf("release machine connection control transaction: %v", err)
		}

		connected := integrationdb.AwaitSuccess(t, connectDone, "connect machine before project deletion")
		if err := integrationdb.Await(t, deleteDone, "project deletion"); err != nil {
			t.Fatalf("delete project after machine connection: %v", err)
		}

		var machineActive, tokenActive bool
		var grantCount int
		if err := pool.QueryRow(ctx, `
SELECT machine.deleted_at IS NULL AND machine.lifecycle_state = 'active',
       token.revoked_at IS NULL,
       (SELECT count(*)::integer
        FROM project_machine_grants project_grant
        WHERE project_grant.org_id = machine.org_id
          AND project_grant.project_id = $3
          AND project_grant.machine_id = machine.id)
FROM machines machine
JOIN machine_daemon_tokens token ON token.org_id = machine.org_id
  AND token.machine_id = machine.id
  AND token.id = $2
WHERE machine.org_id = $1 AND machine.id = $4
`, testOrgID, connected.DaemonToken.Record.ID, testProjectID, connected.Machine.ID).Scan(
			&machineActive,
			&tokenActive,
			&grantCount,
		); err != nil {
			t.Fatalf("load connected machine after project deletion: %v", err)
		}
		if !machineActive || !tokenActive || grantCount != 0 {
			t.Fatalf(
				"connected state after project deletion: machine_active=%t token_active=%t grants=%d",
				machineActive,
				tokenActive,
				grantCount,
			)
		}
	})

	t.Run("deletion wins", func(t *testing.T) {
		ctx := context.Background()
		pool := openIntegrationDB(t, ctx)
		seedMigratedDB(t, ctx, pool)
		store := newIntegrationStore(pool)
		user := mustCreateIdentityUser(
			t,
			ctx,
			store,
			"project-deletion-wins@example.com",
			"Project Deletion Wins",
		)
		actor, err := executionstore.OmnaraActorParams(testOrgID, userPrincipal(user.ID))
		if err != nil {
			t.Fatalf("build project deletion actor: %v", err)
		}

		controlTx := integrationdb.BeginTx(t, ctx, pool)
		if _, err := controlTx.Exec(
			ctx,
			`SELECT id FROM projects WHERE org_id = $1 AND id = $2 FOR UPDATE`,
			testOrgID,
			testProjectID,
		); err != nil {
			t.Fatalf("lock project row: %v", err)
		}

		deleteDone := integrationdb.RunAsyncError(func() error {
			_, deleteErr := store.Organizations().DeleteProjectOnceForIntegration(
				ctx,
				testOrgID,
				testProjectID,
				actor,
			)
			return deleteErr
		})
		integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "DeleteProject", 1)

		connectDone := integrationdb.RunAsyncError(func() error {
			_, connectErr := store.Execution().ConnectBYOMachine(
				ctx,
				executionstore.ConnectBYOMachineInput{
					OrgID:       testOrgID,
					DisplayName: "Rejected Project Connection",
					ProjectIDs:  []ID{testProjectID},
					TokenName:   "deletion-wins",
				},
			)
			return connectErr
		})
		integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockProjectLifecycleShared", 1)
		if err := controlTx.Commit(ctx); err != nil {
			t.Fatalf("release project deletion control transaction: %v", err)
		}

		if err := integrationdb.Await(t, deleteDone, "project deletion"); err != nil {
			t.Fatalf("delete project before machine connection: %v", err)
		}
		if err := integrationdb.Await(t, connectDone, "rejected machine connection"); !errors.Is(err, storeerr.ErrNotFound) {
			t.Fatalf("machine connection after project deletion error = %v, want not found", err)
		}

		var machineCount, tokenCount int
		if err := pool.QueryRow(ctx, `
SELECT (SELECT count(*)::integer
        FROM machines
        WHERE org_id = $1 AND display_name = 'Rejected Project Connection'),
       (SELECT count(*)::integer
        FROM machine_daemon_tokens
        WHERE org_id = $1 AND name = 'deletion-wins')
`, testOrgID).Scan(&machineCount, &tokenCount); err != nil {
			t.Fatalf("count rejected machine connection state: %v", err)
		}
		if machineCount != 0 || tokenCount != 0 {
			t.Fatalf("rejected machine connection rows: machines=%d tokens=%d", machineCount, tokenCount)
		}
	})

	t.Run("organization deletion wins", func(t *testing.T) {
		ctx := context.Background()
		pool := openIntegrationDB(t, ctx)
		seedMigratedDB(t, ctx, pool)
		store := newIntegrationStore(pool)
		user := mustCreateIdentityUser(
			t,
			ctx,
			store,
			"organization-deletion-wins@example.com",
			"Organization Deletion Wins",
		)
		actor, err := executionstore.OmnaraActorParams(testOrgID, userPrincipal(user.ID))
		if err != nil {
			t.Fatalf("build organization deletion actor: %v", err)
		}

		controlTx := integrationdb.BeginTx(t, ctx, pool)
		if _, err := controlTx.Exec(
			ctx,
			`SELECT id FROM orgs WHERE id = $1 FOR UPDATE`,
			testOrgID,
		); err != nil {
			t.Fatalf("lock organization row: %v", err)
		}

		deleteDone := integrationdb.RunAsyncError(func() error {
			_, deleteErr := store.Organizations().DeleteOrganizationOnceForIntegration(
				ctx,
				testOrgID,
				actor,
			)
			return deleteErr
		})
		integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "DeleteOrganization", 1)

		connectDone := integrationdb.RunAsyncError(func() error {
			_, connectErr := store.Execution().ConnectBYOMachine(
				ctx,
				executionstore.ConnectBYOMachineInput{
					OrgID:       testOrgID,
					DisplayName: "Rejected Organization Connection",
					TokenName:   "organization-deletion-wins",
				},
			)
			return connectErr
		})
		integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockOrganizationLifecycleShared", 1)
		if err := controlTx.Commit(ctx); err != nil {
			t.Fatalf("release organization deletion control transaction: %v", err)
		}

		if err := integrationdb.Await(t, deleteDone, "organization deletion"); err != nil {
			t.Fatalf("delete organization before machine connection: %v", err)
		}
		if err := integrationdb.Await(t, connectDone, "rejected organization machine connection"); !errors.Is(err, storeerr.ErrNotFound) {
			t.Fatalf("machine connection after organization deletion error = %v, want not found", err)
		}

		var machineCount, tokenCount int
		if err := pool.QueryRow(ctx, `
SELECT (SELECT count(*)::integer
        FROM machines
        WHERE org_id = $1 AND display_name = 'Rejected Organization Connection'),
       (SELECT count(*)::integer
        FROM machine_daemon_tokens
        WHERE org_id = $1 AND name = 'organization-deletion-wins')
`, testOrgID).Scan(&machineCount, &tokenCount); err != nil {
			t.Fatalf("count rejected organization machine connection state: %v", err)
		}
		if machineCount != 0 || tokenCount != 0 {
			t.Fatalf(
				"rejected organization machine connection rows: machines=%d tokens=%d",
				machineCount,
				tokenCount,
			)
		}
	})
}
