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
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
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
	SubagentMessageKindTimeout         = "timeout"

	SubagentStateRunning         = "running"
	SubagentStateIdle            = "idle"
	SubagentStateWaitingOnParent = "waiting_on_parent"
	SubagentStateArchived        = "archived"

	AgentWaitModeAll = "all"
	AgentWaitModeAny = "any"
)

type SubagentLaunch struct {
	ParentAgentID           ID
	SpawnToolCallID         ID
	Handle                  string
	MaxConcurrent           *int
	MaxSubagents            *int
	ShareParentMachines     bool
	ArchiveAfterIdleMinutes *int
}

type SubagentStatus struct {
	AgentID         ID
	Name            string
	Handle          string
	State           string
	LastActivityAt  time.Time
	CreatedAt       time.Time
	Archived        bool
	IsRunning       bool
	HasOpenQuestion bool
	HasModelOutput  bool
}

type AgentWaitRecord struct {
	ID         ID
	OrgID      ID
	ProjectID  ID
	AgentID    ID
	ToolCallID ID
	Mode       string
	State      string
	DeadlineAt *time.Time
}

type AgentWaitTargetOutcome struct {
	AgentID    ID     `json:"-"`
	PublicID   string `json:"agent_id"`
	Name       string `json:"name,omitempty"`
	Handle     string `json:"handle"`
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
	Text           string
	InteractionID  ID
	IdempotencyKey string
}

type AgentModelUsageSummary struct {
	AgentCount              int
	ModelCallCount          int
	InputTokensTotal        int64
	UncachedInputTokens     int64
	CacheReadInputTokens    int64
	CacheWriteInputTokens   int64
	OutputTokensTotal       int64
	ReasoningOutputTokens   int64
	ProviderReportedCostUSD string
}

func agentWaitRecordFromSQLC(
	id, orgID, projectID, agentID, toolCallID ID,
	mode, state string,
	deadlineAt *time.Time,
) AgentWaitRecord {
	return AgentWaitRecord{
		ID:         id,
		OrgID:      orgID,
		ProjectID:  projectID,
		AgentID:    agentID,
		ToolCallID: toolCallID,
		Mode:       mode,
		State:      state,
		DeadlineAt: deadlineAt,
	}
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
	if agent.SubagentHandle != "" {
		return agent.SubagentHandle
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
	if isNilID(launch.ParentAgentID) || launch.Handle == "" {
		return AgentRecord{}, errors.New("subagent launch requires a parent agent and handle")
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
	if launch.MaxConcurrent != nil {
		count, err := qtx.CountActiveChildAgents(ctx, dbsqlc.CountActiveChildAgentsParams{
			ProjectID:      projectID,
			ParentAgentID:  &launch.ParentAgentID,
			SubagentHandle: launch.Handle,
		})
		if err != nil {
			return AgentRecord{}, fmt.Errorf("count active subagents for handle: %w", err)
		}
		if int(count) >= *launch.MaxConcurrent {
			return AgentRecord{}, resourceLimitExceeded(
				fmt.Sprintf("active subagents for handle %q", launch.Handle),
				int64(*launch.MaxConcurrent),
			)
		}
	}
	if launch.MaxSubagents != nil {
		count, err := qtx.CountActiveChildAgents(ctx, dbsqlc.CountActiveChildAgentsParams{
			ProjectID:     projectID,
			ParentAgentID: &launch.ParentAgentID,
		})
		if err != nil {
			return AgentRecord{}, fmt.Errorf("count active subagents: %w", err)
		}
		if int(count) >= *launch.MaxSubagents {
			return AgentRecord{}, resourceLimitExceeded("active subagents", int64(*launch.MaxSubagents))
		}
	}
	if name != nil && *name != "" {
		exists, err := qtx.ActiveChildAgentNameExists(ctx, dbsqlc.ActiveChildAgentNameExistsParams{
			ProjectID:     projectID,
			ParentAgentID: &launch.ParentAgentID,
			Name:          *name,
		})
		if err != nil {
			return AgentRecord{}, fmt.Errorf("check subagent name: %w", err)
		}
		if exists {
			return AgentRecord{}, storeerr.InvalidRequest(
				fmt.Errorf("an active subagent named %q already exists", *name),
			)
		}
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
		AgentID:         row.ID,
		Name:            row.Name,
		Handle:          row.SubagentHandle,
		LastActivityAt:  row.LastActivityAt,
		CreatedAt:       row.CreatedAt,
		Archived:        row.State == string(AgentStateArchived),
		IsRunning:       row.IsRunning,
		HasOpenQuestion: row.HasOpenQuestion,
		HasModelOutput:  row.HasModelOutput,
	}
	switch {
	case status.Archived:
		status.State = SubagentStateArchived
	case status.HasOpenQuestion:
		status.State = SubagentStateWaitingOnParent
	case status.IsRunning:
		status.State = SubagentStateRunning
	default:
		status.State = SubagentStateIdle
	}
	return status
}

func (status SubagentStatus) settled() bool {
	if status.Archived || status.HasOpenQuestion {
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
	row, err := qtx.LatestModelOutputTextForAgent(ctx, dbsqlc.LatestModelOutputTextForAgentParams{
		ProjectID: projectID,
		AgentID:   agentID,
	})
	if err != nil {
		return "", fmt.Errorf("load latest subagent output: %w", err)
	}
	return row.ResultText, nil
}

func openQuestionTextTx(ctx context.Context, qtx *dbsqlc.Queries, projectID, agentID ID) (string, ID, error) {
	row, err := qtx.GetOpenQuestionInteractionForAgent(ctx, dbsqlc.GetOpenQuestionInteractionForAgentParams{
		ProjectID: projectID,
		AgentID:   agentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", NilID, nil
	}
	if err != nil {
		return "", NilID, fmt.Errorf("load open subagent question: %w", err)
	}
	form, err := interactionform.Parse(row.Request)
	if err != nil {
		return "", NilID, err
	}
	return renderQuestionForParent(row.ID, form), row.ID, nil
}

func renderQuestionForParent(interactionID ID, form interactionform.Form) string {
	interactionPublicID, err := publicid.Encode(publicid.KindAgentInteraction, interactionID)
	if err != nil {
		interactionPublicID = interactionID.String()
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
	return builder.String()
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
		text, _, err := openQuestionTextTx(ctx, qtx, projectID, status.AgentID)
		return SubagentMessageKindWaitingOnParent, text, err
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
	_, err := t.q.GetAgentWaitByToolCall(ctx, dbsqlc.GetAgentWaitByToolCallParams{
		ProjectID:  t.input.ProjectID,
		AgentID:    t.input.AgentID,
		ToolCallID: t.input.ToolCallID,
	})
	if err == nil {
		t.hasDurableCompletionOwner = true
		if err := t.lockOrAcceptExisting(ctx); err != nil {
			return AgentWaitOutcome{}, false, err
		}
		return AgentWaitOutcome{}, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return AgentWaitOutcome{}, false, fmt.Errorf("load agent wait: %w", err)
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
	wait, err := t.q.InsertAgentWait(ctx, dbsqlc.InsertAgentWaitParams{
		ToolCallID:     t.input.ToolCallID,
		Mode:           input.Mode,
		TimeoutSeconds: sqlcInt32Ptr(input.TimeoutSeconds),
		ProjectID:      t.input.ProjectID,
		AgentID:        t.input.AgentID,
	})
	if err != nil {
		return AgentWaitOutcome{}, false, fmt.Errorf("create agent wait: %w", err)
	}
	for _, entry := range outcome.Agents {
		if err := t.q.InsertAgentWaitTarget(ctx, dbsqlc.InsertAgentWaitTargetParams{
			WaitID:        wait.ID,
			ProjectID:     t.input.ProjectID,
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
			WaitID:        wait.ID,
			TargetAgentID: entry.AgentID,
		}); err != nil {
			return AgentWaitOutcome{}, false, fmt.Errorf("record settled agent wait target: %w", err)
		}
	}
	t.hasDurableCompletionOwner = true
	if err := t.startToolCall(ctx, false); err != nil {
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
		Handle:   status.Handle,
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
	changed, err := qtx.CompleteAgentWait(ctx, dbsqlc.CompleteAgentWaitParams{
		State:     "completed",
		ProjectID: wait.ProjectID,
		ID:        wait.ID,
	})
	if err != nil {
		return fmt.Errorf("complete agent wait: %w", err)
	}
	if changed == 0 {
		return nil
	}
	targets, err := qtx.ListAgentWaitTargets(ctx, dbsqlc.ListAgentWaitTargetsParams{WaitID: wait.ID})
	if err != nil {
		return fmt.Errorf("list agent wait targets: %w", err)
	}
	outcome := AgentWaitOutcome{TimedOut: timedOut, Agents: make([]AgentWaitTargetOutcome, 0, len(targets))}
	for _, target := range targets {
		agentPublicID, err := publicid.Encode(publicid.KindAgent, target.TargetAgentID)
		if err != nil {
			return fmt.Errorf("encode subagent id: %w", err)
		}
		state := SubagentStateRunning
		switch target.ResultKind {
		case SubagentMessageKindArchived:
			state = SubagentStateArchived
		case SubagentMessageKindWaitingOnParent:
			state = SubagentStateWaitingOnParent
		case SubagentMessageKindResult, SubagentMessageKindFailed, SubagentMessageKindCanceled:
			state = SubagentStateIdle
		}
		if target.AgentState == string(AgentStateArchived) {
			state = SubagentStateArchived
		}
		outcome.Agents = append(outcome.Agents, AgentWaitTargetOutcome{
			AgentID:    target.TargetAgentID,
			PublicID:   agentPublicID,
			Name:       target.Name,
			Handle:     target.SubagentHandle,
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
	row, err := qtx.CompleteToolCallFromAgentWait(ctx, dbsqlc.CompleteToolCallFromAgentWaitParams{
		ProjectID: wait.ProjectID,
		AgentID:   wait.AgentID,
		WaitID:    wait.ID,
		Outcome:   string(ToolResultOutcomeSucceeded),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("complete tool call from agent wait: %w", err)
	}
	record := toolCallRecordFromSQLC(
		row.ID, row.ProjectID, row.AgentID, row.TurnID,
		row.SourceEventID, row.ModelCallContextID, row.ProviderCallID,
		row.Name, row.Input, row.Type,
		row.State, row.Outcome, row.RuntimeLockID,
		row.ResultContentParts, row.CreatedAt, nil,
	)
	record.ResultContentParts = contentParts
	if _, err := appendToolResultEventTx(ctx, txNotifications, tx, record); err != nil {
		return err
	}
	if err := qtx.MarkAgentWakeup(ctx, dbsqlc.MarkAgentWakeupParams{
		ProjectID: wait.ProjectID,
		AgentID:   wait.AgentID,
		Metadata:  []byte(`{"reason":"subagent_wait"}`),
	}); err != nil {
		return fmt.Errorf("mark agent wakeup after subagent wait: %w", err)
	}
	return nil
}

func cancelOpenAgentWaitsTx(ctx context.Context, qtx *dbsqlc.Queries, projectID, agentID ID) error {
	if _, err := qtx.CancelOpenAgentWaitsForAgent(ctx, dbsqlc.CancelOpenAgentWaitsForAgentParams{
		ProjectID: projectID,
		AgentID:   agentID,
	}); err != nil {
		return fmt.Errorf("cancel open agent waits: %w", err)
	}
	return nil
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
	waits, err := qtx.ListOpenAgentWaitsForTarget(ctx, dbsqlc.ListOpenAgentWaitsForTargetParams{
		ProjectID:     child.ProjectID,
		TargetAgentID: child.ID,
	})
	if err != nil {
		return fmt.Errorf("list open agent waits: %w", err)
	}
	if len(waits) == 0 {
		return notifyParentAgentTx(ctx, txNotifications, tx, qtx, child, message)
	}
	for _, row := range waits {
		wait := agentWaitRecordFromSQLC(
			row.ID, row.OrgID, row.ProjectID, row.AgentID, row.ToolCallID, row.Mode, row.State, row.DeadlineAt,
		)
		if _, err := qtx.MarkAgentWaitTargetDone(ctx, dbsqlc.MarkAgentWaitTargetDoneParams{
			ResultKind:    message.Kind,
			ResultText:    message.Text,
			WaitID:        wait.ID,
			TargetAgentID: child.ID,
		}); err != nil {
			return fmt.Errorf("record agent wait target outcome: %w", err)
		}
		pending, err := qtx.CountPendingAgentWaitTargets(ctx, dbsqlc.CountPendingAgentWaitTargetsParams{WaitID: wait.ID})
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
		"handle":   child.SubagentHandle,
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
	text := subagentMessageText(child, childPublicID, message)
	contentBlocksJSON, err := marshalJSON([]map[string]any{{"type": "text", "text": text}})
	if err != nil {
		return fmt.Errorf("marshal subagent message content: %w", err)
	}
	contentBlocks, err := parseAgentInputContentBlocks(contentBlocksJSON)
	if err != nil {
		return err
	}
	contentBlocksJSON, err = marshalAgentInputContentBlocks(contentBlocks)
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
	label := fmt.Sprintf("Subagent %q (%s, handle %q)", subagentDisplayName(child), childPublicID, child.SubagentHandle)
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
	child, err := loadAgentInProjectTx(ctx, tx, projectID, agentID)
	if err != nil {
		return err
	}
	if isNilID(child.ParentAgentID) {
		return nil
	}
	return handleSubagentMessageTx(ctx, txNotifications, tx, qtx, child, message)
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
	text := renderQuestionForParent(interaction.ID, form)
	waits, err := qtx.ListOpenAgentWaitsForTarget(ctx, dbsqlc.ListOpenAgentWaitsForTargetParams{
		ProjectID:     child.ProjectID,
		TargetAgentID: child.ID,
	})
	if err != nil {
		return fmt.Errorf("list open agent waits: %w", err)
	}
	message := subagentMessage{
		Kind:           SubagentMessageKindQuestion,
		Text:           text,
		InteractionID:  interaction.ID,
		IdempotencyKey: "question:" + interaction.ID.String(),
	}
	if len(waits) == 0 {
		return notifyParentAgentTx(ctx, txNotifications, tx, qtx, child, message)
	}
	message.Kind = SubagentMessageKindWaitingOnParent
	return handleSubagentMessageTx(ctx, txNotifications, tx, qtx, child, message)
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
	contentBlocksJSON, err := marshalJSON([]map[string]any{{"type": "text", "text": input.Message}})
	if err != nil {
		return fmt.Errorf("marshal subagent message content: %w", err)
	}
	contentBlocks, err := parseAgentInputContentBlocks(contentBlocksJSON)
	if err != nil {
		return err
	}
	contentBlocksJSON, err = marshalAgentInputContentBlocks(contentBlocks)
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

func (s *Store) ExpireAgentWaits(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, fmt.Errorf("begin expire agent waits: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	rows, err := qtx.ClaimExpiredAgentWaits(ctx, dbsqlc.ClaimExpiredAgentWaitsParams{RowLimit: int32(limit)})
	if err != nil {
		return 0, fmt.Errorf("claim expired agent waits: %w", err)
	}
	for _, row := range rows {
		wait := agentWaitRecordFromSQLC(
			row.ID, row.OrgID, row.ProjectID, row.AgentID, row.ToolCallID, row.Mode, row.State, row.DeadlineAt,
		)
		if _, err := qtx.LockAgentInProject(
			ctx, dbsqlc.LockAgentInProjectParams{ProjectID: wait.ProjectID, ID: wait.AgentID},
		); err != nil {
			return 0, fmt.Errorf("lock waiting agent: %w", err)
		}
		targets, err := qtx.ListAgentWaitTargets(ctx, dbsqlc.ListAgentWaitTargetsParams{WaitID: wait.ID})
		if err != nil {
			return 0, fmt.Errorf("list agent wait targets: %w", err)
		}
		for _, target := range targets {
			if target.State != "pending" {
				continue
			}
			if _, err := qtx.MarkAgentWaitTargetDone(ctx, dbsqlc.MarkAgentWaitTargetDoneParams{
				ResultKind:    SubagentMessageKindTimeout,
				ResultText:    "",
				WaitID:        wait.ID,
				TargetAgentID: target.TargetAgentID,
			}); err != nil {
				return 0, fmt.Errorf("time out agent wait target: %w", err)
			}
		}
		if err := completeAgentWaitTx(ctx, txNotifications, tx, qtx, wait, true); err != nil {
			return 0, err
		}
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "expire agent waits"); err != nil {
		return 0, err
	}
	return len(rows), nil
}

func (s *Store) ArchiveIdleSubagents(ctx context.Context, limit int) ([]MachineRecord, int, error) {
	if limit <= 0 {
		limit = 50
	}
	candidates, err := s.q.ListIdleSubagentsForArchive(
		ctx, dbsqlc.ListIdleSubagentsForArchiveParams{RowLimit: int32(limit)},
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

func (s *Store) SumAgentModelUsage(ctx context.Context, projectID ID, agentIDs []ID) (AgentModelUsageSummary, error) {
	if isNilID(projectID) {
		return AgentModelUsageSummary{}, errors.New("project is required")
	}
	if len(agentIDs) == 0 {
		return AgentModelUsageSummary{}, nil
	}
	row, err := s.q.SumAgentModelUsage(ctx, dbsqlc.SumAgentModelUsageParams{ProjectID: projectID, AgentIds: agentIDs})
	if err != nil {
		return AgentModelUsageSummary{}, fmt.Errorf("sum agent model usage: %w", err)
	}
	return AgentModelUsageSummary{
		AgentCount:              len(agentIDs),
		ModelCallCount:          int(row.ModelCallCount),
		InputTokensTotal:        row.InputTokensTotal,
		UncachedInputTokens:     row.UncachedInputTokens,
		CacheReadInputTokens:    row.CacheReadInputTokens,
		CacheWriteInputTokens:   row.CacheWriteInputTokens,
		OutputTokensTotal:       row.OutputTokensTotal,
		ReasoningOutputTokens:   row.ReasoningOutputTokens,
		ProviderReportedCostUSD: row.ProviderReportedCostUsd,
	}, nil
}
