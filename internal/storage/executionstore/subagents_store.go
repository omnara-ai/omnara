package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	subagentMessageIdempotencyScope = "subagent_message"

	SubagentMessageKindResult          = "result"
	SubagentMessageKindRefused         = "refused"
	SubagentMessageKindContentFiltered = "content_filtered"
	SubagentMessageKindFailed          = "failed"
	SubagentMessageKindQuestion        = "question"

	SubagentStateRunning              = "running"
	SubagentStateIdle                 = "idle"
	SubagentStateWaitingOnInteraction = "waiting_on_interaction"
	SubagentStateArchived             = "archived"
)

type SubagentLaunch struct {
	ParentAgentID uuid.UUID
	Key           string
	MaxInstances  *int
	MaxSubagents  *int
	MaxDepth      int
}

type SubagentStatus struct {
	AgentID           uuid.UUID
	Name              string
	Key               string
	State             string
	LastActivityAt    time.Time
	Archived          bool
	IsRunning         bool
	HasOpenQuestion   bool
	HasOpenPermission bool
}

func SubagentActorParams(orgID uuid.UUID, agent AgentRecord) (*ActorParams, error) {
	tenantID, err := publicid.Encode(publicid.KindOrganization, orgID)
	if err != nil {
		return nil, fmt.Errorf("encode subagent actor tenant: %w", err)
	}
	agentPublicID, err := publicid.Encode(publicid.KindAgent, agent.ID)
	if err != nil {
		return nil, fmt.Errorf("encode subagent actor principal: %w", err)
	}
	return &ActorParams{
		Provider:         ActorProviderOmnara,
		ProviderTenantID: tenantID,
		ProviderUserID:   agentPublicID,
	}, nil
}

func subagentDisplayName(agent AgentRecord) string {
	if agent.Name != "" {
		return agent.Name
	}
	if agent.SubagentKey != "" {
		return agent.SubagentKey
	}
	return "agent"
}

func lockSubagentParentSourcesTx(ctx context.Context, tx dbsqlc.DBTX, launch SubagentLaunch) error {
	if launch.ParentAgentID == uuid.Nil || launch.Key == "" {
		return errors.New("subagent launch requires a parent agent and key")
	}
	if launch.MaxDepth < 1 || launch.MaxDepth > agentconfig.MaxSubagentDepth {
		return fmt.Errorf("subagent launch max depth must be between 1 and %d", agentconfig.MaxSubagentDepth)
	}
	return lifecyclelock.AgentSources(ctx, tx, launch.ParentAgentID)
}

func (s *Store) AgentDepth(ctx context.Context, projectID, agentID uuid.UUID) (int, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil {
		return 0, errors.New("project id and agent id are required")
	}
	depth, err := s.q.CountAgentAncestors(
		ctx,
		dbsqlc.CountAgentAncestorsParams{ProjectID: projectID, AgentID: agentID},
	)
	if err != nil {
		return 0, fmt.Errorf("count agent ancestors: %w", err)
	}
	return int(depth), nil
}

func lockParentMachineBindingsForSharingTx(
	ctx context.Context,
	tx dbsqlc.DBTX,
	qtx *dbsqlc.Queries,
	orgID, projectID, parentAgentID uuid.UUID,
) ([]dbsqlc.ListParentMachineBindingsForSharingRow, error) {
	sharedBindings, err := qtx.ListParentMachineBindingsForSharing(
		ctx,
		dbsqlc.ListParentMachineBindingsForSharingParams{ProjectID: projectID, AgentID: parentAgentID},
	)
	if err != nil {
		return nil, fmt.Errorf("list parent machine bindings: %w", err)
	}
	machineRefs := make([]lifecyclelock.MachineRef, 0, len(sharedBindings))
	for _, row := range sharedBindings {
		machineRefs = append(machineRefs, lifecyclelock.MachineRef{OrgID: orgID, MachineID: row.MachineID})
	}
	if err := lifecyclelock.Machines(ctx, tx, machineRefs); err != nil {
		return nil, err
	}
	revalidated, err := qtx.ListParentMachineBindingsForSharing(
		ctx,
		dbsqlc.ListParentMachineBindingsForSharingParams{
			ProjectID: projectID, AgentID: parentAgentID,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("revalidate parent machine bindings: %w", err)
	}
	if !slices.EqualFunc(
		sharedBindings,
		revalidated,
		func(a, b dbsqlc.ListParentMachineBindingsForSharingRow) bool {
			return a.MachineID == b.MachineID && a.ProjectMachineGrantID == b.ProjectMachineGrantID
		},
	) {
		return nil, fmt.Errorf(
			"parent machine bindings changed during launch: %w",
			storeutil.ErrRetryTransaction,
		)
	}
	return revalidated, nil
}

func admitSubagentLaunchTx(
	ctx context.Context,
	unit *agentexecution.Unit,
	qtx *dbsqlc.Queries,
	projectID uuid.UUID,
	launch SubagentLaunch,
) error {
	tx := unit.DB()
	if err := unit.LockAgentRefs(ctx, []lifecyclelock.AgentRef{{
		ProjectID: projectID,
		AgentID:   launch.ParentAgentID,
	}}, agentexecution.LifecycleAuthority{}); err != nil {
		if errors.Is(err, storeerr.ErrNotFound) {
			return fmt.Errorf("parent agent: %w", storeerr.ErrNotFound)
		}
		return err
	}
	parent, err := loadAgentInProjectTx(ctx, tx, projectID, launch.ParentAgentID)
	if err != nil {
		return err
	}
	if parent.State != AgentStateActive {
		return fmt.Errorf("parent agent is archived: %w", storeerr.ErrStateTransitionConflict)
	}
	depth, err := qtx.CountAgentAncestors(ctx, dbsqlc.CountAgentAncestorsParams{
		ProjectID: projectID,
		AgentID:   launch.ParentAgentID,
	})
	if err != nil {
		return fmt.Errorf("count subagent ancestors: %w", err)
	}
	if int(depth)+1 > launch.MaxDepth {
		return storeerr.InvalidRequest(
			fmt.Errorf("subagent depth limit of %d reached", launch.MaxDepth),
		)
	}
	siblings, err := qtx.CountActiveChildAgentsForLaunch(ctx, dbsqlc.CountActiveChildAgentsForLaunchParams{
		SubagentKey:   launch.Key,
		ProjectID:     projectID,
		ParentAgentID: &launch.ParentAgentID,
	})
	if err != nil {
		return fmt.Errorf("count active subagents: %w", err)
	}
	if launch.MaxInstances != nil && int(siblings.SameKey) >= *launch.MaxInstances {
		return resourceLimitExceeded(
			fmt.Sprintf("active subagents for key %q", launch.Key),
			int64(*launch.MaxInstances),
		)
	}
	if launch.MaxSubagents != nil && int(siblings.Total) >= *launch.MaxSubagents {
		return resourceLimitExceeded("active subagents", int64(*launch.MaxSubagents))
	}
	return nil
}

func shareParentMachineBindingsTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, childAgentID uuid.UUID,
	rows []dbsqlc.ListParentMachineBindingsForSharingRow,
) ([]AgentMachineBindingRecord, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	bindings := make([]AgentMachineBindingRecord, 0, len(rows))
	for _, row := range rows {
		binding, err := insertAgentMachineBindingTx(ctx, qtx, insertAgentMachineBindingInput{
			ProjectID:             projectID,
			AgentID:               childAgentID,
			ProjectMachineGrantID: row.ProjectMachineGrantID,
			BindingKind:           MachineBindingKindExplicit,
			Description:           row.Description,
			Cwd:                   row.Cwd,
			EnvOverlay:            row.EnvOverlay,
			SecretEnvOverlay:      row.SecretEnvOverlay,
			Metadata:              json.RawMessage(`{"shared_from_parent":"true"}`),
		})
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, nil
}

func subagentStatusFromSQLC(row dbsqlc.ListChildAgentsRow) SubagentStatus {
	status := SubagentStatus{
		AgentID:           row.ID,
		Name:              row.Name,
		Key:               row.SubagentKey,
		LastActivityAt:    row.LastActivityAt,
		Archived:          row.State == string(AgentStateArchived),
		IsRunning:         row.IsRunning,
		HasOpenQuestion:   row.HasOpenQuestion,
		HasOpenPermission: row.HasOpenPermission,
	}
	status.State = agentActivityState(
		status.Archived,
		status.HasOpenQuestion,
		status.HasOpenPermission,
		status.IsRunning,
	)
	return status
}

func agentActivityState(archived, hasOpenQuestion, hasOpenPermission, isRunning bool) string {
	switch {
	case archived:
		return SubagentStateArchived
	case hasOpenQuestion, hasOpenPermission:
		return SubagentStateWaitingOnInteraction
	case isRunning:
		return SubagentStateRunning
	default:
		return SubagentStateIdle
	}
}

func listChildAgentsTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, parentAgentID uuid.UUID,
	includeArchived bool,
) ([]SubagentStatus, error) {
	rows, err := qtx.ListChildAgents(ctx, dbsqlc.ListChildAgentsParams{
		ProjectID:       projectID,
		ParentAgentID:   &parentAgentID,
		IncludeArchived: includeArchived,
	})
	if err != nil {
		return nil, fmt.Errorf("list subagents: %w", err)
	}
	out := make([]SubagentStatus, 0, len(rows))
	for _, row := range rows {
		out = append(out, subagentStatusFromSQLC(row))
	}
	return out, nil
}

func (r *ToolCallReader) ListSubagents(ctx context.Context, includeArchived bool) ([]SubagentStatus, error) {
	return listChildAgentsTx(
		ctx, r.transaction.q, r.transaction.input.ProjectID, r.transaction.input.AgentID, includeArchived,
	)
}

func (r *ToolCallReader) ResolveSubagent(ctx context.Context, agentPublicID string) (SubagentStatus, error) {
	return resolveSubagentTx(
		ctx, r.transaction.q, r.transaction.input.ProjectID, r.transaction.input.AgentID, agentPublicID,
	)
}

func resolveSubagentTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, parentAgentID uuid.UUID,
	agentPublicID string,
) (SubagentStatus, error) {
	agentID, err := publicid.Decode(publicid.KindAgent, strings.TrimSpace(agentPublicID))
	if err != nil {
		return SubagentStatus{}, storeerr.InvalidRequest(fmt.Errorf("agent_id: %w", err))
	}
	children, err := listChildAgentsTx(ctx, qtx, projectID, parentAgentID, true)
	if err != nil {
		return SubagentStatus{}, err
	}
	for _, child := range children {
		if child.AgentID == agentID {
			return child, nil
		}
	}
	return SubagentStatus{}, storeerr.InvalidRequest(
		fmt.Errorf("no subagent matches agent_id %q", agentPublicID),
	)
}

func textInputContentBlocks(text string) ([]CreateContentBlockInput, json.RawMessage, error) {
	contentBlocks := []CreateContentBlockInput{{
		Ordinal:     0,
		BlockKind:   ContentBlockKindText,
		TextContent: text,
	}}
	contentBlocksJSON, err := marshalAgentInputContentBlocks(contentBlocks)
	if err != nil {
		return nil, nil, err
	}
	return contentBlocks, contentBlocksJSON, nil
}

type ListAgentInteractionsInput struct {
	ProjectID uuid.UUID
	AgentIDs  []uuid.UUID
	State     AgentInteractionState
	Limit     int
	After     listing.KeysetCursor
}

type AgentTreeInteraction struct {
	AgentInteractionRecord
	AgentName   string
	SubagentKey string
}

type ListAgentInteractionsResult struct {
	Interactions []AgentTreeInteraction
	HasMore      bool
}

func (s *Store) ListAgentInteractions(
	ctx context.Context,
	input ListAgentInteractionsInput,
) (ListAgentInteractionsResult, error) {
	if input.ProjectID == uuid.Nil || len(input.AgentIDs) == 0 {
		return ListAgentInteractionsResult{}, errors.New("project and at least one agent are required")
	}
	if input.Limit <= 0 {
		return ListAgentInteractionsResult{}, errors.New("limit must be positive")
	}
	params := dbsqlc.ListAgentInteractionsForAgentsParams{
		ProjectID: input.ProjectID,
		AgentIds:  input.AgentIDs,
		State:     string(input.State),
		RowLimit:  int64(input.Limit) + 1,
	}
	if input.After.Set {
		createdAt := input.After.CreatedAt
		id := input.After.ID
		params.CursorCreatedAt = &createdAt
		params.CursorID = &id
	}
	rows, err := s.q.ListAgentInteractionsForAgents(ctx, params)
	if err != nil {
		return ListAgentInteractionsResult{}, fmt.Errorf("list agent interactions: %w", err)
	}
	result := ListAgentInteractionsResult{}
	if len(rows) > input.Limit {
		result.HasMore = true
		rows = rows[:input.Limit]
	}
	result.Interactions = make([]AgentTreeInteraction, 0, len(rows))
	for _, row := range rows {
		result.Interactions = append(result.Interactions, AgentTreeInteraction{
			AgentInteractionRecord: agentInteractionRecordFromSQLC(dbsqlc.AgentInteractionReadProjection{
				ID:                  row.ID,
				ProjectID:           row.ProjectID,
				AgentID:             row.AgentID,
				TurnID:              row.TurnID,
				ModelCallContextID:  row.ModelCallContextID,
				ToolCallID:          row.ToolCallID,
				ProviderCallID:      row.ProviderCallID,
				InteractionKind:     row.InteractionKind,
				State:               row.State,
				Request:             row.Request,
				Resolution:          row.Resolution,
				ResolvedByInputID:   row.ResolvedByInputID,
				CreatedAt:           row.CreatedAt,
				ResolvedAt:          row.ResolvedAt,
				Destination:         row.Destination,
				PresentationReceipt: row.PresentationReceipt,
			}),
			AgentName:   row.AgentName,
			SubagentKey: row.SubagentKey,
		})
	}
	return result, nil
}

type SendSubagentMessageInput struct {
	TargetAgentID uuid.UUID
	Message       string
}

func SendSubagentMessageForToolCall(
	input SendSubagentMessageInput,
	completion ToolCallCompletionInput,
) ToolCallCommand {
	return toolCallCommandFunc(func(ctx context.Context, tx *toolCallTransaction) (any, error) {
		if err := tx.sendSubagentMessage(ctx, input); err != nil {
			return nil, err
		}
		if _, err := tx.completeToolCall(ctx, completion); err != nil {
			return nil, err
		}
		return nil, nil
	})
}

func (t *toolCallTransaction) sendSubagentMessage(ctx context.Context, input SendSubagentMessageInput) error {
	if err := t.unit.LockAgentRefs(ctx, []lifecyclelock.AgentRef{
		{ProjectID: t.input.ProjectID, AgentID: t.input.AgentID},
		{ProjectID: t.input.ProjectID, AgentID: input.TargetAgentID},
	},
		agentexecution.RuntimeAuthority{AgentID: t.input.AgentID,
			RuntimeLockID: t.input.RuntimeLockID}); err != nil {
		return err
	}
	if err := t.lockForMutation(ctx); err != nil {
		return err
	}
	parent, err := loadAgentInProjectTx(ctx, t.tx, t.input.ProjectID, t.input.AgentID)
	if err != nil {
		return err
	}
	child, err := loadAgentInProjectTx(ctx, t.tx, t.input.ProjectID, input.TargetAgentID)
	if err != nil {
		return err
	}
	if child.ParentAgentID != parent.ID {
		return storeerr.InvalidRequest(errors.New("target agent is not a subagent of this agent"))
	}
	if child.State != AgentStateActive {
		return storeerr.InvalidRequest(errors.New("subagent is archived"))
	}
	actor, err := SubagentActorParams(parent.OrgID, parent)
	if err != nil {
		return err
	}
	contentBlocks, contentBlocksJSON, err := textInputContentBlocks(input.Message)
	if err != nil {
		return err
	}
	parentPublicID, err := publicid.Encode(publicid.KindAgent, parent.ID)
	if err != nil {
		return fmt.Errorf("encode parent agent id: %w", err)
	}
	metadata, err := marshalJSON(map[string]any{"parent_message": map[string]any{"agent_id": parentPublicID}})
	if err != nil {
		return fmt.Errorf("marshal parent message metadata: %w", err)
	}
	if _, err := createAgentContentInputTx(ctx, t.unit, t.q, CreateAgentContentInputInput{
		ProjectID:              child.ProjectID,
		AgentID:                child.ID,
		Actor:                  actor,
		ContentBlocks:          contentBlocksJSON,
		Metadata:               metadata,
		DeliveryMode:           DeliveryModeSteering,
		CancelOpenInteractions: true,
		IdempotencyScope:       subagentMessageIdempotencyScope,
		IdempotencyKey:         "tool_call:" + t.input.ToolCallID.String(),
	}, contentBlocks); err != nil {
		return fmt.Errorf("deliver message to subagent: %w", err)
	}
	return nil
}

func LaunchSubagentForToolCall(
	input LaunchAgentInput,
	completion ToolCallCompletionBuilder[LaunchAgentResult],
) ToolCallCommand {
	return toolCallCommandFunc(func(ctx context.Context, tx *toolCallTransaction) (any, error) {
		if completion == nil {
			return nil, errors.New("subagent launch completion builder is required")
		}
		input, err := validateLaunchAgentInput(input)
		if err != nil {
			return nil, err
		}
		if input.Subagent == nil || input.Subagent.ParentAgentID != tx.input.AgentID {
			return nil, errors.New("subagent launch must name the calling agent as parent")
		}
		result, err := tx.store.launchAgentTx(ctx, tx.unit, tx.q, input)
		if err != nil {
			return nil, err
		}
		if err := tx.lockForMutation(ctx); err != nil {
			return nil, err
		}
		toolCompletion, err := completion(result)
		if err != nil {
			return nil, err
		}
		if _, err := tx.completeToolCall(ctx, toolCompletion); err != nil {
			return nil, err
		}
		return result, nil
	})
}

type StopSubagentInput struct {
	TargetAgentID uuid.UUID
	Archive       bool
}

func StopSubagentForToolCall(
	input StopSubagentInput,
	completion ToolCallCompletionInput,
) ToolCallCommand {
	return toolCallCommandFunc(func(ctx context.Context, tx *toolCallTransaction) (any, error) {
		if err := enterActiveAgentProjectTx(ctx, tx.tx, tx.q, tx.input.ProjectID); err != nil {
			return nil, err
		}
		child, err := loadAgentInProjectTx(ctx, tx.tx, tx.input.ProjectID, input.TargetAgentID)
		if err != nil {
			return nil, err
		}
		if child.ParentAgentID != tx.input.AgentID {
			return nil, storeerr.InvalidRequest(errors.New("target agent is not a subagent of this agent"))
		}
		var machines []MachineRecord
		if input.Archive {
			machines, err = archiveAgentTreeTx(
				ctx,
				tx.unit,
				tx.q,
				child.ProjectID,
				child.ID,
				nil,
			)
			if err != nil {
				return nil, err
			}
		} else if err := tx.cancelSubagent(ctx, child); err != nil {
			return nil, err
		}
		if err := tx.lockForMutation(ctx); err != nil {
			return nil, err
		}
		if _, err := tx.completeToolCall(ctx, completion); err != nil {
			return nil, err
		}
		return machines, nil
	})
}

func (t *toolCallTransaction) cancelSubagent(ctx context.Context, child AgentRecord) error {
	if child.State != AgentStateActive {
		return storeerr.InvalidRequest(errors.New("subagent is archived"))
	}
	if err := t.unit.LockAgentRefs(ctx, []lifecyclelock.AgentRef{
		{ProjectID: child.ProjectID, AgentID: child.ID},
		{ProjectID: t.input.ProjectID, AgentID: t.input.AgentID},
	},
		agentexecution.RuntimeAuthority{AgentID: t.input.AgentID,
			RuntimeLockID: t.input.RuntimeLockID}); err != nil {
		return err
	}
	parent, err := loadAgentInProjectTx(ctx, t.tx, t.input.ProjectID, t.input.AgentID)
	if err != nil {
		return err
	}
	actor, err := SubagentActorParams(parent.OrgID, parent)
	if err != nil {
		return err
	}
	actorID, err := resolveActorTx(ctx, t.q, child.ProjectID, actor)
	if err != nil {
		return err
	}
	h, err := t.unit.Handle(child.ProjectID, child.ID)
	if err != nil {
		return err
	}
	_, err = h.Cancel(ctx, agentexecution.CancelInput{ActorID: actorID, Reason: cancelReasonAgentCanceled,
		Message: "The model call was canceled by the parent agent."})
	return err
}

type idleArchiveCandidate struct {
	ProjectID uuid.UUID
	ID        uuid.UUID
}

func (s *Store) ArchiveIdleAgents(ctx context.Context, limit int) ([]MachineRecord, int, error) {
	return s.archiveIdleAgents(ctx, nil, limit)
}

func (s *Store) archiveIdleAgents(
	ctx context.Context,
	asOf *time.Time,
	limit int,
) ([]MachineRecord, int, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.q.ListIdleAgentsForArchive(
		ctx, dbsqlc.ListIdleAgentsForArchiveParams{AsOf: asOf, RowLimit: int32(limit)},
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list idle agents: %w", err)
	}
	candidates := make([]idleArchiveCandidate, 0, len(rows))
	for _, row := range rows {
		candidates = append(candidates, idleArchiveCandidate{ProjectID: row.ProjectID, ID: row.ID})
	}
	return s.archiveIdleCandidates(ctx, candidates, asOf)
}

var errIdleArchiveNoLongerEligible = errors.New("agent is no longer idle")

func (s *Store) archiveIdleCandidates(
	ctx context.Context,
	candidates []idleArchiveCandidate,
	asOf *time.Time,
) ([]MachineRecord, int, error) {
	var machines []MachineRecord
	archived := 0
	for _, candidate := range candidates {
		released, err := storeutil.RetryTransaction(
			ctx,
			"archive_idle_agent",
			func() ([]MachineRecord, error) {
				return s.archiveIdleCandidateOnce(ctx, candidate, asOf)
			},
		)
		if err != nil {
			if errors.Is(err, storeerr.ErrNotFound) || errors.Is(err, errIdleArchiveNoLongerEligible) {
				continue
			}
			return machines, archived, err
		}
		machines = append(machines, released...)
		archived++
	}
	return machines, archived, nil
}

func (s *Store) archiveIdleCandidateOnce(
	ctx context.Context,
	candidate idleArchiveCandidate,
	asOf *time.Time,
) ([]MachineRecord, error) {
	unit, err := s.cell.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin archive idle agent: %w", err)
	}
	defer func() { _ = unit.Rollback(ctx) }()
	tx := unit.DB()

	qtx := dbsqlc.New(tx)
	if err := enterActiveAgentProjectTx(ctx, tx, qtx, candidate.ProjectID); err != nil {
		return nil, err
	}
	locked, err := lockAgentTreeTx(ctx, unit, qtx, candidate.ProjectID, candidate.ID)
	if err != nil {
		return nil, err
	}
	idle, err := qtx.AgentIdleForArchive(ctx, dbsqlc.AgentIdleForArchiveParams{
		ProjectID: candidate.ProjectID,
		AgentID:   candidate.ID,
		AsOf:      asOf,
	})
	if err != nil {
		return nil, fmt.Errorf("recheck idle agent for archive: %w", err)
	}
	if !idle {
		return nil, errIdleArchiveNoLongerEligible
	}
	released, err := archiveLockedAgentTreeTx(ctx, unit, qtx, locked, nil)
	if err != nil {
		return nil, err
	}
	if err := unit.Commit(ctx, "archive idle agent"); err != nil {
		return nil, err
	}
	return released, nil
}

func (r *ToolCallReader) ReadSubagentTurns(
	ctx context.Context,
	agentPublicID string,
	beforeTurnSequence int64,
	limit int32,
) (SubagentStatus, []AgentTurnReadRecord, error) {
	status, err := r.ResolveSubagent(ctx, agentPublicID)
	if err != nil {
		return SubagentStatus{}, nil, err
	}
	turns, err := listAgentTurnsForReadTx(
		ctx, r.transaction.q, r.transaction.input.ProjectID, status.AgentID, beforeTurnSequence, limit,
	)
	if err != nil {
		return SubagentStatus{}, nil, err
	}
	return status, turns, nil
}

func (r *ToolCallReader) ReadSubagentTurnEvents(
	ctx context.Context,
	agentPublicID string,
	turnID uuid.UUID,
	beforeSequence int64,
	limit int32,
) (SubagentStatus, []AgentEventReadRecord, error) {
	status, err := r.ResolveSubagent(ctx, agentPublicID)
	if err != nil {
		return SubagentStatus{}, nil, err
	}
	projectID := r.transaction.input.ProjectID
	owned, err := r.transaction.q.AgentTurnExistsInProject(
		ctx, dbsqlc.AgentTurnExistsInProjectParams{ProjectID: projectID, AgentID: status.AgentID, ID: turnID},
	)
	if err != nil {
		return SubagentStatus{}, nil, fmt.Errorf("check subagent turn ownership: %w", err)
	}
	if !owned {
		return SubagentStatus{}, nil, storeerr.InvalidRequest(
			errors.New("no turn with that turn_id belongs to the subagent"),
		)
	}
	events, err := listTurnEventsForReadTx(
		ctx,
		r.transaction.q,
		projectID,
		status.AgentID,
		turnID,
		beforeSequence,
		limit,
	)
	if err != nil {
		return SubagentStatus{}, nil, err
	}
	return status, events, nil
}

func (s *Store) ListSubagents(
	ctx context.Context,
	projectID, parentAgentID uuid.UUID,
) ([]SubagentStatus, error) {
	if projectID == uuid.Nil || parentAgentID == uuid.Nil {
		return nil, errors.New("project and parent agent are required")
	}
	return listChildAgentsTx(ctx, s.q, projectID, parentAgentID, true)
}

func (s *Store) ListAgentDescendantIDs(
	ctx context.Context,
	projectID, agentID uuid.UUID,
) ([]uuid.UUID, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil {
		return nil, errors.New("project and agent are required")
	}
	rows, err := s.q.ListAgentDescendantIDs(
		ctx, dbsqlc.ListAgentDescendantIDsParams{ProjectID: projectID, AgentID: &agentID},
	)
	if err != nil {
		return nil, fmt.Errorf("list agent descendants: %w", err)
	}
	return rows, nil
}
