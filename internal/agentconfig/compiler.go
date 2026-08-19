package agentconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

const CompilerVersion = ""

const (
	defaultPoolMaxMachines        = 1
	defaultPoolInitialNumMachines = 1
)

type Compiled struct {
	Version        string                       `json:"version,omitempty"`
	Instruction    string                       `json:"instruction"`
	Model          ModelCompiled                `json:"model,omitempty"`
	MachineSources []MachineSourceCompiled      `json:"machine_sources,omitempty"`
	Tools          map[string]ToolCompiled      `json:"tools,omitempty"`
	MCP            map[string]MCPServerCompiled `json:"mcp,omitempty"`
	Skills         []SkillCompiled              `json:"skills,omitempty"`
}

// SkillCompiled pins a skill's identity into the agent contract. Only the
// public id is captured: name and description belong to the skill's latest
// revision and are resolved at model-call time, so updated skills reach
// agents without recompiling their configs.
type SkillCompiled struct {
	PublicID string `json:"public_id"`
}

// SkillResolution is what a ResolveSkillID callback returns at compile time.
// Name is used only for compile-time duplicate detection and is not stored
// in the compiled contract.
type SkillResolution struct {
	PublicID string
	Name     string
}

type ModelCompiled struct {
	ConfiguredModelID      string                  `json:"configured_model_id,omitempty"`
	ContextWindowTokens    *int                    `json:"context_window_tokens,omitempty"`
	DefaultMaxOutputTokens *int                    `json:"default_max_output_tokens,omitempty"`
	CacheRetention         string                  `json:"cache_retention,omitempty"`
	Reasoning              *ModelReasoningCompiled `json:"reasoning,omitempty"`
}

type ModelReasoningCompiled struct {
	Effort string `json:"effort"`
}

type ModelOverrides struct {
	ContextWindowTokens    *int
	DefaultMaxOutputTokens *int
	CacheRetention         string
	ReasoningEffort        string
}

func (m ModelCompiled) Overrides() ModelOverrides {
	reasoningEffort := ""
	if m.Reasoning != nil {
		reasoningEffort = m.Reasoning.Effort
	}
	return ModelOverrides{
		ContextWindowTokens:    m.ContextWindowTokens,
		DefaultMaxOutputTokens: m.DefaultMaxOutputTokens,
		CacheRetention:         m.CacheRetention,
		ReasoningEffort:        reasoningEffort,
	}
}

type compiledModelResult struct {
	configuredModelID    string
	model                ModelCompiled
	sourceProviderConfig string
	sourceName           string
	supportsTools        *bool
}

type MachineSourceCompiled struct {
	MachineID                     string                     `json:"machine_id,omitempty"`
	MachinePoolID                 string                     `json:"machine_pool_id,omitempty"`
	MaxMachines                   int                        `json:"max_machines,omitempty"`
	InitialNumMachines            int                        `json:"initial_num_machines,omitempty"`
	Cwd                           string                     `json:"cwd,omitempty"`
	MachineCPU                    *int                       `json:"machine_cpu,omitempty"`
	MachineMemoryMB               *int                       `json:"machine_memory_mb,omitempty"`
	EnvOverlay                    map[string]*string         `json:"env_overlay,omitempty"`
	SecretEnvOverlay              map[string]*string         `json:"secret_env_overlay,omitempty"`
	MachineProviderOptionsOverlay map[string]json.RawMessage `json:"machine_provider_options_overlay,omitempty"`
	Description                   string                     `json:"description,omitempty"`
}

type ToolCompiled struct {
	Enabled     bool                     `json:"enabled"`
	Type        string                   `json:"type,omitempty"`
	Permission  toolpermission.Selection `json:"permission"`
	Description string                   `json:"description,omitempty"`
	InputSchema json.RawMessage          `json:"input_schema,omitempty"`
}

type MCPServerCompiled struct {
	URL            string                     `json:"url"`
	Auth           *MCPAuthCompiled           `json:"auth,omitempty"`
	DefaultEnabled bool                       `json:"default_enabled"`
	Permission     toolpermission.Selection   `json:"permission"`
	Tools          map[string]MCPToolCompiled `json:"tools,omitempty"`
}

type MCPAuthCompiled struct {
	Type     string `json:"type"`
	SecretID string `json:"secret_id"`
	Service  string `json:"service,omitempty"`
	Region   string `json:"region,omitempty"`
}

type MCPToolCompiled struct {
	Enabled    *bool                     `json:"enabled,omitempty"`
	Permission *toolpermission.Selection `json:"permission,omitempty"`
}

// Result is the complete compiler output for one agent config source. It is
// the only intended input to agent config writes, so persisted compiled
// state always corresponds to a source that passed compilation.
type Result struct {
	Compiled        Compiled
	CanonicalJSON   []byte
	Hash            string
	Source          string
	SourceFormat    SourceFormat
	CompilerVersion string
}

type CompileOptions struct {
	AllowInsecureLocalMCPHTTP bool
	ResolveModelSelection     func(providerConfig string, configuredModelName string) (ResolvedModelSelection, error)
	ValidateSecretID          func(secretID string, expectedKind secrets.Kind) error
	ResolveMachineName        func(machineName string) (string, error)
	ResolveMachinePoolName    func(machinePoolName string) (string, error)
	ResolveSkillID            func(skillID string) (SkillResolution, error)
}

type ResolvedModelSelection struct {
	ConfiguredModelID string
	SupportsTools     *bool
}

func Compile(format SourceFormat, raw []byte, opts CompileOptions) (Result, error) {
	source, err := ParseSource(format, raw)
	if err != nil {
		return Result{}, err
	}
	compiled, err := compile(source, opts)
	if err != nil {
		return Result{}, err
	}
	canonical, err := json.Marshal(compiled)
	if err != nil {
		return Result{}, fmt.Errorf("marshal compiled agent config: %w", err)
	}
	canonical = canonicalizeJSON(canonical)
	sum := sha256.Sum256(canonical)
	return Result{
		Compiled:        compiled,
		CanonicalJSON:   canonical,
		Hash:            hex.EncodeToString(sum[:]),
		Source:          string(raw),
		SourceFormat:    format,
		CompilerVersion: CompilerVersion,
	}, nil
}

func canonicalizeJSON(raw []byte) []byte {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return raw
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return canonical
}

func compile(source AgentConfigSource, opts CompileOptions) (Compiled, error) {
	compiledModel, err := compileModel(source.Model, opts)
	if err != nil {
		return Compiled{}, err
	}
	compiled := Compiled{
		Version:     source.Version,
		Instruction: strings.TrimSpace(source.Instruction),
		Model:       compiledModel.model,
	}
	machines, err := compileMachineSources(source.MachineSources, opts)
	if err != nil {
		return Compiled{}, err
	}
	if len(machines) > 0 {
		compiled.MachineSources = machines
	}
	if len(source.Tools) > 0 {
		catalog, err := toolcatalog.Default()
		if err != nil {
			return Compiled{}, err
		}
		compiled.Tools = make(map[string]ToolCompiled, len(source.Tools))
		for name, tool := range source.Tools {
			enabled := true
			if tool.Enabled != nil {
				enabled = *tool.Enabled
			}
			var compiledTool ToolCompiled
			var err error
			if tool.Type == toolcatalog.ToolTypeCustom {
				compiledTool, err = compileCustomTool(name, tool, enabled, catalog)
			} else {
				compiledTool, err = compileBuiltInTool(name, tool, enabled, catalog)
			}
			if err != nil {
				return Compiled{}, err
			}
			compiled.Tools[name] = compiledTool
		}
	}
	if len(source.MCP) > 0 {
		mcpServers, err := compileMCPServers(source.MCP, opts)
		if err != nil {
			return Compiled{}, err
		}
		compiled.MCP = mcpServers
	}
	if len(source.Skills) > 0 {
		skills, err := compileSkills(source.Skills, opts)
		if err != nil {
			return Compiled{}, err
		}
		compiled.Skills = skills
	}
	if compiledModel.supportsTools != nil && !*compiledModel.supportsTools && requiresModelToolSupport(compiled) {
		return Compiled{}, fmt.Errorf("model %q does not support tools", compiledModel.sourceName)
	}
	return compiled, nil
}

func compileModel(source AgentConfigModelSource, opts CompileOptions) (compiledModelResult, error) {
	if strings.TrimSpace(source.ProviderConfig) == "" {
		return compiledModelResult{}, errors.New("model.provider_config is required")
	}
	if strings.TrimSpace(source.Name) == "" {
		return compiledModelResult{}, errors.New("configured model name is required")
	}
	compiled := compiledModelResult{
		sourceProviderConfig: strings.TrimSpace(source.ProviderConfig),
		sourceName:           strings.TrimSpace(source.Name),
	}
	compiled.model = ModelCompiled{
		ContextWindowTokens:    source.ContextWindowTokens,
		DefaultMaxOutputTokens: source.DefaultMaxOutputTokens,
		CacheRetention:         strings.TrimSpace(source.CacheRetention),
	}
	if source.Reasoning != nil {
		compiled.model.Reasoning = &ModelReasoningCompiled{Effort: strings.TrimSpace(source.Reasoning.Effort)}
	}
	if opts.ResolveModelSelection == nil {
		return compiled, nil
	}
	resolved, err := opts.ResolveModelSelection(compiled.sourceProviderConfig, compiled.sourceName)
	if err != nil {
		return compiledModelResult{}, err
	}
	compiled.configuredModelID = resolved.ConfiguredModelID
	compiled.model.ConfiguredModelID = resolved.ConfiguredModelID
	compiled.supportsTools = resolved.SupportsTools
	return compiled, nil
}

func requiresModelToolSupport(compiled Compiled) bool {
	for _, tool := range compiled.Tools {
		if tool.Enabled {
			return true
		}
	}
	if len(compiled.MCP) > 0 {
		return true
	}
	return implicitlyEnablesSkillTool(compiled)
}

func implicitlyEnablesSkillTool(compiled Compiled) bool {
	_, skillConfigured := compiled.Tools[toolcatalog.ToolNameSkill]
	return len(compiled.Skills) > 0 && !skillConfigured
}

// compileSkills validates and pins the attached skill set. Skills do not
// require machine sources: a skill can be pure SKILL.md instructions, and the
// skill tool installs supporting files only on whatever machines are attached
// at invocation time.
func compileSkills(skillIDs []string, opts CompileOptions) ([]SkillCompiled, error) {
	if opts.ResolveSkillID == nil {
		return nil, fmt.Errorf("skills require a ResolveSkillID callback")
	}
	resolved := make([]SkillResolution, 0, len(skillIDs))
	for i, skillID := range skillIDs {
		rec, err := opts.ResolveSkillID(skillID)
		if err != nil {
			return nil, fmt.Errorf("skills[%d]: %w", i, err)
		}
		if rec.PublicID == "" || rec.Name == "" {
			return nil, fmt.Errorf("skills[%d]: resolver returned incomplete record", i)
		}
		resolved = append(resolved, rec)
	}
	seenNames := make(map[string]string, len(resolved))
	compiledSkills := make([]SkillCompiled, 0, len(resolved))
	for _, rec := range resolved {
		if existing, ok := seenNames[rec.Name]; ok {
			return nil, fmt.Errorf(
				"skills: name %q is attached more than once (skills %s and %s); "+
					"skill names must be unique across the agent's attached set",
				rec.Name,
				existing,
				rec.PublicID,
			)
		}
		seenNames[rec.Name] = rec.PublicID
		compiledSkills = append(compiledSkills, SkillCompiled{PublicID: rec.PublicID})
	}
	return compiledSkills, nil
}

func compileBuiltInTool(
	name string,
	source AgentConfigToolSource,
	enabled bool,
	catalog toolcatalog.Catalog,
) (ToolCompiled, error) {
	entry, ok := catalog.Lookup(name)
	if !ok {
		return ToolCompiled{}, fmt.Errorf("tool %q is not registered", name)
	}
	permission := entry.DefaultPermission
	if source.Permission != nil {
		var err error
		permission, err = toolpermission.ValidateSelection(*source.Permission, entry.PermissionModes)
		if err != nil {
			return ToolCompiled{}, fmt.Errorf("tool %q permission: %w", name, err)
		}
	}
	compiled := ToolCompiled{
		Enabled:    enabled,
		Permission: permission,
	}
	return compiled, nil
}

func compileCustomTool(
	name string,
	source AgentConfigToolSource,
	enabled bool,
	catalog toolcatalog.Catalog,
) (ToolCompiled, error) {
	if toolcatalog.UsesMCPRuntimeNamespace(name) {
		return ToolCompiled{}, fmt.Errorf("tool %q: custom tool name uses the reserved MCP tool namespace", name)
	}
	if _, ok := catalog.Lookup(name); ok {
		return ToolCompiled{}, fmt.Errorf("tool %q: custom tool name collides with a built-in tool", name)
	}
	schema, err := valueToCanonicalJSON(source.InputSchema)
	if err != nil {
		return ToolCompiled{}, fmt.Errorf("tool %q input_schema: %w", name, err)
	}
	if err := validateCustomInputSchema(schema); err != nil {
		return ToolCompiled{}, fmt.Errorf("tool %q input_schema: %w", name, err)
	}
	permission := toolcatalog.DefaultCustomToolPermission()
	if source.Permission != nil {
		permission, err = toolpermission.ValidateSelection(*source.Permission, toolcatalog.CustomToolPermissionModes())
		if err != nil {
			return ToolCompiled{}, fmt.Errorf("tool %q permission: %w", name, err)
		}
	}
	return ToolCompiled{
		Enabled:     enabled,
		Type:        toolcatalog.ToolTypeCustom,
		Permission:  permission,
		Description: strings.TrimSpace(source.Description),
		InputSchema: schema,
	}, nil
}

func valueToCanonicalJSON(value any) (json.RawMessage, error) {
	rawJSON, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal JSON: %w", err)
	}
	return json.RawMessage(canonicalizeJSON(rawJSON)), nil
}

func compileMachineSources(sources []AgentConfigMachineSource, opts CompileOptions) ([]MachineSourceCompiled, error) {
	if len(sources) == 0 {
		return nil, nil
	}
	machines := make([]MachineSourceCompiled, 0, len(sources))
	seenSources := map[string]bool{}
	for index, source := range sources {
		machine, err := compileMachineSource(source, index, opts)
		if err != nil {
			return nil, err
		}
		if machine.MachineID != "" {
			if seenSources[machine.MachineID] {
				return nil, fmt.Errorf("machine_sources[%d] duplicates a machine id", index)
			}
			seenSources[machine.MachineID] = true
		}
		if machine.MachinePoolID != "" {
			if seenSources[machine.MachinePoolID] {
				return nil, fmt.Errorf("machine_sources[%d] duplicates a machine pool id", index)
			}
			seenSources[machine.MachinePoolID] = true
		}
		machines = append(machines, machine)
	}
	return machines, nil
}

func compileMachineSource(
	source AgentConfigMachineSource,
	index int,
	opts CompileOptions,
) (MachineSourceCompiled, error) {
	machineName := strings.TrimSpace(source.MachineName)
	machinePoolName := strings.TrimSpace(source.MachinePoolName)
	cwd := strings.TrimSpace(source.Cwd)
	description := strings.TrimSpace(source.Description)
	if strings.ContainsRune(cwd, 0) {
		return MachineSourceCompiled{}, fmt.Errorf("machine_sources[%d].cwd cannot contain NUL", index)
	}
	if err := validateMachineSourceSecrets(source, index, opts); err != nil {
		return MachineSourceCompiled{}, err
	}
	if machineName != "" {
		if hasMachineProvisioningFields(source) {
			return MachineSourceCompiled{}, fmt.Errorf(
				"machine_sources[%d] machine provisioning fields are only valid for machine_pool_name sources",
				index,
			)
		}
		if source.MaxMachines != nil {
			return MachineSourceCompiled{}, fmt.Errorf(
				"machine_sources[%d].max_machines is only valid for machine_pool_name sources",
				index,
			)
		}
		if source.InitialNumMachines != nil {
			return MachineSourceCompiled{}, fmt.Errorf(
				"machine_sources[%d].initial_num_machines is only valid for machine_pool_name sources",
				index,
			)
		}
		machineID, err := resolveMachineSourceMachineName(machineName, index, opts.ResolveMachineName)
		if err != nil {
			return MachineSourceCompiled{}, err
		}
		return MachineSourceCompiled{
			MachineID:        machineID,
			Cwd:              cwd,
			EnvOverlay:       source.EnvOverlay,
			SecretEnvOverlay: source.SecretEnvOverlay,
			Description:      description,
		}, nil
	}
	maxMachines, initialNumMachines, err := compilePoolMachineCounts(source, index)
	if err != nil {
		return MachineSourceCompiled{}, err
	}
	machinePoolID, err := resolveMachineSourceMachinePoolName(machinePoolName, index, opts.ResolveMachinePoolName)
	if err != nil {
		return MachineSourceCompiled{}, err
	}
	return MachineSourceCompiled{
		MachinePoolID:                 machinePoolID,
		MaxMachines:                   maxMachines,
		InitialNumMachines:            initialNumMachines,
		Cwd:                           cwd,
		MachineCPU:                    source.MachineCPU,
		MachineMemoryMB:               source.MachineMemoryMB,
		EnvOverlay:                    source.EnvOverlay,
		SecretEnvOverlay:              source.SecretEnvOverlay,
		MachineProviderOptionsOverlay: source.MachineProviderOptionsOverlay,
		Description:                   description,
	}, nil
}

func resolveMachineSourceMachineName(
	machineName string,
	index int,
	resolve func(string) (string, error),
) (string, error) {
	if resolve == nil {
		return "", fmt.Errorf("machine_sources[%d].machine_name resolver is required", index)
	}
	resolved, err := resolve(machineName)
	if err != nil {
		return "", fmt.Errorf("machine_sources[%d].machine_name: %w", index, err)
	}
	resolved = strings.ToLower(strings.TrimSpace(resolved))
	if _, err := publicid.Decode(publicid.KindMachine, resolved); err != nil {
		return "", fmt.Errorf("machine_sources[%d].machine_name resolved to invalid public id: %w", index, err)
	}
	return resolved, nil
}

func resolveMachineSourceMachinePoolName(
	machinePoolName string,
	index int,
	resolve func(string) (string, error),
) (string, error) {
	if resolve == nil {
		return "", fmt.Errorf("machine_sources[%d].machine_pool_name resolver is required", index)
	}
	resolved, err := resolve(machinePoolName)
	if err != nil {
		return "", fmt.Errorf("machine_sources[%d].machine_pool_name: %w", index, err)
	}
	resolved = strings.ToLower(strings.TrimSpace(resolved))
	if _, err := publicid.Decode(publicid.KindMachinePool, resolved); err != nil {
		return "", fmt.Errorf("machine_sources[%d].machine_pool_name resolved to invalid public id: %w", index, err)
	}
	return resolved, nil
}

func hasMachineProvisioningFields(source AgentConfigMachineSource) bool {
	return source.MachineCPU != nil ||
		source.MachineMemoryMB != nil ||
		source.MachineProviderOptionsOverlay != nil
}

func validateMachineSourceSecrets(source AgentConfigMachineSource, index int, opts CompileOptions) error {
	if opts.ValidateSecretID == nil {
		return nil
	}
	for key, secretID := range source.SecretEnvOverlay {
		if secretID == nil {
			continue
		}
		if err := opts.ValidateSecretID(*secretID, secrets.KindGeneric); err != nil {
			return fmt.Errorf("machine_sources[%d].secret_env_overlay.%s: %w", index, key, err)
		}
	}
	return nil
}

func compilePoolMachineCounts(source AgentConfigMachineSource, index int) (int, int, error) {
	maxMachines := defaultPoolMaxMachines
	if source.MaxMachines != nil {
		maxMachines = *source.MaxMachines
	}
	initialNumMachines := defaultPoolInitialNumMachines
	if source.InitialNumMachines != nil {
		initialNumMachines = *source.InitialNumMachines
	}
	if source.MaxMachines != nil && maxMachines == 0 && source.InitialNumMachines == nil {
		initialNumMachines = 0
	}
	if maxMachines < 0 {
		return 0, 0, fmt.Errorf("machine_sources[%d].max_machines cannot be negative", index)
	}
	if initialNumMachines < 0 {
		return 0, 0, fmt.Errorf("machine_sources[%d].initial_num_machines cannot be negative", index)
	}
	if maxMachines > math.MaxInt32 {
		return 0, 0, fmt.Errorf("machine_sources[%d].max_machines must fit the machine pool capacity range", index)
	}
	if initialNumMachines > math.MaxInt32 {
		return 0, 0, fmt.Errorf(
			"machine_sources[%d].initial_num_machines must fit the machine pool capacity range",
			index,
		)
	}
	if initialNumMachines > maxMachines {
		return 0, 0, fmt.Errorf("machine_sources[%d].initial_num_machines cannot exceed max_machines", index)
	}
	return maxMachines, initialNumMachines, nil
}

// validateCustomInputSchema enforces the one constraint the source JSON
// Schema cannot express: every required field must be declared in properties.
func validateCustomInputSchema(raw json.RawMessage) error {
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		return fmt.Errorf("decode JSON schema: %w", err)
	}
	for _, name := range schema.Required {
		if _, ok := schema.Properties[name]; !ok {
			return fmt.Errorf("required field %q is not declared in properties", name)
		}
	}
	return nil
}

func resolveToolPermission(
	enabled *bool,
	permission *toolpermission.Selection,
	defaultEnabled bool,
	defaultPermission toolpermission.Selection,
) (toolpermission.Selection, bool) {
	enabledValue := defaultEnabled
	if enabled != nil {
		enabledValue = *enabled
	}
	if permission == nil {
		return defaultPermission, enabledValue
	}
	return *permission, enabledValue
}
