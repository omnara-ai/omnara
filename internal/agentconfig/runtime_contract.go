package agentconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

type RuntimeContract struct {
	Instruction    string
	Model          ModelCompiled
	MachineSources []RuntimeMachine
	Tools          []RuntimeTool
	MCPServers     []RuntimeMCPServer
	Skills         []SkillCompiled
}

func (contract RuntimeContract) RequiresModelToolSupport() bool {
	return len(contract.Tools) > 0 || len(contract.MCPServers) > 0
}

type RuntimeTool struct {
	Name        string
	Type        string
	Permission  toolpermission.Selection
	Description string
	InputSchema json.RawMessage
}

type RuntimeMachine struct {
	MachineID                     string
	MachinePoolID                 string
	MaxMachines                   int
	InitialNumMachines            int
	Cwd                           string
	MachineCPU                    *int
	MachineMemoryMB               *int
	EnvOverlay                    map[string]*string
	SecretEnvOverlay              map[string]*string
	MachineProviderOptionsOverlay map[string]json.RawMessage
	Description                   string
}

func RuntimeContractFromCompiled(
	compiledJSON json.RawMessage,
	compilerVersion string,
	definitionHash string,
) (RuntimeContract, error) {
	if len(compiledJSON) == 0 {
		return RuntimeContract{}, errors.New("agent config compiled definition is required")
	}
	if compilerVersion != CompilerVersion {
		return RuntimeContract{}, fmt.Errorf("agent config compiler contract %q is not supported", compilerVersion)
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
	tools, err := runtimeTools(compiled.Tools)
	if err != nil {
		return RuntimeContract{}, err
	}
	mcpServers, err := runtimeMCPServers(compiled.MCP)
	if err != nil {
		return RuntimeContract{}, err
	}
	if implicitlyEnablesSkillTool(compiled) {
		catalog, err := toolcatalog.Default()
		if err != nil {
			return RuntimeContract{}, err
		}
		entry, ok := catalog.Lookup(toolcatalog.ToolNameSkill)
		if !ok {
			return RuntimeContract{}, errors.New("skill tool is not registered")
		}
		tools = append(tools, runtimeBuiltInTool(entry, entry.DefaultPermission))
	}
	tools, err = appendArtifactRetrievalTools(compiled, tools, len(mcpServers) > 0)
	if err != nil {
		return RuntimeContract{}, err
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return RuntimeContract{
		Instruction:    compiled.Instruction,
		Model:          compiled.Model,
		MachineSources: runtimeMachineSources(compiled.MachineSources),
		Tools:          tools,
		MCPServers:     mcpServers,
		Skills:         compiled.Skills,
	}, nil
}

func appendArtifactRetrievalTools(
	compiled Compiled,
	tools []RuntimeTool,
	hasMCP bool,
) ([]RuntimeTool, error) {
	hasCapability := hasMCP
	for _, tool := range tools {
		if tool.Name != toolcatalog.ToolNameReadArtifact &&
			tool.Name != toolcatalog.ToolNameSearchArtifact {
			hasCapability = true
			break
		}
	}
	if !hasCapability {
		return tools, nil
	}
	catalog, err := toolcatalog.Default()
	if err != nil {
		return nil, err
	}
	for _, name := range []string{
		toolcatalog.ToolNameReadArtifact,
		toolcatalog.ToolNameSearchArtifact,
	} {
		if configured, explicitlyConfigured := compiled.Tools[name]; explicitlyConfigured {
			// runtimeTools already included an explicitly enabled entry and
			// deliberately omitted an explicitly disabled one.
			if !configured.Enabled {
				continue
			}
		} else {
			entry, ok := catalog.Lookup(name)
			if !ok {
				return nil, fmt.Errorf("%s tool is not registered", name)
			}
			tools = append(tools, runtimeBuiltInTool(entry, entry.DefaultPermission))
		}
	}
	return tools, nil
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
				Description: tool.Description,
				InputSchema: tool.InputSchema,
			})
			continue
		}
		if !builtInName {
			return nil, fmt.Errorf("compiled tool %q is not registered", name)
		}
		out = append(out, runtimeBuiltInTool(entry, tool.Permission))
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
	if tool.Type == toolcatalog.ToolTypeCustom {
		if toolcatalog.UsesMCPRuntimeNamespace(name) {
			return fmt.Errorf("compiled custom tool %q uses the reserved MCP tool namespace", name)
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
	if _, err := toolpermission.ValidateSelection(
		tool.Permission,
		entry.PermissionModes,
	); err != nil {
		return fmt.Errorf("compiled built-in tool %q permission: %w", name, err)
	}
	return nil
}
