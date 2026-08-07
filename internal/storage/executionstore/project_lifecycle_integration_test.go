//go:build integration

package executionstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestProjectChildAdmissionSerializesWithDeletion(t *testing.T) {
	t.Parallel()
	t.Run("creation wins", func(t *testing.T) {
		ctx := context.Background()
		fixture := newMachineLifecycleLockOrderFixture(t, ctx, "project-child-create-wins")
		actor := scopeDeletionActor(t, fixture)

		controlTx, err := fixture.pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin project child control transaction: %v", err)
		}
		defer func() { _ = controlTx.Rollback(ctx) }()
		if err := dbsqlc.New(controlTx).LockResourceCreation(
			ctx,
			dbsqlc.LockResourceCreationParams{
				ResourceKind: "agent_profiles",
				Scope:        testProjectID.String(),
			},
		); err != nil {
			t.Fatalf("lock project profile creation: %v", err)
		}

		type profileOutcome struct {
			record executionstore.AgentProfileRecord
			err    error
		}
		profileDone := make(chan profileOutcome, 1)
		go func() {
			record, createErr := fixture.store.Execution().CreateAgentProfile(
				ctx,
				executionstore.CreateAgentProfileInput{
					ProjectID:       testProjectID,
					Name:            "Concurrent Project Child",
					CurrentConfigID: fixture.agent.CurrentConfigID,
					IdempotencyKey:  "concurrent-project-child",
				},
			)
			profileDone <- profileOutcome{record: record, err: createErr}
		}()
		waitForNamedLockWaiters(t, ctx, fixture.pool, "LockResourceCreation", 1)

		deleteDone := make(chan error, 1)
		go func() {
			_, deleteErr := fixture.store.Organizations().DeleteProjectOnceForIntegration(
				ctx,
				testOrgID,
				testProjectID,
				actor,
			)
			deleteDone <- deleteErr
		}()
		waitForNamedLockWaiters(t, ctx, fixture.pool, "LockProjectLifecycleExclusive", 1)
		if err := controlTx.Commit(ctx); err != nil {
			t.Fatalf("release project child control transaction: %v", err)
		}

		var created executionstore.AgentProfileRecord
		select {
		case outcome := <-profileDone:
			if outcome.err != nil {
				t.Fatalf("create profile before project deletion: %v", outcome.err)
			}
			created = outcome.record
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for project child creation")
		}
		select {
		case err := <-deleteDone:
			if err != nil {
				t.Fatalf("delete project after child creation: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for project deletion")
		}
		var activeProfileCount, deletedProfileCount int
		if err := fixture.pool.QueryRow(
			ctx,
			`SELECT
			   count(*) FILTER (WHERE deleted_at IS NULL)::integer,
			   count(*) FILTER (WHERE deleted_at IS NOT NULL)::integer
			 FROM agent_profiles
			 WHERE id = $1`,
			created.ID,
		).Scan(&activeProfileCount, &deletedProfileCount); err != nil {
			t.Fatalf("count profile after project deletion: %v", err)
		}
		if activeProfileCount != 0 || deletedProfileCount != 1 {
			t.Fatalf(
				"profile rows after project deletion: active=%d deleted=%d, want active=0 deleted=1",
				activeProfileCount,
				deletedProfileCount,
			)
		}
	})

	t.Run("deletion wins", func(t *testing.T) {
		ctx := context.Background()
		fixture := newMachineLifecycleLockOrderFixture(t, ctx, "project-delete-child")
		actor := scopeDeletionActor(t, fixture)

		controlTx, err := fixture.pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin project deletion control transaction: %v", err)
		}
		defer func() { _ = controlTx.Rollback(ctx) }()
		if err := dbsqlc.New(controlTx).LockAgentMachineSources(
			ctx,
			dbsqlc.LockAgentMachineSourcesParams{AgentID: fixture.agent.ID},
		); err != nil {
			t.Fatalf("lock project agent machine sources: %v", err)
		}

		deleteDone := make(chan error, 1)
		go func() {
			_, deleteErr := fixture.store.Organizations().DeleteProjectOnceForIntegration(
				ctx,
				testOrgID,
				testProjectID,
				actor,
			)
			deleteDone <- deleteErr
		}()
		waitForNamedLockWaiters(t, ctx, fixture.pool, "LockAgentMachineSources", 1)

		profileDone := make(chan error, 1)
		go func() {
			_, createErr := fixture.store.Execution().CreateAgentProfile(
				ctx,
				executionstore.CreateAgentProfileInput{
					ProjectID:       testProjectID,
					Name:            "Rejected Project Child",
					CurrentConfigID: fixture.agent.CurrentConfigID,
					IdempotencyKey:  "rejected-project-child",
				},
			)
			profileDone <- createErr
		}()
		waitForNamedLockWaiters(t, ctx, fixture.pool, "LockProjectLifecycleShared", 1)
		if err := controlTx.Commit(ctx); err != nil {
			t.Fatalf("release project deletion control transaction: %v", err)
		}

		select {
		case err := <-deleteDone:
			if err != nil {
				t.Fatalf("delete project before child creation: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for project deletion")
		}
		select {
		case err := <-profileDone:
			if !errors.Is(err, storeerr.ErrNotFound) {
				t.Fatalf("profile creation after project deletion error = %v, want not found", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for rejected project child creation")
		}
		var profileCount int
		if err := fixture.pool.QueryRow(
			ctx,
			`SELECT count(*)::integer FROM agent_profiles WHERE project_id = $1 AND name = $2`,
			testProjectID,
			"Rejected Project Child",
		).Scan(&profileCount); err != nil {
			t.Fatalf("count rejected project child: %v", err)
		}
		if profileCount != 0 {
			t.Fatalf("rejected project child rows = %d, want zero", profileCount)
		}
	})
}

func TestStandaloneActorWritesRejectDeletedProjectAfterWaiting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMachineLifecycleLockOrderFixture(t, ctx, "actor-project-delete")
	actor := scopeDeletionActor(t, fixture)
	before := "Before deletion"
	existing, err := fixture.store.Execution().PutActor(ctx, executionstore.PutActorInput{
		ProjectID:        testProjectID,
		ProviderTenantID: "actor-tenant",
		ProviderUserID:   "existing-user",
		DisplayName:      &before,
	})
	if err != nil {
		t.Fatalf("create existing actor: %v", err)
	}
	controlTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin actor lifecycle control transaction: %v", err)
	}
	defer func() { _ = controlTx.Rollback(ctx) }()
	if err := dbsqlc.New(controlTx).LockAgentMachineSources(
		ctx,
		dbsqlc.LockAgentMachineSourcesParams{AgentID: fixture.agent.ID},
	); err != nil {
		t.Fatalf("lock project agent machine sources: %v", err)
	}
	deleteDone := make(chan error, 1)
	go func() {
		_, deleteErr := fixture.store.Organizations().DeleteProjectOnceForIntegration(
			context.Background(),
			testOrgID,
			testProjectID,
			actor,
		)
		deleteDone <- deleteErr
	}()
	waitForNamedLockWaiters(t, ctx, fixture.pool, "LockAgentMachineSources", 1)
	putDone := make(chan error, 1)
	go func() {
		displayName := "Created too late"
		_, putErr := fixture.store.Execution().PutActor(
			context.Background(),
			executionstore.PutActorInput{
				ProjectID:        testProjectID,
				ProviderTenantID: "actor-tenant",
				ProviderUserID:   "new-user",
				DisplayName:      &displayName,
			},
		)
		putDone <- putErr
	}()
	updateDone := make(chan error, 1)
	go func() {
		updateDone <- fixture.store.Execution().UpdateActorDisplayName(
			context.Background(),
			executionstore.UpdateActorDisplayNameInput{
				ProjectID:        testProjectID,
				Provider:         executionstore.ActorProviderExternal,
				ProviderTenantID: "actor-tenant",
				ProviderUserID:   "existing-user",
				DisplayName:      "Updated too late",
			},
		)
	}()
	waitForNamedLockWaiters(t, ctx, fixture.pool, "LockProjectLifecycleShared", 2)
	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release actor lifecycle control transaction: %v", err)
	}
	if err := <-deleteDone; err != nil {
		t.Fatalf("delete project before actor writes: %v", err)
	}
	if err := <-putDone; !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("put actor after project deletion error = %v, want not found", err)
	}
	if err := <-updateDone; !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("update actor after project deletion error = %v, want not found", err)
	}
	var existingName string
	if err := fixture.pool.QueryRow(
		ctx,
		`SELECT display_name FROM actors WHERE id = $1`,
		existing.ID,
	).Scan(&existingName); err != nil {
		t.Fatalf("load existing actor after project deletion: %v", err)
	}
	if existingName != before {
		t.Fatalf("existing actor display name = %q, want %q", existingName, before)
	}
	var newActorCount int
	if err := fixture.pool.QueryRow(
		ctx,
		`SELECT count(*) FROM actors WHERE project_id = $1 AND provider_user_id = 'new-user'`,
		testProjectID,
	).Scan(&newActorCount); err != nil {
		t.Fatalf("count actor created after project deletion: %v", err)
	}
	if newActorCount != 0 {
		t.Fatalf("actors created after project deletion = %d, want 0", newActorCount)
	}
}

func TestOrganizationChildAdmissionSerializesWithDeletion(t *testing.T) {
	t.Parallel()
	t.Run("creation wins", func(t *testing.T) {
		ctx := context.Background()
		fixture := newMachineLifecycleLockOrderFixture(t, ctx, "org-child-create-wins")
		actor := scopeDeletionActor(t, fixture)

		controlTx, err := fixture.pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin organization child control transaction: %v", err)
		}
		defer func() { _ = controlTx.Rollback(ctx) }()
		if err := dbsqlc.New(controlTx).LockResourceCreation(
			ctx,
			dbsqlc.LockResourceCreationParams{
				ResourceKind: "org_invitations",
				Scope:        testOrgID.String(),
			},
		); err != nil {
			t.Fatalf("lock organization invitation creation: %v", err)
		}

		type invitationOutcome struct {
			record identitystore.OrgInvitationRecord
			err    error
		}
		invitationDone := make(chan invitationOutcome, 1)
		go func() {
			record, createErr := fixture.store.Identity().CreateOrgInvitation(
				ctx,
				identitystore.CreateOrgInvitationInput{
					OrgID: testOrgID,
					Email: "concurrent-org-child@example.com",
					Role:  "member",
				},
			)
			invitationDone <- invitationOutcome{record: record, err: createErr}
		}()
		waitForNamedLockWaiters(t, ctx, fixture.pool, "LockResourceCreation", 1)

		deleteDone := make(chan error, 1)
		go func() {
			_, deleteErr := fixture.store.Organizations().DeleteOrganizationOnceForIntegration(
				ctx,
				testOrgID,
				actor,
			)
			deleteDone <- deleteErr
		}()
		waitForNamedLockWaiters(t, ctx, fixture.pool, "LockOrganizationLifecycleExclusive", 1)
		if err := controlTx.Commit(ctx); err != nil {
			t.Fatalf("release organization child control transaction: %v", err)
		}

		var created identitystore.OrgInvitationRecord
		select {
		case outcome := <-invitationDone:
			if outcome.err != nil {
				t.Fatalf("create invitation before organization deletion: %v", outcome.err)
			}
			created = outcome.record
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for organization child creation")
		}
		select {
		case err := <-deleteDone:
			if err != nil {
				t.Fatalf("delete organization after child creation: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for organization deletion")
		}
		var invitationCount int
		if err := fixture.pool.QueryRow(
			ctx,
			`SELECT count(*)::integer FROM org_invitations WHERE id = $1`,
			created.ID,
		).Scan(&invitationCount); err != nil {
			t.Fatalf("count invitation after organization deletion: %v", err)
		}
		if invitationCount != 0 {
			t.Fatalf("invitation rows after organization deletion = %d, want zero", invitationCount)
		}
	})

	t.Run("deletion wins", func(t *testing.T) {
		ctx := context.Background()
		fixture := newMachineLifecycleLockOrderFixture(t, ctx, "org-delete-child")
		actor := scopeDeletionActor(t, fixture)

		controlTx, err := fixture.pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin organization deletion control transaction: %v", err)
		}
		defer func() { _ = controlTx.Rollback(ctx) }()
		if err := dbsqlc.New(controlTx).LockAgentMachineSources(
			ctx,
			dbsqlc.LockAgentMachineSourcesParams{AgentID: fixture.agent.ID},
		); err != nil {
			t.Fatalf("lock organization agent machine sources: %v", err)
		}

		deleteDone := make(chan error, 1)
		go func() {
			_, deleteErr := fixture.store.Organizations().DeleteOrganizationOnceForIntegration(
				ctx,
				testOrgID,
				actor,
			)
			deleteDone <- deleteErr
		}()
		waitForNamedLockWaiters(t, ctx, fixture.pool, "LockAgentMachineSources", 1)

		invitationDone := make(chan error, 1)
		go func() {
			_, createErr := fixture.store.Identity().CreateOrgInvitation(
				ctx,
				identitystore.CreateOrgInvitationInput{
					OrgID: testOrgID,
					Email: "rejected-org-child@example.com",
					Role:  "member",
				},
			)
			invitationDone <- createErr
		}()
		waitForNamedLockWaiters(t, ctx, fixture.pool, "LockOrganizationLifecycleShared", 1)
		if err := controlTx.Commit(ctx); err != nil {
			t.Fatalf("release organization deletion control transaction: %v", err)
		}

		select {
		case err := <-deleteDone:
			if err != nil {
				t.Fatalf("delete organization before child creation: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for organization deletion")
		}
		select {
		case err := <-invitationDone:
			if !errors.Is(err, storeerr.ErrNotFound) {
				t.Fatalf("invitation creation after organization deletion error = %v, want not found", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for rejected organization child creation")
		}
		var invitationCount int
		if err := fixture.pool.QueryRow(
			ctx,
			`SELECT count(*)::integer FROM org_invitations WHERE org_id = $1 AND normalized_email = $2`,
			testOrgID,
			identitystore.NormalizeEmail("rejected-org-child@example.com"),
		).Scan(&invitationCount); err != nil {
			t.Fatalf("count rejected organization child: %v", err)
		}
		if invitationCount != 0 {
			t.Fatalf("rejected organization child rows = %d, want zero", invitationCount)
		}
	})
}

func scopeDeletionActor(
	t *testing.T,
	fixture machineLifecycleLockOrderFixture,
) *executionstore.ActorParams {
	t.Helper()
	actor, err := executionstore.OmnaraActorParams(
		testOrgID,
		identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: fixture.userID},
	)
	if err != nil {
		t.Fatalf("build scope deletion actor: %v", err)
	}
	return actor
}

func TestProjectMembershipAdmissionWaitingBehindDeletionRejectsInactiveProject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newMachineLifecycleLockOrderFixture(t, ctx, "membership-admission")

	targetUser, err := fixture.store.Identity().CreateVerifiedUser(ctx, CreateVerifiedUserInput{
		Email:       "project-membership-admission@example.com",
		DisplayName: "Project Membership Admission",
	})
	if err != nil {
		t.Fatalf("create target user: %v", err)
	}
	if _, err := fixture.store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
		OrgID:  testOrgID,
		UserID: targetUser.ID,
		Role:   "member",
	}); err != nil {
		t.Fatalf("create target user organization membership: %v", err)
	}
	targetKey, err := fixture.store.Identity().CreateOrgAPIKeyWithPlaintext(ctx, identitystore.CreateOrgAPIKeyInput{
		OrgID:           testOrgID,
		CreatedByUserID: fixture.userID,
		Name:            "Project membership admission",
		OrgRole:         "member",
	})
	if err != nil {
		t.Fatalf("create target organization API key: %v", err)
	}

	controlTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin source lock control transaction: %v", err)
	}
	defer func() { _ = controlTx.Rollback(ctx) }()
	controlQ := dbsqlc.New(controlTx)
	if err := controlQ.LockAgentMachineSources(
		ctx,
		dbsqlc.LockAgentMachineSourcesParams{AgentID: fixture.agent.ID},
	); err != nil {
		t.Fatalf("lock agent machine sources: %v", err)
	}

	deleteDone := make(chan error, 1)
	go func() {
		_, deleteErr := fixture.store.Organizations().DeleteProject(
			ctx,
			testOrgID,
			testProjectID,
			identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: fixture.userID},
		)
		deleteDone <- deleteErr
	}()
	waitForNamedLockWaiters(t, ctx, fixture.pool, "LockAgentMachineSources", 1)

	userRoleDone := make(chan error, 1)
	go func() {
		_, addErr := fixture.store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
			OrgID:     testOrgID,
			ProjectID: testProjectID,
			UserID:    targetUser.ID,
			Role:      "viewer",
		})
		userRoleDone <- addErr
	}()
	keyRoleDone := make(chan error, 1)
	go func() {
		_, addErr := fixture.store.Identity().SetOrgAPIKeyProjectRole(ctx, identitystore.OrgAPIKeyProjectRoleInput{
			OrgID:     testOrgID,
			KeyID:     targetKey.Record.ID,
			ProjectID: testProjectID,
			Role:      "developer",
		})
		keyRoleDone <- addErr
	}()
	waitForNamedLockWaiters(t, ctx, fixture.pool, "LockProjectLifecycleShared", 2)

	if err := controlTx.Commit(ctx); err != nil {
		t.Fatalf("release source lock control transaction: %v", err)
	}
	select {
	case err := <-deleteDone:
		if err != nil {
			t.Fatalf("delete project: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for project deletion")
	}
	for label, done := range map[string]<-chan error{
		"user project role":         userRoleDone,
		"organization API key role": keyRoleDone,
	} {
		select {
		case err := <-done:
			if !errors.Is(err, storeerr.ErrNotFound) {
				t.Fatalf("%s after project deletion error = %v, want not found", label, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", label)
		}
	}

	var relationshipCount int
	if err := fixture.pool.QueryRow(
		ctx,
		`SELECT count(*)::integer
		 FROM project_memberships membership
		 JOIN org_memberships organization_membership
		   ON organization_membership.org_id = membership.org_id
		  AND organization_membership.id = membership.org_membership_id
		 WHERE membership.project_id = $1
		   AND (organization_membership.user_id = $2
		        OR organization_membership.org_api_key_id = $3)`,
		testProjectID,
		targetUser.ID,
		targetKey.Record.ID,
	).Scan(&relationshipCount); err != nil {
		t.Fatalf("count project roles beneath deleted project: %v", err)
	}
	if relationshipCount != 0 {
		t.Fatalf("project roles beneath deleted project = %d, want zero", relationshipCount)
	}
}

func TestProjectAuthorizationRolesExcludeTombstonedProjects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	store := NewStore(pool)
	seedDefaultProject(t, ctx, store)

	member, err := store.Identity().CreateVerifiedUser(ctx, CreateVerifiedUserInput{
		Email:       "tombstoned-project-membership@example.com",
		DisplayName: "Tombstoned Project Membership",
	})
	if err != nil {
		t.Fatalf("create project member: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(ctx, identitystore.AddOrgMembershipInput{
		OrgID:  testOrgID,
		UserID: member.ID,
		Role:   "member",
	}); err != nil {
		t.Fatalf("create organization membership: %v", err)
	}
	if _, err := store.Identity().AddProjectMembership(ctx, identitystore.AddProjectMembershipInput{
		OrgID:     testOrgID,
		ProjectID: testProjectID,
		UserID:    member.ID,
		Role:      "viewer",
	}); err != nil {
		t.Fatalf("create project membership: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE projects
		 SET deleted_at = transaction_timestamp(), updated_at = transaction_timestamp()
		 WHERE org_id = $1 AND id = $2`,
		testOrgID,
		testProjectID,
	); err != nil {
		t.Fatalf("tombstone project while retaining membership fixture: %v", err)
	}

	var roleCount int
	if err := pool.QueryRow(
		ctx,
		`SELECT count(*)::integer
		 FROM principal_project_authorization_roles
		 WHERE project_id = $1 AND user_id = $2`,
		testProjectID,
		member.ID,
	).Scan(&roleCount); err != nil {
		t.Fatalf("count authorization roles for tombstoned project: %v", err)
	}
	if roleCount != 0 {
		t.Fatalf("authorization roles for tombstoned project = %d, want zero", roleCount)
	}
}
