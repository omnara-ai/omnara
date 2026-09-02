package agentconfig

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
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
	child.Tools = toolsWithEncodablePermissions(base.Tools)
	child.MCP = mcpWithEncodablePermissions(base.MCP)
	if handle.Type == SubagentTypeSelf {
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
	return child
}

// Selections decoded from a source omit parameters when the document did, and
// an empty RawMessage re-encodes as null, which the schema rejects.
func encodablePermission(selection *toolpermission.Selection) *toolpermission.Selection {
	if selection == nil {
		return nil
	}
	copied := *selection
	if len(copied.Parameters) == 0 {
		copied.Parameters = json.RawMessage(`{}`)
	}
	return &copied
}

func toolsWithEncodablePermissions(tools map[string]AgentConfigToolSource) map[string]AgentConfigToolSource {
	if len(tools) == 0 {
		return nil
	}
	out := make(map[string]AgentConfigToolSource, len(tools))
	for name, tool := range tools {
		tool.Permission = encodablePermission(tool.Permission)
		out[name] = tool
	}
	return out
}

func mcpWithEncodablePermissions(servers map[string]AgentConfigMCPSource) map[string]AgentConfigMCPSource {
	if len(servers) == 0 {
		return nil
	}
	out := make(map[string]AgentConfigMCPSource, len(servers))
	for key, server := range servers {
		server.Permission = encodablePermission(server.Permission)
		if len(server.Tools) > 0 {
			tools := make(map[string]AgentConfigMCPToolSource, len(server.Tools))
			for name, tool := range server.Tools {
				tool.Permission = encodablePermission(tool.Permission)
				tools[name] = tool
			}
			server.Tools = tools
		}
		out[key] = server
	}
	return out
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
