package agentconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

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
	EventWebhook   *EventWebhookCompiled        `json:"event_webhook,omitempty"`
	Skills         []SkillCompiled              `json:"skills,omitempty"`
	Subagents      map[string]SubagentCompiled  `json:"subagents,omitempty"`
	MaxSubagents   *int                         `json:"max_subagents,omitempty"`
	MaxDepth       *int                         `json:"max_depth,omitempty"`
}

type SkillCompiled struct {
	ID uuid.UUID `json:"id"`
}

type EventWebhookCompiled struct {
	Events          []string  `json:"events"`
	SigningSecretID uuid.UUID `json:"signing_secret_id,omitzero"`
	URL             string    `json:"url"`
}

// SkillResolution is what a ResolveSkillID callback returns at compile time.
// Name is used only for compile-time duplicate detection and is not stored
// in the compiled contract.
type SkillResolution struct {
	ID   uuid.UUID
	Name string
}

type ModelCompiled struct {
	ConfiguredModelID      uuid.UUID               `json:"configured_model_id,omitzero"`
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
	configuredModelID    uuid.UUID
	model                ModelCompiled
	sourceProviderConfig string
	sourceName           string
	supportsTools        *bool
}

type MachineSourceCompiled struct {
	MachineID                     uuid.UUID                  `json:"machine_id,omitzero"`
	MachinePoolID                 uuid.UUID                  `json:"machine_pool_id,omitzero"`
	MaxMachines                   int                        `json:"max_machines,omitempty"`
	InitialNumMachines            int                        `json:"initial_num_machines,omitempty"`
	DeleteAfterIdleMinutes        *int                       `json:"delete_after_idle_minutes,omitempty"`
	Cwd                           string                     `json:"cwd,omitempty"`
	MachineCPU                    *int                       `json:"machine_cpu,omitempty"`
	MachineMemoryMB               *int                       `json:"machine_memory_mb,omitempty"`
	EnvOverlay                    map[string]*string         `json:"env_overlay,omitempty"`
	SecretEnvOverlay              map[string]*uuid.UUID      `json:"secret_env_overlay,omitempty"`
	MachineProviderOptionsOverlay map[string]json.RawMessage `json:"machine_provider_options_overlay,omitempty"`
	Description                   string                     `json:"description,omitempty"`
}

type ToolCompiled struct {
	Enabled     bool                     `json:"enabled"`
	Type        string                   `json:"type,omitempty"`
	Permission  toolpermission.Selection `json:"permission"`
	Deferred    bool                     `json:"deferred,omitempty"`
	Description string                   `json:"description,omitempty"`
	InputSchema json.RawMessage          `json:"input_schema,omitempty"`
}

type MCPServerCompiled struct {
	URL            string                     `json:"url"`
	Auth           *MCPAuthCompiled           `json:"auth,omitempty"`
	DefaultEnabled bool                       `json:"default_enabled"`
	Permission     toolpermission.Selection   `json:"permission"`
	Deferred       bool                       `json:"deferred,omitempty"`
	Tools          map[string]MCPToolCompiled `json:"tools,omitempty"`
}

type MCPAuthCompiled struct {
	Type     string    `json:"type"`
	SecretID uuid.UUID `json:"secret_id"`
	Service  string    `json:"service,omitempty"`
	Region   string    `json:"region,omitempty"`
}

type MCPToolCompiled struct {
	Enabled    *bool                     `json:"enabled,omitempty"`
	Permission *toolpermission.Selection `json:"permission,omitempty"`
	Deferred   *bool                     `json:"deferred,omitempty"`
}

// Result is the complete compiler output for one agent config source. It is
// the only intended input to agent config writes, so persisted compiled
// state always corresponds to a source that passed compilation.
type Result struct {
	Compiled      Compiled
	CanonicalJSON []byte
	Hash          string
	Source        string
	SourceFormat  SourceFormat
}

type CompileOptions struct {
	AllowInsecureLocalMCPHTTP bool
	ResolveModelSelection     func(providerConfig string, configuredModelName string) (ResolvedModelSelection, error)
	ValidateSecretID          func(secretID uuid.UUID, expectedKind secrets.Kind) error
	ResolveMachineName        func(machineName string) (uuid.UUID, error)
	ResolveMachinePoolName    func(machinePoolName string) (uuid.UUID, error)
	ResolveSkillID            func(skillID string) (SkillResolution, error)
	ResolveAgentProfileName   func(profileName string) (uuid.UUID, error)
}

type ResolvedModelSelection struct {
	ConfiguredModelID uuid.UUID
	SupportsTools     *bool
}

func Compile(format SourceFormat, raw []byte, opts CompileOptions) (Result, error) {
	source, root, err := parseSource(format, raw)
	if err != nil {
		return Result{}, err
	}
	compiled, err := compile(source, opts)
	if err != nil {
		return Result{}, validationErrorFrom(err, root)
	}
	encoded, err := EncodeCompiled(compiled)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Compiled:      compiled,
		CanonicalJSON: encoded.CanonicalJSON,
		Hash:          encoded.Hash,
		Source:        string(raw),
		SourceFormat:  format,
	}, nil
}

type EncodedCompiled struct {
	CanonicalJSON []byte
	Hash          string
}

func EncodeCompiled(compiled Compiled) (EncodedCompiled, error) {
	canonical, err := json.Marshal(compiled)
	if err != nil {
		return EncodedCompiled{}, fmt.Errorf("marshal compiled agent config: %w", err)
	}
	canonical = canonicalizeJSON(canonical)
	sum := sha256.Sum256(canonical)
	return EncodedCompiled{CanonicalJSON: canonical, Hash: hex.EncodeToString(sum[:])}, nil
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
	if source.EventWebhook != nil {
		webhookURL, err := ValidateEventWebhookURL(source.EventWebhook.URL)
		if err != nil {
			return Compiled{}, issueAt("/event_webhook/url", err)
		}
		var secretID uuid.UUID
		if raw := strings.TrimSpace(source.EventWebhook.SigningSecretID); raw != "" {
			secretID, err = publicid.Decode(publicid.KindSecret, raw)
			if err != nil {
				return Compiled{}, issueAt("/event_webhook/signing_secret_id", err)
			}
			if opts.ValidateSecretID != nil {
				if err := opts.ValidateSecretID(secretID, secrets.KindGeneric); err != nil {
					return Compiled{}, issueOr("/event_webhook/signing_secret_id", err)
				}
			}
		}
		compiled.EventWebhook = &EventWebhookCompiled{
			URL: webhookURL, SigningSecretID: secretID, Events: source.EventWebhook.Events,
		}
	}
	machines, err := compileMachineSources(source.MachineSources, opts)
	if err != nil {
		return Compiled{}, err
	}
	if len(machines) > 0 {
		compiled.MachineSources = machines
	}
	compiled.Tools, err = compileTools(source)
	if err != nil {
		return Compiled{}, err
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
	if err := validateSubagentToolConfiguration(source); err != nil {
		return Compiled{}, err
	}
	subagents, err := compileSubagents(source, opts)
	if err != nil {
		return Compiled{}, err
	}
	if len(subagents) > 0 {
		compiled.Subagents = subagents
		compiled.MaxSubagents = source.MaxSubagents
	}
	compiled.MaxDepth = source.MaxDepth
	if compiledModel.supportsTools != nil && !*compiledModel.supportsTools && requiresModelToolSupport(compiled) {
		return Compiled{}, issuef(jsonPointer("model", "name"), "model %q does not support tools", compiledModel.sourceName)
	}
	return compiled, nil
}

func compileModel(source AgentConfigModelSource, opts CompileOptions) (compiledModelResult, error) {
	if source.ProviderConfig == "" {
		return compiledModelResult{}, issuef(jsonPointer("model", "provider_config"), "is required")
	}
	if source.Name == "" {
		return compiledModelResult{}, issuef(jsonPointer("model", "name"), "is required")
	}
	compiled := compiledModelResult{
		sourceProviderConfig: source.ProviderConfig,
		sourceName:           source.Name,
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
		return compiledModelResult{}, issueOr(jsonPointer("model"), err)
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
	return len(compiled.MCP) > 0
}

func missingDefaultToolNames(source AgentConfigSource) []string {
	var names []string
	if len(source.MachineSources) > 0 {
		names = append(names, toolcatalog.MachineToolNames()...)
		for _, machine := range source.MachineSources {
			if machine.MachinePoolName != "" {
				names = append(names, toolcatalog.MachinePoolToolNames()...)
				break
			}
		}
	}
	if len(source.Skills) > 0 {
		names = append(names, toolcatalog.ToolNameSkill)
	}
	if len(source.Subagents) > 0 {
		names = append(names, toolcatalog.SubagentToolNames()...)
	}
	if sourceDefersAnyTool(source) {
		names = append(names, toolcatalog.ToolNameToolSearch)
	}
	names = slices.DeleteFunc(names, func(name string) bool {
		_, configured := source.Tools[name]
		return configured
	})
	hasTools := len(names) > 0 || len(source.MCP) > 0
	for _, tool := range source.Tools {
		if tool.Enabled == nil || *tool.Enabled {
			hasTools = true
			break
		}
	}
	if hasTools {
		for _, name := range []string{toolcatalog.ToolNameReadFile, toolcatalog.ToolNameSearchFiles} {
			if _, configured := source.Tools[name]; !configured {
				names = append(names, name)
			}
		}
	}
	return names
}

func sourceDefersAnyTool(source AgentConfigSource) bool {
	for _, tool := range source.Tools {
		if tool.Deferred && (tool.Enabled == nil || *tool.Enabled) {
			return true
		}
	}
	for _, server := range source.MCP {
		if server.Deferred {
			return true
		}
		for _, tool := range server.Tools {
			if tool.Deferred != nil && *tool.Deferred {
				return true
			}
		}
	}
	return false
}

// compileSkills validates and pins the attached skill set. Skills do not
// require machine sources: a skill can be pure SKILL.md instructions, and the
// skill tool installs supporting files only on whatever machines are attached
// at invocation time.
func compileSkills(skillIDs []string, opts CompileOptions) ([]SkillCompiled, error) {
	if opts.ResolveSkillID == nil {
		return nil, issuef(jsonPointer("skills"), "skills require a ResolveSkillID callback")
	}
	resolved := make([]SkillResolution, 0, len(skillIDs))
	for i, skillID := range skillIDs {
		rec, err := opts.ResolveSkillID(skillID)
		if err != nil {
			return nil, issueOr(jsonPointer("skills", i), err)
		}
		if rec.ID == uuid.Nil || rec.Name == "" {
			return nil, issuef(jsonPointer("skills", i), "resolver returned incomplete record")
		}
		resolved = append(resolved, rec)
	}
	seenNames := make(map[string]string, len(resolved))
	compiledSkills := make([]SkillCompiled, 0, len(resolved))
	for i, rec := range resolved {
		if existing, ok := seenNames[rec.Name]; ok {
			return nil, issuef(
				jsonPointer("skills", i),
				"name %q is attached more than once (skills %s and %s); "+
					"skill names must be unique across the agent's attached set",
				rec.Name,
				existing,
				skillIDs[i],
			)
		}
		seenNames[rec.Name] = skillIDs[i]
		compiledSkills = append(compiledSkills, SkillCompiled{ID: rec.ID})
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
		return ToolCompiled{}, issuef(jsonPointer("tools", name), "tool %q is not registered", name)
	}
	permission := entry.DefaultPermission
	if source.Permission != nil {
		var err error
		permission, err = toolpermission.ValidateSelection(*source.Permission, entry.PermissionModes)
		if err != nil {
			return ToolCompiled{}, issueAt(jsonPointer("tools", name, "permission"), err)
		}
	}
	if source.Deferred && name == toolcatalog.ToolNameToolSearch {
		return ToolCompiled{}, issuef(jsonPointer("tools", name, "deferred"), "tool_search cannot be deferred")
	}
	compiled := ToolCompiled{
		Enabled:    enabled,
		Permission: permission,
		Deferred:   source.Deferred,
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
		return ToolCompiled{}, issuef(jsonPointer("tools", name), "custom tool name uses the reserved MCP tool namespace")
	}
	if _, ok := catalog.Lookup(name); ok {
		return ToolCompiled{}, issuef(jsonPointer("tools", name), "custom tool name collides with a built-in tool")
	}
	if toolcatalog.IsReservedWireToolName(name) {
		return ToolCompiled{}, issuef(jsonPointer("tools", name), "custom tool name is reserved")
	}
	schema, err := valueToCanonicalJSON(source.InputSchema)
	if err != nil {
		return ToolCompiled{}, issueAt(jsonPointer("tools", name, "input_schema"), err)
	}
	if err := validateCustomInputSchema(schema); err != nil {
		return ToolCompiled{}, issueAt(jsonPointer("tools", name, "input_schema"), err)
	}
	permission := toolcatalog.DefaultCustomToolPermission()
	if source.Permission != nil {
		permission, err = toolpermission.ValidateSelection(*source.Permission, toolcatalog.CustomToolPermissionModes())
		if err != nil {
			return ToolCompiled{}, issueAt(jsonPointer("tools", name, "permission"), err)
		}
	}
	return ToolCompiled{
		Enabled:     enabled,
		Type:        toolcatalog.ToolTypeCustom,
		Permission:  permission,
		Deferred:    source.Deferred,
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
	seenMachines := map[uuid.UUID]bool{}
	seenPools := map[uuid.UUID]bool{}
	for index, source := range sources {
		machine, err := compileMachineSource(source, index, opts)
		if err != nil {
			return nil, err
		}
		if machine.MachineID != uuid.Nil {
			if seenMachines[machine.MachineID] {
				return nil, issuef(jsonPointer("machine_sources", index, "machine_name"), "duplicates a machine id")
			}
			seenMachines[machine.MachineID] = true
		}
		if machine.MachinePoolID != uuid.Nil {
			if seenPools[machine.MachinePoolID] {
				return nil, issuef(jsonPointer("machine_sources", index, "machine_pool_name"), "duplicates a machine pool id")
			}
			seenPools[machine.MachinePoolID] = true
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
	machineName := source.MachineName
	machinePoolName := source.MachinePoolName
	cwd := strings.TrimSpace(source.Cwd)
	description := strings.TrimSpace(source.Description)
	if strings.ContainsRune(cwd, 0) {
		return MachineSourceCompiled{}, issuef(jsonPointer("machine_sources", index, "cwd"), "cannot contain NUL")
	}
	secretEnv, err := compileMachineSourceSecrets(source, index, opts)
	if err != nil {
		return MachineSourceCompiled{}, err
	}
	if machineName != "" {
		if hasMachineProvisioningFields(source) {
			return MachineSourceCompiled{}, issuef(
				jsonPointer("machine_sources", index),
				"machine provisioning fields are only valid for machine_pool_name sources",
			)
		}
		if source.MaxMachines != nil {
			return MachineSourceCompiled{}, issuef(
				jsonPointer("machine_sources", index, "max_machines"),
				"is only valid for machine_pool_name sources",
			)
		}
		if source.InitialNumMachines != nil {
			return MachineSourceCompiled{}, issuef(
				jsonPointer("machine_sources", index, "initial_num_machines"),
				"is only valid for machine_pool_name sources",
			)
		}
		if source.DeleteAfterIdleMinutes != nil {
			return MachineSourceCompiled{}, issuef(
				jsonPointer("machine_sources", index, "delete_after_idle_minutes"),
				"is only valid for machine_pool_name sources",
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
			SecretEnvOverlay: secretEnv,
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
		DeleteAfterIdleMinutes:        source.DeleteAfterIdleMinutes,
		Cwd:                           cwd,
		MachineCPU:                    source.MachineCPU,
		MachineMemoryMB:               source.MachineMemoryMB,
		EnvOverlay:                    source.EnvOverlay,
		SecretEnvOverlay:              secretEnv,
		MachineProviderOptionsOverlay: source.MachineProviderOptionsOverlay,
		Description:                   description,
	}, nil
}

func resolveMachineSourceMachineName(
	machineName string,
	index int,
	resolve func(string) (uuid.UUID, error),
) (uuid.UUID, error) {
	if resolve == nil {
		return uuid.Nil, issuef(jsonPointer("machine_sources", index, "machine_name"), "resolver is required")
	}
	resolved, err := resolve(machineName)
	if err != nil {
		return uuid.Nil, issueOr(jsonPointer("machine_sources", index, "machine_name"), err)
	}
	if resolved == uuid.Nil {
		return uuid.Nil, issuef(jsonPointer("machine_sources", index, "machine_name"), "resolved to nil machine id")
	}
	return resolved, nil
}

func resolveMachineSourceMachinePoolName(
	machinePoolName string,
	index int,
	resolve func(string) (uuid.UUID, error),
) (uuid.UUID, error) {
	if resolve == nil {
		return uuid.Nil, issuef(jsonPointer("machine_sources", index, "machine_pool_name"), "resolver is required")
	}
	resolved, err := resolve(machinePoolName)
	if err != nil {
		return uuid.Nil, issueOr(jsonPointer("machine_sources", index, "machine_pool_name"), err)
	}
	if resolved == uuid.Nil {
		return uuid.Nil, issuef(jsonPointer("machine_sources", index, "machine_pool_name"), "resolved to nil machine pool id")
	}
	return resolved, nil
}

func hasMachineProvisioningFields(source AgentConfigMachineSource) bool {
	return source.MachineCPU != nil ||
		source.MachineMemoryMB != nil ||
		source.MachineProviderOptionsOverlay != nil
}

func compileMachineSourceSecrets(
	source AgentConfigMachineSource,
	index int,
	opts CompileOptions,
) (map[string]*uuid.UUID, error) {
	result := make(map[string]*uuid.UUID, len(source.SecretEnvOverlay))
	for key, secretID := range source.SecretEnvOverlay {
		if secretID == nil {
			result[key] = nil
			continue
		}
		id, err := publicid.Decode(publicid.KindSecret, *secretID)
		if err != nil {
			return nil, issueOr(jsonPointer("machine_sources", index, "secret_env_overlay", key), err)
		}
		if opts.ValidateSecretID != nil {
			if err := opts.ValidateSecretID(id, secrets.KindGeneric); err != nil {
				return nil, issueOr(jsonPointer("machine_sources", index, "secret_env_overlay", key), err)
			}
		}
		result[key] = &id
	}
	return result, nil
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
		return 0, 0, issuef(jsonPointer("machine_sources", index, "max_machines"), "cannot be negative")
	}
	if initialNumMachines < 0 {
		return 0, 0, issuef(jsonPointer("machine_sources", index, "initial_num_machines"), "cannot be negative")
	}
	if maxMachines > math.MaxInt32 {
		return 0, 0, issuef(jsonPointer("machine_sources", index, "max_machines"), "must fit the machine pool capacity range")
	}
	if initialNumMachines > math.MaxInt32 {
		return 0, 0, issuef(
			jsonPointer("machine_sources", index, "initial_num_machines"),
			"must fit the machine pool capacity range",
		)
	}
	if initialNumMachines > maxMachines {
		return 0, 0, issuef(jsonPointer("machine_sources", index, "initial_num_machines"), "cannot exceed max_machines")
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
