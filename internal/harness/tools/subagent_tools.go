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
	"github.com/omnara-ai/omnara/internal/httpapi/publicevents"
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
	AgentID            string `json:"agent_id"`
	TurnID             string `json:"turn_id,omitempty"`
	BeforeTurnSequence *int64 `json:"before_turn_sequence,omitempty"`
	BeforeSequence     *int64 `json:"before_sequence,omitempty"`
	Limit              *int32 `json:"limit,omitempty"`
}

type sendAgentMessageRequest struct {
	AgentID string `json:"agent_id"`
	Message string `json:"message"`
}

type stopAgentRequest struct {
	AgentID string `json:"agent_id"`
	Archive bool   `json:"archive,omitempty"`
}

type subagentSummary struct {
	AgentID        string `json:"agent_id"`
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
	input.AgentID = strings.TrimSpace(input.AgentID)
	if input.AgentID == "" {
		return sendAgentMessageRequest{}, errors.New("send_agent_message agent_id is required")
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
	input.AgentID = strings.TrimSpace(input.AgentID)
	if input.AgentID == "" {
		return stopAgentRequest{}, errors.New("stop_agent agent_id is required")
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
	input.AgentID = strings.TrimSpace(input.AgentID)
	input.TurnID = strings.TrimSpace(input.TurnID)
	if input.AgentID == "" {
		return readAgentRequest{}, errors.New("read_agent agent_id is required")
	}
	if input.TurnID == "" && input.BeforeSequence != nil {
		return readAgentRequest{}, errors.New("read_agent before_sequence requires turn_id")
	}
	if input.TurnID != "" && input.BeforeTurnSequence != nil {
		return readAgentRequest{}, errors.New("read_agent before_turn_sequence applies only without turn_id")
	}
	if _, err := publicevents.SequenceBoundary(input.BeforeTurnSequence, "before_turn_sequence"); err != nil {
		return readAgentRequest{}, fmt.Errorf("read_agent %w", err)
	}
	if _, err := publicevents.SequenceBoundary(input.BeforeSequence, "before_sequence"); err != nil {
		return readAgentRequest{}, fmt.Errorf("read_agent %w", err)
	}
	maxLimit := executionstore.MaxAgentTurnsReadPageLimit
	if input.TurnID != "" {
		maxLimit = executionstore.MaxAgentEventsReadPageLimit
	}
	if _, err := publicevents.TimelineLimit(input.Limit, 1, maxLimit); err != nil {
		return readAgentRequest{}, fmt.Errorf("read_agent %w", err)
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
	contract, parentConfig, err := call.Reader.RuntimeContract(ctx, call.Turn.ModelCallContextID)
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
	parent, err := call.Reader.Agent(ctx)
	if err != nil {
		return nil, err
	}
	parentDepth, err := call.Reader.AgentDepth(ctx)
	if err != nil {
		return nil, err
	}
	depth := agentconfig.SubagentDepth{MaxDepth: contract.MaxDepth, Depth: parentDepth + 1}
	launchConfig, err := subagentLaunchConfigForSpawn(ctx, call.Reader, parent, parentConfig, subagent, depth)
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
			DerivedConfig: &launchConfig.derived,
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
				ParentAgentID: parent.ID,
				Key:           input.Agent,
				MaxInstances:  subagent.MaxInstances,
				MaxSubagents:  contract.MaxSubagents,
				MaxDepth:      depth.Limit(),
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
		"agent_id": childPublicID,
		"name":     child.Name,
		"key":      key,
		"state":    executionstore.SubagentStateRunning,
		"message": "Subagent started. Its final answer will arrive as a message from it; " +
			"use read_agent to check its progress.",
	})
}

func provisionSubagentMachinesInBackground(ctx context.Context, call backgroundToolContext) error {
	launch, ok := call.CommandResult.(executionstore.LaunchAgentResult)
	if !ok {
		return fmt.Errorf("spawn_agent command result is %T, want LaunchAgentResult", call.CommandResult)
	}
	if len(launch.ProvisionMachineIDs) == 0 || call.Executor.MachinePoolManager == nil {
		return nil
	}
	call.Executor.MachinePoolManager.StartLaunchProvisioning(
		ctx, call.Executor.logger(), launch.Agent.OrgID, launch.ProvisionMachineIDs,
	)
	return nil
}

type subagentLaunchConfig struct {
	profileID storage.ID
	derived   executionstore.CreateAgentConfigInput
}

// subagentLaunchConfigForSpawn builds the config a spawned subagent launches
// from: the base (the parent's own config for self, the profile's current
// config for profile) with the key's overrides and the tree's depth limit
// applied, created inside the launch transaction. Every read stays on the tool
// call transaction. The child is attributed to the profile it was launched
// from: the configured profile, or for self subagents the parent's profile.
func subagentLaunchConfigForSpawn(
	ctx context.Context,
	reader *executionstore.ToolCallReader,
	parent executionstore.AgentRecord,
	parentConfig executionstore.AgentConfigRecord,
	subagent agentconfig.SubagentCompiled,
	depth agentconfig.SubagentDepth,
) (subagentLaunchConfig, error) {
	var baseConfig executionstore.AgentConfigRecord
	var profileID storage.ID
	switch subagent.Type {
	case agentconfig.SubagentTypeSelf:
		profileID = parent.AgentProfileID
		baseConfig = parentConfig
	case agentconfig.SubagentTypeProfile:
		configuredProfileID, err := publicid.Decode(publicid.KindAgentProfile, subagent.ProfileID)
		if err != nil {
			return subagentLaunchConfig{}, fmt.Errorf("decode subagent profile id: %w", err)
		}
		profile, err := reader.GetAgentProfile(ctx, configuredProfileID)
		if err != nil {
			if storeerr.IsNotFound(err) {
				return subagentLaunchConfig{}, fmt.Errorf("subagent profile %s no longer exists", subagent.ProfileID)
			}
			return subagentLaunchConfig{}, err
		}
		baseConfig = profile.CurrentConfig
		profileID = profile.ID
	default:
		return subagentLaunchConfig{}, fmt.Errorf("unsupported subagent type %q", subagent.Type)
	}
	body, err := agentconfigcompile.DeriveSubagentConfig(
		baseConfig,
		subagent,
		depth,
		agentconfigcompile.SubagentModelResolver(ctx, reader.Models(), parent.OrgID, parent.ProjectID),
	)
	if err != nil {
		return subagentLaunchConfig{}, fmt.Errorf("derive subagent config: %w", err)
	}
	return subagentLaunchConfig{derived: body.CreateInput(parent.ProjectID), profileID: profileID}, nil
}

func readAgent(ctx context.Context, call transactionalToolContext) (transactionalPhaseResult, error) {
	input, err := resolveReadAgentRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	if input.TurnID == "" {
		return readAgentTurns(ctx, call, input)
	}
	return readAgentTurnEvents(ctx, call, input)
}

func readAgentTurns(
	ctx context.Context,
	call transactionalToolContext,
	input readAgentRequest,
) (transactionalPhaseResult, error) {
	beforeTurnSequence, _ := publicevents.SequenceBoundary(input.BeforeTurnSequence, "before_turn_sequence")
	limit, _ := publicevents.TimelineLimit(
		input.Limit, readAgentDefaultTurnLimit, executionstore.MaxAgentTurnsReadPageLimit,
	)
	status, turns, err := call.Reader.ReadSubagentTurns(ctx, input.AgentID, beforeTurnSequence, limit+1)
	if err != nil {
		return failSubagentTransactionForStorageError("read_agent_failed", err)
	}
	summary, err := subagentSummaryFromStatus(status)
	if err != nil {
		return nil, err
	}
	var nextBeforeTurnSequence *int64
	if len(turns) > int(limit) {
		turns = turns[:limit]
		nextBeforeTurnSequence = &turns[len(turns)-1].TurnSequence
	}
	response, err := publicevents.TurnsFromReadRecords(turns)
	if err != nil {
		return nil, err
	}
	content, err := structuredToolResultContent(map[string]any{
		"agent":                     summary,
		"turns":                     response,
		"next_before_turn_sequence": nextBeforeTurnSequence,
	})
	if err != nil {
		return nil, err
	}
	return completeInTransaction(content), nil
}

func readAgentTurnEvents(
	ctx context.Context,
	call transactionalToolContext,
	input readAgentRequest,
) (transactionalPhaseResult, error) {
	turnID, err := publicid.Decode(publicid.KindAgentTurn, input.TurnID)
	if err != nil {
		return failSubagentTransaction("read_agent_failed", fmt.Errorf("turn_id: %w", err))
	}
	beforeSequence, _ := publicevents.SequenceBoundary(input.BeforeSequence, "before_sequence")
	limit, _ := publicevents.TimelineLimit(
		input.Limit, readAgentDefaultEventLimit, executionstore.MaxAgentEventsReadPageLimit,
	)
	status, events, err := call.Reader.ReadSubagentTurnEvents(ctx, input.AgentID, turnID, beforeSequence, limit+1)
	if err != nil {
		return failSubagentTransactionForStorageError("read_agent_failed", err)
	}
	summary, err := subagentSummaryFromStatus(status)
	if err != nil {
		return nil, err
	}
	events, nextBeforeSequence := publicevents.TrimEventsBeforePage(events, limit)
	response, err := publicevents.EventsFromReadRecords(events)
	if err != nil {
		return nil, err
	}
	content, err := structuredToolResultContent(map[string]any{
		"agent":                summary,
		"turn_id":              input.TurnID,
		"events":               response,
		"next_before_sequence": nextBeforeSequence,
	})
	if err != nil {
		return nil, err
	}
	return completeInTransaction(content), nil
}

const (
	readAgentDefaultTurnLimit  int32 = 10
	readAgentDefaultEventLimit int32 = 20
)

func sendAgentMessage(ctx context.Context, call transactionalToolContext) (transactionalPhaseResult, error) {
	input, err := resolveSendAgentMessageRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	target, err := call.Reader.ResolveSubagent(ctx, input.AgentID)
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
		"message": "Message delivered. The subagent reads it after its current model call and tool batch " +
			"finish; any open question or permission request is canceled. The reply arrives later as a " +
			"message from it.",
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
	target, err := call.Reader.ResolveSubagent(ctx, input.AgentID)
	if err != nil {
		return failSubagentTransactionForStorageError("stop_agent_failed", err)
	}
	targetPublicID, err := publicid.Encode(publicid.KindAgent, target.AgentID)
	if err != nil {
		return nil, fmt.Errorf("encode subagent id: %w", err)
	}
	result := map[string]any{
		"agent_id": targetPublicID,
		"name":     target.Name,
		"archived": input.Archive,
	}
	if input.Archive {
		result["state"] = executionstore.SubagentStateArchived
	} else {
		result["message"] = "Current work canceled. The subagent keeps its context; send_agent_message resumes it."
	}
	content, err := structuredToolResultContent(result)
	if err != nil {
		return nil, err
	}
	completion, err := successfulToolCallCompletion(content)
	if err != nil {
		return nil, err
	}
	command := executionstore.StopSubagentForToolCall(
		executionstore.StopSubagentInput{TargetAgentID: target.AgentID, Archive: input.Archive},
		completion,
	)
	return executeInTransaction(command, func(err error) (transactionalPhaseResult, error) {
		return failSubagentTransactionForStorageError("stop_agent_failed", err)
	}), nil
}

// stopAgentInBackground deletes the pool machines the archive released,
// matching the immediate deletion the API archive path starts.
func stopAgentInBackground(ctx context.Context, call backgroundToolContext) error {
	machines, ok := call.CommandResult.([]executionstore.MachineRecord)
	if !ok {
		return fmt.Errorf("stop_agent command result is %T, want released machines", call.CommandResult)
	}
	if len(machines) == 0 || call.Executor.MachinePoolManager == nil {
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
