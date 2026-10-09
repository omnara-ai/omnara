package agentconfig

import (
	"bytes"
	"encoding/json"
	"maps"
	"reflect"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"gopkg.in/yaml.v3"
)

type SourceNames struct {
	Model       func(configuredModelID uuid.UUID) (providerConfig, name string, err error)
	Machine     func(uuid.UUID) (string, error)
	MachinePool func(uuid.UUID) (string, error)
	Profile     func(uuid.UUID) (string, error)
	MemoryStore func(uuid.UUID) (string, error)
}

func SourceFromCompiled(compiled Compiled, names SourceNames) (AgentConfigSource, error) {
	providerConfig, modelName, err := names.Model(compiled.Model.ConfiguredModelID)
	try := func(value string, valueErr error) string {
		if err == nil {
			err = valueErr
		}
		return value
	}
	source := AgentConfigSource{
		Version:     compiled.Version,
		Instruction: compiled.Instruction,
		Model: AgentConfigModelSource{
			ProviderConfig:         providerConfig,
			Name:                   modelName,
			ContextWindowTokens:    compiled.Model.ContextWindowTokens,
			DefaultMaxOutputTokens: compiled.Model.DefaultMaxOutputTokens,
			CacheRetention:         compiled.Model.CacheRetention,
			Reasoning:              (*AgentConfigModelReasoningSource)(compiled.Model.Reasoning),
		},
		MCP:                 make(map[string]AgentConfigMCPSource, len(compiled.MCP)),
		InteractionHandlers: make(map[string]AgentConfigIntegrationCapabilitySource, len(compiled.InteractionHandlers)),
		Subagents:           make(map[string]AgentConfigSubagentSource, len(compiled.Subagents)),
		MaxSubagents:        compiled.MaxSubagents,
		MaxDepth:            compiled.MaxDepth,
	}
	if webhook := compiled.EventWebhook; webhook != nil {
		source.EventWebhook = &EventWebhook{URL: webhook.URL, Events: webhook.Events}
		if webhook.SigningSecretID != uuid.Nil {
			source.EventWebhook.SigningSecretID = try(publicid.Encode(publicid.KindSecret, webhook.SigningSecretID))
		}
	}
	for _, machine := range compiled.MachineSources {
		entry := AgentConfigMachineSource{
			DeleteAfterIdleMinutes:        machine.DeleteAfterIdleMinutes,
			Cwd:                           machine.Cwd,
			MachineCPU:                    machine.MachineCPU,
			MachineMemoryMB:               machine.MachineMemoryMB,
			EnvOverlay:                    machine.EnvOverlay,
			SecretEnvOverlay:              make(map[string]*string, len(machine.SecretEnvOverlay)),
			MachineProviderOptionsOverlay: machine.MachineProviderOptionsOverlay,
			Description:                   machine.Description,
		}
		if machine.MachineID != uuid.Nil {
			entry.MachineName = try(names.Machine(machine.MachineID))
		} else {
			entry.MachinePoolName = try(names.MachinePool(machine.MachinePoolID))
			entry.MaxMachines, entry.InitialNumMachines = &machine.MaxMachines, &machine.InitialNumMachines
		}
		for key, secretID := range machine.SecretEnvOverlay {
			entry.SecretEnvOverlay[key] = nil
			if secretID != nil {
				entry.SecretEnvOverlay[key] = new(try(publicid.Encode(publicid.KindSecret, *secretID)))
			}
		}
		source.MachineSources = append(source.MachineSources, entry)
	}
	for key, server := range compiled.MCP {
		entry := AgentConfigMCPSource{
			URL:      server.URL,
			Deferred: server.Deferred,
			Tools:    make(map[string]AgentConfigMCPToolSource, len(server.Tools)),
		}
		if !server.DefaultEnabled {
			entry.DefaultEnabled = new(false)
		}
		if !reflect.DeepEqual(server.Permission, toolcatalog.DefaultMCPToolPermission()) {
			entry.Permission = &server.Permission
		}
		if auth := server.Auth; auth != nil {
			entry.Auth = &AgentConfigMCPAuthSource{
				Type:     auth.Type,
				SecretID: try(publicid.Encode(publicid.KindSecret, auth.SecretID)),
				Service:  auth.Service,
				Region:   auth.Region,
			}
		}
		for name, tool := range server.Tools {
			entry.Tools[name] = AgentConfigMCPToolSource(tool)
		}
		source.MCP[key] = entry
	}
	for name := range compiled.InteractionHandlers {
		source.InteractionHandlers[name] = AgentConfigIntegrationCapabilitySource{}
	}
	if compiled.GitCredentials != nil {
		source.GitCredentials = &GitCredentialsSource{Integration: compiled.GitCredentials.Integration}
	}
	for _, skill := range compiled.Skills {
		source.Skills = append(source.Skills, try(publicid.Encode(publicid.KindSkill, skill.ID)))
	}
	for key, subagent := range compiled.Subagents {
		entry := AgentConfigSubagentSource{
			Type:                    subagent.Type,
			Description:             subagent.Description,
			MaxInstances:            subagent.MaxInstances,
			ArchiveAfterIdleMinutes: subagent.ArchiveAfterIdleMinutes,
		}
		if subagent.InstructionAppend != "" {
			entry.Instruction = &AgentConfigSubagentInstructionSource{Append: subagent.InstructionAppend}
		}
		if subagent.ProfileID != uuid.Nil {
			entry.Profile = try(names.Profile(subagent.ProfileID))
		}
		if model := subagent.Model; model != nil {
			entry.Model = &AgentConfigSubagentModelSource{
				ContextWindowTokens:    model.ContextWindowTokens,
				DefaultMaxOutputTokens: model.DefaultMaxOutputTokens,
				CacheRetention:         model.CacheRetention,
				Reasoning:              (*AgentConfigModelReasoningSource)(model.Reasoning),
			}
			if model.ConfiguredModelID != uuid.Nil && err == nil {
				entry.Model.ProviderConfig, entry.Model.Name, err = names.Model(model.ConfiguredModelID)
			}
		}
		source.Subagents[key] = entry
	}
	for _, store := range compiled.MemoryStores {
		source.MemoryStores = append(source.MemoryStores, MemoryStoreSource{
			Name:   try(names.MemoryStore(store.ID)),
			Access: store.Access,
		})
	}
	if err != nil {
		return AgentConfigSource{}, err
	}
	source.Tools, err = toolSourcesFromCompiled(compiled.Tools, source)
	return source, err
}

func toolSourcesFromCompiled(
	compiled map[string]ToolCompiled,
	source AgentConfigSource,
) (map[string]AgentConfigToolSource, error) {
	catalog, err := toolcatalog.Default()
	if err != nil {
		return nil, err
	}
	tools := make(map[string]AgentConfigToolSource, len(compiled))
	for name, tool := range compiled {
		entry := AgentConfigToolSource{Deferred: tool.Deferred}
		if !tool.Enabled {
			entry.Enabled = new(false)
		}
		var defaultPermission *toolpermission.Selection
		switch {
		case toolcatalog.UsesIntegrationToolNamespace(name):
		case tool.Type == toolcatalog.ToolTypeCustom:
			entry.Type, entry.Description = tool.Type, tool.Description
			if len(tool.InputSchema) > 0 {
				if err := json.Unmarshal(tool.InputSchema, &entry.InputSchema); err != nil {
					return nil, err
				}
			}
			defaultPermission = new(toolcatalog.DefaultCustomToolPermission())
		default:
			if catalogEntry, ok := catalog.Lookup(name); ok {
				defaultPermission = &catalogEntry.DefaultPermission
			}
		}
		if defaultPermission == nil || !reflect.DeepEqual(tool.Permission, *defaultPermission) {
			entry.Permission = &tool.Permission
		}
		tools[name] = entry
	}
	trimmed := source
	trimmed.Tools = maps.Clone(tools)
	maps.DeleteFunc(trimmed.Tools, func(_ string, entry AgentConfigToolSource) bool {
		return reflect.DeepEqual(entry, AgentConfigToolSource{})
	})
	for _, name := range missingDefaultToolNames(trimmed) {
		delete(tools, name)
	}
	return tools, nil
}

func EncodeSourceYAML(source AgentConfigSource) (string, error) {
	raw, err := json.Marshal(source)
	if err != nil {
		return "", err
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return out.String(), nil
}
