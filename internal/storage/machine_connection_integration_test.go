//go:build integration

package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/tokenutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
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
	token := executionstore.MachineDaemonTokenPlaintextPrefix + "machine-connection-success"
	result, err := store.Execution().ConnectBYOMachine(ctx, executionstore.ConnectBYOMachineInput{
		OrgID:       testOrgID,
		DisplayName: "Connected Machine",
		ProjectIDs:  []ID{secondProjectID, testProjectID},
		TokenName:   "web-console",
		Token:       token,
	})
	if err != nil {
		t.Fatalf("connect machine: %v", err)
	}
	if !result.Machine.Created || result.Machine.DisplayName != "Connected Machine" {
		t.Fatalf("unexpected machine: %+v", result.Machine)
	}
	if result.TokenRecord.MachineID != result.Machine.ID {
		t.Fatalf("unexpected token record: %+v", result.TokenRecord)
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
	principal, err := store.Execution().AuthenticateMachineDaemonToken(ctx, token)
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
		Token:       executionstore.MachineDaemonTokenPlaintextPrefix + "machine-connection-no-grants",
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
	token := executionstore.MachineDaemonTokenPlaintextPrefix + "machine-connection-rollback"
	_, err := store.Execution().ConnectBYOMachine(ctx, executionstore.ConnectBYOMachineInput{
		OrgID:       testOrgID,
		DisplayName: "Rolled Back Machine",
		ProjectIDs:  []ID{testProjectID},
		TokenName:   "web-console",
		Token:       token,
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
SELECT count(*) FROM machine_daemon_tokens WHERE token_hash = $1
`, tokenutil.Hash(token)).Scan(&tokenCount); err != nil {
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
		Token:       executionstore.MachineDaemonTokenPlaintextPrefix + "machine-connection-invalid-project",
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
