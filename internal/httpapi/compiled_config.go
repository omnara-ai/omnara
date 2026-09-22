package httpapi

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
)

func publicCompiledDefinition(raw json.RawMessage) (openapi.CompiledAgentConfig, error) {
	var compiled agentconfig.Compiled
	if err := json.Unmarshal(raw, &compiled); err != nil {
		return openapi.CompiledAgentConfig{}, err
	}
	modelID, err := publicCompiledID(publicid.KindConfiguredModel, compiled.Model.ConfiguredModelID)
	if err != nil {
		return openapi.CompiledAgentConfig{}, err
	}
	response := openapi.CompiledAgentConfig{
		Version: compiled.Version, Instruction: compiled.Instruction,
		MaxDepth: compiled.MaxDepth, MaxSubagents: compiled.MaxSubagents,
		Model: openapi.CompiledAgentModel{
			ConfiguredModelId:      modelID,
			ContextWindowTokens:    compiled.Model.ContextWindowTokens,
			DefaultMaxOutputTokens: compiled.Model.DefaultMaxOutputTokens,
			CacheRetention:         openapi.ModelCacheRetention(compiled.Model.CacheRetention),
			Reasoning:              (*openapi.CompiledModelReasoning)(compiled.Model.Reasoning),
		},
	}
	for _, machine := range compiled.MachineSources {
		source := openapi.CompiledMachineSource{
			MaxMachines: machine.MaxMachines, InitialNumMachines: machine.InitialNumMachines,
			DeleteAfterIdleMinutes: machine.DeleteAfterIdleMinutes, Cwd: machine.Cwd,
			MachineCpu: machine.MachineCPU, MachineMemoryMb: machine.MachineMemoryMB,
			EnvOverlay: machine.EnvOverlay, MachineProviderOptionsOverlay: machine.MachineProviderOptionsOverlay,
			Description: machine.Description,
		}
		if machine.MachineID != uuid.Nil {
			source.MachineId, err = publicCompiledID(publicid.KindMachine, machine.MachineID)
			if err != nil {
				return openapi.CompiledAgentConfig{}, err
			}
		}
		if machine.MachinePoolID != uuid.Nil {
			source.MachinePoolId, err = publicCompiledID(publicid.KindMachinePool, machine.MachinePoolID)
			if err != nil {
				return openapi.CompiledAgentConfig{}, err
			}
		}
		if len(machine.SecretEnvOverlay) > 0 {
			source.SecretEnvOverlay = make(map[string]*publicid.ID, len(machine.SecretEnvOverlay))
			for key, id := range machine.SecretEnvOverlay {
				source.SecretEnvOverlay[key] = nil
				if id != nil {
					encoded, err := publicCompiledID(publicid.KindSecret, *id)
					if err != nil {
						return openapi.CompiledAgentConfig{}, err
					}
					source.SecretEnvOverlay[key] = &encoded
				}
			}
		}
		response.MachineSources = append(response.MachineSources, source)
	}
	if webhook := compiled.EventWebhook; webhook != nil {
		response.EventWebhook = &openapi.CompiledEventWebhook{Url: webhook.URL}
		for _, event := range webhook.Events {
			response.EventWebhook.Events = append(response.EventWebhook.Events, openapi.CompiledEventWebhookEvents(event))
		}
		if webhook.SigningSecretID != uuid.Nil {
			response.EventWebhook.SigningSecretId, err = publicCompiledID(publicid.KindSecret, webhook.SigningSecretID)
			if err != nil {
				return openapi.CompiledAgentConfig{}, err
			}
		}
	}
	response.Tools = make(map[string]openapi.CompiledTool, len(compiled.Tools))
	for name, tool := range compiled.Tools {
		projected := openapi.CompiledTool{
			Enabled: tool.Enabled, Type: openapi.CompiledToolType(tool.Type), Permission: tool.Permission,
			Deferred: tool.Deferred, Description: tool.Description, InputSchema: tool.InputSchema,
		}
		if tool.AppID != uuid.Nil {
			projected.AppId, err = publicCompiledID(publicid.KindProjectApp, tool.AppID)
			if err != nil {
				return openapi.CompiledAgentConfig{}, err
			}
		}
		response.Tools[name] = projected
	}
	response.InteractionHandlers = make(map[string]openapi.CompiledAppCapability, len(compiled.InteractionHandlers))
	for name, capability := range compiled.InteractionHandlers {
		appID, err := publicCompiledID(publicid.KindProjectApp, capability.AppID)
		if err != nil {
			return openapi.CompiledAgentConfig{}, err
		}
		response.InteractionHandlers[name] = openapi.CompiledAppCapability{AppId: appID}
	}
	response.Mcp = make(map[string]openapi.CompiledMCPServer, len(compiled.MCP))
	for name, server := range compiled.MCP {
		mcp := openapi.CompiledMCPServer{
			Url: server.URL, DefaultEnabled: server.DefaultEnabled,
			Permission: server.Permission, Deferred: server.Deferred,
			Tools: make(map[string]openapi.CompiledMCPTool, len(server.Tools)),
		}
		if server.Auth != nil {
			secretID, err := publicCompiledID(publicid.KindSecret, server.Auth.SecretID)
			if err != nil {
				return openapi.CompiledAgentConfig{}, err
			}
			mcp.Auth = &openapi.CompiledMCPAuth{}
			switch server.Auth.Type {
			case agentconfig.MCPAuthTypeBearer:
				err = mcp.Auth.FromCompiledMCPAuthBearer(openapi.CompiledMCPAuthBearer{SecretId: secretID})
			case agentconfig.MCPAuthTypeOAuth:
				err = mcp.Auth.FromCompiledMCPAuthOAuth(openapi.CompiledMCPAuthOAuth{SecretId: secretID})
			case agentconfig.MCPAuthTypeSigV4:
				err = mcp.Auth.FromCompiledMCPAuthSigV4(openapi.CompiledMCPAuthSigV4{
					SecretId: secretID, Service: server.Auth.Service, Region: server.Auth.Region,
				})
			default:
				return openapi.CompiledAgentConfig{}, fmt.Errorf("unsupported compiled MCP auth type %q", server.Auth.Type)
			}
			if err != nil {
				return openapi.CompiledAgentConfig{}, err
			}
		}
		for toolName, tool := range server.Tools {
			mcp.Tools[toolName] = openapi.CompiledMCPTool{
				Enabled: tool.Enabled, Permission: tool.Permission, Deferred: tool.Deferred,
			}
		}
		response.Mcp[name] = mcp
	}
	for _, skill := range compiled.Skills {
		id, err := publicCompiledID(publicid.KindSkill, skill.ID)
		if err != nil {
			return openapi.CompiledAgentConfig{}, err
		}
		response.Skills = append(response.Skills, openapi.CompiledSkill{Id: id})
	}
	response.Subagents = make(map[string]openapi.CompiledSubagent, len(compiled.Subagents))
	for name, subagent := range compiled.Subagents {
		var model *openapi.CompiledSubagentModel
		if subagent.Model != nil {
			model = &openapi.CompiledSubagentModel{
				ContextWindowTokens:    subagent.Model.ContextWindowTokens,
				DefaultMaxOutputTokens: subagent.Model.DefaultMaxOutputTokens,
				CacheRetention:         openapi.ModelCacheRetention(subagent.Model.CacheRetention),
				Reasoning:              (*openapi.CompiledModelReasoning)(subagent.Model.Reasoning),
			}
			if subagent.Model.ConfiguredModelID != uuid.Nil {
				model.ConfiguredModelId, err = publicCompiledID(
					publicid.KindConfiguredModel, subagent.Model.ConfiguredModelID,
				)
				if err != nil {
					return openapi.CompiledAgentConfig{}, err
				}
			}
		}
		var child openapi.CompiledSubagent
		switch subagent.Type {
		case agentconfig.SubagentTypeSelf:
			err = child.FromCompiledSelfSubagent(openapi.CompiledSelfSubagent{
				Model: model, Description: subagent.Description,
				InstructionAppend: subagent.InstructionAppend, MaxInstances: subagent.MaxInstances,
				ArchiveAfterIdleMinutes: subagent.ArchiveAfterIdleMinutes,
			})
		case agentconfig.SubagentTypeProfile:
			var profileID publicid.ID
			profileID, err = publicCompiledID(publicid.KindAgentProfile, subagent.ProfileID)
			if err != nil {
				return openapi.CompiledAgentConfig{}, err
			}
			err = child.FromCompiledProfileSubagent(openapi.CompiledProfileSubagent{
				ProfileId: profileID, Model: model, Description: subagent.Description,
				InstructionAppend: subagent.InstructionAppend, MaxInstances: subagent.MaxInstances,
				ArchiveAfterIdleMinutes: subagent.ArchiveAfterIdleMinutes,
			})
		default:
			return openapi.CompiledAgentConfig{}, fmt.Errorf("unsupported compiled subagent type %q", subagent.Type)
		}
		if err != nil {
			return openapi.CompiledAgentConfig{}, err
		}
		response.Subagents[name] = child
	}
	return response, nil
}

func publicCompiledID(kind publicid.Kind, id uuid.UUID) (publicid.ID, error) {
	encoded, err := publicid.Encode(kind, id)
	return publicid.ID(encoded), err
}
