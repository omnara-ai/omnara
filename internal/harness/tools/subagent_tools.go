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
	AgentRef      string `json:"agent_ref"`
	Message       string `json:"message"`
	InteractionID string `json:"interaction_id,omitempty"`
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
	input.InteractionID = strings.TrimSpace(input.InteractionID)
	if input.AgentRef == "" {
		return sendAgentMessageRequest{}, errors.New("send_agent_message agent_ref is required")
	}
	if strings.TrimSpace(input.Message) == "" {
		return sendAgentMessageRequest{}, errors.New("send_agent_message message is required")
	}
	if input.InteractionID != "" {
		if _, err := publicid.Decode(publicid.KindAgentInteraction, input.InteractionID); err != nil {
			return sendAgentMessageRequest{}, fmt.Errorf("send_agent_message interaction_id: %w", err)
		}
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

func failSubagentAsync(code string, cause error) (asyncPhaseResult, error) {
	content, err := toolFailureContent(code, cause.Error(), false)
	if err != nil {
		return nil, err
	}
	return failAsynchronously(content, cause), nil
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

func spawnAgent(ctx context.Context, call asyncToolContext) (asyncPhaseResult, error) {
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
		return failSubagentAsync("spawn_agent_failed", fmt.Errorf(
			"unknown subagent key %q; configured keys: %s",
			input.Agent,
			strings.Join(contract.SubagentKeys(), ", "),
		))
	}
	parent, err := executor.Store.Execution().GetAgentInProject(ctx, call.Turn.ProjectID, call.Turn.AgentID)
	if err != nil {
		return nil, err
	}
	baseConfig, err := executor.subagentBaseConfig(ctx, call.Turn, subagent)
	if err != nil {
		return failSubagentAsync("spawn_agent_failed", err)
	}
	baseSource, err := agentconfig.ParseSource(
		agentconfig.SourceFormat(baseConfig.SourceFormat),
		[]byte(baseConfig.Source),
	)
	if err != nil {
		return nil, fmt.Errorf("parse base agent config source: %w", err)
	}
	childSource, err := json.Marshal(agentconfig.SubagentSource(baseSource, subagent))
	if err != nil {
		return nil, fmt.Errorf("encode subagent config source: %w", err)
	}
	body, err := agentconfigcompile.Compile(
		ctx,
		executor.Store,
		parent.OrgID,
		parent.ProjectID,
		executor.AgentConfigOptions,
		agentconfig.SourceFormatJSON,
		string(childSource),
	)
	if err != nil {
		return failSubagentAsync("spawn_agent_failed", fmt.Errorf("compile subagent config: %w", err))
	}
	childConfig, err := executor.Store.Execution().CreateAgentConfig(ctx, body.CreateInput(parent.ProjectID))
	if err != nil {
		return nil, fmt.Errorf("store subagent config: %w", err)
	}
	actor, err := executionstore.SubagentActorParams(parent.OrgID, parent)
	if err != nil {
		return nil, err
	}
	name := input.Name
	if name == "" {
		name = input.Agent
	}
	launch, err := executor.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID:     parent.ProjectID,
		AgentConfigID: childConfig.ID,
		LaunchedBy: identitystore.PrincipalRecord{
			Type: identitystore.PrincipalTypeSystem,
			ID:   parent.ID,
		},
		Name:           &name,
		Message:        input.Task,
		MessageActor:   actor,
		IdempotencyKey: "spawn:" + call.ToolCallID.String(),
		Subagent: &executionstore.SubagentLaunch{
			ParentAgentID:           parent.ID,
			Key:                     input.Agent,
			MaxConcurrent:           subagent.MaxConcurrent,
			MaxSubagents:            contract.MaxSubagents,
			ShareParentMachines:     subagent.Type == agentconfig.SubagentTypeSelf,
			ArchiveAfterIdleMinutes: subagent.ArchiveAfterIdleMinutes,
		},
	})
	if err != nil {
		if subagentStorageErrorIsToolFailure(err) {
			return failSubagentAsync("spawn_agent_failed", err)
		}
		return nil, err
	}
	if len(launch.ProvisionMachineIDs) > 0 && executor.MachinePoolManager != nil {
		for _, machineID := range launch.ProvisionMachineIDs {
			attemptCtx, cancel := context.WithTimeout(ctx, machinepool.DefaultImmediateProvisioningTimeout)
			err := executor.MachinePoolManager.ProvisionMachine(attemptCtx, launch.Agent.OrgID, machineID)
			cancel()
			if err != nil {
				return nil, fmt.Errorf("provision subagent machine: %w", err)
			}
		}
	}
	childPublicID, err := publicid.Encode(publicid.KindAgent, launch.Agent.ID)
	if err != nil {
		return nil, fmt.Errorf("encode subagent id: %w", err)
	}
	content, err := structuredToolResultContent(map[string]any{
		"agent_id":  childPublicID,
		"agent_ref": executionstore.SubagentRef(launch.Agent.ID),
		"name":      launch.Agent.Name,
		"key":       input.Agent,
		"state":     executionstore.SubagentStateRunning,
		"message": "Subagent started. Its final answer will arrive as a message from it; " +
			"use read_agent to check its progress.",
	})
	if err != nil {
		return nil, err
	}
	return completeAsynchronously(content), nil
}

func (e Executor) subagentBaseConfig(
	ctx context.Context,
	turn Turn,
	subagent agentconfig.SubagentCompiled,
) (executionstore.AgentConfigRecord, error) {
	switch subagent.Type {
	case agentconfig.SubagentTypeSelf:
		contextRow, found, err := e.Store.Execution().GetModelCallContext(
			ctx, turn.ProjectID, turn.AgentID, turn.ModelCallContextID,
		)
		if err != nil {
			return executionstore.AgentConfigRecord{}, err
		}
		if !found {
			return executionstore.AgentConfigRecord{}, fmt.Errorf("model call context %s not found", turn.ModelCallContextID)
		}
		config, found, err := e.Store.Execution().GetAgentConfig(ctx, turn.ProjectID, contextRow.AgentConfigID)
		if err != nil {
			return executionstore.AgentConfigRecord{}, err
		}
		if !found {
			return executionstore.AgentConfigRecord{}, fmt.Errorf("agent config %s not found", contextRow.AgentConfigID)
		}
		return config, nil
	case agentconfig.SubagentTypeProfile:
		profileID, err := publicid.Decode(publicid.KindAgentProfile, subagent.ProfileID)
		if err != nil {
			return executionstore.AgentConfigRecord{}, fmt.Errorf("decode subagent profile id: %w", err)
		}
		profile, err := e.Store.Execution().GetAgentProfile(ctx, turn.ProjectID, profileID)
		if err != nil {
			if storeerr.IsNotFound(err) {
				return executionstore.AgentConfigRecord{}, fmt.Errorf("subagent profile %s no longer exists", subagent.ProfileID)
			}
			return executionstore.AgentConfigRecord{}, err
		}
		return profile.CurrentConfig, nil
	default:
		return executionstore.AgentConfigRecord{}, fmt.Errorf("unsupported subagent type %q", subagent.Type)
	}
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
	interactionID := storage.NilID
	if input.InteractionID != "" {
		decoded, err := publicid.Decode(publicid.KindAgentInteraction, input.InteractionID)
		if err != nil {
			return nil, err
		}
		interactionID = decoded
	}
	targetPublicID, err := publicid.Encode(publicid.KindAgent, target.AgentID)
	if err != nil {
		return nil, fmt.Errorf("encode subagent id: %w", err)
	}
	content, err := structuredToolResultContent(map[string]any{
		"agent_id":  targetPublicID,
		"name":      target.Name,
		"delivered": true,
		"message":   "Message delivered. The subagent's reply will arrive as a message from it.",
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
			InteractionID: interactionID,
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
