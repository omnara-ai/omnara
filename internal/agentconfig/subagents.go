package agentconfig

import (
	"maps"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const (
	SubagentTypeProfile = "profile"
	SubagentTypeSelf    = "self"

	MaxSubagentDepth     = 8
	DefaultSubagentDepth = 1
)

type SubagentDepth struct {
	MaxDepth *int
	Depth    int
}

func (depth SubagentDepth) Limit() int {
	if depth.MaxDepth != nil {
		return min(*depth.MaxDepth, MaxSubagentDepth)
	}
	return DefaultSubagentDepth
}

func (depth SubagentDepth) CanSpawn() bool {
	return depth.Depth < depth.Limit()
}

type AgentConfigSubagentSource struct {
	Type                    string                                `json:"type"`
	Profile                 string                                `json:"profile,omitempty"`
	Description             string                                `json:"description,omitempty"`
	Model                   *AgentConfigSubagentModelSource       `json:"model,omitempty"`
	Instruction             *AgentConfigSubagentInstructionSource `json:"instruction,omitempty"`
	MaxInstances            *int                                  `json:"max_instances,omitempty"`
	ArchiveAfterIdleMinutes *int                                  `json:"archive_after_idle_minutes,omitempty"`
}

type AgentConfigSubagentModelSource struct {
	ProviderConfig         string                           `json:"provider_config,omitempty"`
	Name                   string                           `json:"name,omitempty"`
	ContextWindowTokens    *int                             `json:"context_window_tokens,omitempty"`
	DefaultMaxOutputTokens *int                             `json:"default_max_output_tokens,omitempty"`
	CacheRetention         string                           `json:"cache_retention,omitempty"`
	Reasoning              *AgentConfigModelReasoningSource `json:"reasoning,omitempty"`
}

type AgentConfigSubagentInstructionSource struct {
	Append string `json:"append,omitempty"`
}

type SubagentCompiled struct {
	Type                    string         `json:"type"`
	ProfileID               uuid.UUID      `json:"profile_id,omitzero"`
	Description             string         `json:"description,omitempty"`
	Model                   *ModelCompiled `json:"model,omitempty"`
	InstructionAppend       string         `json:"instruction_append,omitempty"`
	MaxInstances            *int           `json:"max_instances,omitempty"`
	ArchiveAfterIdleMinutes *int           `json:"archive_after_idle_minutes,omitempty"`
}

func SubagentCompiledFrom(
	base Compiled,
	subagent SubagentCompiled,
	depth SubagentDepth,
) Compiled {
	child := base
	child.Tools = copyTools(base.Tools)
	child.InteractionHandlers = nil
	child.MCP = maps.Clone(base.MCP)
	for name, tool := range child.Tools {
		if tool.IntegrationID != uuid.Nil || toolcatalog.UsesIntegrationToolNamespace(name) ||
			toolcatalog.IsInteractionHandlerTool(name) {
			delete(child.Tools, name)
		}
	}
	child.MaxDepth = depth.MaxDepth
	if subagent.InstructionAppend != "" {
		child.Instruction = strings.TrimSpace(base.Instruction) + "\n\n" + subagent.InstructionAppend
	}
	if !depth.CanSpawn() {
		child.Subagents = nil
		child.MaxSubagents = nil
		delete(child.Tools, toolcatalog.ToolNameSpawnAgent)
		if len(child.Tools) == 0 {
			child.Tools = nil
		}
	}
	if subagent.Model == nil {
		return child
	}
	override := *subagent.Model
	if override.ConfiguredModelID != uuid.Nil {
		child.Model.ConfiguredModelID = override.ConfiguredModelID
	}
	if override.ContextWindowTokens != nil {
		child.Model.ContextWindowTokens = override.ContextWindowTokens
	}
	if override.DefaultMaxOutputTokens != nil {
		child.Model.DefaultMaxOutputTokens = override.DefaultMaxOutputTokens
	}
	if override.CacheRetention != "" {
		child.Model.CacheRetention = strings.TrimSpace(override.CacheRetention)
	}
	if override.Reasoning != nil {
		child.Model.Reasoning = &ModelReasoningCompiled{Effort: strings.TrimSpace(override.Reasoning.Effort)}
	}
	return child
}

func copyTools(tools map[string]ToolCompiled) map[string]ToolCompiled {
	if len(tools) == 0 {
		return nil
	}
	out := make(map[string]ToolCompiled, len(tools))
	for name, tool := range tools {
		out[name] = tool
	}
	return out
}

func compileSubagents(
	source AgentConfigSource,
	opts CompileOptions,
) (map[string]SubagentCompiled, error) {
	keys := slices.Sorted(maps.Keys(source.Subagents))
	compiled := make(map[string]SubagentCompiled, len(keys))
	for _, key := range keys {
		entry := source.Subagents[key]
		pointer := jsonPointer("subagents", key)
		out := SubagentCompiled{
			Type:                    entry.Type,
			Description:             strings.TrimSpace(entry.Description),
			MaxInstances:            entry.MaxInstances,
			ArchiveAfterIdleMinutes: entry.ArchiveAfterIdleMinutes,
		}
		if entry.Instruction != nil {
			out.InstructionAppend = strings.TrimSpace(entry.Instruction.Append)
		}
		if entry.Model != nil {
			out.Model = &ModelCompiled{
				ContextWindowTokens:    entry.Model.ContextWindowTokens,
				DefaultMaxOutputTokens: entry.Model.DefaultMaxOutputTokens,
				CacheRetention:         strings.TrimSpace(entry.Model.CacheRetention),
			}
			if entry.Model.Reasoning != nil {
				out.Model.Reasoning = &ModelReasoningCompiled{Effort: strings.TrimSpace(entry.Model.Reasoning.Effort)}
			}
			if entry.Model.ProviderConfig != "" && opts.ResolveModelSelection != nil {
				resolved, err := opts.ResolveModelSelection(entry.Model.ProviderConfig, entry.Model.Name)
				if err != nil {
					return nil, issueAt(pointer, issueOr("/model", err))
				}
				if resolved.ConfiguredModelID == uuid.Nil {
					return nil, issuef(jsonPointer("subagents", key, "model"), "resolver returned an empty model id")
				}
				out.Model.ConfiguredModelID = resolved.ConfiguredModelID
			}
		}
		switch entry.Type {
		case SubagentTypeProfile:
			if opts.ResolveAgentProfileName == nil {
				return nil, issuef(pointer, "profile subagents require a ResolveAgentProfileName callback")
			}
			profileID, err := opts.ResolveAgentProfileName(entry.Profile)
			if err != nil {
				return nil, issueOr(jsonPointer("subagents", key, "profile"), err)
			}
			if profileID == uuid.Nil {
				return nil, issuef(jsonPointer("subagents", key, "profile"), "resolver returned an empty profile id")
			}
			out.ProfileID = profileID
		case SubagentTypeSelf:
		default:
			return nil, issuef(
				jsonPointer("subagents", key, "type"),
				"must be %q or %q",
				SubagentTypeProfile,
				SubagentTypeSelf,
			)
		}
		compiled[key] = out
	}
	return compiled, nil
}

func validateSubagentToolConfiguration(source AgentConfigSource) error {
	if _, ok := source.Tools[toolcatalog.ToolNameSpawnAgent]; ok && len(source.Subagents) == 0 {
		return issuef(
			jsonPointer("tools", toolcatalog.ToolNameSpawnAgent),
			"%q requires at least one entry under subagents",
			toolcatalog.ToolNameSpawnAgent,
		)
	}
	if source.MaxSubagents != nil && len(source.Subagents) == 0 {
		return issuef(jsonPointer("max_subagents"), "requires at least one entry under subagents")
	}
	return nil
}
