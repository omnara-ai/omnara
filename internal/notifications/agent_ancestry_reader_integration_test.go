//go:build integration

package notifications_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
)

func TestMain(m *testing.M) {
	integrationdb.RunTestMain(m)
}

func TestAgentNotificationReaderBoundsAndScopesCommittedAncestry(t *testing.T) {
	ctx := context.Background()
	pool := integrationdb.OpenMigratedPool(t, ctx, "../../migrations")
	ids := storagefixture.ProjectIDs{
		OrgID: uuid.New(), ProjectID: uuid.New(), ProviderAdminUserID: uuid.New(),
		ProviderSecretID: uuid.New(), ProviderSecretVersionID: uuid.New(), ProviderConfigID: uuid.New(),
	}
	storagefixture.SeedProject(t, ctx, pool, ids, time.Now())
	store := storage.NewStore(pool)
	config := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID, `
instruction: Test notification ancestry.
model:
  provider_config: openai-prod
  name: gpt-test
`)
	// A chain beyond the admission limit proves that the read itself enforces
	// its work bound, independently of launch validation.
	chain := make([]uuid.UUID, 11)
	for i := range chain {
		chain[i] = uuid.New()
		var parentID *uuid.UUID
		key := ""
		if i > 0 {
			parentID = &chain[i-1]
			key = "child"
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO agents (id, org_id, project_id, current_config_id, state,
                    parent_agent_id, subagent_key, created_at, updated_at)
VALUES ($1, $2, $3, $4, 'active', $5, $6, now(), now())`,
			chain[i], ids.OrgID, ids.ProjectID, config.ID, parentID, key); err != nil {
			t.Fatal(err)
		}
	}
	// Archived ancestors remain routing destinations; status is not membership.
	if _, err := pool.Exec(ctx,
		`UPDATE agents SET state = 'archived', archived_at = now() WHERE id = $1`, chain[1]); err != nil {
		t.Fatal(err)
	}
	reader := executionstore.NewAgentNotificationReader(pool)
	assertAncestors := func(projectID, ownerID uuid.UUID, want []uuid.UUID) {
		t.Helper()
		got, err := reader.ListAgentAncestors(ctx, projectID, ownerID)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("ancestors(%s/%s) = %v, want %v", projectID, ownerID, got, want)
		}
	}
	assertAncestors(ids.ProjectID, chain[0], nil)
	assertAncestors(ids.ProjectID, chain[2], []uuid.UUID{chain[1], chain[0]})
	assertAncestors(ids.ProjectID, chain[10], []uuid.UUID{
		chain[9], chain[8], chain[7], chain[6], chain[5], chain[4], chain[3], chain[2],
	})
	assertAncestors(uuid.New(), chain[2], nil)
	assertAncestors(ids.ProjectID, uuid.New(), nil)

	childID := uuid.New()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
INSERT INTO agents (id, org_id, project_id, current_config_id, state,
                    parent_agent_id, subagent_key, created_at, updated_at)
VALUES ($1, $2, $3, $4, 'active', $5, 'child', now(), now())`,
		childID, ids.OrgID, ids.ProjectID, config.ID, chain[0]); err != nil {
		t.Fatal(err)
	}
	assertAncestors(ids.ProjectID, childID, nil)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	assertAncestors(ids.ProjectID, childID, []uuid.UUID{chain[0]})
	for _, scope := range [][2]uuid.UUID{{uuid.Nil, childID}, {ids.ProjectID, uuid.Nil}} {
		if _, err := reader.ListAgentAncestors(ctx, scope[0], scope[1]); err == nil {
			t.Fatal("reader accepted incomplete scope")
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := reader.ListAgentAncestors(canceled, ids.ProjectID, childID); err == nil {
		t.Fatal("reader ignored cancellation")
	}
}
