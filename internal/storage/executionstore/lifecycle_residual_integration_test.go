//go:build integration

package executionstore_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/stretchr/testify/require"
)

func TestOrganizationDeletionIncludesDeletedProjectPoolAgents(t *testing.T) {
	ctx := t.Context()
	f := newMachineLifecycleLockOrderFixture(t, ctx, "residual-project")
	created, err := f.createMachineOnce(ctx)
	require.NoError(t, err)
	_, err = f.store.Organizations().DeleteProject(ctx, testOrgID, testProjectID, userPrincipal(f.userID))
	require.NoError(t, err)
	var state string
	require.NoError(t,
		f.pool.QueryRow(ctx,
			`SELECT lifecycle_state FROM machines WHERE id=$1`,
			created.Machine.Machine.ID).Scan(&state))
	require.Equal(t, "deleting", state)
	project := uuid.MustParse("ffffffff-ffff-4fff-8fff-ffffffffffff")
	_,
		err = f.pool.Exec(ctx,
		`INSERT INTO projects(id,org_id,name,created_at,updated_at) VALUES($1,$2,'remaining',now(),now())`,
		project,
		testOrgID)
	require.NoError(t, err)
	var config uuid.UUID
	require.NoError(t,
		f.pool.QueryRow(ctx,
			`INSERT INTO agent_configs(org_id,project_id,configured_model_id,source,source_format,source_hash,compiled_definition,effective_definition_hash,created_at)
 SELECT org_id,$1,configured_model_id,source,source_format,source_hash,compiled_definition,
 effective_definition_hash,now()
 FROM agent_configs WHERE id=$2 RETURNING id`,
			project,
			f.agent.CurrentConfigID).Scan(&config))
	_,
		err = f.store.Execution().CreateAgentFixture(ctx,
		executionstore.AgentFixtureInput{ProjectID: project,
			CurrentConfigID: config})
	require.NoError(t, err)
	_, err = f.store.Organizations().DeleteOrganization(ctx, testOrgID, userPrincipal(f.userID))
	require.NoError(t, err)
}

func TestGrantRevocationIncludesStartingProcessWithReleasedBinding(t *testing.T) {
	ctx := t.Context()
	f := newProcessDaemonFixture(t, ctx, "released-starting")
	tool := createToolCallForProcessTest(t, ctx, f, "starting", "run_command")
	process, err := startProcessForTest(ctx, f.Store, executionstore.ExecuteToolCallInput{
		ProjectID:     testProjectID,
		AgentID:       f.AgentID,
		RuntimeLockID: f.Lock.ID,
		ToolCallID:    tool},
		executionstore.CreateProcessInput{
			ExecutionSpec: processcmd.ForShell("sleep 1", "sh", ""), AgentMachineBindingID: f.BindingID, Cwd: "/work"})
	require.NoError(t, err)
	accepted, found, err := acceptDaemonProcessForTest(ctx, f.Store, testOrgID, f.MachineID, f.RuntimeID, uuid.Nil)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, process.ID, accepted.Process.ID)
	n,
		err := f.Store.q.ReleaseExplicitAgentMachineBinding(ctx,
		dbsqlc.ReleaseExplicitAgentMachineBindingParams{ProjectID: testProjectID,
			AgentID:   f.AgentID,
			MachineID: f.MachineID})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	sibling := uuid.MustParse("ffffffff-ffff-4fff-8fff-ffffffffffff")
	_,
		err = f.Store.pool.Exec(ctx,
		`INSERT INTO agents(id,org_id,project_id,root_agent_id,state,current_config_id,created_at,updated_at)
 SELECT $1,org_id,project_id,$1,'active',current_config_id,now(),now() FROM agents WHERE id=$2`, sibling, f.AgentID)
	require.NoError(t, err)
	_,
		err = executionstore.IntegrationInsertAgentMachineBindingTx(ctx,
		f.Store.q,
		executionstore.IntegrationInsertAgentMachineBindingInput{ProjectID: testProjectID,
			AgentID:               sibling,
			ProjectMachineGrantID: f.GrantID,
			BindingKind:           "explicit",
			Cwd:                   "/work"})
	require.NoError(t, err)
	_, err = f.Store.Execution().DeleteProjectMachineGrant(ctx, testOrgID, testProjectID, f.GrantID)
	require.NoError(t, err)
	var state string
	require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT state FROM processes WHERE id=$1`, process.ID).Scan(&state))
	require.Equal(t, "unknown", state)
}
