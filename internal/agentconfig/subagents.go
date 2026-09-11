package agentconfig

import (
	"maps"
	"slices"
	"strings"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const (
	SubagentTypeProfile = "profile"
	SubagentTypeSelf    = "self"
)

type AgentConfigSubagentSource struct {
	Type                    string                                `json:"type"`
	Profile                 string                                `json:"profile,omitempty"`
	Description             string                                `json:"description,omitempty"`
	Model                   *AgentConfigSubagentModelSource       `json:"model,omitempty"`
	Instruction             *AgentConfigSubagentInstructionSource `json:"instruction,omitempty"`
	MaxConcurrent           *int                                  `json:"max_concurrent,omitempty"`
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
	Type                    string                 `json:"type"`
	ProfileID               string                 `json:"profile_id,omitempty"`
	Description             string                 `json:"description,omitempty"`
	Model                   *SubagentModelCompiled `json:"model,omitempty"`
	InstructionAppend       string                 `json:"instruction_append,omitempty"`
	MaxConcurrent           *int                   `json:"max_concurrent,omitempty"`
	ArchiveAfterIdleMinutes *int                   `json:"archive_after_idle_minutes,omitempty"`
}

type SubagentModelCompiled struct {
	ProviderConfig         string                  `json:"provider_config,omitempty"`
	Name                   string                  `json:"name,omitempty"`
	ContextWindowTokens    *int                    `json:"context_window_tokens,omitempty"`
	DefaultMaxOutputTokens *int                    `json:"default_max_output_tokens,omitempty"`
	CacheRetention         string                  `json:"cache_retention,omitempty"`
	Reasoning              *ModelReasoningCompiled `json:"reasoning,omitempty"`
}

func (override *SubagentModelCompiled) ApplyTo(base AgentConfigModelSource) AgentConfigModelSource {
	if override == nil {
		return base
	}
	if override.ProviderConfig != "" {
		base.ProviderConfig = override.ProviderConfig
	}
	if override.Name != "" {
		base.Name = override.Name
	}
	if override.ContextWindowTokens != nil {
		base.ContextWindowTokens = override.ContextWindowTokens
	}
	if override.DefaultMaxOutputTokens != nil {
		base.DefaultMaxOutputTokens = override.DefaultMaxOutputTokens
	}
	if override.CacheRetention != "" {
		base.CacheRetention = override.CacheRetention
	}
	if override.Reasoning != nil {
		base.Reasoning = &AgentConfigModelReasoningSource{Effort: override.Reasoning.Effort}
	}
	return base
}

type SubagentModelResolver func(
	baseConfiguredModelID string,
	override SubagentModelCompiled,
) (ResolvedModelSelection, error)

func SubagentCompiledFrom(
	base Compiled,
	subagent SubagentCompiled,
	resolveModel SubagentModelResolver,
) (Compiled, error) {
	child := base
	child.Tools = copyTools(base.Tools)
	if subagent.InstructionAppend != "" {
		child.Instruction = strings.TrimSpace(base.Instruction) + "\n\n" + subagent.InstructionAppend
	}
	if subagent.Type == SubagentTypeSelf {
		child.Subagents = nil
		child.MaxSubagents = nil
		for name := range child.Tools {
			if toolcatalog.IsSubagentToolName(name) {
				delete(child.Tools, name)
			}
		}
		if len(child.Tools) == 0 {
			child.Tools = nil
		}
	}
	if subagent.Model == nil {
		return child, nil
	}
	override := *subagent.Model
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
	if override.ProviderConfig == "" && override.Name == "" {
		return child, nil
	}
	if resolveModel == nil {
		return Compiled{}, issuef(jsonPointer("model"), "subagent model overrides require a SubagentModelResolver")
	}
	resolved, err := resolveModel(base.Model.ConfiguredModelID, override)
	if err != nil {
		return Compiled{}, issueOr(jsonPointer("model"), err)
	}
	child.Model.ConfiguredModelID = resolved.ConfiguredModelID
	if resolved.SupportsTools != nil && !*resolved.SupportsTools && requiresModelToolSupport(child) {
		return Compiled{}, issuef(jsonPointer("model", "name"), "model %q does not support tools", override.Name)
	}
	return child, nil
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
			MaxConcurrent:           entry.MaxConcurrent,
			ArchiveAfterIdleMinutes: entry.ArchiveAfterIdleMinutes,
		}
		if entry.Instruction != nil {
			out.InstructionAppend = strings.TrimSpace(entry.Instruction.Append)
		}
		if entry.Model != nil {
			out.Model = &SubagentModelCompiled{
				ProviderConfig:         entry.Model.ProviderConfig,
				Name:                   entry.Model.Name,
				ContextWindowTokens:    entry.Model.ContextWindowTokens,
				DefaultMaxOutputTokens: entry.Model.DefaultMaxOutputTokens,
				CacheRetention:         strings.TrimSpace(entry.Model.CacheRetention),
			}
			if entry.Model.Reasoning != nil {
				out.Model.Reasoning = &ModelReasoningCompiled{Effort: strings.TrimSpace(entry.Model.Reasoning.Effort)}
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
			if profileID == "" {
				return nil, issuef(jsonPointer("subagents", key, "profile"), "resolver returned an empty profile id")
			}
			out.ProfileID = profileID
			if out.Model != nil && out.Model.ProviderConfig != "" && out.Model.Name != "" &&
				opts.ResolveModelSelection != nil {
				if _, err := opts.ResolveModelSelection(out.Model.ProviderConfig, out.Model.Name); err != nil {
					return nil, issueOr(jsonPointer("subagents", key, "model"), err)
				}
			}
		case SubagentTypeSelf:
			if out.Model != nil && opts.ResolveModelSelection != nil {
				merged := out.Model.ApplyTo(source.Model)
				if _, err := opts.ResolveModelSelection(merged.ProviderConfig, merged.Name); err != nil {
					return nil, issueOr(jsonPointer("subagents", key, "model"), err)
				}
			}
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
	for name := range source.Tools {
		if !toolcatalog.IsSubagentToolName(name) {
			continue
		}
		if len(source.Subagents) == 0 {
			return issuef(jsonPointer("tools", name), "%q requires at least one entry under subagents", name)
		}
	}
	if source.MaxSubagents != nil && len(source.Subagents) == 0 {
		return issuef(jsonPointer("max_subagents"), "requires at least one entry under subagents")
	}
	return nil
}
