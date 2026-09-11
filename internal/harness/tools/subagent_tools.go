package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/agentconfigcompile"
	"github.com/omnara-ai/omnara/internal/machinepool"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type spawnAgentRequest struct {
	Agent string `json:"agent"`
	Task  string `json:"task"`
	Name  string `json:"name,omitempty"`
}

type readAgentRequest struct {
	AgentRef       string `json:"agent_ref"`
	AfterSequence  *int64 `json:"after_sequence,omitempty"`
	BeforeSequence *int64 `json:"before_sequence,omitempty"`
	Limit          *int   `json:"limit,omitempty"`
}

type subagentEventSummary struct {
	Sequence   int64  `json:"sequence"`
	Kind       string `json:"kind"`
	CreatedAt  string `json:"created_at"`
	Text       string `json:"text,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Outcome    string `json:"outcome,omitempty"`
}

type sendAgentMessageRequest struct {
	AgentRef string `json:"agent_ref"`
	Message  string `json:"message"`
}

type stopAgentRequest struct {
	AgentRef string `json:"agent_ref"`
}

type subagentSummary struct {
	AgentID        string `json:"agent_id"`
	AgentRef       string `json:"agent_ref"`
	Name           string `json:"name,omitempty"`
	Key            string `json:"key"`
	State          string `json:"state"`
	LastActivityAt string `json:"last_activity_at"`
}

func decodeStrictToolRequest(toolName string, raw json.RawMessage, target any) error {
	if err := decodeSingleStrictJSON(raw, target, toolName+" request"); err != nil {
		return fmt.Errorf("parse %s request: %w", toolName, err)
	}
	return nil
}

func resolveSpawnAgentRequest(raw json.RawMessage) (spawnAgentRequest, error) {
	var input spawnAgentRequest
	if err := decodeStrictToolRequest("spawn_agent", raw, &input); err != nil {
		return spawnAgentRequest{}, err
	}
	input.Agent = strings.TrimSpace(input.Agent)
	input.Name = strings.TrimSpace(input.Name)
	if input.Agent == "" {
		return spawnAgentRequest{}, errors.New("spawn_agent agent is required")
	}
	if strings.TrimSpace(input.Task) == "" {
		return spawnAgentRequest{}, errors.New("spawn_agent task is required")
	}
	return input, nil
}

func resolveSendAgentMessageRequest(raw json.RawMessage) (sendAgentMessageRequest, error) {
	var input sendAgentMessageRequest
	err := decodeStrictToolRequest("send_agent_message", raw, &input)
	if err != nil {
		return sendAgentMessageRequest{}, err
	}
	input.AgentRef = strings.TrimSpace(input.AgentRef)
	if input.AgentRef == "" {
		return sendAgentMessageRequest{}, errors.New("send_agent_message agent_ref is required")
	}
	if strings.TrimSpace(input.Message) == "" {
		return sendAgentMessageRequest{}, errors.New("send_agent_message message is required")
	}
	return input, nil
}

func resolveStopAgentRequest(raw json.RawMessage) (stopAgentRequest, error) {
	var input stopAgentRequest
	if err := decodeStrictToolRequest("stop_agent", raw, &input); err != nil {
		return stopAgentRequest{}, err
	}
	input.AgentRef = strings.TrimSpace(input.AgentRef)
	if input.AgentRef == "" {
		return stopAgentRequest{}, errors.New("stop_agent agent_ref is required")
	}
	return input, nil
}

func validateSpawnAgentInput(raw json.RawMessage) error {
	_, err := resolveSpawnAgentRequest(raw)
	return err
}

func resolveReadAgentRequest(raw json.RawMessage) (readAgentRequest, error) {
	var input readAgentRequest
	if err := decodeStrictToolRequest("read_agent", raw, &input); err != nil {
		return readAgentRequest{}, err
	}
	input.AgentRef = strings.TrimSpace(input.AgentRef)
	if input.AgentRef == "" {
		return readAgentRequest{}, errors.New("read_agent agent_ref is required")
	}
	if input.AfterSequence != nil && *input.AfterSequence < 0 {
		return readAgentRequest{}, errors.New("read_agent after_sequence must be at least 0")
	}
	if input.BeforeSequence != nil && *input.BeforeSequence < 0 {
		return readAgentRequest{}, errors.New("read_agent before_sequence must be at least 0")
	}
	if input.Limit != nil && (*input.Limit < 1 || *input.Limit > 100) {
		return readAgentRequest{}, errors.New("read_agent limit must be between 1 and 100")
	}
	return input, nil
}

func validateReadAgentInput(raw json.RawMessage) error {
	_, err := resolveReadAgentRequest(raw)
	return err
}

func validateSendAgentMessageInput(raw json.RawMessage) error {
	_, err := resolveSendAgentMessageRequest(raw)
	return err
}

func validateStopAgentInput(raw json.RawMessage) error {
	_, err := resolveStopAgentRequest(raw)
	return err
}

func validateListAgentsInput(raw json.RawMessage) error {
	var input struct{}
	return decodeStrictToolRequest("list_agents", raw, &input)
}

func subagentStorageErrorIsToolFailure(cause error) bool {
	return errors.Is(cause, storeerr.ErrInvalidRequest) ||
		errors.Is(cause, storeerr.ErrNotFound) ||
		errors.Is(cause, storeerr.ErrConflict) ||
		errors.Is(cause, storeerr.ErrStateTransitionConflict)
}

func failSubagentTransactionForStorageError(code string, cause error) (transactionalPhaseResult, error) {
	if !subagentStorageErrorIsToolFailure(cause) {
		return nil, cause
	}
	content, err := toolFailureContent(code, cause.Error(), false)
	if err != nil {
		return nil, err
	}
	return failInTransaction(content, cause), nil
}

func failSubagentTransaction(code string, cause error) (transactionalPhaseResult, error) {
	content, err := toolFailureContent(code, cause.Error(), false)
	if err != nil {
		return nil, err
	}
	return failInTransaction(content, cause), nil
}

func subagentSummaryFromStatus(status executionstore.SubagentStatus) (subagentSummary, error) {
	agentPublicID, err := publicid.Encode(publicid.KindAgent, status.AgentID)
	if err != nil {
		return subagentSummary{}, fmt.Errorf("encode subagent id: %w", err)
	}
	return subagentSummary{
		AgentID:        agentPublicID,
		AgentRef:       status.AgentRef,
		Name:           status.Name,
		Key:            status.Key,
		State:          status.State,
		LastActivityAt: status.LastActivityAt.UTC().Format(time.RFC3339),
	}, nil
}

func spawnAgent(ctx context.Context, call transactionalToolContext) (transactionalPhaseResult, error) {
	input, err := resolveSpawnAgentRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	executor := call.Executor
	if executor.Store == nil {
		return nil, errors.New("tool executor store is required")
	}
	contract, err := executor.runtimeContractForTurn(ctx, call.Turn)
	if err != nil {
		return nil, err
	}
	subagent, ok := contract.Subagents[input.Agent]
	if !ok {
		return failSubagentTransaction("spawn_agent_failed", fmt.Errorf(
			"unknown subagent key %q; configured keys: %s",
			input.Agent,
			strings.Join(contract.SubagentKeys(), ", "),
		))
	}
	parent, err := executor.Store.Execution().GetAgentInProject(ctx, call.Turn.ProjectID, call.Turn.AgentID)
	if err != nil {
		return nil, err
	}
	launchConfig, err := executor.subagentLaunchConfig(ctx, call.Turn, parent, subagent)
	if err != nil {
		return failSubagentTransaction("spawn_agent_failed", err)
	}
	actor, err := executionstore.SubagentActorParams(parent.OrgID, parent)
	if err != nil {
		return nil, err
	}
	name := input.Name
	if name == "" {
		name = input.Agent
	}
	command := executionstore.LaunchSubagentForToolCall(
		executionstore.LaunchAgentInput{
			ProjectID:     parent.ProjectID,
			ProfileID:     launchConfig.profileID,
			AgentConfigID: launchConfig.configID,
			DerivedConfig: launchConfig.derived,
			LaunchedBy: identitystore.PrincipalRecord{
				Type: identitystore.PrincipalTypeSystem,
				ID:   parent.ID,
			},
			Name:                    &name,
			Message:                 input.Task,
			MessageActor:            actor,
			IdempotencyKey:          "spawn:" + call.ToolCallID.String(),
			ArchiveAfterIdleMinutes: subagent.ArchiveAfterIdleMinutes,
			Subagent: &executionstore.SubagentLaunch{
				ParentAgentID:       parent.ID,
				Key:                 input.Agent,
				MaxConcurrent:       subagent.MaxConcurrent,
				MaxSubagents:        contract.MaxSubagents,
				ShareParentMachines: subagent.Type == agentconfig.SubagentTypeSelf,
			},
		},
		func(launch executionstore.LaunchAgentResult) (executionstore.ToolCallCompletionInput, error) {
			content, err := spawnAgentResultContent(launch.Agent, input.Agent)
			if err != nil {
				return executionstore.ToolCallCompletionInput{}, err
			}
			return successfulToolCallCompletion(content)
		},
	)
	return executeInTransaction(command, func(err error) (transactionalPhaseResult, error) {
		if errors.Is(err, storeerr.ErrManagedWorkAdmissionDenied) {
			content, contentErr := toolFailureContent(
				storeerr.ManagedWorkAdmissionDeniedCode, storeerr.InsufficientOmnaraCreditsMessage, false,
			)
			if contentErr != nil {
				return nil, contentErr
			}
			return failInTransaction(content, err), nil
		}
		return failSubagentTransactionForStorageError("spawn_agent_failed", err)
	}), nil
}

func spawnAgentResultContent(child executionstore.AgentRecord, key string) (toolResultContent, error) {
	childPublicID, err := publicid.Encode(publicid.KindAgent, child.ID)
	if err != nil {
		return toolResultContent{}, fmt.Errorf("encode subagent id: %w", err)
	}
	return structuredToolResultContent(map[string]any{
		"agent_id":  childPublicID,
		"agent_ref": executionstore.SubagentRef(child.ID),
		"name":      child.Name,
		"key":       key,
		"state":     executionstore.SubagentStateRunning,
		"message": "Subagent started. Its final answer will arrive as a message from it; " +
			"use read_agent to check its progress.",
	})
}

func provisionSubagentMachinesInBackground(ctx context.Context, call backgroundToolContext) error {
	launch, ok := call.CommandResult.(executionstore.LaunchAgentResult)
	if !ok || len(launch.ProvisionMachineIDs) == 0 || call.Executor.MachinePoolManager == nil {
		return nil
	}
	call.Executor.MachinePoolManager.StartLaunchProvisioning(
		ctx, call.Executor.logger(), launch.Agent.OrgID, launch.ProvisionMachineIDs,
	)
	return nil
}

type subagentLaunchConfig struct {
	configID  storage.ID
	profileID storage.ID
	derived   *executionstore.CreateAgentConfigInput
}

// subagentLaunchConfig picks the config a spawned subagent launches from. A
// profile subagent with no overrides launches the profile's current config
// and stays linked to the profile; any other subagent launches an unlinked
// config derived from its base's compiled definition with the key's
// overrides applied, created inside the launch transaction.
func (e Executor) subagentLaunchConfig(
	ctx context.Context,
	turn Turn,
	parent executionstore.AgentRecord,
	subagent agentconfig.SubagentCompiled,
) (subagentLaunchConfig, error) {
	var baseConfig executionstore.AgentConfigRecord
	switch subagent.Type {
	case agentconfig.SubagentTypeSelf:
		contextRow, found, err := e.Store.Execution().GetModelCallContext(
			ctx, turn.ProjectID, turn.AgentID, turn.ModelCallContextID,
		)
		if err != nil {
			return subagentLaunchConfig{}, err
		}
		if !found {
			return subagentLaunchConfig{}, fmt.Errorf("model call context %s not found", turn.ModelCallContextID)
		}
		config, found, err := e.Store.Execution().GetAgentConfig(ctx, turn.ProjectID, contextRow.AgentConfigID)
		if err != nil {
			return subagentLaunchConfig{}, err
		}
		if !found {
			return subagentLaunchConfig{}, fmt.Errorf("agent config %s not found", contextRow.AgentConfigID)
		}
		baseConfig = config
	case agentconfig.SubagentTypeProfile:
		profileID, err := publicid.Decode(publicid.KindAgentProfile, subagent.ProfileID)
		if err != nil {
			return subagentLaunchConfig{}, fmt.Errorf("decode subagent profile id: %w", err)
		}
		profile, err := e.Store.Execution().GetAgentProfile(ctx, turn.ProjectID, profileID)
		if err != nil {
			if storeerr.IsNotFound(err) {
				return subagentLaunchConfig{}, fmt.Errorf("subagent profile %s no longer exists", subagent.ProfileID)
			}
			return subagentLaunchConfig{}, err
		}
		if subagent.Model == nil && subagent.InstructionAppend == "" {
			return subagentLaunchConfig{configID: profile.CurrentConfig.ID, profileID: profile.ID}, nil
		}
		baseConfig = profile.CurrentConfig
	default:
		return subagentLaunchConfig{}, fmt.Errorf("unsupported subagent type %q", subagent.Type)
	}
	body, err := agentconfigcompile.DeriveSubagentConfig(
		ctx,
		e.Store,
		parent.OrgID,
		parent.ProjectID,
		baseConfig,
		subagent,
	)
	if err != nil {
		return subagentLaunchConfig{}, fmt.Errorf("derive subagent config: %w", err)
	}
	derived := body.CreateInput(parent.ProjectID)
	return subagentLaunchConfig{derived: &derived}, nil
}

func readAgent(ctx context.Context, call transactionalToolContext) (transactionalPhaseResult, error) {
	input, err := resolveReadAgentRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	limit := int32(20)
	if input.Limit != nil {
		limit = int32(*input.Limit)
	}
	afterSequence := int64(0)
	if input.AfterSequence != nil {
		afterSequence = *input.AfterSequence
	}
	beforeSequence := int64(-1)
	if input.BeforeSequence != nil && input.AfterSequence == nil {
		beforeSequence = *input.BeforeSequence
	}
	status, events, err := call.Reader.ReadSubagentEvents(ctx, input.AgentRef, afterSequence, beforeSequence, limit)
	if err != nil {
		return failSubagentTransactionForStorageError("read_agent_failed", err)
	}
	summary, err := subagentSummaryFromStatus(status)
	if err != nil {
		return nil, err
	}
	entries := make([]subagentEventSummary, 0, len(events))
	for _, event := range events {
		entries = append(entries, subagentEventSummaryFromRecord(event))
	}
	content, err := structuredToolResultContent(map[string]any{
		"agent":  summary,
		"events": entries,
	})
	if err != nil {
		return nil, err
	}
	return completeInTransaction(content), nil
}

func subagentEventSummaryFromRecord(event executionstore.AgentEventReadRecord) subagentEventSummary {
	entry := subagentEventSummary{
		Sequence:   event.Sequence,
		Kind:       event.EventKind,
		CreatedAt:  event.CreatedAt.UTC().Format(time.RFC3339),
		Text:       agentEventText(event.ContentBlocks),
		StopReason: string(event.ModelStopReason),
		Outcome:    string(event.ToolOutcome),
	}
	if event.ToolCallID != storage.NilID {
		if toolCallID, err := publicid.Encode(publicid.KindToolCall, event.ToolCallID); err == nil {
			entry.ToolCallID = toolCallID
		}
	}
	if event.CheckpointSummary != "" && entry.Text == "" {
		entry.Text = event.CheckpointSummary
	}
	return entry
}

func agentEventText(contentBlocks json.RawMessage) string {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(contentBlocks, &blocks); err != nil {
		return ""
	}
	var out strings.Builder
	for _, block := range blocks {
		var piece string
		switch block.Type {
		case "text", "error":
			piece = block.Text
		case "tool_call":
			piece = "[tool_call " + block.Name + "]"
		default:
			continue
		}
		if piece == "" {
			continue
		}
		if out.Len() > 0 {
			out.WriteString("\n")
		}
		out.WriteString(piece)
	}
	return out.String()
}

func sendAgentMessage(ctx context.Context, call transactionalToolContext) (transactionalPhaseResult, error) {
	input, err := resolveSendAgentMessageRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	target, err := call.Reader.ResolveSubagentReference(ctx, input.AgentRef)
	if err != nil {
		return failSubagentTransactionForStorageError("send_agent_message_failed", err)
	}
	targetPublicID, err := publicid.Encode(publicid.KindAgent, target.AgentID)
	if err != nil {
		return nil, fmt.Errorf("encode subagent id: %w", err)
	}
	content, err := structuredToolResultContent(map[string]any{
		"agent_id":  targetPublicID,
		"name":      target.Name,
		"delivered": true,
		"message": "Message delivered. It interrupts the subagent's current work and cancels any open " +
			"question or permission request. The reply arrives later as a message from it.",
	})
	if err != nil {
		return nil, err
	}
	completion, err := successfulToolCallCompletion(content)
	if err != nil {
		return nil, err
	}
	command := executionstore.SendSubagentMessageForToolCall(
		executionstore.SendSubagentMessageInput{
			TargetAgentID: target.AgentID,
			Message:       input.Message,
		},
		completion,
	)
	return executeInTransaction(command, func(err error) (transactionalPhaseResult, error) {
		return failSubagentTransactionForStorageError("send_agent_message_failed", err)
	}), nil
}

func stopAgent(ctx context.Context, call transactionalToolContext) (transactionalPhaseResult, error) {
	input, err := resolveStopAgentRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	target, err := call.Reader.ResolveSubagentReference(ctx, input.AgentRef)
	if err != nil {
		return failSubagentTransactionForStorageError("stop_agent_failed", err)
	}
	targetPublicID, err := publicid.Encode(publicid.KindAgent, target.AgentID)
	if err != nil {
		return nil, fmt.Errorf("encode subagent id: %w", err)
	}
	content, err := structuredToolResultContent(map[string]any{
		"agent_id": targetPublicID,
		"name":     target.Name,
		"state":    executionstore.SubagentStateArchived,
	})
	if err != nil {
		return nil, err
	}
	completion, err := successfulToolCallCompletion(content)
	if err != nil {
		return nil, err
	}
	command := executionstore.StopSubagentForToolCall(target.AgentID, completion)
	return executeInTransaction(command, func(err error) (transactionalPhaseResult, error) {
		return failSubagentTransactionForStorageError("stop_agent_failed", err)
	}), nil
}

// stopAgentInBackground deletes the pool machines the archive released,
// matching the immediate deletion the API archive path starts.
func stopAgentInBackground(ctx context.Context, call backgroundToolContext) error {
	machines, ok := call.CommandResult.([]executionstore.MachineRecord)
	if !ok || len(machines) == 0 || call.Executor.MachinePoolManager == nil {
		return nil
	}
	attemptCtx, cancel := context.WithTimeout(ctx, machinepool.DefaultImmediateDeletionTimeout)
	defer cancel()
	if _, err := call.Executor.MachinePoolManager.DeleteMachines(attemptCtx, machines); err != nil {
		return fmt.Errorf("delete stopped subagent machines: %w", err)
	}
	return nil
}

func listAgents(ctx context.Context, call transactionalToolContext) (transactionalPhaseResult, error) {
	statuses, err := call.Reader.ListSubagents(ctx, false)
	if err != nil {
		return nil, err
	}
	summaries := make([]subagentSummary, 0, len(statuses))
	for _, status := range statuses {
		summary, err := subagentSummaryFromStatus(status)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	content, err := structuredToolResultContent(map[string]any{"agents": summaries})
	if err != nil {
		return nil, err
	}
	return completeInTransaction(content), nil
}
