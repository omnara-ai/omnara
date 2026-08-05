package modelcontext

import (
	"encoding/json"
	"fmt"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage"
)

type Normalizer interface {
	Normalize(bundle Bundle) error
}

type ProjectionNormalizer struct{}

func (ProjectionNormalizer) Normalize(bundle Bundle) error {
	if bundle.ProjectID == storage.NilID || bundle.AgentID == storage.NilID || bundle.TurnID == storage.NilID ||
		len(bundle.OpeningInputIDs) == 0 {
		return fmt.Errorf("project, agent, turn, and opening inputs are required")
	}
	for _, inputID := range bundle.OpeningInputIDs {
		if inputID == storage.NilID {
			return fmt.Errorf("opening input id is required")
		}
	}
	lastSequence := int64(0)
	seenMessages := map[string]bool{}
	for _, message := range bundle.Messages {
		if message.ID == "" || message.Role == "" {
			return fmt.Errorf("message id and role are required")
		}
		if message.Role != modelprotocol.RoleUser &&
			message.Role != modelprotocol.RoleAssistant {
			return fmt.Errorf("message %s has unsupported role %q", message.ID, message.Role)
		}
		if seenMessages[message.ID] {
			return fmt.Errorf("duplicate message in context: %s", message.ID)
		}
		seenMessages[message.ID] = true
		if !jsonIsArray(message.Content) {
			return fmt.Errorf("message %s content must be a json array", message.ID)
		}
		if err := validateMessageContentBlocks(message.Role, message.Content); err != nil {
			return fmt.Errorf("message %s content: %w", message.ID, err)
		}
		if message.Sequence <= 0 {
			return fmt.Errorf("message %s event sequence is required", message.ID)
		}
		if lastSequence != 0 && message.Sequence <= lastSequence {
			return fmt.Errorf("context events must be strictly ordered by sequence")
		}
		if message.Sequence > bundle.InputEventSequence {
			return fmt.Errorf("message %s exceeds event watermark", message.ID)
		}
		lastSequence = message.Sequence
	}
	lastCheckpointEnd := int64(0)
	if checkpoint := bundle.ContextCheckpoint; checkpoint != nil {
		if checkpoint.ID == "" || checkpoint.SummarizedThroughEventSequence <= 0 ||
			checkpoint.Summary == "" {
			return fmt.Errorf("invalid checkpoint ref")
		}
		if checkpoint.SummarizedThroughEventSequence > bundle.InputEventSequence {
			return fmt.Errorf("checkpoint %s exceeds event watermark", checkpoint.ID)
		}
		lastCheckpointEnd = checkpoint.SummarizedThroughEventSequence
	}
	if len(bundle.Messages) > 0 && lastCheckpointEnd > 0 && bundle.Messages[0].Sequence <= lastCheckpointEnd {
		return fmt.Errorf("transcript tail overlaps checkpoint range")
	}
	seenProcesses := map[string]bool{}
	for _, process := range bundle.ActiveProcesses {
		if process.ProcessID == "" || process.State == "" {
			return fmt.Errorf("active process id and state are required")
		}
		if seenProcesses[process.ProcessID] {
			return fmt.Errorf("duplicate active process in context: %s", process.ProcessID)
		}
		seenProcesses[process.ProcessID] = true
	}
	seenMachines := map[string]bool{}
	for _, machine := range bundle.AttachedMachines {
		if machine.MachineRef == "" {
			return fmt.Errorf("attached machine ref is required")
		}
		if seenMachines[machine.MachineRef] {
			return fmt.Errorf("duplicate attached machine in context: %s", machine.MachineRef)
		}
		seenMachines[machine.MachineRef] = true
	}
	seenIntegrationTargets := map[string]bool{}
	currentIntegrationTargets := 0
	for _, target := range bundle.IntegrationTargets {
		if target.TargetRef == "" || target.DurableID == "" || target.Provider == "" ||
			target.ProviderRefKind == "" ||
			target.Label == "" {
			return fmt.Errorf(
				"integration target ref, durable id, provider, ref kind, and label are required",
			)
		}
		if seenIntegrationTargets[target.TargetRef] {
			return fmt.Errorf("duplicate integration target in context: %s", target.TargetRef)
		}
		seenIntegrationTargets[target.TargetRef] = true
		if target.IsCurrent {
			currentIntegrationTargets++
		}
	}
	if currentIntegrationTargets > 1 {
		return fmt.Errorf("multiple current integration targets in context")
	}
	seenAvailableMachinePools := map[string]bool{}
	for _, pool := range bundle.AvailableMachinePools {
		if pool.MachinePoolName == "" {
			return fmt.Errorf("machine pool name is required")
		}
		if seenAvailableMachinePools[pool.MachinePoolName] {
			return fmt.Errorf("duplicate machine pool in context: %s", pool.MachinePoolName)
		}
		seenAvailableMachinePools[pool.MachinePoolName] = true
	}
	seenToolResults := map[string]bool{}
	for _, result := range bundle.ToolResults {
		if result.ToolCallID == "" || result.Name == "" {
			return fmt.Errorf("tool result id and name are required")
		}
		if result.DurableID == "" {
			return fmt.Errorf("tool result durable id is required")
		}
		if result.ProviderCallID == "" {
			return fmt.Errorf("tool result %s provider call id is required", result.DurableID)
		}
		if err := modelenvelope.ValidateToolInput(result.Input); err != nil {
			return fmt.Errorf("tool result %s input: %w", result.DurableID, err)
		}
		if result.SourceEventSequence <= 0 || result.ResultEventSequence <= 0 {
			return fmt.Errorf("tool result %s source and result event sequences are required", result.DurableID)
		}
		if result.SourceEventSequence >= result.ResultEventSequence ||
			result.ResultEventSequence > bundle.InputEventSequence {
			return fmt.Errorf("tool result %s does not follow its source within the event watermark", result.DurableID)
		}
		if lastCheckpointEnd > 0 && result.SourceEventSequence <= lastCheckpointEnd {
			return fmt.Errorf("tool result %s overlaps the checkpoint range", result.DurableID)
		}
		if !result.Outcome.IsTerminal() {
			return fmt.Errorf("tool result %s terminal outcome is required", result.DurableID)
		}
		if !jsonIsArray(result.ContentParts) {
			return fmt.Errorf("tool result %s content must be a json array", result.ToolCallID)
		}
		if err := validateToolResultContentBlocks(result.ContentParts); err != nil {
			return fmt.Errorf("tool result %s content: %w", result.ToolCallID, err)
		}
		if seenToolResults[result.ToolCallID] {
			return fmt.Errorf("duplicate tool result in context: %s", result.ToolCallID)
		}
		seenToolResults[result.ToolCallID] = true
	}
	return nil
}

func normalizerOrDefault(n Normalizer) Normalizer {
	if n != nil {
		return n
	}
	return ProjectionNormalizer{}
}

func jsonIsArray(raw json.RawMessage) bool {
	if len(raw) == 0 || !json.Valid(raw) {
		return false
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return false
	}
	_, ok := value.([]any)
	return ok
}
