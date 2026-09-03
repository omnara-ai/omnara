package orglifecycle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/skillops"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type CreateOrgForUserInput struct {
	OrgID                         ID
	UserID                        ID
	Name                          string
	IdempotencyKey                string
	DefaultMachinePools           []executionstore.DefaultMachinePoolTemplate
	ProvisionDefaultModelProvider bool
}

func (s *Service) CreateOrgForUser(
	ctx context.Context,
	input CreateOrgForUserInput,
) (identitystore.CreateOrgForUserRecord, error) {
	if isNilID(input.UserID) {
		return identitystore.CreateOrgForUserRecord{}, errors.New("user id is required")
	}
	if input.Name == "" {
		return identitystore.CreateOrgForUserRecord{}, errors.New("organization name is required")
	}
	normalizedName, err := resourcename.CanonicalizeRequired("organization name", input.Name)
	if err != nil {
		return identitystore.CreateOrgForUserRecord{}, storeerr.InvalidRequest(err)
	}
	input.Name = normalizedName
	if isNilID(input.OrgID) {
		orgID, err := uuid.NewV7()
		if err != nil {
			return identitystore.CreateOrgForUserRecord{}, fmt.Errorf("generate org id: %w", err)
		}
		input.OrgID = orgID
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return identitystore.CreateOrgForUserRecord{}, fmt.Errorf("begin create org for user: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	record, err := s.identity.ProvisionOrganizationTx(ctx, tx, identitystore.ProvisionOrganizationInput{
		OrgID:          input.OrgID,
		UserID:         input.UserID,
		Name:           input.Name,
		IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		return identitystore.CreateOrgForUserRecord{}, err
	}
	if record.Created {
		if err := s.execution.ProvisionOrganizationDefaultsTx(
			ctx,
			tx,
			record.Org.ID,
			record.Project.ID,
			input.DefaultMachinePools,
		); err != nil {
			return identitystore.CreateOrgForUserRecord{}, err
		}
		if input.ProvisionDefaultModelProvider {
			rows, err := s.q.WithTx(tx).EnqueueDefaultModelProviderProvisioning(
				ctx,
				dbsqlc.EnqueueDefaultModelProviderProvisioningParams{
					OrganizationID: record.Org.ID,
					CreatorUserID:  input.UserID,
				},
			)
			if err != nil {
				return identitystore.CreateOrgForUserRecord{}, fmt.Errorf(
					"enqueue default model provider provisioning: %w",
					err,
				)
			}
			if rows != 1 {
				return identitystore.CreateOrgForUserRecord{}, storeerr.ErrStateTransitionConflict
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return identitystore.CreateOrgForUserRecord{}, fmt.Errorf("commit create org for user: %w", err)
	}
	return record, nil
}

//nolint:lll // Keeping each generated query parameter beside its operation makes this cascade auditable.
func deleteProjectRelationshipsTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	orgID, projectID ID,
) error {
	hasActiveAgents, err := q.ProjectHasActiveAgentsForDeletion(
		ctx,
		dbsqlc.ProjectHasActiveAgentsForDeletionParams{ProjectID: projectID},
	)
	if err != nil {
		return fmt.Errorf("check project agents before deletion: %w", err)
	}
	if hasActiveAgents {
		return fmt.Errorf("project still has active agents after teardown: %w", storeerr.ErrConflict)
	}
	if err := q.DeleteProjectMemberships(ctx, dbsqlc.DeleteProjectMembershipsParams{OrgID: orgID, ProjectID: projectID}); err != nil {
		return fmt.Errorf("delete project memberships: %w", err)
	}
	if err := q.DeleteProjectMachineGrantsForProjectDeletion(ctx, dbsqlc.DeleteProjectMachineGrantsForProjectDeletionParams{OrgID: orgID, ProjectID: projectID}); err != nil {
		return fmt.Errorf("delete project machine grants: %w", err)
	}
	if err := q.DeleteProjectMachinePoolGrantsForProjectDeletion(ctx, dbsqlc.DeleteProjectMachinePoolGrantsForProjectDeletionParams{OrgID: orgID, ProjectID: projectID}); err != nil {
		return fmt.Errorf("delete project machine pool grants: %w", err)
	}
	if err := q.DeleteProjectModelGrantsForProjectDeletion(ctx, dbsqlc.DeleteProjectModelGrantsForProjectDeletionParams{OrgID: orgID, ProjectID: projectID}); err != nil {
		return fmt.Errorf("delete project model grants: %w", err)
	}
	if err := q.DeleteProjectAgentProfiles(ctx, dbsqlc.DeleteProjectAgentProfilesParams{ProjectID: projectID}); err != nil {
		return fmt.Errorf("delete project agent profiles: %w", err)
	}
	if err := q.DeleteProjectAgentProfileVersions(ctx, dbsqlc.DeleteProjectAgentProfileVersionsParams{ProjectID: projectID}); err != nil {
		return fmt.Errorf("delete project agent profile versions: %w", err)
	}
	if err := q.DeleteProjectCronTriggers(ctx, dbsqlc.DeleteProjectCronTriggersParams{ProjectID: projectID}); err != nil {
		return fmt.Errorf("delete project cron triggers: %w", err)
	}
	if err := q.RevokeProjectIntegrationTargetBindings(ctx, dbsqlc.RevokeProjectIntegrationTargetBindingsParams{ProjectID: projectID}); err != nil {
		return fmt.Errorf("revoke project integration target bindings: %w", err)
	}
	if err := q.DeleteProjectIntegrationInstalls(ctx, dbsqlc.DeleteProjectIntegrationInstallsParams{OrgID: orgID, ProjectID: projectID}); err != nil {
		return fmt.Errorf("delete project integration installs: %w", err)
	}
	if err := q.DeleteProjectIntegrationApps(ctx, dbsqlc.DeleteProjectIntegrationAppsParams{OrgID: orgID, ProjectID: projectID}); err != nil {
		return fmt.Errorf("delete project integration apps: %w", err)
	}
	if err := q.DeleteProjectIntegrationRuntimeUnits(ctx, dbsqlc.DeleteProjectIntegrationRuntimeUnitsParams{OrgID: orgID, ProjectID: projectID}); err != nil {
		return fmt.Errorf("delete project integration runtime units: %w", err)
	}
	if err := q.DeleteProjectIntegrationRoutes(ctx, dbsqlc.DeleteProjectIntegrationRoutesParams{ProjectID: projectID}); err != nil {
		return fmt.Errorf("delete project integration routes: %w", err)
	}
	if err := q.DeleteProjectIntegrationTargets(ctx, dbsqlc.DeleteProjectIntegrationTargetsParams{ProjectID: projectID}); err != nil {
		return fmt.Errorf("delete project integration targets: %w", err)
	}
	return nil
}

//nolint:lll // Keeping each generated query parameter beside its operation makes this cascade auditable.
func deleteProjectOwnedContentTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	orgID, projectID ID,
) ([]skillops.ArchiveRef, error) {
	skillArchives, err := skillops.ListArchiveRefs(ctx, q, orgID, &projectID)
	if err != nil {
		return nil, err
	}
	if err := q.DeleteSkillsForOwner(ctx, dbsqlc.DeleteSkillsForOwnerParams{OrgID: orgID, OwnerProjectID: &projectID}); err != nil {
		return nil, fmt.Errorf("delete project skills: %w", err)
	}
	if err := q.DeleteSkillRevisionsForOwner(ctx, dbsqlc.DeleteSkillRevisionsForOwnerParams{OrgID: orgID, OwnerProjectID: &projectID}); err != nil {
		return nil, fmt.Errorf("delete project skill revisions: %w", err)
	}
	if err := q.DeleteProjectSkillGrants(ctx, dbsqlc.DeleteProjectSkillGrantsParams{OrgID: orgID, ProjectID: projectID}); err != nil {
		return nil, fmt.Errorf("delete project skill grants: %w", err)
	}
	referenced, err := q.ProjectOwnedSecretsReferenced(ctx, dbsqlc.ProjectOwnedSecretsReferencedParams{OrgID: orgID, ProjectID: &projectID})
	if err != nil {
		return nil, fmt.Errorf("check project secret references: %w", err)
	}
	if referenced {
		return nil, fmt.Errorf("a project-owned secret is still referenced outside the project: %w", storeerr.ErrConflict)
	}
	if err := q.DeleteProjectSecretGrants(ctx, dbsqlc.DeleteProjectSecretGrantsParams{OrgID: orgID, ProjectID: projectID}); err != nil {
		return nil, fmt.Errorf("delete project secret grants: %w", err)
	}
	if err := q.DeleteProjectSecretVersions(ctx, dbsqlc.DeleteProjectSecretVersionsParams{OrgID: orgID, ProjectID: &projectID}); err != nil {
		return nil, fmt.Errorf("destroy project secret versions: %w", err)
	}
	if err := q.DeleteProjectSecrets(ctx, dbsqlc.DeleteProjectSecretsParams{OrgID: orgID, ProjectID: &projectID}); err != nil {
		return nil, fmt.Errorf("delete project secrets: %w", err)
	}
	return skillArchives, nil
}

func (s *Service) teardownProjectAgentsTx(
	ctx context.Context,
	tx pgx.Tx,
	q *dbsqlc.Queries,
	txNotifications *notifications.TxNotifications,
	projectID ID,
	actor *executionstore.ActorParams,
) ([]executionstore.MachineRecord, error) {
	agentIDs, err := q.ListActiveAgentIDsForProjectDeletion(
		ctx,
		dbsqlc.ListActiveAgentIDsForProjectDeletionParams{ProjectID: projectID},
	)
	if err != nil {
		return nil, fmt.Errorf("list project agents for deletion: %w", err)
	}
	machines := make([]executionstore.MachineRecord, 0)
	for _, agentID := range agentIDs {
		agentMachines, err := s.execution.ArchiveAgentTx(ctx, tx, txNotifications, projectID, agentID, actor)
		if err != nil {
			return nil, fmt.Errorf("archive project agent %s: %w", agentID, err)
		}
		machines = append(machines, agentMachines...)
	}
	return machines, nil
}

func lockProjectLifecyclesTx(ctx context.Context, q *dbsqlc.Queries, projectIDs []ID) error {
	sorted := make([]ID, len(projectIDs))
	copy(sorted, projectIDs)
	sort.Slice(sorted, func(i, j int) bool {
		return bytes.Compare(sorted[i][:], sorted[j][:]) < 0
	})
	for _, projectID := range sorted {
		if err := q.LockProjectLifecycleExclusive(
			ctx,
			dbsqlc.LockProjectLifecycleExclusiveParams{ProjectID: projectID.String()},
		); err != nil {
			return fmt.Errorf("lock project lifecycle: %w", err)
		}
	}
	return nil
}

func (s *Service) DeleteProject(
	ctx context.Context,
	orgID, projectID ID,
	deletedBy identitystore.PrincipalRecord,
) ([]executionstore.MachineRecord, error) {
	if isNilID(orgID) || isNilID(projectID) {
		return nil, errors.New("org and project are required")
	}
	if isNilID(deletedBy.ID) {
		return nil, errors.New("actor is required")
	}
	actor, err := executionstore.OmnaraActorParams(orgID, deletedBy)
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin delete project: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	if err := lockProjectLifecyclesTx(ctx, q, []ID{projectID}); err != nil {
		return nil, err
	}
	txNotifications := s.newTxNotifications()
	machines, err := s.teardownProjectAgentsTx(ctx, tx, q, txNotifications, projectID, actor)
	if err != nil {
		return nil, err
	}
	if err := deleteProjectRelationshipsTx(ctx, q, orgID, projectID); err != nil {
		return nil, err
	}
	skillArchives, err := deleteProjectOwnedContentTx(ctx, q, orgID, projectID)
	if err != nil {
		return nil, err
	}
	rows, err := q.DeleteProject(ctx, dbsqlc.DeleteProjectParams{OrgID: orgID, ID: projectID})
	if err != nil {
		return nil, fmt.Errorf("delete project: %w", err)
	}
	if rows == 0 {
		return nil, storeerr.ErrNotFound
	}
	if err := storeutil.CommitTxWithNotifications(
		ctx,
		tx,
		txNotifications,
		s.postCommitPublisher,
		"delete project",
	); err != nil {
		return nil, err
	}
	skillops.Purge(ctx, s.blobs, skillArchives)
	return machines, nil
}

const deleteOrganizationLockAttempts = 5

func (s *Service) DeleteOrganization(
	ctx context.Context,
	orgID ID,
	deletedBy identitystore.PrincipalRecord,
) ([]executionstore.MachineRecord, error) {
	if isNilID(orgID) {
		return nil, errors.New("org is required")
	}
	if isNilID(deletedBy.ID) {
		return nil, errors.New("actor is required")
	}
	actor, err := executionstore.OmnaraActorParams(orgID, deletedBy)
	if err != nil {
		return nil, err
	}
	lockProjectIDs, err := s.q.ListActiveProjectIDsForOrganization(
		ctx,
		dbsqlc.ListActiveProjectIDsForOrganizationParams{OrgID: orgID},
	)
	if err != nil {
		return nil, fmt.Errorf("list organization projects for lifecycle locks: %w", err)
	}
	for range deleteOrganizationLockAttempts {
		machines, unlockedProjectIDs, err := s.deleteOrganizationAttempt(
			ctx,
			orgID,
			actor,
			lockProjectIDs,
		)
		if err != nil {
			return nil, err
		}
		if len(unlockedProjectIDs) == 0 {
			return machines, nil
		}
		lockProjectIDs = append(lockProjectIDs, unlockedProjectIDs...)
	}
	return nil, fmt.Errorf("organization projects kept changing during deletion: %w", storeerr.ErrConflict)
}

// deleteOrganizationAttempt aborts and returns the unlocked project IDs when
// the in-transaction project list contains projects outside the locked set,
// so DeleteOrganization can extend the lock set and retry.
//
//nolint:lll // Keeping generated query parameters inline makes the transaction's cascade order auditable.
func (s *Service) deleteOrganizationAttempt(
	ctx context.Context,
	orgID ID,
	actor *executionstore.ActorParams,
	lockProjectIDs []ID,
) ([]executionstore.MachineRecord, []ID, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("begin delete organization: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	if err := lockProjectLifecyclesTx(ctx, q, lockProjectIDs); err != nil {
		return nil, nil, err
	}
	txNotifications := s.newTxNotifications()
	rows, err := q.DeleteOrganization(ctx, dbsqlc.DeleteOrganizationParams{ID: orgID})
	if err != nil {
		return nil, nil, fmt.Errorf("delete organization: %w", err)
	}
	if rows == 0 {
		return nil, nil, storeerr.ErrNotFound
	}
	orgProjectIDs, err := q.ListActiveProjectIDsForOrganization(ctx, dbsqlc.ListActiveProjectIDsForOrganizationParams{OrgID: orgID})
	if err != nil {
		return nil, nil, fmt.Errorf("list organization projects: %w", err)
	}
	locked := make(map[ID]bool, len(lockProjectIDs))
	for _, projectID := range lockProjectIDs {
		locked[projectID] = true
	}
	unlockedProjectIDs := make([]ID, 0)
	for _, projectID := range orgProjectIDs {
		if !locked[projectID] {
			unlockedProjectIDs = append(unlockedProjectIDs, projectID)
		}
	}
	if len(unlockedProjectIDs) > 0 {
		return nil, unlockedProjectIDs, nil
	}
	machines := make([]executionstore.MachineRecord, 0)
	for _, projectID := range orgProjectIDs {
		projectMachines, err := s.teardownProjectAgentsTx(ctx, tx, q, txNotifications, projectID, actor)
		if err != nil {
			return nil, nil, err
		}
		machines = append(machines, projectMachines...)
	}
	poolIDs, err := q.ListActiveMachinePoolIDsForOrganizationDeletion(
		ctx,
		dbsqlc.ListActiveMachinePoolIDsForOrganizationDeletionParams{OrgID: orgID},
	)
	if err != nil {
		return nil, nil, fmt.Errorf("list organization machine pools: %w", err)
	}
	for _, poolID := range poolIDs {
		poolMachines, err := s.execution.DeleteMachinePoolTx(ctx, tx, txNotifications, orgID, poolID)
		if err != nil {
			return nil, nil, fmt.Errorf("delete organization machine pool %s: %w", poolID, err)
		}
		machines = append(machines, poolMachines...)
	}
	byoMachineIDs, err := q.ListActiveBYOMachineIDsForOrganizationDeletion(
		ctx,
		dbsqlc.ListActiveBYOMachineIDsForOrganizationDeletionParams{OrgID: orgID},
	)
	if err != nil {
		return nil, nil, fmt.Errorf("list organization BYO machines: %w", err)
	}
	for _, machineID := range byoMachineIDs {
		_, err := s.execution.DeleteMachineTx(ctx, tx, txNotifications, executionstore.DeleteMachineInput{
			OrgID: orgID, MachineID: machineID,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("delete organization BYO machine %s: %w", machineID, err)
		}
	}
	for _, projectID := range orgProjectIDs {
		if err := deleteProjectRelationshipsTx(ctx, q, orgID, projectID); err != nil {
			return nil, nil, err
		}
	}
	if err := q.DeleteOrganizationIntegrationApps(ctx, dbsqlc.DeleteOrganizationIntegrationAppsParams{OrgID: orgID}); err != nil {
		return nil, nil, fmt.Errorf("delete organization integration apps: %w", err)
	}
	if err := q.DeleteOrganizationIntegrationRuntimeUnits(ctx, dbsqlc.DeleteOrganizationIntegrationRuntimeUnitsParams{OrgID: orgID}); err != nil {
		return nil, nil, fmt.Errorf("delete organization integration runtime units: %w", err)
	}
	if err := q.DeleteOrganizationConfiguredModels(ctx, dbsqlc.DeleteOrganizationConfiguredModelsParams{OrgID: orgID}); err != nil {
		return nil, nil, fmt.Errorf("delete organization configured models: %w", err)
	}
	if err := q.DeleteOrganizationModelProviderConfigs(ctx, dbsqlc.DeleteOrganizationModelProviderConfigsParams{OrgID: orgID}); err != nil {
		return nil, nil, fmt.Errorf("delete organization model provider configs: %w", err)
	}
	skillArchives, err := skillops.ListArchiveRefs(ctx, q, orgID, nil)
	if err != nil {
		return nil, nil, err
	}
	if err := q.DeleteSkillsForOwner(ctx, dbsqlc.DeleteSkillsForOwnerParams{OrgID: orgID, OwnerProjectID: nil}); err != nil {
		return nil, nil, fmt.Errorf("delete organization skills: %w", err)
	}
	if err := q.DeleteSkillRevisionsForOwner(ctx, dbsqlc.DeleteSkillRevisionsForOwnerParams{OrgID: orgID, OwnerProjectID: nil}); err != nil {
		return nil, nil, fmt.Errorf("delete organization skill revisions: %w", err)
	}
	if err := q.DeleteOrganizationSkillGrants(ctx, dbsqlc.DeleteOrganizationSkillGrantsParams{OrgID: orgID}); err != nil {
		return nil, nil, fmt.Errorf("delete organization skill grants: %w", err)
	}
	if err := q.DeleteOrganizationSecretGrants(ctx, dbsqlc.DeleteOrganizationSecretGrantsParams{OrgID: orgID}); err != nil {
		return nil, nil, fmt.Errorf("delete organization secret grants: %w", err)
	}
	if err := q.DeleteOrganizationSecretOAuthLeases(ctx, dbsqlc.DeleteOrganizationSecretOAuthLeasesParams{OrgID: orgID}); err != nil {
		return nil, nil, fmt.Errorf("delete organization secret oauth leases: %w", err)
	}
	if err := q.DeleteDefaultModelProviderProvisioningForOrganization(
		ctx,
		dbsqlc.DeleteDefaultModelProviderProvisioningForOrganizationParams{OrganizationID: orgID},
	); err != nil {
		return nil, nil, fmt.Errorf("delete default model provider provisioning: %w", err)
	}
	if err := q.DeleteOrganizationSecrets(ctx, dbsqlc.DeleteOrganizationSecretsParams{OrgID: orgID}); err != nil {
		return nil, nil, fmt.Errorf("delete organization secrets: %w", err)
	}
	// Ciphertext of secrets still referenced by pool rows awaiting machine
	// teardown survives until teardown completion re-runs this inline.
	if _, err := q.DestroyUnreferencedSecretVersionsForDeletedOrg(ctx, dbsqlc.DestroyUnreferencedSecretVersionsForDeletedOrgParams{OrgID: orgID}); err != nil {
		return nil, nil, fmt.Errorf("destroy organization secret versions: %w", err)
	}
	if err := q.DeleteOrgInvitationsForOrgDeletion(ctx, dbsqlc.DeleteOrgInvitationsForOrgDeletionParams{OrgID: orgID}); err != nil {
		return nil, nil, fmt.Errorf("delete organization invitations: %w", err)
	}
	if err := q.DeleteOrganizationProjects(ctx, dbsqlc.DeleteOrganizationProjectsParams{OrgID: orgID}); err != nil {
		return nil, nil, fmt.Errorf("delete organization projects: %w", err)
	}
	if err := q.DeleteOrganizationMemberships(ctx, dbsqlc.DeleteOrganizationMembershipsParams{OrgID: orgID}); err != nil {
		return nil, nil, fmt.Errorf("delete organization memberships: %w", err)
	}
	if err := q.DeleteOrganizationOrgAPIKeys(ctx, dbsqlc.DeleteOrganizationOrgAPIKeysParams{OrgID: orgID}); err != nil {
		return nil, nil, fmt.Errorf("delete organization api keys: %w", err)
	}
	if err := storeutil.CommitTxWithNotifications(
		ctx,
		tx,
		txNotifications,
		s.postCommitPublisher,
		"delete organization",
	); err != nil {
		return nil, nil, err
	}
	skillops.Purge(ctx, s.blobs, skillArchives)
	return machines, nil, nil
}
