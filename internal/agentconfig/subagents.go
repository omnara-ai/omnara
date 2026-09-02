package agentconfig

import (
	"sort"
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

// SubagentSource derives the source document a subagent launches from: the
// base config minus everything that would let the child spawn further
// subagents (for self forks), plus the handle's model and instruction
// overrides.
func SubagentSource(base AgentConfigSource, handle SubagentCompiled) AgentConfigSource {
	child := base
	child.Model = handle.Model.ApplyTo(base.Model)
	if handle.InstructionAppend != "" {
		child.Instruction = strings.TrimSpace(base.Instruction) + "\n\n" + handle.InstructionAppend
	}
	if handle.Type == SubagentTypeSelf {
		child.Subagents = nil
		child.MaxSubagents = nil
		if len(base.Tools) > 0 {
			child.Tools = make(map[string]AgentConfigToolSource, len(base.Tools))
			for name, tool := range base.Tools {
				if toolcatalog.IsSubagentToolName(name) {
					continue
				}
				child.Tools[name] = tool
			}
			if len(child.Tools) == 0 {
				child.Tools = nil
			}
		}
	}
	return child
}

func compileSubagents(
	source AgentConfigSource,
	opts CompileOptions,
) (map[string]SubagentCompiled, error) {
	handles := make([]string, 0, len(source.Subagents))
	for handle := range source.Subagents {
		handles = append(handles, handle)
	}
	sort.Strings(handles)
	compiled := make(map[string]SubagentCompiled, len(handles))
	for _, handle := range handles {
		entry := source.Subagents[handle]
		pointer := jsonPointer("subagents", handle)
		if toolcatalog.IsSubagentToolName(handle) {
			return nil, issuef(pointer, "handle %q collides with a subagent tool name", handle)
		}
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
				return nil, issueOr(jsonPointer("subagents", handle, "profile"), err)
			}
			if profileID == "" {
				return nil, issuef(jsonPointer("subagents", handle, "profile"), "resolver returned an empty profile id")
			}
			out.ProfileID = profileID
			if out.Model != nil && out.Model.ProviderConfig != "" && out.Model.Name != "" &&
				opts.ResolveModelSelection != nil {
				if _, err := opts.ResolveModelSelection(out.Model.ProviderConfig, out.Model.Name); err != nil {
					return nil, issueOr(jsonPointer("subagents", handle, "model"), err)
				}
			}
		case SubagentTypeSelf:
			if out.Model != nil && opts.ResolveModelSelection != nil {
				merged := out.Model.ApplyTo(source.Model)
				if _, err := opts.ResolveModelSelection(merged.ProviderConfig, merged.Name); err != nil {
					return nil, issueOr(jsonPointer("subagents", handle, "model"), err)
				}
			}
		default:
			return nil, issuef(
				jsonPointer("subagents", handle, "type"),
				"must be %q or %q",
				SubagentTypeProfile,
				SubagentTypeSelf,
			)
		}
		compiled[handle] = out
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
