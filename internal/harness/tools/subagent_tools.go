package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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

type waitAgentsRequest struct {
	Agents         []string `json:"agents,omitempty"`
	Mode           string   `json:"mode,omitempty"`
	TimeoutSeconds *int     `json:"timeout_seconds,omitempty"`
}

type sendAgentMessageRequest struct {
	Agent         string `json:"agent"`
	Message       string `json:"message"`
	InteractionID string `json:"interaction_id,omitempty"`
}

type stopAgentRequest struct {
	Agent string `json:"agent"`
}

type subagentSummary struct {
	AgentID        string `json:"agent_id"`
	Name           string `json:"name,omitempty"`
	Handle         string `json:"handle"`
	State          string `json:"state"`
	LastActivityAt string `json:"last_activity_at"`
}

func decodeStrictToolRequest(toolName string, raw json.RawMessage, target any, allowed ...string) error {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return fmt.Errorf("parse %s request: %w", toolName, err)
	}
	for field, value := range body {
		known := false
		for _, name := range allowed {
			if name == field {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("%s request has unsupported field %q", toolName, field)
		}
		if string(value) == "null" {
			return fmt.Errorf("%s request field %q cannot be null", toolName, field)
		}
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("parse %s request: %w", toolName, err)
	}
	return nil
}

func resolveSpawnAgentRequest(raw json.RawMessage) (spawnAgentRequest, error) {
	var input spawnAgentRequest
	if err := decodeStrictToolRequest("spawn_agent", raw, &input, "agent", "task", "name"); err != nil {
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

func resolveWaitAgentsRequest(raw json.RawMessage) (waitAgentsRequest, error) {
	var input waitAgentsRequest
	if err := decodeStrictToolRequest("wait_agents", raw, &input, "agents", "mode", "timeout_seconds"); err != nil {
		return waitAgentsRequest{}, err
	}
	if input.Mode == "" {
		input.Mode = executionstore.AgentWaitModeAll
	}
	if input.Mode != executionstore.AgentWaitModeAll && input.Mode != executionstore.AgentWaitModeAny {
		return waitAgentsRequest{}, fmt.Errorf(
			"wait_agents mode must be %q or %q",
			executionstore.AgentWaitModeAll,
			executionstore.AgentWaitModeAny,
		)
	}
	if input.TimeoutSeconds != nil && (*input.TimeoutSeconds < 1 || *input.TimeoutSeconds > 86400) {
		return waitAgentsRequest{}, errors.New("wait_agents timeout_seconds must be between 1 and 86400")
	}
	for index, reference := range input.Agents {
		input.Agents[index] = strings.TrimSpace(reference)
		if input.Agents[index] == "" {
			return waitAgentsRequest{}, errors.New("wait_agents agents entries cannot be empty")
		}
	}
	return input, nil
}

func resolveSendAgentMessageRequest(raw json.RawMessage) (sendAgentMessageRequest, error) {
	var input sendAgentMessageRequest
	err := decodeStrictToolRequest("send_agent_message", raw, &input, "agent", "message", "interaction_id")
	if err != nil {
		return sendAgentMessageRequest{}, err
	}
	input.Agent = strings.TrimSpace(input.Agent)
	input.InteractionID = strings.TrimSpace(input.InteractionID)
	if input.Agent == "" {
		return sendAgentMessageRequest{}, errors.New("send_agent_message agent is required")
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
	if err := decodeStrictToolRequest("stop_agent", raw, &input, "agent"); err != nil {
		return stopAgentRequest{}, err
	}
	input.Agent = strings.TrimSpace(input.Agent)
	if input.Agent == "" {
		return stopAgentRequest{}, errors.New("stop_agent agent is required")
	}
	return input, nil
}

func validateSpawnAgentInput(raw json.RawMessage) error {
	_, err := resolveSpawnAgentRequest(raw)
	return err
}

func validateWaitAgentsInput(raw json.RawMessage) error {
	_, err := resolveWaitAgentsRequest(raw)
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

func subagentToolFailureContent(code, message string) (toolResultContent, error) {
	return structuredToolResultContent(
		map[string]any{"error_code": code, "error": message, "message": message, "retryable": false},
	)
}

func failSubagentTransactionForStorageError(code string, cause error) (transactionalPhaseResult, error) {
	if !errors.Is(cause, storeerr.ErrInvalidRequest) &&
		!errors.Is(cause, storeerr.ErrNotFound) &&
		!errors.Is(cause, storeerr.ErrConflict) &&
		!errors.Is(cause, storeerr.ErrStateTransitionConflict) {
		return nil, cause
	}
	content, err := subagentToolFailureContent(code, cause.Error())
	if err != nil {
		return nil, err
	}
	return failInTransaction(content, cause), nil
}

func failSubagentAsync(code string, cause error) (asyncPhaseResult, error) {
	content, err := subagentToolFailureContent(code, cause.Error())
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
		Name:           status.Name,
		Handle:         status.Handle,
		State:          status.State,
		LastActivityAt: status.LastActivityAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
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
	handle, ok := contract.Subagents[input.Agent]
	if !ok {
		return failSubagentAsync("spawn_agent_failed", fmt.Errorf(
			"unknown subagent handle %q; configured handles: %s",
			input.Agent,
			strings.Join(contract.SubagentHandles(), ", "),
		))
	}
	parent, err := executor.Store.Execution().GetAgentInProject(ctx, call.Turn.ProjectID, call.Turn.AgentID)
	if err != nil {
		return nil, err
	}
	baseConfig, err := executor.subagentBaseConfig(ctx, call.Turn, handle)
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
	childSource, err := json.Marshal(agentconfig.SubagentSource(baseSource, handle))
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
			SpawnToolCallID:         call.ToolCallID,
			Handle:                  input.Agent,
			MaxConcurrent:           handle.MaxConcurrent,
			MaxSubagents:            contract.MaxSubagents,
			ShareParentMachines:     handle.Type == agentconfig.SubagentTypeSelf,
			ArchiveAfterIdleMinutes: handle.ArchiveAfterIdleMinutes,
		},
	})
	if err != nil {
		if errors.Is(err, storeerr.ErrInvalidRequest) ||
			errors.Is(err, storeerr.ErrConflict) ||
			errors.Is(err, storeerr.ErrNotFound) ||
			errors.Is(err, storeerr.ErrStateTransitionConflict) {
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
		"agent_id": childPublicID,
		"name":     launch.Agent.Name,
		"handle":   input.Agent,
		"state":    executionstore.SubagentStateRunning,
		"message": "Subagent started. Its final answer will arrive as a message from it, " +
			"or call wait_agents to block until it finishes.",
	})
	if err != nil {
		return nil, err
	}
	return completeAsynchronously(content), nil
}

func (e Executor) subagentBaseConfig(
	ctx context.Context,
	turn Turn,
	handle agentconfig.SubagentCompiled,
) (executionstore.AgentConfigRecord, error) {
	switch handle.Type {
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
		profileID, err := publicid.Decode(publicid.KindAgentProfile, handle.ProfileID)
		if err != nil {
			return executionstore.AgentConfigRecord{}, fmt.Errorf("decode subagent profile id: %w", err)
		}
		profile, err := e.Store.Execution().GetAgentProfile(ctx, turn.ProjectID, profileID)
		if err != nil {
			if storeerr.IsNotFound(err) {
				return executionstore.AgentConfigRecord{}, fmt.Errorf("subagent profile %s no longer exists", handle.ProfileID)
			}
			return executionstore.AgentConfigRecord{}, err
		}
		return profile.CurrentConfig, nil
	default:
		return executionstore.AgentConfigRecord{}, fmt.Errorf("unsupported subagent type %q", handle.Type)
	}
}

func resolveSubagentTargets(
	ctx context.Context,
	reader *executionstore.ToolCallReader,
	references []string,
) ([]storage.ID, error) {
	ids := make([]storage.ID, 0, len(references))
	seen := make(map[storage.ID]struct{}, len(references))
	for _, reference := range references {
		status, err := reader.ResolveSubagentReference(ctx, reference)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[status.AgentID]; duplicate {
			continue
		}
		seen[status.AgentID] = struct{}{}
		ids = append(ids, status.AgentID)
	}
	return ids, nil
}

func waitAgents(ctx context.Context, call transactionalToolContext) (transactionalPhaseResult, error) {
	input, err := resolveWaitAgentsRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	targets, err := resolveSubagentTargets(ctx, call.Reader, input.Agents)
	if err != nil {
		return failSubagentTransactionForStorageError("wait_agents_failed", err)
	}
	command := executionstore.CreateAgentWaitForToolCall(
		executionstore.CreateAgentWaitInput{
			TargetAgentIDs: targets,
			Mode:           input.Mode,
			TimeoutSeconds: input.TimeoutSeconds,
		},
		func(outcome executionstore.AgentWaitOutcome) (executionstore.ToolCallCompletionInput, error) {
			content, err := structuredToolResultContent(outcome)
			if err != nil {
				return executionstore.ToolCallCompletionInput{}, err
			}
			return successfulToolCallCompletion(content)
		},
	)
	return executeInTransaction(command, func(err error) (transactionalPhaseResult, error) {
		return failSubagentTransactionForStorageError("wait_agents_failed", err)
	}), nil
}

func sendAgentMessage(ctx context.Context, call transactionalToolContext) (transactionalPhaseResult, error) {
	input, err := resolveSendAgentMessageRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	target, err := call.Reader.ResolveSubagentReference(ctx, input.Agent)
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
	target, err := call.Reader.ResolveSubagentReference(ctx, input.Agent)
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
	if err := validateListAgentsInput(call.Call.Input); err != nil {
		return nil, err
	}
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
