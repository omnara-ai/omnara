package orglifecycle

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/skillops"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type CreateOrgForUserInput struct {
	OrgID                ID
	UserID               ID
	Name                 string
	IdempotencyKey       string
	DefaultMachinePools  []executionstore.DefaultMachinePoolTemplate
	DefaultModelProvider *modelstore.ProvisionedDefaultModelProvider
}

func (s *Service) CreateOrgForUser(
	ctx context.Context,
	input CreateOrgForUserInput,
) (identitystore.CreateOrgForUserRecord, error) {
	if isNilID(input.UserID) {
		return identitystore.CreateOrgForUserRecord{}, errors.New("user id is required")
	}
	if input.Name == "" {
		return identitystore.CreateOrgForUserRecord{}, errors.New("org name is required")
	}
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
	if err := lifecyclelock.OrganizationShared(ctx, tx, input.OrgID); err != nil {
		return identitystore.CreateOrgForUserRecord{}, err
	}
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
		if err := s.createDefaultModelProviderForOrgTx(
			ctx,
			tx,
			record.Org.ID,
			record.Project.ID,
			input.UserID,
			input.DefaultModelProvider,
		); err != nil {
			return identitystore.CreateOrgForUserRecord{}, err
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
	if err := q.DeleteProjectIntegrationInstalls(ctx, dbsqlc.DeleteProjectIntegrationInstallsParams{OrgID: orgID, ProjectID: projectID}); err != nil {
		return fmt.Errorf("delete project integration installs: %w", err)
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
	txNotifications *notifications.TxNotifications,
	projectID ID,
	agentIDs []ID,
	actor *executionstore.ActorParams,
) ([]executionstore.MachineRecord, error) {
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

type organizationMachineLifecyclePlan struct {
	agentIDsByProject map[ID][]ID
	poolIDs           []ID
	byoMachineIDs     []ID
}

func prelockProjectMachineLifecycleTx(
	ctx context.Context,
	tx pgx.Tx,
	q *dbsqlc.Queries,
	orgID, projectID ID,
) ([]ID, error) {
	agentIDs, err := q.ListActiveAgentIDsForProjectDeletion(
		ctx,
		dbsqlc.ListActiveAgentIDsForProjectDeletionParams{ProjectID: projectID},
	)
	if err != nil {
		return nil, fmt.Errorf("list project agents for lifecycle: %w", err)
	}
	if err := lifecyclelock.AgentSources(ctx, tx, agentIDs); err != nil {
		return nil, err
	}
	agentIDs, err = q.ListActiveAgentIDsForProjectDeletion(
		ctx,
		dbsqlc.ListActiveAgentIDsForProjectDeletionParams{ProjectID: projectID},
	)
	if err != nil {
		return nil, fmt.Errorf("reload project agents for lifecycle: %w", err)
	}
	agentRefs := make([]lifecyclelock.AgentRef, 0, len(agentIDs))
	for _, agentID := range agentIDs {
		agentRefs = append(agentRefs, lifecyclelock.AgentRef{ProjectID: projectID, AgentID: agentID})
	}
	grantRows, err := q.ListProjectMachinePoolGrantRefsForProjectLifecycle(
		ctx,
		dbsqlc.ListProjectMachinePoolGrantRefsForProjectLifecycleParams{
			OrgID: orgID, ProjectID: projectID,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list project pool grants for lifecycle: %w", err)
	}
	poolRefs := make([]lifecyclelock.PoolRef, 0, len(grantRows))
	for _, grantRow := range grantRows {
		poolRefs = append(poolRefs, lifecyclelock.PoolRef{OrgID: orgID, PoolID: grantRow.MachinePoolID})
	}
	if err := lifecyclelock.Pools(ctx, tx, poolRefs); err != nil {
		return nil, err
	}
	grantRows, err = q.ListProjectMachinePoolGrantRefsForProjectLifecycle(
		ctx,
		dbsqlc.ListProjectMachinePoolGrantRefsForProjectLifecycleParams{
			OrgID: orgID, ProjectID: projectID,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("reload project pool grants for lifecycle: %w", err)
	}
	grantIDs := make([]ID, 0, len(grantRows))
	for _, grantRow := range grantRows {
		grantIDs = append(grantIDs, grantRow.ID)
	}
	if err := lifecyclelock.PoolGrants(ctx, tx, grantIDs); err != nil {
		return nil, err
	}
	machineIDs, err := q.ListProjectMachineIDsForLifecycle(
		ctx,
		dbsqlc.ListProjectMachineIDsForLifecycleParams{OrgID: orgID, ProjectID: projectID},
	)
	if err != nil {
		return nil, fmt.Errorf("list project machines for lifecycle: %w", err)
	}
	machineRefs := make([]lifecyclelock.MachineRef, 0, len(machineIDs))
	for _, machineID := range machineIDs {
		machineRefs = append(machineRefs, lifecyclelock.MachineRef{OrgID: orgID, MachineID: machineID})
	}
	if err := lifecyclelock.Machines(ctx, tx, machineRefs); err != nil {
		return nil, err
	}
	if err := lifecyclelock.Agents(ctx, tx, agentRefs); err != nil {
		return nil, err
	}
	return agentIDs, nil
}

func prelockOrganizationMachineLifecycleTx(
	ctx context.Context,
	tx pgx.Tx,
	q *dbsqlc.Queries,
	orgID ID,
) (organizationMachineLifecyclePlan, error) {
	agentRows, err := q.ListActiveAgentRefsForOrganizationDeletion(
		ctx,
		dbsqlc.ListActiveAgentRefsForOrganizationDeletionParams{OrgID: orgID},
	)
	if err != nil {
		return organizationMachineLifecyclePlan{}, fmt.Errorf(
			"list organization agents for lifecycle: %w",
			err,
		)
	}
	sourceRefs, _ := organizationAgentLifecycleRefs(agentRows)
	sourceIDs := make([]uuid.UUID, 0, len(sourceRefs))
	for _, ref := range sourceRefs {
		sourceIDs = append(sourceIDs, ref.AgentID)
	}
	if err := lifecyclelock.AgentSources(ctx, tx, sourceIDs); err != nil {
		return organizationMachineLifecyclePlan{}, err
	}
	agentRows, err = q.ListActiveAgentRefsForOrganizationDeletion(
		ctx,
		dbsqlc.ListActiveAgentRefsForOrganizationDeletionParams{OrgID: orgID},
	)
	if err != nil {
		return organizationMachineLifecyclePlan{}, fmt.Errorf(
			"reload organization agents for lifecycle: %w",
			err,
		)
	}
	agentRefs, agentIDsByProject := organizationAgentLifecycleRefs(agentRows)
	poolIDs, err := q.ListActiveMachinePoolIDsForOrganizationDeletion(
		ctx,
		dbsqlc.ListActiveMachinePoolIDsForOrganizationDeletionParams{OrgID: orgID},
	)
	if err != nil {
		return organizationMachineLifecyclePlan{}, fmt.Errorf(
			"list organization machine pools for lifecycle: %w",
			err,
		)
	}
	grantRows, err := q.ListProjectMachinePoolGrantRefsForOrganizationLifecycle(
		ctx,
		dbsqlc.ListProjectMachinePoolGrantRefsForOrganizationLifecycleParams{OrgID: orgID},
	)
	if err != nil {
		return organizationMachineLifecyclePlan{}, fmt.Errorf(
			"list organization pool grants for lifecycle: %w",
			err,
		)
	}
	poolRefs := make([]lifecyclelock.PoolRef, 0, len(poolIDs)+len(grantRows))
	for _, poolID := range poolIDs {
		poolRefs = append(poolRefs, lifecyclelock.PoolRef{OrgID: orgID, PoolID: poolID})
	}
	for _, grantRow := range grantRows {
		poolRefs = append(poolRefs, lifecyclelock.PoolRef{OrgID: orgID, PoolID: grantRow.MachinePoolID})
	}
	if err := lifecyclelock.Pools(ctx, tx, poolRefs); err != nil {
		return organizationMachineLifecyclePlan{}, err
	}
	poolIDs, err = q.ListActiveMachinePoolIDsForOrganizationDeletion(
		ctx,
		dbsqlc.ListActiveMachinePoolIDsForOrganizationDeletionParams{OrgID: orgID},
	)
	if err != nil {
		return organizationMachineLifecyclePlan{}, fmt.Errorf(
			"reload organization machine pools for lifecycle: %w",
			err,
		)
	}
	grantRows, err = q.ListProjectMachinePoolGrantRefsForOrganizationLifecycle(
		ctx,
		dbsqlc.ListProjectMachinePoolGrantRefsForOrganizationLifecycleParams{OrgID: orgID},
	)
	if err != nil {
		return organizationMachineLifecyclePlan{}, fmt.Errorf(
			"reload organization pool grants for lifecycle: %w",
			err,
		)
	}
	grantIDs := make([]ID, 0, len(grantRows))
	for _, grantRow := range grantRows {
		grantIDs = append(grantIDs, grantRow.ID)
	}
	if err := lifecyclelock.PoolGrants(ctx, tx, grantIDs); err != nil {
		return organizationMachineLifecyclePlan{}, err
	}
	machineIDs, err := q.ListOrganizationMachineIDsForLifecycle(
		ctx,
		dbsqlc.ListOrganizationMachineIDsForLifecycleParams{OrgID: orgID},
	)
	if err != nil {
		return organizationMachineLifecyclePlan{}, fmt.Errorf(
			"list organization machines for lifecycle: %w",
			err,
		)
	}
	machineRefs := make([]lifecyclelock.MachineRef, 0, len(machineIDs))
	for _, machineID := range machineIDs {
		machineRefs = append(machineRefs, lifecyclelock.MachineRef{OrgID: orgID, MachineID: machineID})
	}
	if err := lifecyclelock.Machines(ctx, tx, machineRefs); err != nil {
		return organizationMachineLifecyclePlan{}, err
	}
	if err := lifecyclelock.Agents(ctx, tx, agentRefs); err != nil {
		return organizationMachineLifecyclePlan{}, err
	}
	byoMachineIDs, err := q.ListActiveBYOMachineIDsForOrganizationDeletion(
		ctx,
		dbsqlc.ListActiveBYOMachineIDsForOrganizationDeletionParams{OrgID: orgID},
	)
	if err != nil {
		return organizationMachineLifecyclePlan{}, fmt.Errorf(
			"list organization BYO machines: %w",
			err,
		)
	}
	return organizationMachineLifecyclePlan{
		agentIDsByProject: agentIDsByProject,
		poolIDs:           poolIDs,
		byoMachineIDs:     byoMachineIDs,
	}, nil
}

func organizationAgentLifecycleRefs(
	rows []dbsqlc.ListActiveAgentRefsForOrganizationDeletionRow,
) ([]lifecyclelock.AgentRef, map[ID][]ID) {
	refs := make([]lifecyclelock.AgentRef, 0, len(rows))
	idsByProject := make(map[ID][]ID)
	for _, row := range rows {
		refs = append(refs, lifecyclelock.AgentRef{ProjectID: row.ProjectID, AgentID: row.AgentID})
		idsByProject[row.ProjectID] = append(idsByProject[row.ProjectID], row.AgentID)
	}
	return refs, idsByProject
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
	return storeutil.RetryTransaction(ctx, func() ([]executionstore.MachineRecord, error) {
		return s.deleteProjectOnce(ctx, orgID, projectID, actor)
	})
}

func (s *Service) deleteProjectOnce(
	ctx context.Context,
	orgID, projectID ID,
	actor *executionstore.ActorParams,
) ([]executionstore.MachineRecord, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin delete project: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	if err := lifecyclelock.OrganizationShared(ctx, tx, orgID); err != nil {
		return nil, err
	}
	if err := lifecyclelock.ProjectsExclusive(ctx, tx, []ID{projectID}); err != nil {
		return nil, err
	}
	if _, err := q.GetProject(ctx, dbsqlc.GetProjectParams{OrgID: orgID, ID: projectID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, storeerr.ErrNotFound
		}
		return nil, fmt.Errorf("load project for deletion: %w", err)
	}
	agentIDs, err := prelockProjectMachineLifecycleTx(ctx, tx, q, orgID, projectID)
	if err != nil {
		return nil, err
	}
	txNotifications := s.newTxNotifications()
	machines, err := s.teardownProjectAgentsTx(
		ctx,
		tx,
		txNotifications,
		projectID,
		agentIDs,
		actor,
	)
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
	return storeutil.RetryTransaction(ctx, func() ([]executionstore.MachineRecord, error) {
		return s.deleteOrganizationOnce(ctx, orgID, actor)
	})
}

//nolint:lll // Keeping generated query parameters inline makes the transaction's cascade order auditable.
func (s *Service) deleteOrganizationOnce(
	ctx context.Context,
	orgID ID,
	actor *executionstore.ActorParams,
) ([]executionstore.MachineRecord, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin delete organization: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	if err := lifecyclelock.OrganizationExclusive(ctx, tx, orgID); err != nil {
		return nil, err
	}
	active, err := q.OrgExistsActive(ctx, dbsqlc.OrgExistsActiveParams{ID: orgID})
	if err != nil {
		return nil, fmt.Errorf("load organization for deletion: %w", err)
	}
	if !active {
		return nil, storeerr.ErrNotFound
	}
	orgProjectIDs, err := q.ListActiveProjectIDsForOrganization(
		ctx,
		dbsqlc.ListActiveProjectIDsForOrganizationParams{OrgID: orgID},
	)
	if err != nil {
		return nil, fmt.Errorf("list organization projects: %w", err)
	}
	if err := lifecyclelock.ProjectsExclusive(ctx, tx, orgProjectIDs); err != nil {
		return nil, err
	}
	plan, err := prelockOrganizationMachineLifecycleTx(ctx, tx, q, orgID)
	if err != nil {
		return nil, err
	}
	txNotifications := s.newTxNotifications()
	rows, err := q.DeleteOrganization(ctx, dbsqlc.DeleteOrganizationParams{ID: orgID})
	if err != nil {
		return nil, fmt.Errorf("delete organization: %w", err)
	}
	if rows == 0 {
		return nil, storeerr.ErrNotFound
	}
	machines := make([]executionstore.MachineRecord, 0)
	for _, projectID := range orgProjectIDs {
		projectMachines, err := s.teardownProjectAgentsTx(
			ctx,
			tx,
			txNotifications,
			projectID,
			plan.agentIDsByProject[projectID],
			actor,
		)
		if err != nil {
			return nil, err
		}
		machines = append(machines, projectMachines...)
	}
	for _, poolID := range plan.poolIDs {
		poolMachines, err := s.execution.DeleteMachinePoolTx(ctx, tx, txNotifications, orgID, poolID)
		if err != nil {
			return nil, fmt.Errorf("delete organization machine pool %s: %w", poolID, err)
		}
		machines = append(machines, poolMachines...)
	}
	for _, machineID := range plan.byoMachineIDs {
		_, err := s.execution.DeleteMachineTx(ctx, tx, txNotifications, executionstore.DeleteMachineInput{
			OrgID: orgID, MachineID: machineID,
		})
		if err != nil {
			return nil, fmt.Errorf("delete organization BYO machine %s: %w", machineID, err)
		}
	}
	for _, projectID := range orgProjectIDs {
		if err := deleteProjectRelationshipsTx(ctx, q, orgID, projectID); err != nil {
			return nil, err
		}
	}
	if err := q.DeleteOrganizationConfiguredModels(ctx, dbsqlc.DeleteOrganizationConfiguredModelsParams{OrgID: orgID}); err != nil {
		return nil, fmt.Errorf("delete organization configured models: %w", err)
	}
	if err := q.DeleteOrganizationModelProviderConfigs(ctx, dbsqlc.DeleteOrganizationModelProviderConfigsParams{OrgID: orgID}); err != nil {
		return nil, fmt.Errorf("delete organization model provider configs: %w", err)
	}
	skillArchives, err := skillops.ListArchiveRefs(ctx, q, orgID, nil)
	if err != nil {
		return nil, err
	}
	if err := q.DeleteSkillsForOwner(ctx, dbsqlc.DeleteSkillsForOwnerParams{OrgID: orgID, OwnerProjectID: nil}); err != nil {
		return nil, fmt.Errorf("delete organization skills: %w", err)
	}
	if err := q.DeleteSkillRevisionsForOwner(ctx, dbsqlc.DeleteSkillRevisionsForOwnerParams{OrgID: orgID, OwnerProjectID: nil}); err != nil {
		return nil, fmt.Errorf("delete organization skill revisions: %w", err)
	}
	if err := q.DeleteOrganizationSkillGrants(ctx, dbsqlc.DeleteOrganizationSkillGrantsParams{OrgID: orgID}); err != nil {
		return nil, fmt.Errorf("delete organization skill grants: %w", err)
	}
	if err := q.DeleteOrganizationSecretGrants(ctx, dbsqlc.DeleteOrganizationSecretGrantsParams{OrgID: orgID}); err != nil {
		return nil, fmt.Errorf("delete organization secret grants: %w", err)
	}
	if err := q.DeleteOrganizationSecretOAuthLeases(ctx, dbsqlc.DeleteOrganizationSecretOAuthLeasesParams{OrgID: orgID}); err != nil {
		return nil, fmt.Errorf("delete organization secret oauth leases: %w", err)
	}
	if err := q.DeleteOrganizationSecrets(ctx, dbsqlc.DeleteOrganizationSecretsParams{OrgID: orgID}); err != nil {
		return nil, fmt.Errorf("delete organization secrets: %w", err)
	}
	// Ciphertext of secrets still referenced by pool rows awaiting machine
	// teardown survives until teardown completion re-runs this inline.
	if _, err := q.DestroyUnreferencedSecretVersionsForDeletedOrg(ctx, dbsqlc.DestroyUnreferencedSecretVersionsForDeletedOrgParams{OrgID: orgID}); err != nil {
		return nil, fmt.Errorf("destroy organization secret versions: %w", err)
	}
	if err := q.DeleteOrgInvitationsForOrgDeletion(ctx, dbsqlc.DeleteOrgInvitationsForOrgDeletionParams{OrgID: orgID}); err != nil {
		return nil, fmt.Errorf("delete organization invitations: %w", err)
	}
	if err := q.DeleteOrganizationProjects(ctx, dbsqlc.DeleteOrganizationProjectsParams{OrgID: orgID}); err != nil {
		return nil, fmt.Errorf("delete organization projects: %w", err)
	}
	if err := q.DeleteOrganizationMemberships(ctx, dbsqlc.DeleteOrganizationMembershipsParams{OrgID: orgID}); err != nil {
		return nil, fmt.Errorf("delete organization memberships: %w", err)
	}
	if err := q.DeleteOrganizationOrgAPIKeys(ctx, dbsqlc.DeleteOrganizationOrgAPIKeysParams{OrgID: orgID}); err != nil {
		return nil, fmt.Errorf("delete organization api keys: %w", err)
	}
	if err := storeutil.CommitTxWithNotifications(
		ctx,
		tx,
		txNotifications,
		s.postCommitPublisher,
		"delete organization",
	); err != nil {
		return nil, err
	}
	skillops.Purge(ctx, s.blobs, skillArchives)
	return machines, nil
}
