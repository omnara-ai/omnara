package agentconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

type RuntimeContract struct {
	Instruction         string
	Model               ModelCompiled
	MachineSources      []RuntimeMachine
	Tools               []RuntimeTool
	MCPServers          []RuntimeMCPServer
	AppTools            map[string]ToolCompiled
	InteractionHandlers map[string]AppCapabilityCompiled
	Skills              []SkillCompiled
	Subagents           map[string]SubagentCompiled
	MaxSubagents        *int
	MaxDepth            *int
}

func (contract RuntimeContract) SubagentDepthLimit() int {
	return SubagentDepth{MaxDepth: contract.MaxDepth}.Limit()
}

func (contract RuntimeContract) SubagentKeys() []string {
	return slices.Sorted(maps.Keys(contract.Subagents))
}

func (contract RuntimeContract) RequiresModelToolSupport() bool {
	for _, tool := range contract.AppTools {
		if tool.Enabled {
			return true
		}
	}
	return len(contract.Tools) > 0 || len(contract.MCPServers) > 0
}

type RuntimeTool struct {
	Name        string
	Type        string
	Permission  toolpermission.Selection
	Deferred    bool
	Description string
	InputSchema json.RawMessage
}

type RuntimeMachine struct {
	MachineID                     uuid.UUID
	MachinePoolID                 uuid.UUID
	MaxMachines                   int
	InitialNumMachines            int
	DeleteAfterIdleMinutes        *int
	Cwd                           string
	MachineCPU                    *int
	MachineMemoryMB               *int
	EnvOverlay                    map[string]*string
	SecretEnvOverlay              map[string]*uuid.UUID
	MachineProviderOptionsOverlay map[string]json.RawMessage
	Description                   string
}

func RuntimeContractFromCompiled(
	compiledJSON json.RawMessage,
	definitionHash string,
) (RuntimeContract, error) {
	if len(compiledJSON) == 0 {
		return RuntimeContract{}, errors.New("agent config compiled definition is required")
	}
	canonical := canonicalizeJSON(compiledJSON)
	sum := sha256.Sum256(canonical)
	if got := hex.EncodeToString(sum[:]); got != definitionHash {
		return RuntimeContract{}, fmt.Errorf(
			"agent config definition hash mismatch: got %s want %s",
			got,
			definitionHash,
		)
	}
	var compiled Compiled
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&compiled); err != nil {
		return RuntimeContract{}, fmt.Errorf("parse compiled agent config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return RuntimeContract{}, errors.New("parse compiled agent config: trailing JSON value")
		}
		return RuntimeContract{}, fmt.Errorf("parse compiled agent config: %w", err)
	}
	if err := validateCompiledApps(compiled); err != nil {
		return RuntimeContract{}, fmt.Errorf("compiled app capabilities: %w", err)
	}
	tools, err := runtimeTools(compiled.Tools)
	if err != nil {
		return RuntimeContract{}, err
	}
	mcpServers, err := runtimeMCPServers(compiled.MCP)
	if err != nil {
		return RuntimeContract{}, err
	}
	contract := RuntimeContract{
		Instruction:         compiled.Instruction,
		Model:               compiled.Model,
		MachineSources:      runtimeMachineSources(compiled.MachineSources),
		Tools:               tools,
		MCPServers:          mcpServers,
		AppTools:            appToolsFromCompiled(compiled),
		InteractionHandlers: compiled.InteractionHandlers,
		Skills:              compiled.Skills,
		Subagents:           compiled.Subagents,
		MaxSubagents:        compiled.MaxSubagents,
		MaxDepth:            compiled.MaxDepth,
	}
	return contract, nil
}

func (contract RuntimeContract) DefersAnyTool() bool {
	for _, tool := range contract.AppTools {
		if tool.Enabled && tool.Deferred {
			return true
		}
	}
	for _, tool := range contract.Tools {
		if tool.Deferred {
			return true
		}
	}
	for _, server := range contract.MCPServers {
		if server.DefersAnyTool() {
			return true
		}
	}
	return false
}

func runtimeMachineSources(compiled []MachineSourceCompiled) []RuntimeMachine {
	if len(compiled) == 0 {
		return nil
	}
	machines := make([]RuntimeMachine, 0, len(compiled))
	for _, machine := range compiled {
		machines = append(machines, RuntimeMachine(machine))
	}
	return machines
}

func runtimeTools(compiled map[string]ToolCompiled) ([]RuntimeTool, error) {
	if len(compiled) == 0 {
		return nil, nil
	}
	catalog, err := toolcatalog.Default()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(compiled))
	for name := range compiled {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]RuntimeTool, 0, len(names))
	for _, name := range names {
		tool := compiled[name]
		if toolcatalog.UsesAppToolNamespace(name) {
			continue
		}
		entry, builtInName := catalog.Lookup(name)
		if err := validateRuntimeTool(name, tool, entry, builtInName); err != nil {
			return nil, err
		}
		if !tool.Enabled {
			continue
		}
		if tool.Type == toolcatalog.ToolTypeCustom {
			out = append(out, RuntimeTool{
				Name:        name,
				Type:        toolcatalog.ToolTypeCustom,
				Permission:  tool.Permission,
				Deferred:    tool.Deferred,
				Description: tool.Description,
				InputSchema: tool.InputSchema,
			})
			continue
		}
		if !builtInName {
			return nil, fmt.Errorf("compiled tool %q is not registered", name)
		}
		runtime := runtimeBuiltInTool(entry, tool.Permission)
		runtime.Deferred = tool.Deferred
		out = append(out, runtime)
	}
	return out, nil
}

func runtimeBuiltInTool(
	entry toolcatalog.Entry,
	permission toolpermission.Selection,
) RuntimeTool {
	return RuntimeTool{
		Name:        entry.Name,
		Type:        toolcatalog.ToolTypeBuiltIn,
		Permission:  permission,
		Description: entry.Description,
		InputSchema: entry.InputSchema,
	}
}

func validateRuntimeTool(
	name string,
	tool ToolCompiled,
	entry toolcatalog.Entry,
	builtInName bool,
) error {
	if tool.Type != "" && tool.Type != toolcatalog.ToolTypeBuiltIn && tool.Type != toolcatalog.ToolTypeCustom {
		return fmt.Errorf("compiled tool %q has unsupported type", name)
	}
	if tool.Type == toolcatalog.ToolTypeCustom {
		if toolcatalog.UsesMCPRuntimeNamespace(name) || toolcatalog.UsesAppToolNamespace(name) {
			return fmt.Errorf("compiled custom tool %q uses a reserved app or MCP tool namespace", name)
		}
		if toolcatalog.IsReservedWireToolName(name) {
			return fmt.Errorf("compiled custom tool %q uses a reserved name", name)
		}
		if builtInName {
			return fmt.Errorf("compiled custom tool %q collides with a built-in tool", name)
		}
		if _, err := toolpermission.ValidateSelection(
			tool.Permission,
			toolcatalog.CustomToolPermissionModes(),
		); err != nil {
			return fmt.Errorf("compiled custom tool %q permission: %w", name, err)
		}
		return nil
	}
	if !builtInName {
		return fmt.Errorf("compiled tool %q is not registered", name)
	}
	if tool.Deferred && name == toolcatalog.ToolNameToolSearch {
		return fmt.Errorf("compiled built-in tool %q cannot be deferred", name)
	}
	if _, err := toolpermission.ValidateSelection(
		tool.Permission,
		entry.PermissionModes,
	); err != nil {
		return fmt.Errorf("compiled built-in tool %q permission: %w", name, err)
	}
	return nil
}
