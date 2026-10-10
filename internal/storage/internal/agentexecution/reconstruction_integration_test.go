//go:build integration

package agentexecution_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/omnara-ai/omnara/internal/dbmigrate"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	schemamigrations "github.com/omnara-ai/omnara/migrations"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	integrationdb.RunTestMain(m)
}

type executionFixture struct {
	pool     *pgxpool.Pool
	org      uuid.UUID
	project  uuid.UUID
	config   uuid.UUID
	revision uuid.UUID
	now      time.Time
}

func releasedExecutionFixture(t *testing.T) executionFixture {
	t.Helper()
	ctx := t.Context()
	pool := integrationdb.OpenUnmigratedPool(t, ctx)
	files := fstest.MapFS{}
	entries, err := os.ReadDir("../../../../migrations")
	require.NoError(t, err)
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		version, parseErr := strconv.Atoi(strings.Split(entry.Name(), "_")[0])
		require.NoError(t, parseErr)
		if version > 51 {
			continue
		}
		body, readErr := os.ReadFile(filepath.Join("../../../../migrations", entry.Name()))
		require.NoError(t, readErr)
		files[entry.Name()] = &fstest.MapFile{Data: body, Mode: fs.FileMode(0600)}
	}
	db := stdlib.OpenDBFromPool(pool)
	require.NoError(t, dbmigrate.ApplyPostgres(ctx, db, files, schemamigrations.GoMigrations()...))
	require.NoError(t, db.Close())
	return seedExecutionFixture(t, pool)
}

func (f executionFixture) agent(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.pool.Exec(
		t.Context(),
		`INSERT INTO agents(id,org_id,project_id,state,current_config_id,created_at,updated_at)
 VALUES($1,$2,$3,'active',$4,$5,$5)`,
		id,
		f.org,
		f.project,
		f.config,
		f.now,
	)
	require.NoError(t, err)
	return id
}

func (f executionFixture) opening(
	t *testing.T,
	agent uuid.UUID,
	sequence int64,
	turn uuid.UUID,
	kind string,
) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := t.Context()
	tx, err := f.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	input, event := uuid.New(), uuid.New()
	fresh := turn == uuid.Nil
	if fresh {
		turn = uuid.New()
	}
	var config *uuid.UUID
	mode := "steering"
	if kind == "config_change" {
		config = &f.config
		mode = "immediate"
	}
	_, err = tx.Exec(
		ctx,
		`INSERT INTO agent_inputs(id,project_id,agent_id,state,input_kind,delivery_mode,agent_config_id,queued_at)
 VALUES($1,$2,$3,'received',$4,$5,$6,$7)`,
		input,
		f.project,
		agent,
		kind,
		mode,
		config,
		f.now,
	)
	require.NoError(t, err)
	_, err = tx.Exec(
		ctx,
		`
INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,agent_input_id,is_opening_event,
idempotency_key,created_at)
 VALUES($1,$2,$3,$4,'agent_input',$5,$6,'agent_input:'||($5::uuid)::text,$7)`,
		event,
		agent,
		turn,
		sequence,
		input,
		fresh,
		f.now,
	)
	require.NoError(t, err)
	_, err = tx.Exec(
		ctx,
		`UPDATE agent_inputs SET state='resolved',admitted_event_id=$2,admitted_at=$3,resolved_at=$3 WHERE id=$1`,
		input,
		event,
		f.now,
	)
	require.NoError(t, err)
	if fresh {
		_, err = tx.Exec(
			ctx,
			`INSERT INTO agent_turns(id,agent_id,turn_sequence,latest_event_id,latest_semantic_event_id)
 VALUES($1,$2,$3,$4,$4)`,
			turn,
			agent,
			sequence,
			event,
		)
	} else {
		_, err = tx.Exec(ctx, `
UPDATE agent_turns SET latest_event_id=$2,latest_semantic_event_id=$2 WHERE id=$1
`, turn, event)
	}
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	return turn, input
}

func (f executionFixture) modelContext(
	t *testing.T,
	agent uuid.UUID,
	watermark int64,
	operation string,
	source *int64,
	attempt int,
) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.pool.Exec(
		t.Context(),
		`INSERT INTO model_call_contexts(id,org_id,project_id,agent_id,operation_kind,attempt_number,
 agent_config_id,configured_model_revision_id,input_event_sequence,source_event_sequence_end,runtime_lock_id,
created_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		id,
		f.org,
		f.project,
		agent,
		operation,
		attempt,
		f.config,
		f.revision,
		watermark,
		source,
		uuid.New(),
		f.now,
	)
	require.NoError(t, err)
	return id
}

func (f executionFixture) fail(t *testing.T, id uuid.UUID, recovery string) {
	t.Helper()
	var retry *time.Time
	if recovery == "retry" {
		retry = new(f.now.Add(time.Hour))
	}
	_, err := f.pool.Exec(
		t.Context(),
		`UPDATE model_call_contexts SET state='failed',recovery_kind=$2,retry_at=$3,
 error_kind='transient',completed_at=$4 WHERE id=$1`,
		id,
		recovery,
		retry,
		f.now,
	)
	require.NoError(t, err)
}

func (f executionFixture) output(
	t *testing.T,
	agent, turn, model uuid.UUID,
	sequence int64,
	reason string,
	toolState string,
) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := t.Context()
	tx, err := f.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	output, event, call := uuid.New(), uuid.New(), uuid.Nil
	_, err = tx.Exec(
		ctx,
		`INSERT INTO model_outputs(id,agent_id,model_call_context_id,stop_reason,created_at) VALUES($1,$2,$3,$4,$5)`,
		output,
		agent,
		model,
		reason,
		f.now,
	)
	require.NoError(t, err)
	_, err = tx.Exec(
		ctx,
		`INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,model_output_id,created_at)
 VALUES($1,$2,$3,$4,'model_output',$5,$6)`,
		event,
		agent,
		turn,
		sequence,
		output,
		f.now,
	)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE agent_turns SET latest_event_id=$2,
 latest_semantic_event_id=CASE WHEN $3='tool_use' THEN latest_semantic_event_id ELSE $2 END WHERE id=$1
`, turn, event, reason)
	require.NoError(t, err)
	if toolState != "" {
		call = uuid.New()
		_, err = tx.Exec(
			ctx,
			`INSERT INTO tool_calls(id,agent_id,model_output_id,provider_call_id,name,input,type,state,created_at)
 VALUES($1,$2,$3,'call','list_agents','{}','built_in',$4,$5)`,
			call,
			agent,
			output,
			toolState,
			f.now,
		)
		require.NoError(t, err)
		_, err = tx.Exec(
			ctx,
			`
INSERT INTO content_blocks(agent_id,owner_kind,owner_model_output_id,ordinal,block_kind,tool_call_id,created_at)
        VALUES($1,'model_output',$2,0,'tool_call',$3,$4)`,
			agent,
			output,
			call,
			f.now,
		)
		require.NoError(t, err)
	}
	_, err = tx.Exec(
		ctx,
		`
UPDATE model_call_contexts SET state='succeeded',api_format='openai-responses',api_variant='default',
completed_at=$2 WHERE id=$1
`,
		model,
		f.now,
	)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	return output, call
}

func (f executionFixture) complete(t *testing.T, agent, turn, call uuid.UUID, sequence int64) {
	t.Helper()
	ctx := t.Context()
	tx, err := f.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	result, event := uuid.New(), uuid.New()
	_, err = tx.Exec(ctx, `UPDATE tool_calls SET state='completed',runtime_lock_id=NULL WHERE id=$1`, call)
	require.NoError(t, err)
	_, err = tx.Exec(
		ctx,
		`INSERT INTO tool_call_results(id,agent_id,tool_call_id,outcome,completed_at) VALUES($1,$2,$3,'succeeded',$4)`,
		result,
		agent,
		call,
		f.now,
	)
	require.NoError(t, err)
	_, err = tx.Exec(
		ctx,
		`INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,tool_call_result_id,created_at)
 VALUES($1,$2,$3,$4,'tool_result',$5,$6)`,
		event,
		agent,
		turn,
		sequence,
		result,
		f.now,
	)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE agent_turns SET latest_event_id=$2 WHERE id=$1`, turn, event)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
}

func (f executionFixture) checkpoint(
	t *testing.T,
	agent, turn, model uuid.UUID,
	sequence, source int64,
) uuid.UUID {
	t.Helper()
	ctx := t.Context()
	tx, err := f.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	checkpoint, event := uuid.New(), uuid.New()
	_, err = tx.Exec(
		ctx,
		`
INSERT INTO context_checkpoints(id,agent_id,summarized_through_event_sequence,producer_model_call_context_id,
summary,created_at)
 VALUES($1,$2,$3,$4,'summary',$5)`,
		checkpoint,
		agent,
		source,
		model,
		f.now,
	)
	require.NoError(t, err)
	_, err = tx.Exec(
		ctx,
		`INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,context_checkpoint_id,created_at)
 VALUES($1,$2,$3,$4,'context_checkpoint',$5,$6)`,
		event,
		agent,
		turn,
		sequence,
		checkpoint,
		f.now,
	)
	require.NoError(t, err)
	_, err = tx.Exec(
		ctx,
		`
UPDATE model_call_contexts SET state='succeeded',api_format='openai-responses',api_variant='default',
completed_at=$2 WHERE id=$1
`,
		model,
		f.now,
	)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `UPDATE agent_turns SET latest_event_id=$2 WHERE id=$1`, turn, event)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	return checkpoint
}

func applyExecutionCutover(ctx context.Context, pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	return dbmigrate.ApplyPostgres(ctx, db, os.DirFS("../../../../migrations"), schemamigrations.GoMigrations()...)
}

func (f executionFixture) handle(
	t *testing.T,
	agent uuid.UUID,
) (*agentexecution.Unit, *agentexecution.Handle) {
	t.Helper()
	ctx := t.Context()
	cell := agentexecution.NewCell("test", f.pool, nil, nil)
	u, err := cell.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = u.Rollback(context.Background()) })
	plan, err := u.PlanAgents(
		ctx,
		[]lifecyclelock.AgentRef{{ProjectID: f.project, AgentID: agent}},
		agentexecution.RepairAuthority{},
	)
	require.NoError(t, err)
	require.NoError(t, u.LockAgents(ctx, plan))
	var root uuid.UUID
	require.NoError(
		t,
		u.DB().QueryRow(ctx, `SELECT root_agent_id FROM agents WHERE id=$1`, agent).Scan(&root),
	)
	h, err := u.Agent(
		agentexecution.AgentRoute{CellID: cell.ID(), ProjectID: f.project, AgentID: agent, RootAgentID: root},
	)
	require.NoError(t, err)
	return u, h
}

func TestReleasedExecutionReconstruction(t *testing.T) {
	f := releasedExecutionFixture(t)
	cases := []struct {
		name   string
		build  func(uuid.UUID)
		work   agentexecution.WorkKind
		origin agentexecution.CandidateOrigin
	}{
		{"empty", func(uuid.UUID) {}, agentexecution.WorkNone, 0},
		{
			"initial",
			func(a uuid.UUID) { f.opening(t, a, 1, uuid.Nil, "content") },
			agentexecution.WorkModel,
			agentexecution.OriginInitial,
		},
		{"retry", func(a uuid.UUID) {
			f.opening(t, a, 1, uuid.Nil, "content")
			c := f.modelContext(t, a, 1, "normal", nil, 1)
			f.fail(t, c, "retry")
		}, agentexecution.WorkNone, agentexecution.OriginRetry},
		{
			"started",
			func(a uuid.UUID) {
				f.opening(t, a, 1, uuid.Nil, "content")
				f.modelContext(t, a, 1, "normal", nil, 1)
			},
			agentexecution.WorkNone,
			0,
		},
		{"output_limit", func(a uuid.UUID) {
			turn, _ := f.opening(t, a, 1, uuid.Nil, "content")
			c := f.modelContext(t, a, 1, "normal", nil, 1)
			f.output(t, a, turn, c, 2, "max_tokens", "")
		}, agentexecution.WorkModel, agentexecution.OriginOutputLimit},
		{"tools", func(a uuid.UUID) {
			turn, _ := f.opening(t, a, 1, uuid.Nil, "content")
			c := f.modelContext(t, a, 1, "normal", nil, 1)
			f.output(t, a, turn, c, 2, "tool_use", "awaiting_authorization")
		}, agentexecution.WorkTool, 0},
		{"completed_tools", func(a uuid.UUID) {
			turn, _ := f.opening(t, a, 1, uuid.Nil, "content")
			c := f.modelContext(t, a, 1, "normal", nil, 1)
			_, call := f.output(t, a, turn, c, 2, "tool_use", "awaiting_authorization")
			f.complete(t, a, turn, call, 3)
		}, agentexecution.WorkModel, agentexecution.OriginTools},
		{"config", func(a uuid.UUID) {
			turn, _ := f.opening(t, a, 1, uuid.Nil, "content")
			c := f.modelContext(t, a, 1, "normal", nil, 1)
			f.output(t, a, turn, c, 2, "end_turn", "")
			f.opening(t, a, 3, turn, "config_change")
		}, agentexecution.WorkModel, agentexecution.OriginConfig},
		{"semantic", func(a uuid.UUID) {
			turn, _ := f.opening(t, a, 1, uuid.Nil, "content")
			c := f.modelContext(t, a, 1, "normal", nil, 1)
			f.fail(t, c, "retry")
			f.opening(t, a, 2, turn, "config_change")
		}, agentexecution.WorkModel, agentexecution.OriginSemantic},
		{"compaction", func(a uuid.UUID) {
			f.opening(t, a, 1, uuid.Nil, "content")
			c := f.modelContext(t, a, 1, "normal", nil, 1)
			f.fail(t, c, "compact")
			f.modelContext(t, a, 1, "compaction", new(int64(1)), 1)
		}, agentexecution.WorkNone, 0},
		{"checkpoint", func(a uuid.UUID) {
			turn, _ := f.opening(t, a, 1, uuid.Nil, "content")
			normal := f.modelContext(t, a, 1, "normal", nil, 1)
			f.fail(t, normal, "compact")
			compact := f.modelContext(t, a, 1, "compaction", new(int64(1)), 1)
			f.checkpoint(t, a, turn, compact, 2, 1)
		}, agentexecution.WorkModel, agentexecution.OriginCheckpoint},
	}

	ids := make([]uuid.UUID, len(cases))
	for i, tc := range cases {
		ids[i] = f.agent(t)
		tc.build(ids[i])
	}
	require.NoError(t, applyExecutionCutover(t.Context(), f.pool))
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, h := f.handle(t, ids[i])
			got, err := h.LoadExecution(t.Context())
			require.NoError(t, err)
			rebuilt, err := h.ReconstructExecution(t.Context())
			require.NoError(t, err)
			got.Head.LogicalReadyAt = nil
			rebuilt.Head.LogicalReadyAt = nil
			require.Equal(t, got.Head, rebuilt.Head)
			require.Equal(t, tc.work, got.Selection.Work)

			if tc.origin != 0 {
				require.NotNil(t, got.Selection.Model)
				require.Equal(t, tc.origin, got.Selection.Model.Origin)
			}
			require.NoError(t, u.Rollback(t.Context()))
		})
	}
}

func TestExecutionCutoverRejectsMultipleBatches(t *testing.T) {
	f := releasedExecutionFixture(t)
	agent := f.agent(t)
	turn, _ := f.opening(t, agent, 1, uuid.Nil, "content")
	first := f.modelContext(t, agent, 1, "normal", nil, 1)
	f.output(t, agent, turn, first, 2, "tool_use", "awaiting_authorization")
	second := f.modelContext(t, agent, 2, "normal", nil, 1)
	f.output(t, agent, turn, second, 3, "tool_use", "awaiting_authorization")
	err := applyExecutionCutover(t.Context(), f.pool)
	require.ErrorContains(t, err, "multiple unconsumed tool outputs")
	var exists bool
	require.NoError(
		t,
		f.pool.QueryRow(t.Context(), `SELECT to_regclass('agent_execution_state') IS NOT NULL`).Scan(&exists),
	)
	require.False(t, exists)
}

func TestExecutionCutoverRejectsEmptyContextLineage(t *testing.T) {
	f := releasedExecutionFixture(t)
	agent := f.agent(t)
	f.opening(t, agent, 1, uuid.Nil, "config_change")
	f.modelContext(t, agent, 1, "normal", nil, 1)
	require.Error(t, applyExecutionCutover(t.Context(), f.pool))
}
