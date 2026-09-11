package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	MaxSubagentDepth = 8

	subagentMessageIdempotencyScope = "subagent_message"

	SubagentMessageKindResult   = "result"
	SubagentMessageKindFailed   = "failed"
	SubagentMessageKindQuestion = "question"
	SubagentMessageKindCanceled = "canceled"
	SubagentMessageKindArchived = "archived"

	SubagentStateRunning        = "running"
	SubagentStateIdle           = "idle"
	SubagentStateWaitingOnHuman = "waiting_on_human"
	SubagentStateArchived       = "archived"
)

type SubagentLaunch struct {
	ParentAgentID       ID
	Key                 string
	MaxConcurrent       *int
	MaxSubagents        *int
	ShareParentMachines bool
}

type SubagentStatus struct {
	AgentID           ID
	AgentRef          string
	Name              string
	Key               string
	State             string
	LastActivityAt    time.Time
	Archived          bool
	IsRunning         bool
	HasOpenQuestion   bool
	HasOpenPermission bool
}

type subagentMessage struct {
	Kind           string
	Text           string
	InteractionID  ID
	IdempotencyKey string
}

func SubagentActorParams(orgID ID, agent AgentRecord) (*ActorParams, error) {
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

func lockSubagentParentSourcesTx(ctx context.Context, tx pgx.Tx, launch SubagentLaunch) error {
	if isNilID(launch.ParentAgentID) || launch.Key == "" {
		return errors.New("subagent launch requires a parent agent and key")
	}
	if !launch.ShareParentMachines {
		return nil
	}
	return lifecyclelock.AgentSources(ctx, tx, launch.ParentAgentID)
}

func admitSubagentLaunchTx(
	ctx context.Context,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	orgID, projectID ID,
	launch SubagentLaunch,
) ([]dbsqlc.ListParentMachineBindingsForSharingRow, error) {
	var sharedBindings []dbsqlc.ListParentMachineBindingsForSharingRow
	if launch.ShareParentMachines {
		rows, err := qtx.ListParentMachineBindingsForSharing(
			ctx,
			dbsqlc.ListParentMachineBindingsForSharingParams{ProjectID: projectID, AgentID: launch.ParentAgentID},
		)
		if err != nil {
			return nil, fmt.Errorf("list parent machine bindings: %w", err)
		}
		machineRefs := make([]lifecyclelock.MachineRef, 0, len(rows))
		for _, row := range rows {
			machineRefs = append(machineRefs, lifecyclelock.MachineRef{OrgID: orgID, MachineID: row.MachineID})
		}
		if err := lifecyclelock.Machines(ctx, tx, machineRefs); err != nil {
			return nil, err
		}
		sharedBindings = rows
	}
	if err := lifecyclelock.Agents(ctx, tx, []lifecyclelock.AgentRef{{
		ProjectID: projectID,
		AgentID:   launch.ParentAgentID,
	}}); err != nil {
		if errors.Is(err, storeerr.ErrNotFound) {
			return nil, fmt.Errorf("parent agent: %w", storeerr.ErrNotFound)
		}
		return nil, err
	}
	parent, err := loadAgentInProjectTx(ctx, tx, projectID, launch.ParentAgentID)
	if err != nil {
		return nil, err
	}
	if parent.State != AgentStateActive {
		return nil, fmt.Errorf("parent agent is archived: %w", storeerr.ErrStateTransitionConflict)
	}
	depth, err := qtx.CountAgentAncestors(ctx, dbsqlc.CountAgentAncestorsParams{
		ProjectID: projectID,
		AgentID:   launch.ParentAgentID,
	})
	if err != nil {
		return nil, fmt.Errorf("count subagent ancestors: %w", err)
	}
	if int(depth)+1 >= MaxSubagentDepth {
		return nil, storeerr.InvalidRequest(
			fmt.Errorf("subagent depth limit of %d reached", MaxSubagentDepth),
		)
	}
	siblings, err := qtx.CountActiveChildAgentsForLaunch(ctx, dbsqlc.CountActiveChildAgentsForLaunchParams{
		SubagentKey:   launch.Key,
		ProjectID:     projectID,
		ParentAgentID: &launch.ParentAgentID,
	})
	if err != nil {
		return nil, fmt.Errorf("count active subagents: %w", err)
	}
	if launch.MaxConcurrent != nil && int(siblings.SameKey) >= *launch.MaxConcurrent {
		return nil, resourceLimitExceeded(
			fmt.Sprintf("active subagents for key %q", launch.Key),
			int64(*launch.MaxConcurrent),
		)
	}
	if launch.MaxSubagents != nil && int(siblings.Total) >= *launch.MaxSubagents {
		return nil, resourceLimitExceeded("active subagents", int64(*launch.MaxSubagents))
	}
	return sharedBindings, nil
}

func shareParentMachineBindingsTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, childAgentID ID,
	rows []dbsqlc.ListParentMachineBindingsForSharingRow,
) ([]AgentMachineBindingRecord, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	machineRefs, err := newMachineRefs(len(rows))
	if err != nil {
		return nil, err
	}
	bindings := make([]AgentMachineBindingRecord, 0, len(rows))
	for index, row := range rows {
		binding, err := insertAgentMachineBindingTx(ctx, qtx, insertAgentMachineBindingInput{
			ProjectID:             projectID,
			AgentID:               childAgentID,
			ProjectMachineGrantID: row.ProjectMachineGrantID,
			MachineRef:            machineRefs[index],
			BindingKind:           MachineBindingKindExplicit,
			Description:           row.Description,
			Cwd:                   row.Cwd,
			EnvOverlay:            row.EnvOverlay,
			SecretEnvOverlay:      row.SecretEnvOverlay,
			Metadata:              json.RawMessage(`{"shared_from_parent":true}`),
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
		AgentRef:          SubagentRef(row.ID),
		Name:              row.Name,
		Key:               row.SubagentKey,
		LastActivityAt:    row.LastActivityAt,
		Archived:          row.State == string(AgentStateArchived),
		IsRunning:         row.IsRunning,
		HasOpenQuestion:   row.HasOpenQuestion,
		HasOpenPermission: row.HasOpenPermission,
	}
	switch {
	case status.Archived:
		status.State = SubagentStateArchived
	case status.HasOpenQuestion, status.HasOpenPermission:
		status.State = SubagentStateWaitingOnHuman
	case status.IsRunning:
		status.State = SubagentStateRunning
	default:
		status.State = SubagentStateIdle
	}
	return status
}

func listChildAgentsTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, parentAgentID ID,
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

// ResolveSubagentReference accepts a subagent's agent_ref or its full public id.
func (r *ToolCallReader) ResolveSubagentReference(ctx context.Context, reference string) (SubagentStatus, error) {
	return resolveSubagentReferenceTx(
		ctx, r.transaction.q, r.transaction.input.ProjectID, r.transaction.input.AgentID, reference,
	)
}

func resolveSubagentReferenceTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, parentAgentID ID,
	reference string,
) (SubagentStatus, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return SubagentStatus{}, storeerr.InvalidRequest(errors.New("subagent reference is required"))
	}
	children, err := listChildAgentsTx(ctx, qtx, projectID, parentAgentID, true)
	if err != nil {
		return SubagentStatus{}, err
	}
	var matches []SubagentStatus
	for _, child := range children {
		if child.AgentRef == reference {
			matches = append(matches, child)
			continue
		}
		if publicID, err := publicid.Encode(publicid.KindAgent, child.AgentID); err == nil && publicID == reference {
			matches = append(matches, child)
		}
	}
	switch len(matches) {
	case 0:
		return SubagentStatus{}, storeerr.InvalidRequest(fmt.Errorf("no subagent matches agent_ref %q", reference))
	case 1:
		return matches[0], nil
	default:
		return SubagentStatus{}, storeerr.InvalidRequest(
			fmt.Errorf("%d subagents match %q; address it by full agent id instead", len(matches), reference),
		)
	}
}

// SubagentRef is the short form of an agent id that subagent tools accept:
// the trailing characters of the public id, which carry its random bits.
func SubagentRef(agentID ID) string {
	publicID, err := publicid.Encode(publicid.KindAgent, agentID)
	if err != nil {
		return ""
	}
	return "agtr-" + publicID[len(publicID)-subagentRefLength:]
}

const subagentRefLength = 8

func renderQuestionForParent(form interactionform.Form) string {
	var builder strings.Builder
	builder.WriteString("Question: ")
	builder.WriteString(form.Title)
	for _, item := range form.Context {
		builder.WriteString("\n")
		builder.WriteString(item.Label)
		builder.WriteString(": ")
		builder.WriteString(item.Value)
	}
	for index, question := range form.Questions {
		builder.WriteString(fmt.Sprintf("\n%d. %s", index+1, question.Prompt))
		for _, option := range question.Options {
			builder.WriteString("\n   - ")
			builder.WriteString(option.Label)
		}
	}
	return builder.String()
}

func lockAgentWithParentTx(ctx context.Context, tx pgx.Tx, qtx *dbsqlc.Queries, projectID, agentID ID) error {
	parentID, err := qtx.GetAgentParentID(ctx, dbsqlc.GetAgentParentIDParams{ProjectID: projectID, ID: agentID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrNotFound
		}
		return fmt.Errorf("load agent parent: %w", err)
	}
	refs := []lifecyclelock.AgentRef{{ProjectID: projectID, AgentID: agentID}}
	if parentID != nil {
		refs = append(refs, lifecyclelock.AgentRef{ProjectID: projectID, AgentID: *parentID})
	}
	return lifecyclelock.Agents(ctx, tx, refs)
}

func notifyParentAgentTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	child AgentRecord,
	message subagentMessage,
) error {
	parent, err := loadAgentInProjectTx(ctx, tx, child.ProjectID, child.ParentAgentID)
	if err != nil {
		return err
	}
	if parent.State == AgentStateArchived {
		return nil
	}
	actor, err := SubagentActorParams(child.OrgID, child)
	if err != nil {
		return err
	}
	childPublicID, err := publicid.Encode(publicid.KindAgent, child.ID)
	if err != nil {
		return fmt.Errorf("encode subagent id: %w", err)
	}
	metadataBody := map[string]any{
		"kind":      message.Kind,
		"agent_id":  childPublicID,
		"agent_ref": SubagentRef(child.ID),
		"name":      child.Name,
		"key":       child.SubagentKey,
	}
	if !isNilID(message.InteractionID) {
		interactionPublicID, err := publicid.Encode(publicid.KindAgentInteraction, message.InteractionID)
		if err != nil {
			return fmt.Errorf("encode interaction id: %w", err)
		}
		metadataBody["interaction_id"] = interactionPublicID
	}
	metadata, err := marshalJSON(map[string]any{"subagent_message": metadataBody})
	if err != nil {
		return fmt.Errorf("marshal subagent message metadata: %w", err)
	}
	contentBlocks, contentBlocksJSON, err := textInputContentBlocks(subagentMessageText(child, childPublicID, message))
	if err != nil {
		return err
	}
	if _, err := createAgentContentInputTx(ctx, txNotifications, tx, qtx, parent, CreateAgentContentInputInput{
		ProjectID:        parent.ProjectID,
		AgentID:          parent.ID,
		Actor:            actor,
		ContentBlocks:    contentBlocksJSON,
		Metadata:         metadata,
		DeliveryMode:     DeliveryModeSteering,
		IdempotencyScope: subagentMessageIdempotencyScope,
		IdempotencyKey:   message.IdempotencyKey,
	}, contentBlocks); err != nil {
		if errors.Is(err, storeerr.ErrStateTransitionConflict) {
			return nil
		}
		return fmt.Errorf("deliver subagent message to parent: %w", err)
	}
	return nil
}

func subagentMessageText(child AgentRecord, childPublicID string, message subagentMessage) string {
	label := fmt.Sprintf(
		"Subagent %q (agent_ref %s, key %q)", subagentDisplayName(child), SubagentRef(child.ID), child.SubagentKey,
	)
	var header string
	switch message.Kind {
	case SubagentMessageKindResult:
		header = label + " finished its turn:"
	case SubagentMessageKindFailed:
		header = label + " failed:"
	case SubagentMessageKindQuestion:
		header = label + " asked a question and is paused until a human answers it. " +
			"Messaging it with send_agent_message cancels the question."
	case SubagentMessageKindCanceled:
		header = label + " was canceled."
	case SubagentMessageKindArchived:
		header = label + " was archived."
	default:
		header = label + ":"
	}
	if strings.TrimSpace(message.Text) == "" {
		return header
	}
	return header + "\n\n" + message.Text
}

func handleSubagentTurnEndedTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	projectID, agentID ID,
	message subagentMessage,
) error {
	parentID, err := qtx.GetAgentParentID(ctx, dbsqlc.GetAgentParentIDParams{ProjectID: projectID, ID: agentID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrNotFound
		}
		return fmt.Errorf("load agent parent: %w", err)
	}
	if parentID == nil {
		return nil
	}
	child, err := loadAgentInProjectTx(ctx, tx, projectID, agentID)
	if err != nil {
		return err
	}
	return notifyParentAgentTx(ctx, txNotifications, tx, qtx, child, message)
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

func handleSubagentQuestionTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	interaction AgentInteractionRecord,
) error {
	child, err := loadAgentInProjectTx(ctx, tx, interaction.ProjectID, interaction.AgentID)
	if err != nil {
		return err
	}
	if isNilID(child.ParentAgentID) {
		return nil
	}
	form, err := interaction.Form()
	if err != nil {
		return err
	}
	return notifyParentAgentTx(ctx, txNotifications, tx, qtx, child, subagentMessage{
		Kind:           SubagentMessageKindQuestion,
		Text:           renderQuestionForParent(form),
		InteractionID:  interaction.ID,
		IdempotencyKey: "question:" + interaction.ID.String(),
	})
}

type ListAgentInteractionsInput struct {
	ProjectID ID
	AgentIDs  []ID
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
	if isNilID(input.ProjectID) || len(input.AgentIDs) == 0 {
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
				ID:                 row.ID,
				ProjectID:          row.ProjectID,
				AgentID:            row.AgentID,
				TurnID:             row.TurnID,
				ModelCallContextID: row.ModelCallContextID,
				ToolCallID:         row.ToolCallID,
				ProviderCallID:     row.ProviderCallID,
				InteractionKind:    row.InteractionKind,
				State:              row.State,
				Request:            row.Request,
				Resolution:         row.Resolution,
				ResolvedByInputID:  row.ResolvedByInputID,
				CreatedAt:          row.CreatedAt,
				ResolvedAt:         row.ResolvedAt,
			}),
			AgentName:   row.AgentName,
			SubagentKey: row.SubagentKey,
		})
	}
	return result, nil
}

type SendSubagentMessageInput struct {
	TargetAgentID ID
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
	if err := lifecyclelock.Agents(ctx, t.tx, []lifecyclelock.AgentRef{
		{ProjectID: t.input.ProjectID, AgentID: t.input.AgentID},
		{ProjectID: t.input.ProjectID, AgentID: input.TargetAgentID},
	}); err != nil {
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
	if _, err := createAgentContentInputTx(ctx, t.notifications, t.tx, t.q, child, CreateAgentContentInputInput{
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
		result, err := tx.store.launchAgentTx(ctx, tx.tx, tx.q, tx.notifications, input)
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

func StopSubagentForToolCall(
	targetAgentID ID,
	completion ToolCallCompletionInput,
) ToolCallCommand {
	return toolCallCommandFunc(func(ctx context.Context, tx *toolCallTransaction) (any, error) {
		child, err := loadAgentInProjectTx(ctx, tx.tx, tx.input.ProjectID, targetAgentID)
		if err != nil {
			return nil, err
		}
		if child.ParentAgentID != tx.input.AgentID {
			return nil, storeerr.InvalidRequest(errors.New("target agent is not a subagent of this agent"))
		}
		machines, err := archiveAgentTreeTx(ctx, tx.tx, tx.q, tx.notifications, child.ProjectID, child.ID, nil, "")
		if err != nil {
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

type idleArchiveCandidate struct {
	ProjectID ID
	ID        ID
}

func (s *Store) ArchiveIdleAgents(ctx context.Context, limit int) ([]MachineRecord, int, error) {
	return s.archiveIdleAgents(ctx, nil, limit)
}

func (s *Store) archiveIdleAgents(ctx context.Context, asOf *time.Time, limit int) ([]MachineRecord, int, error) {
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
		released, err := storeutil.RetryTransaction(ctx, "archive_idle_agent", func() ([]MachineRecord, error) {
			return s.archiveIdleCandidateOnce(ctx, candidate, asOf)
		})
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
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin archive idle agent: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	locked, err := lockAgentTreeTx(ctx, tx, qtx, candidate.ProjectID, candidate.ID)
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
	released, err := archiveLockedAgentTreeTx(
		ctx, tx, qtx, txNotifications, locked, nil, SubagentMessageKindArchived,
	)
	if err != nil {
		return nil, err
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "archive idle agent"); err != nil {
		return nil, err
	}
	return released, nil
}

// ReadSubagentEvents returns one page of a subagent's event log for its
// parent: oldest first after afterSequence or, when beforeSequence is set,
// newest first before that boundary (0 meaning the latest events).
func (r *ToolCallReader) ReadSubagentEvents(
	ctx context.Context,
	reference string,
	afterSequence, beforeSequence int64,
	limit int32,
) (SubagentStatus, []AgentEventReadRecord, error) {
	status, err := r.ResolveSubagentReference(ctx, reference)
	if err != nil {
		return SubagentStatus{}, nil, err
	}
	if limit <= 0 {
		limit = defaultAgentEventsReadLimit
	}
	if limit > maxAgentEventsReadLimit {
		limit = maxAgentEventsReadLimit
	}
	projectID := r.transaction.input.ProjectID
	var rows []dbsqlc.AgentEventReadProjection
	if beforeSequence >= 0 && afterSequence == 0 {
		rows, err = r.transaction.q.ListAgentEventsBeforeForRead(ctx, dbsqlc.ListAgentEventsBeforeForReadParams{
			ProjectID:      projectID,
			AgentID:        status.AgentID,
			BeforeSequence: beforeSequence,
			PageLimit:      limit,
		})
	} else {
		rows, err = r.transaction.q.ListAgentEventsForRead(ctx, dbsqlc.ListAgentEventsForReadParams{
			ProjectID:     projectID,
			AgentID:       status.AgentID,
			AfterSequence: afterSequence,
			PageLimit:     limit,
		})
	}
	if err != nil {
		return SubagentStatus{}, nil, fmt.Errorf("read subagent events: %w", err)
	}
	events := make([]AgentEventReadRecord, 0, len(rows))
	for _, row := range rows {
		events = append(events, agentEventReadRecordFromSQLC(row))
	}
	return status, events, nil
}

func (s *Store) ListSubagents(ctx context.Context, projectID, parentAgentID ID) ([]SubagentStatus, error) {
	if isNilID(projectID) || isNilID(parentAgentID) {
		return nil, errors.New("project and parent agent are required")
	}
	return listChildAgentsTx(ctx, s.q, projectID, parentAgentID, true)
}

func (s *Store) ListAgentDescendantIDs(ctx context.Context, projectID, agentID ID) ([]ID, error) {
	if isNilID(projectID) || isNilID(agentID) {
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
