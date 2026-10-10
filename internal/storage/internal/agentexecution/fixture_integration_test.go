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

func migratedExecutionFixture(t *testing.T) executionFixture {
	t.Helper()
	return seedExecutionFixture(t, integrationdb.OpenMigratedPool(t, t.Context(), "../../../../migrations"))
}

func seedExecutionFixture(t *testing.T, pool *pgxpool.Pool) executionFixture {
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
	store := storage.NewStore(pool)
	config := storagefixture.SeedAgentConfig(
		t,
		ctx,
		store.Models(),
		store.Execution(),
		ids.OrgID,
		ids.ProjectID,
		"instruction: test\nmodel:\n  provider_config: openai-prod\n  name: test\n",
	)
	var revision uuid.UUID
	require.NoError(
		t,
		pool.QueryRow(ctx,
			`SELECT current_revision_id FROM configured_models WHERE id=$1`,
			config.ConfiguredModelID).
			Scan(&revision),
	)
	return executionFixture{
		pool:     pool,
		org:      ids.OrgID,
		project:  ids.ProjectID,
		config:   config.ID,
		revision: revision,
		now:      now,
	}
}

func (f executionFixture) create(t *testing.T, id, parent uuid.UUID) uuid.UUID {
	t.Helper()
	cell := agentexecution.NewCell("test", f.pool, nil, nil)
	u, err := cell.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = u.Rollback(t.Context()) }()
	key := ""
	if parent != uuid.Nil {
		key = id.String()
		require.NoError(t, u.LockAgentRefs(t.Context(),
			[]lifecyclelock.AgentRef{{ProjectID: f.project, AgentID: parent}}, agentexecution.LifecycleAuthority{}))
	}
	_, err = u.CreateAgent(t.Context(), agentexecution.CreateAgentInput{ID: id, ProjectID: f.project,
		ConfigID: f.config, ParentID: parent, SubagentKey: key, Name: "Agent"})
	require.NoError(t, err)
	require.NoError(t, u.Commit(t.Context(), "create fixture"))
	return id
}
