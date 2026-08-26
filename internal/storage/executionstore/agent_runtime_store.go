package executionstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type AgentState string

const (
	AgentStateActive   AgentState = "active"
	AgentStateArchived AgentState = "archived"
)

type insertAgentInput struct {
	OrgID           ID
	ProjectID       ID
	AgentProfileID  ID
	Name            string
	CurrentConfigID ID
	IdempotencyKey  string
}

type AgentRecord struct {
	ID                  ID         `json:"id"`
	OrgID               ID         `json:"org_id"`
	ProjectID           ID         `json:"project_id"`
	AgentProfileID      ID         `json:"agent_profile_id,omitempty"`
	State               AgentState `json:"state"`
	Name                string     `json:"name,omitempty"`
	CurrentConfigID     ID         `json:"current_config_id"`
	Model               AgentModelDisplay
	IntegrationTargetID ID `json:"integration_target_id,omitempty"`
	IntegrationTarget   IntegrationTargetDisplay
	IdempotencyKey      string     `json:"idempotency_key,omitempty"`
	NextEventSequence   int64      `json:"-"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
	ArchivedAt          *time.Time `json:"archived_at,omitempty"`
	Created             bool       `json:"-"`
}

type AgentModelDisplay struct {
	ProviderConfig string `json:"provider_config"`
	Name           string `json:"name"`
}

type IntegrationTargetDisplay struct {
	Provider         string `json:"provider,omitempty"`
	ProviderTenantID string `json:"-"`
	ProviderRef      string `json:"provider_ref,omitempty"`
	ProviderRefKind  string `json:"provider_ref_kind,omitempty"`
	DisplayName      string `json:"display_name,omitempty"`
}

func lockProjectAgentLifecycleSharedTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID ID,
) error {
	if err := qtx.LockProjectAgentLifecycleShared(
		ctx,
		dbsqlc.LockProjectAgentLifecycleSharedParams{ProjectID: projectID.String()},
	); err != nil {
		return fmt.Errorf("lock project agent lifecycle: %w", err)
	}
	return nil
}

func insertAgentWithProjectLifecycleLockTx(
	ctx context.Context,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	input insertAgentInput,
) (AgentRecord, bool, error) {
	row, err := qtx.InsertAgent(ctx, dbsqlc.InsertAgentParams{
		OrgID:           input.OrgID,
		ProjectID:       input.ProjectID,
		AgentProfileID:  sqlcIDFromNil(input.AgentProfileID),
		Name:            input.Name,
		CurrentConfigID: input.CurrentConfigID,
		IdempotencyKey:  sqlcTextFromEmpty(input.IdempotencyKey),
	})
	if err == nil {
		record := agentRecordFromInsertSQLC(row)
		record.Created = true
		if err := lockResourceCreation(ctx, qtx, resourceAgents, input.ProjectID.String()); err != nil {
			return AgentRecord{}, false, err
		}
		limits, err := resolveResourceLimits(ctx, qtx, input.OrgID)
		if err != nil {
			return AgentRecord{}, false, err
		}
		agentCount, err := qtx.CountActiveAgentsForProject(
			ctx,
			dbsqlc.CountActiveAgentsForProjectParams{ProjectID: input.ProjectID},
		)
		if err != nil {
			return AgentRecord{}, false, fmt.Errorf("count active agents: %w", err)
		}
		if agentCount > limits.MaxActiveAgentsPerProject {
			return AgentRecord{}, false, resourceLimitExceeded(
				"active agents",
				limits.MaxActiveAgentsPerProject,
			)
		}
		return record, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		if storeutil.IsUniqueViolation(err) {
			return AgentRecord{}, false, storeerr.ErrIdempotencyConflict
		}
		return AgentRecord{}, false, fmt.Errorf("create agent: %w", err)
	}
	if input.IdempotencyKey == "" {
		return AgentRecord{}, false, fmt.Errorf("create agent: %w", storeerr.ErrIdempotencyConflict)
	}

	record, err := loadAgentByIdempotencyKeyTx(ctx, tx, input.ProjectID, input.IdempotencyKey)
	if err != nil {
		return AgentRecord{}, false, err
	}
	return record, false, nil
}

func loadAgentByIdempotencyKeyTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID ID,
	idempotencyKey string,
) (AgentRecord, error) {
	row, err := dbsqlc.New(tx).
		GetAgentByIdempotencyKey(ctx, dbsqlc.GetAgentByIdempotencyKeyParams{
			ProjectID:      projectID,
			IdempotencyKey: idempotencyKey,
		})
	if err != nil {
		return AgentRecord{}, fmt.Errorf("load idempotent agent: %w", err)
	}
	return agentRecordFromIdempotencySQLC(row), nil
}

func loadAgentTx(ctx context.Context, tx pgx.Tx, id ID) (AgentRecord, error) {
	row, err := dbsqlc.New(tx).GetAgent(ctx, dbsqlc.GetAgentParams{ID: id})
	if err != nil {
		return AgentRecord{}, fmt.Errorf("load agent: %w", err)
	}
	return agentRecordFromGetSQLC(row), nil
}

func loadAgentInProjectTx(ctx context.Context, tx pgx.Tx, projectID, id ID) (AgentRecord, error) {
	row, err := dbsqlc.New(tx).
		GetAgentInProject(ctx, dbsqlc.GetAgentInProjectParams{ProjectID: projectID, ID: id})
	if err != nil {
		return AgentRecord{}, fmt.Errorf("load agent in project: %w", err)
	}
	return agentRecordFromProjectSQLC(row), nil
}

func (s *Store) GetAgentInProject(ctx context.Context, projectID, id ID) (AgentRecord, error) {
	if isNilID(projectID) || isNilID(id) {
		return AgentRecord{}, errors.New("project id and agent id are required")
	}
	row, err := s.q.GetAgentInProject(
		ctx,
		dbsqlc.GetAgentInProjectParams{ProjectID: projectID, ID: id},
	)
	if err != nil {
		return AgentRecord{}, fmt.Errorf("load agent in project: %w", err)
	}
	return agentRecordFromProjectSQLC(row), nil
}

type ListAgentsForProjectInput struct {
	ProjectID ID
	Filters   AgentListFilters
	List      listing.Options
	Limit     int
}

type AgentListFilters struct {
	IntegrationProviders   []string
	IntegrationTargetKinds []string
	HasIntegrationTarget   *bool
	AgentProfileID         *ID
}

type ListAgentsForProjectResult struct {
	Agents  []AgentRecord
	HasMore bool
	Next    listing.Cursor
}

// ListAgentsForProject returns one keyset page of a project's active agents, newest first.
func (s *Store) ListAgentsForProject(
	ctx context.Context,
	input ListAgentsForProjectInput,
) (ListAgentsForProjectResult, error) {
	if isNilID(input.ProjectID) {
		return ListAgentsForProjectResult{}, errors.New("project id is required")
	}
	if input.Limit <= 0 {
		return ListAgentsForProjectResult{}, errors.New("limit must be positive")
	}
	input.List = listing.Normalize(input.List)
	if !listing.SortAllowed(
		input.List.SortField,
		"name", "created_at", "updated_at", "state", "integration_provider", "integration_target_kind",
	) {
		return ListAgentsForProjectResult{}, errors.New("unsupported agent list sort")
	}
	if input.List.SortField == "created_at" && input.List.SortDesc {
		return s.listAgentsForProjectByCreatedAtDesc(ctx, input)
	}
	params := dbsqlc.ListAgentsForProjectParams{
		ProjectID: input.ProjectID, RowLimit: int64(input.Limit) + 1,
		NamePattern: input.List.NamePattern, SortField: input.List.SortField,
		SortDesc: input.List.SortDesc, CursorSet: input.List.After.Set,
		CursorIsNull: input.List.After.IsNull, CursorKey: input.List.After.Key,
		CursorID:               input.List.After.ID,
		IntegrationProviders:   input.Filters.IntegrationProviders,
		IntegrationTargetKinds: input.Filters.IntegrationTargetKinds,
		HasIntegrationTarget:   input.Filters.HasIntegrationTarget,
		AgentProfileID:         input.Filters.AgentProfileID,
	}
	rows, err := s.q.ListAgentsForProject(ctx, params)
	if err != nil {
		return ListAgentsForProjectResult{}, fmt.Errorf("list agents: %w", err)
	}
	result := ListAgentsForProjectResult{}
	if len(rows) > input.Limit {
		result.HasMore = true
		rows = rows[:input.Limit]
	}
	if result.HasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		result.Next = listing.Cursor{Set: true, IsNull: last.SortIsNull, Key: last.SortKey, ID: last.ID}
	}
	result.Agents = make([]AgentRecord, 0, len(rows))
	for _, row := range rows {
		result.Agents = append(result.Agents, agentRecordFromListForProjectSQLC(row))
	}
	return result, nil
}

func (s *Store) listAgentsForProjectByCreatedAtDesc(
	ctx context.Context,
	input ListAgentsForProjectInput,
) (ListAgentsForProjectResult, error) {
	var cursorCreatedAt time.Time
	if input.List.After.Set {
		var err error
		cursorCreatedAt, err = listing.ParseTimestampKey(input.List.After.Key)
		if err != nil {
			return ListAgentsForProjectResult{}, fmt.Errorf("parse created_at cursor: %w", err)
		}
	}
	rows, err := s.q.ListAgentsForProjectByCreatedAtDesc(
		ctx,
		dbsqlc.ListAgentsForProjectByCreatedAtDescParams{
			ProjectID:              input.ProjectID,
			NamePattern:            input.List.NamePattern,
			IntegrationProviders:   input.Filters.IntegrationProviders,
			IntegrationTargetKinds: input.Filters.IntegrationTargetKinds,
			HasIntegrationTarget:   input.Filters.HasIntegrationTarget,
			AgentProfileID:         input.Filters.AgentProfileID,
			CursorSet:              input.List.After.Set,
			CursorCreatedAt:        cursorCreatedAt,
			CursorID:               input.List.After.ID,
			RowLimit:               int64(input.Limit) + 1,
		},
	)
	if err != nil {
		return ListAgentsForProjectResult{}, fmt.Errorf("list agents by created_at descending: %w", err)
	}
	result := ListAgentsForProjectResult{}
	if len(rows) > input.Limit {
		result.HasMore = true
		rows = rows[:input.Limit]
	}
	if result.HasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		result.Next = listing.Cursor{
			Set: true,
			Key: listing.TimestampKey(last.CreatedAt),
			ID:  last.ID,
		}
	}
	result.Agents = make([]AgentRecord, 0, len(rows))
	for _, row := range rows {
		result.Agents = append(result.Agents, agentRecordFromListForProjectByCreatedAtDescSQLC(row))
	}
	return result, nil
}

type ListRecentAgentsForProjectsInput struct {
	ProjectIDs []ID
	Limit      int
}

// ListRecentAgentsForProjects returns the most recently updated active agents
// across the given projects, newest first.
func (s *Store) ListRecentAgentsForProjects(
	ctx context.Context,
	input ListRecentAgentsForProjectsInput,
) ([]AgentRecord, error) {
	if input.Limit <= 0 {
		return nil, errors.New("limit must be positive")
	}
	if len(input.ProjectIDs) == 0 {
		return []AgentRecord{}, nil
	}
	rows, err := s.q.ListRecentAgentsForProjects(ctx, dbsqlc.ListRecentAgentsForProjectsParams{
		ProjectIds: input.ProjectIDs,
		RowLimit:   int64(input.Limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list recent agents: %w", err)
	}
	records := make([]AgentRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, agentRecordFromListRecentForProjectsSQLC(row))
	}
	return records, nil
}

func (s *Store) ArchiveAgent(
	ctx context.Context,
	projectID ID,
	agentID ID,
	archivedBy identitystore.PrincipalRecord,
) (AgentRecord, []MachineRecord, error) {
	if isNilID(projectID) || isNilID(agentID) {
		return AgentRecord{}, nil, errors.New("project id and agent id are required")
	}
	if isNilID(archivedBy.ID) {
		return AgentRecord{}, nil, errors.New("archiving principal is required")
	}
	if err := validateProjectPrincipalActionTx(
		ctx,
		s.q,
		projectID,
		archivedBy,
		identitystore.AgentActionOperate,
	); err != nil {
		return AgentRecord{}, nil, err
	}
	project, err := loadProjectTx(ctx, s.q, projectID)
	if err != nil {
		return AgentRecord{}, nil, err
	}
	actor, err := OmnaraActorParams(project.OrgID, archivedBy)
	if err != nil {
		return AgentRecord{}, nil, err
	}
	txNotifications := s.newTxNotifications()
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AgentRecord{}, nil, fmt.Errorf("begin archive agent: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	machines, err := archiveAgentTx(ctx, tx, qtx, txNotifications, projectID, agentID, actor)
	if err != nil {
		return AgentRecord{}, nil, err
	}
	agent, err := loadAgentInProjectTx(ctx, tx, projectID, agentID)
	if err != nil {
		return AgentRecord{}, nil, err
	}
	if err := s.commitTxWithNotifications(
		ctx,
		tx,
		txNotifications,
		fmt.Sprintf("archive agent %s", agentID.String()),
	); err != nil {
		return AgentRecord{}, nil, err
	}
	return agent, machines, nil
}

func archiveAgentTx(
	ctx context.Context,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	txNotifications *notifications.TxNotifications,
	projectID, agentID ID,
	actor *ActorParams,
) ([]MachineRecord, error) {
	if err := qtx.LockAgentMachineSources(
		ctx,
		dbsqlc.LockAgentMachineSourcesParams{AgentID: agentID},
	); err != nil {
		return nil, fmt.Errorf("lock archived agent machine sources: %w", err)
	}
	if err := qtx.LockAttachedAgentPoolMachines(
		ctx,
		dbsqlc.LockAttachedAgentPoolMachinesParams{ProjectID: projectID, AgentID: agentID},
	); err != nil {
		return nil, fmt.Errorf("lock archived agent pool machines: %w", err)
	}
	if _, err := qtx.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: projectID, ID: agentID},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, storeerr.ErrNotFound
		}
		return nil, fmt.Errorf("lock agent for archive: %w", err)
	}
	actorID, err := resolveActorTx(ctx, qtx, projectID, agentID, actor, NilID)
	if err != nil {
		return nil, err
	}
	rows, err := qtx.ArchiveAgent(ctx, dbsqlc.ArchiveAgentParams{ProjectID: projectID, ID: agentID})
	if err != nil {
		return nil, fmt.Errorf("archive agent: %w", err)
	}
	if rows == 0 {
		return nil, storeerr.ErrNotFound
	}

	if _, err := qtx.CancelQueuedBacklogInputsForAgent(ctx, dbsqlc.CancelQueuedBacklogInputsForAgentParams{
		ProjectID: projectID,
		AgentID:   agentID,
	}); err != nil {
		return nil, fmt.Errorf("cancel archived agent queued backlog inputs: %w", err)
	}
	if _, err := qtx.DeleteCronTriggersForAgent(ctx, dbsqlc.DeleteCronTriggersForAgentParams{
		ProjectID: projectID,
		AgentID:   &agentID,
	}); err != nil {
		return nil, fmt.Errorf("delete archived agent cron triggers: %w", err)
	}
	if err := completeExecutionRevokedProcessesTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		executionRevokedProcessScope{projectID: projectID, agentID: agentID},
		"agent_archived",
	); err != nil {
		return nil, err
	}
	if _, err := cancelAgentTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		cancelAgentTxInput{
			ProjectID:                           projectID,
			AgentID:                             agentID,
			ActorID:                             actorID,
			ReasonCode:                          "agent_archived",
			ModelCallMessage:                    "The model call was stopped because the agent was archived.",
			CancelRuntimeWithoutContinuableTurn: true,
		},
	); err != nil {
		return nil, err
	}
	if _, err := qtx.DeleteAgentWakeup(ctx, dbsqlc.DeleteAgentWakeupParams{
		ProjectID: projectID,
		AgentID:   agentID,
	}); err != nil {
		return nil, fmt.Errorf("delete archived agent wakeup: %w", err)
	}
	machineRows, err := qtx.MarkArchivedAgentPoolMachinesDeleting(ctx, dbsqlc.MarkArchivedAgentPoolMachinesDeletingParams{
		ProjectID:              projectID,
		AgentID:                agentID,
		LifecycleReasonCode:    sqlcTextFromEmpty("agent_archived_cleanup"),
		LifecycleReasonMessage: "cleaning up machine after agent archived",
	})
	if err != nil {
		return nil, fmt.Errorf("mark archived agent pool machines deleting: %w", err)
	}
	releaseParams := dbsqlc.ReleaseExplicitAgentMachineBindingsForAgentParams{
		ProjectID: projectID,
		AgentID:   agentID,
	}
	if err := qtx.ReleaseExplicitAgentMachineBindingsForAgent(ctx, releaseParams); err != nil {
		return nil, fmt.Errorf("release archived agent explicit machine bindings: %w", err)
	}
	machines := make([]MachineRecord, 0, len(machineRows))
	for _, row := range machineRows {
		machines = append(machines, machineRecordFromMarkArchivedAgentPoolMachinesDeletingSQLC(row))
	}
	return machines, nil
}
