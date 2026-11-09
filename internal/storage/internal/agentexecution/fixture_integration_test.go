//go:build integration

package agentexecution_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

const executionFixtureSource = "instruction: test\nmodel:\n  provider_config: openai-prod\n  name: test\n"

func migratedExecutionFixture(t *testing.T) executionFixture {
	t.Helper()
	return seedExecutionFixture(
		t,
		integrationdb.OpenMigratedPool(t, t.Context(), "../../../../migrations"),
		func(store *storage.Store, ids storagefixture.ProjectIDs) (uuid.UUID, uuid.UUID) {
			config := storagefixture.SeedAgentConfig(
				t, t.Context(), store.Models(), store.Execution(), ids.OrgID, ids.ProjectID, executionFixtureSource,
			)
			return config.ID, config.ConfiguredModelID
		},
	)
}

// seedSchema52AgentConfig writes the config in the shape released schema 52
// stored it, before compiled content moved to agent_config_definitions.
func seedSchema52AgentConfig(
	t *testing.T,
	pool *pgxpool.Pool,
) func(*storage.Store, storagefixture.ProjectIDs) (uuid.UUID, uuid.UUID) {
	return func(store *storage.Store, ids storagefixture.ProjectIDs) (uuid.UUID, uuid.UUID) {
		ctx := t.Context()
		compiled := storagefixture.SeedModelAndCompileAgentYAML(
			t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID, executionFixtureSource,
		)
		modelID := compiled.Compiled.Model.ConfiguredModelID
		var configID uuid.UUID
		require.NoError(t, pool.QueryRow(ctx, `INSERT INTO agent_configs(org_id,project_id,configured_model_id,
 source,source_format,source_hash,compiled_definition,effective_definition_hash,created_at)
 VALUES($1,$2,$3,$4,'yaml',encode(sha256(convert_to($4,'UTF8')),'hex'),$5,$6,now()) RETURNING id`,
			ids.OrgID, ids.ProjectID, modelID, executionFixtureSource, compiled.CanonicalJSON, compiled.Hash,
		).Scan(&configID))
		return configID, modelID
	}
}

func seedExecutionFixture(
	t *testing.T,
	pool *pgxpool.Pool,
	seedConfig func(*storage.Store, storagefixture.ProjectIDs) (uuid.UUID, uuid.UUID),
) executionFixture {
	t.Helper()
	ctx := t.Context()
	ids := storagefixture.ProjectIDs{
		OrgID:                   uuid.New(),
		ProjectID:               uuid.New(),
		ProviderAdminUserID:     uuid.New(),
		ProviderSecretID:        uuid.New(),
		ProviderSecretVersionID: uuid.New(),
		ProviderConfigID:        uuid.New(),
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	storagefixture.SeedProject(t, ctx, pool, ids, now)
	configID, configuredModelID := seedConfig(storage.NewStore(pool), ids)
	var revision uuid.UUID
	require.NoError(
		t,
		pool.QueryRow(ctx,
			`SELECT current_revision_id FROM configured_models WHERE id=$1`,
			configuredModelID).
			Scan(&revision),
	)
	return executionFixture{
		pool:     pool,
		org:      ids.OrgID,
		project:  ids.ProjectID,
		config:   configID,
		revision: revision,
		now:      now,
	}
}

func (f executionFixture) create(t *testing.T, id, parent uuid.UUID) uuid.UUID {
	t.Helper()
	cell := agentexecution.NewCell(f.pool, nil, nil)
	u, err := agentexecution.BeginTestUnit(t.Context(), cell)
	require.NoError(t, err)
	defer func() { _ = u.Rollback(t.Context()) }()
	key := ""
	if parent != uuid.Nil {
		key = id.String()
		require.NoError(t, u.LockAgentRefs(t.Context(),
			[]lifecyclelock.AgentRef{{ProjectID: f.project, AgentID: parent}}, nil))
	}
	_, err = u.CreateAgent(t.Context(), agentexecution.CreateAgentInput{ID: id, ProjectID: f.project,
		ConfigID: f.config, ParentID: parent, SubagentKey: key, Name: "Agent"})
	require.NoError(t, err)
	require.NoError(t, u.Commit(t.Context(), "create fixture"))
	return id
}
