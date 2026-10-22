package agentconfig

import (
	"maps"
	"slices"

	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneModelCompiled(value ModelCompiled) ModelCompiled {
	value.ContextWindowTokens = clonePointer(value.ContextWindowTokens)
	value.DefaultMaxOutputTokens = clonePointer(value.DefaultMaxOutputTokens)
	value.Reasoning = clonePointer(value.Reasoning)
	return value
}

func clonePermission(value toolpermission.Selection) toolpermission.Selection {
	value.Parameters = slices.Clone(value.Parameters)
	return value
}

func cloneRuntimeContract(value RuntimeContract) RuntimeContract {
	value.Model = cloneModelCompiled(value.Model)
	value.MaxDepth = clonePointer(value.MaxDepth)
	value.MaxSubagents = clonePointer(value.MaxSubagents)
	value.Skills = slices.Clone(value.Skills)
	value.GitCredentials = clonePointer(value.GitCredentials)
	if value.GitCredentials != nil {
		value.GitCredentials.RepositoryIDs = slices.Clone(value.GitCredentials.RepositoryIDs)
	}
	value.InteractionHandlers = maps.Clone(value.InteractionHandlers)
	value.MachineSources = slices.Clone(value.MachineSources)
	for i := range value.MachineSources {
		machine := &value.MachineSources[i]
		machine.DeleteAfterIdleMinutes = clonePointer(machine.DeleteAfterIdleMinutes)
		machine.MachineCPU = clonePointer(machine.MachineCPU)
		machine.MachineMemoryMB = clonePointer(machine.MachineMemoryMB)
		machine.EnvOverlay = maps.Clone(machine.EnvOverlay)
		for key, v := range machine.EnvOverlay {
			machine.EnvOverlay[key] = clonePointer(v)
		}
		machine.SecretEnvOverlay = maps.Clone(machine.SecretEnvOverlay)
		for key, v := range machine.SecretEnvOverlay {
			machine.SecretEnvOverlay[key] = clonePointer(v)
		}
		machine.MachineProviderOptionsOverlay = maps.Clone(machine.MachineProviderOptionsOverlay)
		for key, v := range machine.MachineProviderOptionsOverlay {
			machine.MachineProviderOptionsOverlay[key] = slices.Clone(v)
		}
	}
	value.Tools = slices.Clone(value.Tools)
	for i := range value.Tools {
		value.Tools[i].Permission = clonePermission(value.Tools[i].Permission)
		value.Tools[i].InputSchema = slices.Clone(value.Tools[i].InputSchema)
	}
	value.IntegrationTools = maps.Clone(value.IntegrationTools)
	for key, tool := range value.IntegrationTools {
		tool.Permission = clonePermission(tool.Permission)
		tool.InputSchema = slices.Clone(tool.InputSchema)
		value.IntegrationTools[key] = tool
	}
	value.Subagents = maps.Clone(value.Subagents)
	for key, subagent := range value.Subagents {
		subagent.Model = clonePointer(subagent.Model)
		if subagent.Model != nil {
			*subagent.Model = cloneModelCompiled(*subagent.Model)
		}
		subagent.MaxInstances = clonePointer(subagent.MaxInstances)
		subagent.ArchiveAfterIdleMinutes = clonePointer(subagent.ArchiveAfterIdleMinutes)
		value.Subagents[key] = subagent
	}
	value.MCPServers = slices.Clone(value.MCPServers)
	for i := range value.MCPServers {
		server := &value.MCPServers[i]
		server.Auth = clonePointer(server.Auth)
		server.Permission = clonePermission(server.Permission)
		server.Tools = maps.Clone(server.Tools)
		for key, tool := range server.Tools {
			tool.Enabled = clonePointer(tool.Enabled)
			tool.Deferred = clonePointer(tool.Deferred)
			tool.Permission = clonePointer(tool.Permission)
			if tool.Permission != nil {
				*tool.Permission = clonePermission(*tool.Permission)
			}
			server.Tools[key] = tool
		}
	}
	return value
}
