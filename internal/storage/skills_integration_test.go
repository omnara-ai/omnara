//go:build integration

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
	"github.com/omnara-ai/omnara/internal/storage/skillstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

type coordinatedSkillBlobStore struct {
	delegate blobstore.Store
	putKeys  chan string
	release  chan struct{}
	once     sync.Once

	mu         sync.Mutex
	deleteKeys []string
}

func newCoordinatedSkillBlobStore(delegate blobstore.Store) *coordinatedSkillBlobStore {
	return &coordinatedSkillBlobStore{
		delegate: delegate,
		putKeys:  make(chan string, 4),
		release:  make(chan struct{}),
	}
}

func (s *coordinatedSkillBlobStore) PutBlob(
	ctx context.Context,
	key string,
	content []byte,
) (blobstore.Metadata, error) {
	s.putKeys <- key
	select {
	case <-ctx.Done():
		return blobstore.Metadata{}, ctx.Err()
	case <-s.release:
		return s.delegate.PutBlob(ctx, key, content)
	}
}

func (s *coordinatedSkillBlobStore) GetBlob(
	ctx context.Context,
	key string,
) ([]byte, blobstore.Metadata, error) {
	return s.delegate.GetBlob(ctx, key)
}

func (s *coordinatedSkillBlobStore) DeleteBlob(ctx context.Context, key string) error {
	s.mu.Lock()
	s.deleteKeys = append(s.deleteKeys, key)
	s.mu.Unlock()
	return s.delegate.DeleteBlob(ctx, key)
}

func (s *coordinatedSkillBlobStore) releasePuts() {
	s.once.Do(func() { close(s.release) })
}

func (s *coordinatedSkillBlobStore) deletedKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.deleteKeys...)
}

func TestSkillsStorageFlatOwnershipVisibilityAndPagination(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithBlobStore(integrationblob.MustOpen(t, ctx)))
	admin := createSecretTestUser(t, ctx, store, "Skills Admin", "admin")
	developer := createSecretTestUser(t, ctx, store, "Skills Developer", "member")
	other := createSecretTestUser(t, ctx, store, "Skills Other", "member")
	if _, err := store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
		OrgID: testOrgID, ProjectID: testProjectID, UserID: developer.ID,
		Role: "admin",
	}); err != nil {
		t.Fatalf("add developer project membership: %v", err)
	}

	orgSkill := createIntegrationSkill(t, ctx, store, skillstore.CreateSkillInput{
		OrgID: testOrgID, OwnerKind: skillstore.SkillOwnerOrg, Name: "org-skill",
		Actor: userPrincipal(admin.ID),
	})
	projectSkill := createIntegrationSkill(t, ctx, store, skillstore.CreateSkillInput{
		OrgID: testOrgID, OwnerKind: skillstore.SkillOwnerProject, OwnerProjectID: testProjectID,
		Name: "project-skill", Actor: userPrincipal(developer.ID),
	})
	userSkill := createIntegrationSkill(t, ctx, store, skillstore.CreateSkillInput{
		OrgID: testOrgID, OwnerKind: skillstore.SkillOwnerUser, OwnerUserID: developer.ID,
		Name: "user-skill", Actor: userPrincipal(developer.ID),
	})
	userSkillPublicID, err := publicSkillID(userSkill.ID)
	if err != nil {
		t.Fatalf("encode user skill id: %v", err)
	}
	orgSkillPublicID, err := publicSkillID(orgSkill.ID)
	if err != nil {
		t.Fatalf("encode organization skill id: %v", err)
	}
	resolved, missing, err := store.Skills().GetSkillsByIDsForCompile(ctx, skillstore.GetSkillsByIDsInput{
		OrgID: testOrgID, ProjectID: testProjectID, IDs: []string{orgSkillPublicID},
	})
	if err != nil {
		t.Fatalf("resolve ungranted organization skill: %v", err)
	}
	if len(resolved) != 0 || len(missing) != 1 || missing[0] != orgSkillPublicID {
		t.Fatalf("ungranted organization skill resolved=%+v missing=%+v", resolved, missing)
	}
	if _, err := store.Skills().GetSkillForDispatch(ctx, testProjectID, orgSkillPublicID); !storeerr.IsNotFound(err) {
		t.Fatalf("dispatch ungranted organization skill error = %v, want not found", err)
	}
	if _, err := store.Skills().CreateSkillGrant(ctx, skillstore.CreateSkillGrantInput{
		OrgID: testOrgID, SkillID: orgSkill.ID, TargetProjectID: testID("missing-skill-grant-project"),
		Actor: userPrincipal(admin.ID),
	}); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("grant skill to missing project error = %v, want ErrNotFound", err)
	}
	orgGrant, err := store.Skills().CreateSkillGrant(ctx, skillstore.CreateSkillGrantInput{
		OrgID: testOrgID, SkillID: orgSkill.ID, TargetProjectID: testProjectID,
		Actor: userPrincipal(admin.ID),
	})
	if err != nil {
		t.Fatalf("grant organization skill to project: %v", err)
	}
	resolved, missing, err = store.Skills().GetSkillsByIDsForCompile(ctx, skillstore.GetSkillsByIDsInput{
		OrgID: testOrgID, ProjectID: testProjectID, IDs: []string{orgSkillPublicID},
	})
	if err != nil {
		t.Fatalf("resolve granted organization skill: %v", err)
	}
	if len(resolved) != 1 || resolved[0].ID != orgSkill.ID || len(missing) != 0 {
		t.Fatalf("granted organization skill resolved=%+v missing=%+v", resolved, missing)
	}
	resolved, missing, err = store.Skills().GetSkillsByIDsForCompile(ctx, skillstore.GetSkillsByIDsInput{
		OrgID: testOrgID, ProjectID: testProjectID, IDs: []string{userSkillPublicID},
	})
	if err != nil {
		t.Fatalf("resolve ungranted user skill: %v", err)
	}
	if len(resolved) != 0 || len(missing) != 1 || missing[0] != userSkillPublicID {
		t.Fatalf("ungranted user skill resolved=%+v missing=%+v", resolved, missing)
	}
	grant, err := store.Skills().CreateSkillGrant(ctx, skillstore.CreateSkillGrantInput{
		OrgID: testOrgID, SkillID: userSkill.ID, TargetProjectID: testProjectID,
		Actor: userPrincipal(developer.ID),
	})
	if err != nil {
		t.Fatalf("grant user skill to project: %v", err)
	}
	resolved, missing, err = store.Skills().GetSkillsByIDsForCompile(ctx, skillstore.GetSkillsByIDsInput{
		OrgID: testOrgID, ProjectID: testProjectID, IDs: []string{userSkillPublicID},
	})
	if err != nil {
		t.Fatalf("resolve granted user skill: %v", err)
	}
	if len(resolved) != 1 || resolved[0].ID != userSkill.ID || len(missing) != 0 {
		t.Fatalf("granted user skill resolved=%+v missing=%+v", resolved, missing)
	}
	available, err := store.Skills().ListProjectAvailableSkills(ctx, skillstore.ListProjectAvailableSkillsInput{
		OrgID: testOrgID, ProjectID: testProjectID, Limit: 10,
	})
	if err != nil {
		t.Fatalf("list project available skills: %v", err)
	}
	wantAvailability := map[ID]string{
		orgSkill.ID: skillstore.SkillAvailabilityGrant, projectSkill.ID: skillstore.SkillAvailabilityDirect,
		userSkill.ID: skillstore.SkillAvailabilityGrant,
	}
	if len(available.Accesses) != len(wantAvailability) {
		t.Fatalf("project available skill count = %d, want %d: %+v", len(available.Accesses), len(wantAvailability), available.Accesses)
	}
	for _, access := range available.Accesses {
		if got, ok := wantAvailability[access.Skill.ID]; !ok || got != access.Availability {
			t.Fatalf("unexpected project skill access: %+v", access)
		}
		if access.Skill.ID == userSkill.ID && access.GrantID != grant.ID {
			t.Fatalf("granted skill grant id = %s, want %s", access.GrantID, grant.ID)
		}
		if access.Skill.ID == orgSkill.ID && access.GrantID != orgGrant.ID {
			t.Fatalf("organization skill grant id = %s, want %s", access.GrantID, orgGrant.ID)
		}
	}
	if dispatched, err := store.Skills().GetSkillForDispatch(ctx, testProjectID, orgSkillPublicID); err != nil || dispatched.ID != orgSkill.ID {
		t.Fatalf("dispatch granted organization skill = %+v, err=%v", dispatched, err)
	}
	if dispatched, err := store.Skills().GetSkillForDispatch(ctx, testProjectID, userSkillPublicID); err != nil || dispatched.ID != userSkill.ID {
		t.Fatalf("dispatch granted user skill = %+v, err=%v", dispatched, err)
	}
	grantPage, err := store.Skills().ListSkillGrants(ctx, skillstore.ListSkillGrantsInput{
		OrgID: testOrgID, SkillID: userSkill.ID, Actor: userPrincipal(developer.ID), Limit: 10,
	})
	if err != nil || len(grantPage.Grants) != 1 || grantPage.Grants[0].Grant.ID != grant.ID {
		t.Fatalf("list user skill grants = %+v, err=%v", grantPage, err)
	}

	visible, err := store.Skills().ListSkills(ctx, skillstore.ListSkillsInput{
		OrgID: testOrgID, Actor: userPrincipal(developer.ID), Limit: 10,
	})
	if err != nil {
		t.Fatalf("list developer skills: %v", err)
	}
	for _, id := range []ID{orgSkill.ID, projectSkill.ID, userSkill.ID} {
		if !containsSkill(visible.Skills, id) {
			t.Fatalf("developer list missing skill %s: %+v", id, visible.Skills)
		}
	}

	otherVisible, err := store.Skills().ListSkills(ctx, skillstore.ListSkillsInput{
		OrgID: testOrgID, Actor: userPrincipal(other.ID), Limit: 10,
	})
	if err != nil {
		t.Fatalf("list other skills: %v", err)
	}
	if !containsSkill(otherVisible.Skills, orgSkill.ID) ||
		containsSkill(otherVisible.Skills, projectSkill.ID) ||
		containsSkill(otherVisible.Skills, userSkill.ID) {
		t.Fatalf("other visibility mismatch: %+v", otherVisible.Skills)
	}

	first, err := store.Skills().ListSkills(ctx, skillstore.ListSkillsInput{
		OrgID: testOrgID, Actor: userPrincipal(developer.ID), Limit: 1,
	})
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if len(first.Skills) != 1 || !first.HasMore {
		t.Fatalf("first page = %+v", first)
	}
	second, err := store.Skills().ListSkills(ctx, skillstore.ListSkillsInput{
		OrgID: testOrgID, Actor: userPrincipal(developer.ID), Limit: 1,
		List: listing.Options{After: first.Next},
	})
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	if len(second.Skills) != 1 || second.Skills[0].ID == first.Skills[0].ID {
		t.Fatalf("second page = %+v after first = %+v", second, first)
	}

	projectPublicID, err := publicSkillID(projectSkill.ID)
	if err != nil {
		t.Fatalf("encode project skill id: %v", err)
	}
	if _, err := store.Skills().GetVisibleSkill(ctx, testOrgID, projectPublicID, userPrincipal(other.ID)); !storeerr.IsNotFound(err) {
		t.Fatalf("get project skill as non-member error = %v, want not found", err)
	}
	if err := store.Skills().DeleteSkill(ctx, skillstore.DeleteSkillInput{
		OrgID: testOrgID, SkillID: userSkill.ID, Actor: userPrincipal(other.ID),
	}); !storeerr.IsNotFound(err) {
		t.Fatalf("delete another user's skill error = %v, want not found", err)
	}
	if _, err := store.Skills().CreateSkillRevision(ctx, skillstore.CreateSkillInput{
		OrgID: testOrgID, OwnerKind: skillstore.SkillOwnerProject, OwnerProjectID: testProjectID,
		Name: "unauthorized", Description: "unauthorized", SkillMd: "# unauthorized",
		ArchiveBytes: []byte("unauthorized"), Actor: userPrincipal(other.ID),
	}); !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("create project skill as non-member error = %v, want unauthorized", err)
	}
	if _, err := store.Skills().DeleteSkillGrant(ctx, skillstore.DeleteSkillGrantInput{
		OrgID: testOrgID, SkillID: userSkill.ID, GrantID: grant.ID, Actor: userPrincipal(developer.ID),
	}); err != nil {
		t.Fatalf("revoke user skill grant: %v", err)
	}
	resolved, missing, err = store.Skills().GetSkillsByIDsForCompile(ctx, skillstore.GetSkillsByIDsInput{
		OrgID: testOrgID, ProjectID: testProjectID, IDs: []string{userSkillPublicID},
	})
	if err != nil || len(resolved) != 0 || len(missing) != 1 {
		t.Fatalf("revoked user skill resolved=%+v missing=%+v err=%v", resolved, missing, err)
	}
	if _, err := store.Skills().GetSkillForDispatch(ctx, testProjectID, userSkillPublicID); !storeerr.IsNotFound(err) {
		t.Fatalf("dispatch revoked user skill error = %v, want not found", err)
	}
}

func TestDeleteSkillBlockedWhileActiveAgentReferencesIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithBlobStore(integrationblob.MustOpen(t, ctx)))
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	admin := createSecretTestUser(t, ctx, store, "Skill Delete Admin", "admin")

	skill := createIntegrationSkill(t, ctx, store, skillstore.CreateSkillInput{
		OrgID: testOrgID, OwnerKind: skillstore.SkillOwnerProject, OwnerProjectID: testProjectID,
		Name: "agent-referenced-skill", Actor: userPrincipal(admin.ID),
	})
	skillPublicID, err := publicSkillID(skill.ID)
	if err != nil {
		t.Fatalf("encode skill id: %v", err)
	}
	sourceYAML := `
name: Skill Delete Agent
instruction: test
model:
  provider_config: openai-prod
  name: test
skills:
  - ` + skillPublicID + `
`
	configuredModel := ensureTestConfiguredModelForSource(t, ctx, store, sourceYAML, now)
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(sourceYAML), agentconfig.CompileOptions{
		ResolveModelSelection: func(providerConfigName string, configuredModelName string) (agentconfig.ResolvedModelSelection, error) {
			return resolvedTestModelSelection(configuredModel), nil
		},
		ResolveSkillID: func(id string) (agentconfig.SkillResolution, error) {
			return agentconfig.SkillResolution{PublicID: id, Name: skill.Name}, nil
		},
	})
	if err != nil {
		t.Fatalf("compile agent yaml with skill: %v", err)
	}
	config, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               testProjectID,
		Definition:              json.RawMessage(compiled.CanonicalJSON),
		Source:                  sourceYAML,
		SourceFormat:            "yaml",
		ConfiguredModelID:       configuredModel.ID,
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiled.Hash,
	})
	if err != nil {
		t.Fatalf("create agent config with skill: %v", err)
	}
	agent, err := store.Execution().CreateAgentFixture(ctx, executionstore.AgentFixtureInput{
		ProjectID: testProjectID, CurrentConfigID: config.ID,
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}

	if err := store.Skills().DeleteSkill(ctx, skillstore.DeleteSkillInput{
		OrgID: testOrgID, SkillID: skill.ID, Actor: userPrincipal(admin.ID),
	}); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("delete referenced skill error = %v, want conflict", err)
	}
	if _, err := store.Skills().GetSkillByPublicID(ctx, testOrgID, skillPublicID); err != nil {
		t.Fatalf("referenced skill should survive blocked delete: %v", err)
	}

	if _, _, err := store.Execution().ArchiveAgent(ctx, testProjectID, agent.ID, userPrincipal(admin.ID)); err != nil {
		t.Fatalf("delete agent: %v", err)
	}
	if err := store.Skills().DeleteSkill(ctx, skillstore.DeleteSkillInput{
		OrgID: testOrgID, SkillID: skill.ID, Actor: userPrincipal(admin.ID),
	}); err != nil {
		t.Fatalf("delete skill after agent deletion: %v", err)
	}
	if _, err := store.Skills().GetSkillByPublicID(ctx, testOrgID, skillPublicID); !storeerr.IsNotFound(err) {
		t.Fatalf("deleted skill lookup error = %v, want not found", err)
	}
}

func TestSkillIdentityRequiresFirstRevision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	emptyID := testID("empty-skill-identity")

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin empty skill identity transaction: %v", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO skills(id, org_id, owner_kind, name, created_at)
VALUES ($1, $2, 'org', 'repair-empty-identity', statement_timestamp())
`, emptyID, testOrgID); err != nil {
		t.Fatalf("insert empty skill identity: %v", err)
	}
	if err := tx.Commit(ctx); !isPgCode(err, "23514") {
		t.Fatalf("commit empty skill identity error = %v, want check violation", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM skills WHERE id = $1`, emptyID).Scan(&count); err != nil {
		t.Fatalf("count empty skill identities: %v", err)
	}
	if count != 0 {
		t.Fatalf("empty skill identity count = %d, want 0", count)
	}
}

func TestConcurrentInitialSkillUploadsShareIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	blobs := newCoordinatedSkillBlobStore(integrationblob.MustOpen(t, ctx))
	defer blobs.releasePuts()
	store := NewStore(pool, WithBlobStore(blobs))
	admin := createSecretTestUser(t, ctx, store, "Concurrent skill admin", "admin")

	type uploadResult struct {
		record skillstore.SkillRecord
		err    error
	}
	results := make(chan uploadResult, 2)
	for revision := 1; revision <= 2; revision++ {
		revision := revision
		go func() {
			record, err := store.Skills().CreateSkillRevision(context.Background(), skillstore.CreateSkillInput{
				OrgID: testOrgID, OwnerKind: skillstore.SkillOwnerOrg, Name: "concurrent-first-upload",
				Description:  "concurrent revision",
				SkillMd:      "# Concurrent revision",
				ArchiveBytes: []byte{byte(revision)},
				Actor:        identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: admin.ID},
			})
			results <- uploadResult{record: record, err: err}
		}()
	}

	firstKeys := make([]string, 0, 2)
	for len(firstKeys) < 2 {
		select {
		case key := <-blobs.putKeys:
			firstKeys = append(firstKeys, key)
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent skill uploads did not both reach blob storage")
		}
	}
	firstSkillKey := strings.Split(firstKeys[0], "/")
	secondSkillKey := strings.Split(firstKeys[1], "/")
	if len(firstSkillKey) < 2 || len(secondSkillKey) < 2 || firstSkillKey[1] == secondSkillKey[1] {
		t.Fatalf("initial uploads did not use distinct candidate identities: %q, %q", firstKeys[0], firstKeys[1])
	}
	blobs.releasePuts()

	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent skill uploads errors = %v, %v", first.err, second.err)
	}
	if first.record.ID != second.record.ID {
		t.Fatalf("concurrent skill identities = %s and %s, want one identity", first.record.ID, second.record.ID)
	}
	if revisions := map[int32]bool{
		first.record.Revision:  true,
		second.record.Revision: true,
	}; !revisions[1] || !revisions[2] || len(revisions) != 2 {
		t.Fatalf("concurrent revision numbers = %d and %d, want 1 and 2", first.record.Revision, second.record.Revision)
	}

	deleted := make(map[string]bool)
	for _, key := range blobs.deletedKeys() {
		deleted[key] = true
	}
	if deleted[firstKeys[0]] == deleted[firstKeys[1]] {
		t.Fatalf("candidate blob cleanup = %+v, want exactly one initial candidate removed", blobs.deletedKeys())
	}

	var identities, revisions int
	if err := pool.QueryRow(ctx, `
SELECT count(DISTINCT skill.id), count(revision.id)
FROM skills skill
JOIN skill_revisions revision ON revision.skill_id = skill.id
WHERE skill.org_id = $1
  AND skill.owner_kind = 'org'
  AND skill.name = 'concurrent-first-upload'
  AND skill.deleted_at IS NULL
  AND revision.deleted_at IS NULL
`, testOrgID).Scan(&identities, &revisions); err != nil {
		t.Fatalf("count concurrent skill identity and revisions: %v", err)
	}
	if identities != 1 || revisions != 2 {
		t.Fatalf("concurrent skill identity/revision counts = %d/%d, want 1/2", identities, revisions)
	}
}

func TestSkillIdentityAndRevisionMutationPolicies(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := NewStore(pool, WithBlobStore(integrationblob.MustOpen(t, ctx)))
	admin := createSecretTestUser(t, ctx, store, "Skill mutation admin", "admin")
	skill := createIntegrationSkill(t, ctx, store, skillstore.CreateSkillInput{
		OrgID: testOrgID, OwnerKind: skillstore.SkillOwnerOrg, Name: "immutable-skill",
		Actor: identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: admin.ID},
	})

	for label, statement := range map[string]string{
		"identity":             `UPDATE skills SET name = name || '-changed' WHERE id = $1`,
		"revision":             `UPDATE skill_revisions SET description = description || '-changed' WHERE skill_id = $1`,
		"revision soft delete": `UPDATE skill_revisions SET deleted_at = statement_timestamp() WHERE skill_id = $1`,
		"revision delete":      `DELETE FROM skill_revisions WHERE skill_id = $1`,
		"identity delete":      `DELETE FROM skills WHERE id = $1`,
	} {
		if _, err := pool.Exec(ctx, statement, skill.ID); !isPgCode(err, "25006") {
			t.Fatalf("%s mutation error = %v, want SQLSTATE 25006", label, err)
		}
	}
}

func TestProjectDeletionSoftDeletesSkillRevisionSet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := NewStore(pool, WithBlobStore(integrationblob.MustOpen(t, ctx)))
	admin := createSecretTestUser(t, ctx, store, "Project skill deletion admin", "admin")
	skill := createIntegrationSkill(t, ctx, store, skillstore.CreateSkillInput{
		OrgID: testOrgID, OwnerKind: skillstore.SkillOwnerProject, OwnerProjectID: testProjectID,
		Name: "project-deletion-skill", Actor: identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: admin.ID},
	})

	if _, err := store.Organizations().DeleteProject(
		ctx,
		testOrgID,
		testProjectID,
		identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: admin.ID},
	); err != nil {
		t.Fatalf("delete project with owned skill: %v", err)
	}
	assertSkillRevisionSetDeleted(t, ctx, pool, skill.ID)
}

func TestOrganizationDeletionSoftDeletesSkillRevisionSet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := NewStore(pool, WithBlobStore(integrationblob.MustOpen(t, ctx)))
	owner := mustCreateIdentityUser(t, ctx, store, "org-skill-delete@example.com", "Org Skill Delete")
	created, err := store.Organizations().CreateOrgForUser(ctx, orglifecycle.CreateOrgForUserInput{
		UserID: owner.ID, Name: "Org Skill Delete", IdempotencyKey: "org-skill-delete",
	})
	if err != nil {
		t.Fatalf("create organization for skill deletion: %v", err)
	}
	skill := createIntegrationSkill(t, ctx, store, skillstore.CreateSkillInput{
		OrgID: created.Org.ID, OwnerKind: skillstore.SkillOwnerOrg, Name: "organization-deletion-skill",
		Actor: identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: owner.ID},
	})

	if _, err := store.Organizations().DeleteOrganization(
		ctx,
		created.Org.ID,
		identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: owner.ID},
	); err != nil {
		t.Fatalf("delete organization with owned skill: %v", err)
	}
	assertSkillRevisionSetDeleted(t, ctx, pool, skill.ID)
}

func assertSkillRevisionSetDeleted(t *testing.T, ctx context.Context, pool *pgxpool.Pool, skillID ID) {
	t.Helper()
	var identityDeleted bool
	var liveRevisions int
	if err := pool.QueryRow(ctx, `
SELECT skill.deleted_at IS NOT NULL,
       count(*) FILTER (WHERE revision.deleted_at IS NULL)
FROM skills skill
JOIN skill_revisions revision ON revision.skill_id = skill.id
WHERE skill.id = $1
GROUP BY skill.deleted_at
`, skillID).Scan(&identityDeleted, &liveRevisions); err != nil {
		t.Fatalf("read deleted skill revision set: %v", err)
	}
	if !identityDeleted || liveRevisions != 0 {
		t.Fatalf(
			"deleted skill revision set: identity_deleted=%v live_revisions=%d, want true/0",
			identityDeleted,
			liveRevisions,
		)
	}
}

func TestCreateSkillRevisionRechecksParentAfterLockWait(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithBlobStore(integrationblob.MustOpen(t, ctx)))
	admin := createSecretTestUser(t, ctx, store, "Skill revision lock admin", "admin")
	skill := createIntegrationSkill(t, ctx, store, skillstore.CreateSkillInput{
		OrgID: testOrgID, OwnerKind: skillstore.SkillOwnerOrg, Name: "revision-parent-lock",
		Actor: userPrincipal(admin.ID),
	})

	blockingTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin skill revision blocker: %v", err)
	}
	defer func() { _ = blockingTx.Rollback(ctx) }()
	var blockingPID int32
	if err := blockingTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockingPID); err != nil {
		t.Fatalf("get skill revision blocker backend: %v", err)
	}
	if _, err := blockingTx.Exec(
		ctx,
		`SELECT id FROM skills WHERE org_id = $1 AND id = $2 FOR UPDATE`,
		testOrgID,
		skill.ID,
	); err != nil {
		t.Fatalf("lock skill for revision: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, createErr := store.Skills().CreateSkillRevision(context.Background(), skillstore.CreateSkillInput{
			OrgID: testOrgID, OwnerKind: skillstore.SkillOwnerOrg, Name: skill.Name,
			Description: "second revision", SkillMd: "# Second revision",
			ArchiveBytes: []byte("second revision"), Actor: userPrincipal(admin.ID),
		})
		done <- createErr
	}()
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, pool, "-- name: LockSkill", blockingPID)
	if _, err := blockingTx.Exec(
		ctx,
		`UPDATE skills SET deleted_at = statement_timestamp()
		 WHERE org_id = $1 AND id = $2 AND deleted_at IS NULL`,
		testOrgID,
		skill.ID,
	); err != nil {
		t.Fatalf("delete skill while revision creation waits: %v", err)
	}
	if _, err := blockingTx.Exec(
		ctx,
		`UPDATE skill_revisions SET deleted_at = statement_timestamp()
		 WHERE skill_id = $1 AND deleted_at IS NULL`,
		skill.ID,
	); err != nil {
		t.Fatalf("delete skill revisions while creation waits: %v", err)
	}
	if err := blockingTx.Commit(ctx); err != nil {
		t.Fatalf("commit skill deletion: %v", err)
	}
	if err := <-done; !storeerr.IsNotFound(err) {
		t.Fatalf("create revision after skill deletion error = %v, want not found", err)
	}
	var liveRevisions int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*) FROM skill_revisions WHERE skill_id = $1 AND deleted_at IS NULL`,
		skill.ID,
	).Scan(&liveRevisions); err != nil {
		t.Fatalf("count live revisions after skill deletion: %v", err)
	}
	if liveRevisions != 0 {
		t.Fatalf("live revisions after skill deletion = %d, want 0", liveRevisions)
	}
}

func TestCreateSkillRevisionTimestampFollowsRevisionLockOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithBlobStore(integrationblob.MustOpen(t, ctx)))
	admin := createSecretTestUser(t, ctx, store, "Skill revision timestamp admin", "admin")
	skill := createIntegrationSkill(t, ctx, store, skillstore.CreateSkillInput{
		OrgID: testOrgID, OwnerKind: skillstore.SkillOwnerOrg, Name: "revision-timestamp-order",
		Actor: userPrincipal(admin.ID),
	})

	blockingTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin skill revision timestamp blocker: %v", err)
	}
	defer func() { _ = blockingTx.Rollback(ctx) }()
	var blockingPID int32
	if err := blockingTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockingPID); err != nil {
		t.Fatalf("get skill revision timestamp blocker backend: %v", err)
	}
	if _, err := blockingTx.Exec(
		ctx,
		`SELECT id FROM skills WHERE org_id = $1 AND id = $2 FOR UPDATE`,
		testOrgID,
		skill.ID,
	); err != nil {
		t.Fatalf("lock skill for revision timestamp: %v", err)
	}
	type createResult struct {
		record skillstore.SkillRecord
		err    error
	}
	done := make(chan createResult, 1)
	go func() {
		record, createErr := store.Skills().CreateSkillRevision(context.Background(), skillstore.CreateSkillInput{
			OrgID: testOrgID, OwnerKind: skillstore.SkillOwnerOrg, Name: skill.Name,
			Description: "second revision", SkillMd: "# Second revision",
			ArchiveBytes: []byte("second revision"), Actor: userPrincipal(admin.ID),
		})
		done <- createResult{record: record, err: createErr}
	}()
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, pool, "-- name: LockSkill", blockingPID)
	var lockReleaseFloor time.Time
	if err := blockingTx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&lockReleaseFloor); err != nil {
		t.Fatalf("read skill revision lock release floor: %v", err)
	}
	if err := blockingTx.Commit(ctx); err != nil {
		t.Fatalf("release skill revision lock: %v", err)
	}
	result := <-done
	if result.err != nil {
		t.Fatalf("create second skill revision: %v", result.err)
	}
	if result.record.Revision != 2 {
		t.Fatalf("skill revision = %d, want 2", result.record.Revision)
	}
	if result.record.UpdatedAt.Before(lockReleaseFloor) {
		t.Fatalf(
			"second revision created_at = %s, before lock release floor %s",
			result.record.UpdatedAt,
			lockReleaseFloor,
		)
	}
}

func TestDeleteSkillIncludesRevisionCommittedBeforeParentLock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool, WithBlobStore(integrationblob.MustOpen(t, ctx)))
	admin := createSecretTestUser(t, ctx, store, "Skill delete lock admin", "admin")
	skill := createIntegrationSkill(t, ctx, store, skillstore.CreateSkillInput{
		OrgID: testOrgID, OwnerKind: skillstore.SkillOwnerOrg, Name: "delete-parent-lock",
		Actor: userPrincipal(admin.ID),
	})

	blockingTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin skill delete blocker: %v", err)
	}
	defer func() { _ = blockingTx.Rollback(ctx) }()
	var blockingPID int32
	if err := blockingTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockingPID); err != nil {
		t.Fatalf("get skill delete blocker backend: %v", err)
	}
	if _, err := blockingTx.Exec(
		ctx,
		`SELECT id FROM skills WHERE org_id = $1 AND id = $2 FOR UPDATE`,
		testOrgID,
		skill.ID,
	); err != nil {
		t.Fatalf("lock skill for delete: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- store.Skills().DeleteSkill(context.Background(), skillstore.DeleteSkillInput{
			OrgID: testOrgID, SkillID: skill.ID, Actor: userPrincipal(admin.ID),
		})
	}()
	integrationdb.WaitForLockWaitBlockedBy(t, ctx, pool, "-- name: LockSkill", blockingPID)
	if _, err := blockingTx.Exec(
		ctx,
		`INSERT INTO skill_revisions(
		   skill_id, revision, description, skill_md, archive_digest, created_at
		 ) VALUES ($1, 2, 'second revision', '# Second revision', 'second-digest', statement_timestamp())`,
		skill.ID,
	); err != nil {
		t.Fatalf("insert revision while delete waits: %v", err)
	}
	var releaseFloor time.Time
	if err := blockingTx.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&releaseFloor); err != nil {
		t.Fatalf("read skill lock release floor: %v", err)
	}
	if err := blockingTx.Commit(ctx); err != nil {
		t.Fatalf("commit skill revision: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("delete skill after revision: %v", err)
	}

	var revisionCount, liveRevisions int
	var deletedAt time.Time
	if err := pool.QueryRow(ctx, `
SELECT count(*), count(*) FILTER (WHERE revision.deleted_at IS NULL), skill.deleted_at
FROM skills skill
JOIN skill_revisions revision ON revision.skill_id = skill.id
WHERE skill.org_id = $1 AND skill.id = $2
GROUP BY skill.deleted_at
`, testOrgID, skill.ID).Scan(&revisionCount, &liveRevisions, &deletedAt); err != nil {
		t.Fatalf("read deleted skill revisions: %v", err)
	}
	if revisionCount != 2 || liveRevisions != 0 {
		t.Fatalf("skill revisions total/live = %d/%d, want 2/0", revisionCount, liveRevisions)
	}
	if deletedAt.Before(releaseFloor) {
		t.Fatalf("skill deleted_at = %s, want at or after lock release floor %s", deletedAt, releaseFloor)
	}
}

func createIntegrationSkill(t *testing.T, ctx context.Context, store *Store, input skillstore.CreateSkillInput) skillstore.SkillRecord {
	t.Helper()
	input.Description = "integration skill"
	input.SkillMd = "# Integration skill"
	input.ArchiveBytes = []byte("integration archive: " + input.Name)
	record, err := store.Skills().CreateSkillRevision(ctx, input)
	if err != nil {
		t.Fatalf("create skill %q: %v", input.Name, err)
	}
	return record
}

func containsSkill(records []skillstore.SkillRecord, id ID) bool {
	for _, record := range records {
		if record.ID == id {
			return true
		}
	}
	return false
}

func publicSkillID(id ID) (string, error) {
	return publicid.Encode(publicid.KindSkill, id)
}
