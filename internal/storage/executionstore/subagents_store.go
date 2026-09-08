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
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

const (
	MaxSubagentDepth = 8

	subagentMessageIdempotencyScope = "subagent_message"

	SubagentMessageKindResult          = "result"
	SubagentMessageKindFailed          = "failed"
	SubagentMessageKindQuestion        = "question"
	SubagentMessageKindCanceled        = "canceled"
	SubagentMessageKindArchived        = "archived"
	SubagentMessageKindWaitingOnParent = "waiting_on_parent"
	SubagentMessageKindWaitingOnHuman  = "waiting_on_human"
	SubagentMessageKindTimeout         = "timeout"

	SubagentStateRunning         = "running"
	SubagentStateIdle            = "idle"
	SubagentStateWaitingOnParent = "waiting_on_parent"
	SubagentStateWaitingOnHuman  = "waiting_on_human"
	SubagentStateArchived        = "archived"

	AgentWaitModeAll = "all"
	AgentWaitModeAny = "any"
)

type SubagentLaunch struct {
	ParentAgentID           ID
	Key                     string
	MaxConcurrent           *int
	MaxSubagents            *int
	ShareParentMachines     bool
	ArchiveAfterIdleMinutes *int
}

type SubagentStatus struct {
	AgentID           ID
	Name              string
	Key               string
	State             string
	LastActivityAt    time.Time
	Archived          bool
	IsRunning         bool
	HasOpenQuestion   bool
	HasOpenPermission bool
	HasModelOutput    bool
}

type AgentWaitRecord struct {
	ProjectID  ID
	AgentID    ID
	ToolCallID ID
	Mode       string
}

type AgentWaitTargetOutcome struct {
	AgentID    ID     `json:"-"`
	PublicID   string `json:"agent_id"`
	Name       string `json:"name,omitempty"`
	Key        string `json:"key"`
	State      string `json:"state"`
	ResultKind string `json:"result_kind"`
	Result     string `json:"result,omitempty"`
}

type AgentWaitOutcome struct {
	Agents   []AgentWaitTargetOutcome `json:"agents"`
	TimedOut bool                     `json:"timed_out"`
}

type subagentMessage struct {
	Kind           string
	WaitKind       string
	WaitOnly       bool
	Text           string
	InteractionID  ID
	IdempotencyKey string
}

func (message subagentMessage) waitResultKind() string {
	if message.WaitKind != "" {
		return message.WaitKind
	}
	return message.Kind
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

func prepareSubagentLaunchTx(
	ctx context.Context,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	projectID ID,
	name *string,
	launch SubagentLaunch,
) (AgentRecord, error) {
	if isNilID(launch.ParentAgentID) || launch.Key == "" {
		return AgentRecord{}, errors.New("subagent launch requires a parent agent and key")
	}
	if _, err := qtx.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: projectID, ID: launch.ParentAgentID},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AgentRecord{}, fmt.Errorf("parent agent: %w", storeerr.ErrNotFound)
		}
		return AgentRecord{}, fmt.Errorf("lock parent agent: %w", err)
	}
	parent, err := loadAgentInProjectTx(ctx, tx, projectID, launch.ParentAgentID)
	if err != nil {
		return AgentRecord{}, err
	}
	if parent.State != AgentStateActive {
		return AgentRecord{}, fmt.Errorf("parent agent is archived: %w", storeerr.ErrStateTransitionConflict)
	}
	depth, err := qtx.CountAgentAncestors(ctx, dbsqlc.CountAgentAncestorsParams{
		ProjectID: projectID,
		AgentID:   launch.ParentAgentID,
	})
	if err != nil {
		return AgentRecord{}, fmt.Errorf("count subagent ancestors: %w", err)
	}
	if int(depth)+1 >= MaxSubagentDepth {
		return AgentRecord{}, storeerr.InvalidRequest(
			fmt.Errorf("subagent depth limit of %d reached", MaxSubagentDepth),
		)
	}
	requestedName := ""
	if name != nil {
		requestedName = *name
	}
	siblings, err := qtx.CountActiveChildAgentsForLaunch(ctx, dbsqlc.CountActiveChildAgentsForLaunchParams{
		SubagentKey:   launch.Key,
		Name:          requestedName,
		ProjectID:     projectID,
		ParentAgentID: &launch.ParentAgentID,
	})
	if err != nil {
		return AgentRecord{}, fmt.Errorf("count active subagents: %w", err)
	}
	if launch.MaxConcurrent != nil && int(siblings.SameKey) >= *launch.MaxConcurrent {
		return AgentRecord{}, resourceLimitExceeded(
			fmt.Sprintf("active subagents for key %q", launch.Key),
			int64(*launch.MaxConcurrent),
		)
	}
	if launch.MaxSubagents != nil && int(siblings.Total) >= *launch.MaxSubagents {
		return AgentRecord{}, resourceLimitExceeded("active subagents", int64(*launch.MaxSubagents))
	}
	if requestedName != "" && siblings.NameExists {
		return AgentRecord{}, storeerr.InvalidRequest(
			fmt.Errorf("an active subagent named %q already exists", requestedName),
		)
	}
	return parent, nil
}

func shareParentMachineBindingsTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, parentAgentID, childAgentID ID,
) ([]AgentMachineBindingRecord, error) {
	rows, err := qtx.ListParentMachineBindingsForSharing(
		ctx,
		dbsqlc.ListParentMachineBindingsForSharingParams{ProjectID: projectID, AgentID: parentAgentID},
	)
	if err != nil {
		return nil, fmt.Errorf("list parent machine bindings: %w", err)
	}
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
		Name:              row.Name,
		Key:               row.SubagentKey,
		LastActivityAt:    row.LastActivityAt,
		Archived:          row.State == string(AgentStateArchived),
		IsRunning:         row.IsRunning,
		HasOpenQuestion:   row.HasOpenQuestion,
		HasOpenPermission: row.HasOpenPermission,
		HasModelOutput:    row.HasModelOutput,
	}
	switch {
	case status.Archived:
		status.State = SubagentStateArchived
	case status.HasOpenQuestion:
		status.State = SubagentStateWaitingOnParent
	case status.HasOpenPermission:
		status.State = SubagentStateWaitingOnHuman
	case status.IsRunning:
		status.State = SubagentStateRunning
	default:
		status.State = SubagentStateIdle
	}
	return status
}

func (status SubagentStatus) settled() bool {
	if status.Archived || status.HasOpenQuestion || status.HasOpenPermission {
		return true
	}
	return !status.IsRunning && status.HasModelOutput
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

// ResolveSubagentReference accepts a subagent public id or the name of one of
// the caller's active subagents.
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
	params := dbsqlc.ListChildAgentsParams{
		ProjectID:       projectID,
		ParentAgentID:   &parentAgentID,
		IncludeArchived: true,
	}
	if decoded, err := publicid.Decode(publicid.KindAgent, reference); err == nil {
		params.AgentID = &decoded
	} else {
		params.Name = reference
		params.IncludeArchived = false
	}
	rows, err := qtx.ListChildAgents(ctx, params)
	if err != nil {
		return SubagentStatus{}, fmt.Errorf("resolve subagent reference: %w", err)
	}
	switch len(rows) {
	case 0:
		return SubagentStatus{}, storeerr.InvalidRequest(fmt.Errorf("no subagent matches %q", reference))
	case 1:
		return subagentStatusFromSQLC(rows[0]), nil
	default:
		return SubagentStatus{}, storeerr.InvalidRequest(
			fmt.Errorf("%d subagents are named %q; address it by agent id instead", len(rows), reference),
		)
	}
}

func latestModelOutputTextTx(ctx context.Context, qtx *dbsqlc.Queries, projectID, agentID ID) (string, error) {
	text, err := qtx.LatestModelOutputTextForAgent(ctx, dbsqlc.LatestModelOutputTextForAgentParams{
		ProjectID: projectID,
		AgentID:   agentID,
	})
	if err != nil {
		return "", fmt.Errorf("load latest subagent output: %w", err)
	}
	return text, nil
}

func openInteractionTextTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID ID,
	kind AgentInteractionKind,
) (string, error) {
	row, err := qtx.GetOpenInteractionForAgentByKind(ctx, dbsqlc.GetOpenInteractionForAgentByKindParams{
		ProjectID:       projectID,
		AgentID:         agentID,
		InteractionKind: string(kind),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load open subagent %s: %w", kind, err)
	}
	if kind == AgentInteractionKindPermission {
		return renderPermissionForParent(row.ID, row.Request)
	}
	form, err := interactionform.Parse(row.Request)
	if err != nil {
		return "", err
	}
	return renderQuestionForParent(row.ID, form)
}

func renderPermissionForParent(interactionID ID, request json.RawMessage) (string, error) {
	parsed, err := toolpermission.ParseRequest(request)
	if err != nil {
		return "", err
	}
	interactionPublicID, err := publicid.Encode(publicid.KindAgentInteraction, interactionID)
	if err != nil {
		return "", fmt.Errorf("encode interaction id: %w", err)
	}
	return fmt.Sprintf(
		"Waiting for a human to approve tool %q (interaction_id %s). Only a person can resolve it from the console.",
		parsed.Authorization.ToolName,
		interactionPublicID,
	), nil
}

func renderQuestionForParent(interactionID ID, form interactionform.Form) (string, error) {
	interactionPublicID, err := publicid.Encode(publicid.KindAgentInteraction, interactionID)
	if err != nil {
		return "", fmt.Errorf("encode interaction id: %w", err)
	}
	var builder strings.Builder
	builder.WriteString("Question (interaction_id ")
	builder.WriteString(interactionPublicID)
	builder.WriteString("): ")
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
	return builder.String(), nil
}

func subagentTargetOutcomeTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID ID,
	status SubagentStatus,
) (string, string, error) {
	switch {
	case status.Archived:
		return SubagentMessageKindArchived, "", nil
	case status.HasOpenQuestion:
		text, err := openInteractionTextTx(ctx, qtx, projectID, status.AgentID, AgentInteractionKindQuestion)
		return SubagentMessageKindWaitingOnParent, text, err
	case status.HasOpenPermission:
		text, err := openInteractionTextTx(ctx, qtx, projectID, status.AgentID, AgentInteractionKindPermission)
		return SubagentMessageKindWaitingOnHuman, text, err
	default:
		text, err := latestModelOutputTextTx(ctx, qtx, projectID, status.AgentID)
		return SubagentMessageKindResult, text, err
	}
}

type CreateAgentWaitInput struct {
	TargetAgentIDs []ID
	Mode           string
	TimeoutSeconds *int
}

func CreateAgentWaitForToolCall(
	input CreateAgentWaitInput,
	completion ToolCallCompletionBuilder[AgentWaitOutcome],
) ToolCallCommand {
	return toolCallCommandFunc(func(ctx context.Context, tx *toolCallTransaction) (any, error) {
		if completion == nil {
			return nil, errors.New("agent wait completion builder is required")
		}
		outcome, waiting, err := tx.createAgentWait(ctx, input)
		if err != nil {
			return nil, err
		}
		if waiting {
			return nil, nil
		}
		toolCompletion, err := completion(outcome)
		if err != nil {
			return nil, err
		}
		if _, err := tx.completeToolCall(ctx, toolCompletion); err != nil {
			return nil, err
		}
		return outcome, nil
	})
}

func (t *toolCallTransaction) createAgentWait(
	ctx context.Context,
	input CreateAgentWaitInput,
) (AgentWaitOutcome, bool, error) {
	if input.Mode != AgentWaitModeAll && input.Mode != AgentWaitModeAny {
		return AgentWaitOutcome{}, false, storeerr.InvalidRequest(fmt.Errorf("unsupported wait mode %q", input.Mode))
	}
	existingTargets, err := t.q.CountAgentWaitTargets(ctx, dbsqlc.CountAgentWaitTargetsParams{
		AgentID:    t.input.AgentID,
		ToolCallID: t.input.ToolCallID,
	})
	if err != nil {
		return AgentWaitOutcome{}, false, fmt.Errorf("count agent wait targets: %w", err)
	}
	if existingTargets > 0 {
		t.hasDurableCompletionOwner = true
		if err := t.lockOrAcceptExisting(ctx); err != nil {
			return AgentWaitOutcome{}, false, err
		}
		return AgentWaitOutcome{}, true, nil
	}
	if err := t.lockForMutation(ctx); err != nil {
		return AgentWaitOutcome{}, false, err
	}
	children, err := listChildAgentsTx(ctx, t.q, t.input.ProjectID, t.input.AgentID, true)
	if err != nil {
		return AgentWaitOutcome{}, false, err
	}
	byID := make(map[ID]SubagentStatus, len(children))
	for _, child := range children {
		byID[child.AgentID] = child
	}
	targets := make([]SubagentStatus, 0, len(input.TargetAgentIDs))
	if len(input.TargetAgentIDs) == 0 {
		for _, child := range children {
			if !child.Archived {
				targets = append(targets, child)
			}
		}
	} else {
		for _, id := range input.TargetAgentIDs {
			child, ok := byID[id]
			if !ok {
				return AgentWaitOutcome{}, false, storeerr.InvalidRequest(
					fmt.Errorf("agent %s is not a subagent of this agent", id),
				)
			}
			targets = append(targets, child)
		}
	}
	if len(targets) == 0 {
		return AgentWaitOutcome{}, false, storeerr.InvalidRequest(errors.New("there are no running subagents to wait for"))
	}
	outcome := AgentWaitOutcome{Agents: make([]AgentWaitTargetOutcome, 0, len(targets))}
	settledCount := 0
	for _, target := range targets {
		entry, err := waitTargetOutcome(target)
		if err != nil {
			return AgentWaitOutcome{}, false, err
		}
		if target.settled() {
			kind, text, err := subagentTargetOutcomeTx(ctx, t.q, t.input.ProjectID, target)
			if err != nil {
				return AgentWaitOutcome{}, false, err
			}
			entry.ResultKind = kind
			entry.Result = text
			settledCount++
		}
		outcome.Agents = append(outcome.Agents, entry)
	}
	satisfied := (input.Mode == AgentWaitModeAny && settledCount > 0) ||
		(input.Mode == AgentWaitModeAll && settledCount == len(targets))
	if satisfied {
		return outcome, false, nil
	}
	for _, entry := range outcome.Agents {
		if err := t.q.InsertAgentWaitTarget(ctx, dbsqlc.InsertAgentWaitTargetParams{
			ProjectID:     t.input.ProjectID,
			AgentID:       t.input.AgentID,
			ToolCallID:    t.input.ToolCallID,
			TargetAgentID: entry.AgentID,
		}); err != nil {
			return AgentWaitOutcome{}, false, fmt.Errorf("create agent wait target: %w", err)
		}
		if entry.ResultKind == "" {
			continue
		}
		if _, err := t.q.MarkAgentWaitTargetDone(ctx, dbsqlc.MarkAgentWaitTargetDoneParams{
			ResultKind:    entry.ResultKind,
			ResultText:    entry.Result,
			AgentID:       t.input.AgentID,
			ToolCallID:    t.input.ToolCallID,
			TargetAgentID: entry.AgentID,
		}); err != nil {
			return AgentWaitOutcome{}, false, fmt.Errorf("record settled agent wait target: %w", err)
		}
	}
	t.hasDurableCompletionOwner = true
	if err := t.startToolCallWithTimeout(ctx, false, input.TimeoutSeconds); err != nil {
		return AgentWaitOutcome{}, false, err
	}
	return AgentWaitOutcome{}, true, nil
}

func waitTargetOutcome(status SubagentStatus) (AgentWaitTargetOutcome, error) {
	agentPublicID, err := publicid.Encode(publicid.KindAgent, status.AgentID)
	if err != nil {
		return AgentWaitTargetOutcome{}, fmt.Errorf("encode subagent id: %w", err)
	}
	return AgentWaitTargetOutcome{
		AgentID:  status.AgentID,
		PublicID: agentPublicID,
		Name:     status.Name,
		Key:      status.Key,
		State:    status.State,
	}, nil
}

func completeAgentWaitTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	wait AgentWaitRecord,
	timedOut bool,
) error {
	row, err := qtx.CompleteWaitingBuiltInToolCall(ctx, dbsqlc.CompleteWaitingBuiltInToolCallParams{
		ProjectID: wait.ProjectID,
		AgentID:   wait.AgentID,
		ID:        wait.ToolCallID,
		Outcome:   string(ToolResultOutcomeSucceeded),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("complete tool call from agent wait: %w", err)
	}
	targets, err := qtx.ListAgentWaitTargets(ctx, dbsqlc.ListAgentWaitTargetsParams{
		AgentID:    wait.AgentID,
		ToolCallID: wait.ToolCallID,
	})
	if err != nil {
		return fmt.Errorf("list agent wait targets: %w", err)
	}
	outcome := AgentWaitOutcome{TimedOut: timedOut, Agents: make([]AgentWaitTargetOutcome, 0, len(targets))}
	for _, target := range targets {
		agentPublicID, err := publicid.Encode(publicid.KindAgent, target.TargetAgentID)
		if err != nil {
			return fmt.Errorf("encode subagent id: %w", err)
		}
		state := subagentStateForResultKind(target.ResultKind)
		if target.AgentState == string(AgentStateArchived) {
			state = SubagentStateArchived
		}
		outcome.Agents = append(outcome.Agents, AgentWaitTargetOutcome{
			AgentID:    target.TargetAgentID,
			PublicID:   agentPublicID,
			Name:       target.Name,
			Key:        target.SubagentKey,
			State:      state,
			ResultKind: target.ResultKind,
			Result:     target.ResultText,
		})
	}
	result, err := marshalJSON(outcome)
	if err != nil {
		return fmt.Errorf("marshal agent wait result: %w", err)
	}
	contentParts, err := ToolResultContentParts(result)
	if err != nil {
		return err
	}
	_, err = finishCompletedToolCallTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		toolCallRecordFromWaitingCompleteSQLC(row),
		toolCallResultInput{Outcome: ToolResultOutcomeSucceeded, ResultContentParts: contentParts},
	)
	return err
}

func subagentStateForResultKind(kind string) string {
	switch kind {
	case SubagentMessageKindArchived:
		return SubagentStateArchived
	case SubagentMessageKindWaitingOnParent:
		return SubagentStateWaitingOnParent
	case SubagentMessageKindWaitingOnHuman:
		return SubagentStateWaitingOnHuman
	case SubagentMessageKindResult, SubagentMessageKindFailed, SubagentMessageKindCanceled:
		return SubagentStateIdle
	default:
		return SubagentStateRunning
	}
}

// handleSubagentMessageTx delivers a subagent's outcome to its parent. When
// the parent is parked in wait_agents on this subagent the outcome completes
// that wait; otherwise it arrives as a queued input from the subagent.
func handleSubagentMessageTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	child AgentRecord,
	message subagentMessage,
) error {
	if isNilID(child.ParentAgentID) {
		return nil
	}
	if _, err := qtx.LockAgentInProject(
		ctx, dbsqlc.LockAgentInProjectParams{ProjectID: child.ProjectID, ID: child.ParentAgentID},
	); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("lock parent agent: %w", err)
	}
	waits, err := qtx.ListOpenAgentWaitsForTarget(ctx, dbsqlc.ListOpenAgentWaitsForTargetParams{
		ProjectID:     child.ProjectID,
		TargetAgentID: child.ID,
	})
	if err != nil {
		return fmt.Errorf("list open agent waits: %w", err)
	}
	if len(waits) == 0 {
		if message.WaitOnly {
			return nil
		}
		return notifyParentAgentTx(ctx, txNotifications, tx, qtx, child, message)
	}
	for _, row := range waits {
		wait := AgentWaitRecord{
			ProjectID:  row.ProjectID,
			AgentID:    row.AgentID,
			ToolCallID: row.ToolCallID,
			Mode:       row.Mode,
		}
		if _, err := qtx.MarkAgentWaitTargetDone(ctx, dbsqlc.MarkAgentWaitTargetDoneParams{
			ResultKind:    message.waitResultKind(),
			ResultText:    message.Text,
			AgentID:       wait.AgentID,
			ToolCallID:    wait.ToolCallID,
			TargetAgentID: child.ID,
		}); err != nil {
			return fmt.Errorf("record agent wait target outcome: %w", err)
		}
		pending, err := qtx.CountPendingAgentWaitTargets(ctx, dbsqlc.CountPendingAgentWaitTargetsParams{
			AgentID:    wait.AgentID,
			ToolCallID: wait.ToolCallID,
		})
		if err != nil {
			return fmt.Errorf("count pending agent wait targets: %w", err)
		}
		if wait.Mode == AgentWaitModeAny || pending == 0 {
			if err := completeAgentWaitTx(ctx, txNotifications, tx, qtx, wait, false); err != nil {
				return err
			}
		}
	}
	return nil
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
		"kind":     message.Kind,
		"agent_id": childPublicID,
		"name":     child.Name,
		"key":      child.SubagentKey,
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
		DeliveryMode:     DeliveryModeQueued,
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
	label := fmt.Sprintf("Subagent %q (%s, key %q)", subagentDisplayName(child), childPublicID, child.SubagentKey)
	var header string
	switch message.Kind {
	case SubagentMessageKindResult:
		header = label + " finished its turn:"
	case SubagentMessageKindFailed:
		header = label + " failed:"
	case SubagentMessageKindQuestion:
		header = label + " asked a question. Answer it with send_agent_message using the interaction_id below."
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
	return handleSubagentMessageTx(ctx, txNotifications, tx, qtx, child, message)
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
	text, err := renderQuestionForParent(interaction.ID, form)
	if err != nil {
		return err
	}
	return handleSubagentMessageTx(ctx, txNotifications, tx, qtx, child, subagentMessage{
		Kind:           SubagentMessageKindQuestion,
		WaitKind:       SubagentMessageKindWaitingOnParent,
		Text:           text,
		InteractionID:  interaction.ID,
		IdempotencyKey: "question:" + interaction.ID.String(),
	})
}

// handleSubagentPermissionTx only settles a parent parked in wait_agents;
// a pending human permission is never announced to the parent model.
func handleSubagentPermissionTx(
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
	text, err := renderPermissionForParent(interaction.ID, interaction.Request)
	if err != nil {
		return err
	}
	return handleSubagentMessageTx(ctx, txNotifications, tx, qtx, child, subagentMessage{
		Kind:           SubagentMessageKindWaitingOnHuman,
		WaitOnly:       true,
		Text:           text,
		InteractionID:  interaction.ID,
		IdempotencyKey: "permission:" + interaction.ID.String(),
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
	InteractionID ID
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
	if !isNilID(input.InteractionID) {
		existing, err := t.q.GetAgentInteraction(ctx, dbsqlc.GetAgentInteractionParams{
			ProjectID: child.ProjectID,
			AgentID:   child.ID,
			ID:        input.InteractionID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return storeerr.InvalidRequest(errors.New("interaction not found on subagent"))
			}
			return fmt.Errorf("load subagent interaction: %w", err)
		}
		if existing.InteractionKind != string(AgentInteractionKindQuestion) {
			return storeerr.InvalidRequest(errors.New("interaction is not a question"))
		}
		if existing.State != string(AgentInteractionStateOpen) {
			return storeerr.InvalidRequest(errors.New("question is no longer open"))
		}
		form, err := interactionform.Parse(existing.Request)
		if err != nil {
			return err
		}
		resolution, err := freeTextResolution(form, input.Message)
		if err != nil {
			return err
		}
		if _, err := resolveAgentInteractionTx(ctx, t.notifications, t.tx, t.q, ResolveAgentInteractionInput{
			ProjectID:  child.ProjectID,
			AgentID:    child.ID,
			ID:         input.InteractionID,
			Resolution: resolution,
			Actor:      actor,
		}); err != nil {
			return err
		}
		return nil
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
		ProjectID:        child.ProjectID,
		AgentID:          child.ID,
		Actor:            actor,
		ContentBlocks:    contentBlocksJSON,
		Metadata:         metadata,
		DeliveryMode:     DeliveryModeQueued,
		IdempotencyScope: subagentMessageIdempotencyScope,
		IdempotencyKey:   "tool_call:" + t.input.ToolCallID.String(),
	}, contentBlocks); err != nil {
		return fmt.Errorf("deliver message to subagent: %w", err)
	}
	return nil
}

func freeTextResolution(form interactionform.Form, text string) (interactionform.Resolution, error) {
	resolution := interactionform.Resolution{Answers: make([]interactionform.Answer, 0, len(form.Questions))}
	for _, question := range form.Questions {
		textOption := -1
		for index, option := range question.Options {
			if option.AllowsText {
				textOption = index
			}
		}
		if textOption < 0 {
			return interactionform.Resolution{}, storeerr.InvalidRequest(
				errors.New("question does not accept a free-text answer"),
			)
		}
		resolution.Answers = append(resolution.Answers, interactionform.Answer{
			OptionIndices: []int{textOption},
			Text:          text,
		})
	}
	return interactionform.NormalizeResolution(form, resolution)
}

func StopSubagentForToolCall(
	targetAgentID ID,
	completion ToolCallCompletionInput,
) ToolCallCommand {
	return toolCallCommandFunc(func(ctx context.Context, tx *toolCallTransaction) (any, error) {
		if err := tx.lockForMutation(ctx); err != nil {
			return nil, err
		}
		child, err := loadAgentInProjectTx(ctx, tx.tx, tx.input.ProjectID, targetAgentID)
		if err != nil {
			return nil, err
		}
		if child.ParentAgentID != tx.input.AgentID {
			return nil, storeerr.InvalidRequest(errors.New("target agent is not a subagent of this agent"))
		}
		machines, err := archiveAgentTreeTx(ctx, tx.tx, tx.q, tx.notifications, child.ProjectID, child.ID, nil, false)
		if err != nil {
			return nil, err
		}
		if _, err := tx.completeToolCall(ctx, completion); err != nil {
			return nil, err
		}
		return machines, nil
	})
}

func timeOutAgentWaitTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	wait AgentWaitRecord,
) error {
	targets, err := qtx.ListAgentWaitTargets(ctx, dbsqlc.ListAgentWaitTargetsParams{
		AgentID:    wait.AgentID,
		ToolCallID: wait.ToolCallID,
	})
	if err != nil {
		return fmt.Errorf("list agent wait targets: %w", err)
	}
	for _, target := range targets {
		if target.State != "pending" {
			continue
		}
		if _, err := qtx.MarkAgentWaitTargetDone(ctx, dbsqlc.MarkAgentWaitTargetDoneParams{
			ResultKind:    SubagentMessageKindTimeout,
			ResultText:    "",
			AgentID:       wait.AgentID,
			ToolCallID:    wait.ToolCallID,
			TargetAgentID: target.TargetAgentID,
		}); err != nil {
			return fmt.Errorf("time out agent wait target: %w", err)
		}
	}
	return completeAgentWaitTx(ctx, txNotifications, tx, qtx, wait, true)
}

func (s *Store) ArchiveIdleSubagents(ctx context.Context, limit int) ([]MachineRecord, int, error) {
	return s.archiveIdleSubagents(ctx, nil, limit)
}

func (s *Store) archiveIdleSubagents(ctx context.Context, asOf *time.Time, limit int) ([]MachineRecord, int, error) {
	if limit <= 0 {
		limit = 50
	}
	candidates, err := s.q.ListIdleSubagentsForArchive(
		ctx, dbsqlc.ListIdleSubagentsForArchiveParams{AsOf: asOf, RowLimit: int32(limit)},
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list idle subagents: %w", err)
	}
	var machines []MachineRecord
	archived := 0
	for _, candidate := range candidates {
		txNotifications := s.newTxNotifications()
		tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return machines, archived, fmt.Errorf("begin archive idle subagent: %w", err)
		}
		released, err := archiveAgentTreeTx(
			ctx, tx, dbsqlc.New(tx), txNotifications, candidate.ProjectID, candidate.ID, nil, true,
		)
		if err != nil {
			_ = tx.Rollback(ctx)
			if errors.Is(err, storeerr.ErrNotFound) {
				continue
			}
			return machines, archived, err
		}
		if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "archive idle subagent"); err != nil {
			_ = tx.Rollback(ctx)
			return machines, archived, err
		}
		machines = append(machines, released...)
		archived++
	}
	return machines, archived, nil
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
