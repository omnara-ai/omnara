package agentconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"sync"

	kjsonschema "github.com/kaptinlin/jsonschema"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"
)

type SourceFormat string

const (
	SourceFormatJSON SourceFormat = "json"
	SourceFormatYAML SourceFormat = "yaml"
)

type AgentConfigSource struct {
	Version        string                           `json:"version,omitempty"`
	Instruction    string                           `json:"instruction"`
	Model          AgentConfigModelSource           `json:"model"`
	MachineSources []AgentConfigMachineSource       `json:"machine_sources,omitempty"`
	Tools          map[string]AgentConfigToolSource `json:"tools,omitempty"`
	MCP            map[string]AgentConfigMCPSource  `json:"mcp,omitempty"`
	Skills         []string                         `json:"skills,omitempty"`
}

type AgentConfigModelSource struct {
	ProviderConfig         string                           `json:"provider_config"`
	Name                   string                           `json:"name"`
	ContextWindowTokens    *int                             `json:"context_window_tokens,omitempty"`
	DefaultMaxOutputTokens *int                             `json:"default_max_output_tokens,omitempty"`
	CacheRetention         string                           `json:"cache_retention,omitempty"`
	Reasoning              *AgentConfigModelReasoningSource `json:"reasoning,omitempty"`
}

type AgentConfigModelReasoningSource struct {
	Effort string `json:"effort"`
}

type AgentConfigMachineSource struct {
	MachineName                   string                     `json:"machine_name,omitempty"`
	MachinePoolName               string                     `json:"machine_pool_name,omitempty"`
	MaxMachines                   *int                       `json:"max_machines,omitempty"`
	InitialNumMachines            *int                       `json:"initial_num_machines,omitempty"`
	DeleteAfterIdleMinutes        *int                       `json:"delete_after_idle_minutes,omitempty"`
	Cwd                           string                     `json:"cwd,omitempty"`
	MachineCPU                    *int                       `json:"machine_cpu,omitempty"`
	MachineMemoryMB               *int                       `json:"machine_memory_mb,omitempty"`
	EnvOverlay                    map[string]*string         `json:"env_overlay,omitempty"`
	SecretEnvOverlay              map[string]*string         `json:"secret_env_overlay,omitempty"`
	MachineProviderOptionsOverlay map[string]json.RawMessage `json:"machine_provider_options_overlay,omitempty"`
	Description                   string                     `json:"description,omitempty"`
}

type AgentConfigToolSource struct {
	Type        string                    `json:"type,omitempty"`
	Enabled     *bool                     `json:"enabled,omitempty"`
	Permission  *toolpermission.Selection `json:"permission,omitempty"`
	Description string                    `json:"description,omitempty"`
	InputSchema map[string]any            `json:"input_schema,omitempty"`
}

type AgentConfigMCPSource struct {
	URL            string                              `json:"url"`
	Auth           *AgentConfigMCPAuthSource           `json:"auth,omitempty"`
	DefaultEnabled *bool                               `json:"default_enabled,omitempty"`
	Permission     *toolpermission.Selection           `json:"permission,omitempty"`
	Tools          map[string]AgentConfigMCPToolSource `json:"tools,omitempty"`
}

type AgentConfigMCPAuthSource struct {
	Type     string `json:"type"`
	SecretID string `json:"secret_id"`
	Service  string `json:"service,omitempty"`
	Region   string `json:"region,omitempty"`
}

type AgentConfigMCPToolSource struct {
	Enabled    *bool                     `json:"enabled,omitempty"`
	Permission *toolpermission.Selection `json:"permission,omitempty"`
}

func ParseSource(format SourceFormat, raw []byte) (AgentConfigSource, error) {
	parsed, _, err := parseSource(format, raw)
	return parsed, err
}

func parseSource(format SourceFormat, raw []byte) (AgentConfigSource, []byte, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return AgentConfigSource{}, nil, errors.New("agent config source is required")
	}
	normalizedRaw, err := normalizeSourceResourceReferences(format, raw)
	if err != nil {
		return AgentConfigSource{}, nil, err
	}
	jsonSource, err := sourceJSON(format, normalizedRaw)
	if err != nil {
		return AgentConfigSource{}, nil, err
	}
	schema, err := compiledSourceSchema()
	if err != nil {
		return AgentConfigSource{}, nil, err
	}
	result := schema.Validate(jsonSource)
	if !result.IsValid() {
		return AgentConfigSource{}, nil, fmt.Errorf(
			"agent config source does not match JSON schema: %s",
			validationErrors(result),
		)
	}
	var parsed AgentConfigSource
	if err := json.Unmarshal(jsonSource, &parsed); err != nil {
		return AgentConfigSource{}, nil, fmt.Errorf("decode agent config source: %w", err)
	}
	if err := normalizeParsedSourceResourceNames(&parsed); err != nil {
		return AgentConfigSource{}, nil, err
	}
	return parsed, normalizedRaw, nil
}

func normalizeParsedSourceResourceNames(source *AgentConfigSource) error {
	providerConfig, err := resourcename.Normalize("model.provider_config", source.Model.ProviderConfig)
	if err != nil {
		return err
	}
	source.Model.ProviderConfig = providerConfig
	modelName, err := resourcename.Normalize("model.name", source.Model.Name)
	if err != nil {
		return err
	}
	source.Model.Name = modelName
	for index := range source.MachineSources {
		machineName, err := resourcename.Normalize(
			fmt.Sprintf("machine_sources[%d].machine_name", index),
			source.MachineSources[index].MachineName,
		)
		if err != nil {
			return err
		}
		source.MachineSources[index].MachineName = machineName
		machinePoolName, err := resourcename.Normalize(
			fmt.Sprintf("machine_sources[%d].machine_pool_name", index),
			source.MachineSources[index].MachinePoolName,
		)
		if err != nil {
			return err
		}
		source.MachineSources[index].MachinePoolName = machinePoolName
	}
	return nil
}

func normalizeSourceResourceReferences(format SourceFormat, raw []byte) ([]byte, error) {
	switch format {
	case SourceFormatJSON:
		return normalizeJSONSourceResourceReferences(raw)
	case SourceFormatYAML:
		return normalizeYAMLSourceResourceReferences(raw)
	default:
		return nil, fmt.Errorf("unsupported agent config source format %q", format)
	}
}

func normalizeJSONSourceResourceReferences(raw []byte) ([]byte, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("parse agent config JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("parse agent config JSON: trailing value")
		}
		return nil, fmt.Errorf("parse agent config JSON: %w", err)
	}
	root, ok := value.(map[string]any)
	if !ok || !normalizeJSONResourceReferences(root) {
		return raw, nil
	}
	normalized, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("encode normalized agent config JSON: %w", err)
	}
	return normalized, nil
}

func normalizeJSONResourceReferences(root map[string]any) bool {
	changed := false
	if model, ok := root["model"].(map[string]any); ok {
		changed = normalizeJSONResourceReference(model, "provider_config") || changed
		changed = normalizeJSONResourceReference(model, "name") || changed
	}
	if machineSources, ok := root["machine_sources"].([]any); ok {
		for _, value := range machineSources {
			machine, ok := value.(map[string]any)
			if !ok {
				continue
			}
			changed = normalizeJSONResourceReference(machine, "machine_name") || changed
			changed = normalizeJSONResourceReference(machine, "machine_pool_name") || changed
		}
	}
	return changed
}

func normalizeJSONResourceReference(object map[string]any, key string) bool {
	value, ok := object[key].(string)
	if !ok {
		return false
	}
	normalized := norm.NFC.String(value)
	if normalized == value {
		return false
	}
	object[key] = normalized
	return true
}

func normalizeYAMLSourceResourceReferences(raw []byte) ([]byte, error) {
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("parse agent config YAML: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("parse agent config YAML: trailing document")
		}
		return nil, fmt.Errorf("parse agent config YAML: %w", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return raw, nil
	}
	root := document.Content[0]
	changed := false
	if model := sourceYAMLMappingValue(root, "model"); model != nil && model.Kind == yaml.MappingNode {
		changed = normalizeYAMLResourceReference(model, "provider_config") || changed
		changed = normalizeYAMLResourceReference(model, "name") || changed
	}
	machineSources := sourceYAMLMappingValue(root, "machine_sources")
	if machineSources != nil && machineSources.Kind == yaml.SequenceNode {
		for _, candidate := range machineSources.Content {
			machine := sourceYAMLDereference(candidate)
			if machine == nil || machine.Kind != yaml.MappingNode {
				continue
			}
			changed = normalizeYAMLResourceReference(machine, "machine_name") || changed
			changed = normalizeYAMLResourceReference(machine, "machine_pool_name") || changed
		}
	}
	if !changed {
		return raw, nil
	}
	var normalized bytes.Buffer
	encoder := yaml.NewEncoder(&normalized)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return nil, fmt.Errorf("encode normalized agent config YAML: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("finish normalized agent config YAML: %w", err)
	}
	return normalized.Bytes(), nil
}

func sourceYAMLMappingValue(mapping *yaml.Node, key string) *yaml.Node {
	return sourceYAMLMappingValueSeen(mapping, key, make(map[*yaml.Node]bool))
}

func sourceYAMLMappingValueSeen(mapping *yaml.Node, key string, seen map[*yaml.Node]bool) *yaml.Node {
	mapping = sourceYAMLDereference(mapping)
	if mapping == nil || mapping.Kind != yaml.MappingNode || seen[mapping] {
		return nil
	}
	seen[mapping] = true
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key && key != "<<" {
			return sourceYAMLDereference(mapping.Content[index+1])
		}
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value != "<<" {
			continue
		}
		merge := sourceYAMLDereference(mapping.Content[index+1])
		if merge == nil {
			continue
		}
		if merge.Kind == yaml.SequenceNode {
			for _, candidate := range merge.Content {
				if value := sourceYAMLMappingValueSeen(candidate, key, seen); value != nil {
					return value
				}
			}
			continue
		}
		if value := sourceYAMLMappingValueSeen(merge, key, seen); value != nil {
			return value
		}
	}
	return nil
}

func sourceYAMLDereference(node *yaml.Node) *yaml.Node {
	for node != nil && node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	return node
}

func normalizeYAMLResourceReference(mapping *yaml.Node, key string) bool {
	value := sourceYAMLMappingValue(mapping, key)
	if value == nil || value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
		return false
	}
	normalized := norm.NFC.String(value.Value)
	if normalized == value.Value {
		return false
	}
	value.Value = normalized
	return true
}

func sourceJSON(format SourceFormat, raw []byte) ([]byte, error) {
	switch format {
	case SourceFormatJSON:
		return raw, nil
	case SourceFormatYAML:
		var value any
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("parse agent config YAML: %w", err)
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			if err == nil {
				return nil, errors.New("parse agent config YAML: trailing document")
			}
			return nil, fmt.Errorf("parse agent config YAML: %w", err)
		}
		normalized, err := normalizeYAMLValue(value)
		if err != nil {
			return nil, fmt.Errorf("normalize agent config YAML: %w", err)
		}
		out, err := json.Marshal(normalized)
		if err != nil {
			return nil, fmt.Errorf("marshal agent config YAML as JSON: %w", err)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported agent config source format %q", format)
	}
}

func normalizeYAMLValue(value any) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized, err := normalizeYAMLValue(item)
			if err != nil {
				return nil, err
			}
			out[key] = normalized
		}
		return out, nil
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			keyString, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("mapping key %v is not a string", key)
			}
			normalized, err := normalizeYAMLValue(item)
			if err != nil {
				return nil, err
			}
			out[keyString] = normalized
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			normalized, err := normalizeYAMLValue(item)
			if err != nil {
				return nil, err
			}
			out[i] = normalized
		}
		return out, nil
	default:
		return value, nil
	}
}

var compiledSourceSchema = sync.OnceValues(func() (*kjsonschema.Schema, error) {
	schemaJSON, err := agentConfigSourceJSONSchemaJSON()
	if err != nil {
		return nil, err
	}
	compiled, err := kjsonschema.NewCompiler().Compile(schemaJSON)
	if err != nil {
		return nil, fmt.Errorf("compile agent config JSON schema: %w", err)
	}
	return compiled, nil
})

func agentConfigSourceJSONSchemaJSON() ([]byte, error) {
	schemaJSON, err := json.Marshal(agentConfigSourceSchema())
	if err != nil {
		return nil, fmt.Errorf("marshal agent config JSON schema: %w", err)
	}
	return canonicalizeJSON(schemaJSON), nil
}

func agentConfigSourceSchema() *kjsonschema.Schema {
	schema := kjsonschema.Object(
		kjsonschema.Prop("version", kjsonschema.Enum("v1")),
		kjsonschema.Prop("instruction", kjsonschema.String(kjsonschema.MinLength(1), kjsonschema.Pattern(`\S`))),
		kjsonschema.Prop("model", kjsonschema.Ref("#/$defs/AgentConfigModelSource")),
		kjsonschema.Prop("machine_sources", kjsonschema.AnyOf(
			kjsonschema.Array(kjsonschema.Items(kjsonschema.Ref("#/$defs/AgentConfigMachineSource"))),
			kjsonschema.Null(),
		)),
		kjsonschema.Prop("tools", kjsonschema.Object(
			kjsonschema.PropertyNames(kjsonschema.AllOf(
				kjsonschema.String(kjsonschema.Pattern(toolcatalog.ToolNamePattern)),
				kjsonschema.Not(kjsonschema.String(kjsonschema.Pattern(`^`+toolcatalog.MCPRuntimeToolPrefix))),
			)),
			kjsonschema.AdditionalPropsSchema(kjsonschema.Ref("#/$defs/AgentConfigToolSource")),
		)),
		kjsonschema.Prop("mcp", kjsonschema.Object(
			kjsonschema.PropertyNames(kjsonschema.String(kjsonschema.Pattern(toolcatalog.MCPServerKeyPattern))),
			kjsonschema.AdditionalPropsSchema(kjsonschema.Ref("#/$defs/AgentConfigMCPSource")),
		)),
		kjsonschema.Prop("skills", kjsonschema.AnyOf(
			kjsonschema.Array(
				kjsonschema.Items(kjsonschema.String(
					kjsonschema.Pattern(`^skl_[a-z2-7]{26}$`),
				)),
				kjsonschema.UniqueItems(true),
			),
			kjsonschema.Null(),
		)),
		kjsonschema.Required("instruction", "model"),
		kjsonschema.AdditionalProps(false),
		kjsonschema.Defs(map[string]*kjsonschema.Schema{
			"AgentConfigModelSource": kjsonschema.Object(
				kjsonschema.Prop("provider_config", resourceNameReferenceSchema()),
				kjsonschema.Prop("name", resourceNameReferenceSchema()),
				kjsonschema.Prop("context_window_tokens", kjsonschema.Integer(kjsonschema.Min(1))),
				kjsonschema.Prop("default_max_output_tokens", kjsonschema.Integer(kjsonschema.Min(1))),
				kjsonschema.Prop("cache_retention", kjsonschema.Enum("none", "short", "long")),
				kjsonschema.Prop("reasoning", kjsonschema.Object(
					kjsonschema.Prop("effort", kjsonschema.String(kjsonschema.MinLength(1), kjsonschema.Pattern(`\S`))),
					kjsonschema.Required("effort"),
					kjsonschema.AdditionalProps(false),
				)),
				kjsonschema.Required("provider_config", "name"),
				kjsonschema.AdditionalProps(false),
			),
			"AgentConfigMachineSource": kjsonschema.Object(
				kjsonschema.Prop("machine_name", resourceNameReferenceSchema()),
				kjsonschema.Prop("machine_pool_name", resourceNameReferenceSchema()),
				kjsonschema.Prop("max_machines", kjsonschema.Integer(kjsonschema.Min(0))),
				kjsonschema.Prop("initial_num_machines", kjsonschema.Integer(kjsonschema.Min(0))),
				kjsonschema.Prop(
					"delete_after_idle_minutes",
					kjsonschema.AnyOf(
						kjsonschema.Const(0),
						kjsonschema.Integer(kjsonschema.Min(5), kjsonschema.Max(float64(math.MaxInt32))),
					),
				),
				kjsonschema.Prop("cwd", kjsonschema.String()),
				kjsonschema.Prop(
					"machine_cpu",
					kjsonschema.Integer(kjsonschema.Min(1), kjsonschema.Max(float64(math.MaxInt32))),
				),
				kjsonschema.Prop(
					"machine_memory_mb",
					kjsonschema.Integer(kjsonschema.Min(1), kjsonschema.Max(float64(math.MaxInt32))),
				),
				kjsonschema.Prop("env_overlay", kjsonschema.AnyOf(
					kjsonschema.Object(
						kjsonschema.PropertyNames(envNameSchema()),
						kjsonschema.AdditionalPropsSchema(kjsonschema.AnyOf(
							kjsonschema.String(),
							kjsonschema.Null(),
						)),
					),
					kjsonschema.Null(),
				)),
				kjsonschema.Prop("secret_env_overlay", kjsonschema.AnyOf(
					kjsonschema.Object(
						kjsonschema.PropertyNames(envNameSchema()),
						kjsonschema.AdditionalPropsSchema(kjsonschema.AnyOf(
							kjsonschema.String(kjsonschema.Pattern(`^sec_[a-z2-7]{26}$`)),
							kjsonschema.Null(),
						)),
					),
					kjsonschema.Null(),
				)),
				kjsonschema.Prop("machine_provider_options_overlay", kjsonschema.AnyOf(
					kjsonschema.Object(),
					kjsonschema.Null(),
				)),
				kjsonschema.Prop("description", kjsonschema.String()),
				kjsonschema.AdditionalProps(false),
				kjsonschema.Keyword(func(s *kjsonschema.Schema) {
					s.OneOf = []*kjsonschema.Schema{
						kjsonschema.Object(kjsonschema.Required("machine_name")),
						kjsonschema.Object(kjsonschema.Required("machine_pool_name")),
					}
				}),
			),
			"AgentConfigToolSource": func() *kjsonschema.Schema {
				def := kjsonschema.Object(
					kjsonschema.Prop("type", kjsonschema.Enum(toolcatalog.ToolTypeBuiltIn, toolcatalog.ToolTypeCustom)),
					kjsonschema.Prop("enabled", kjsonschema.AnyOf(kjsonschema.Boolean(), kjsonschema.Null())),
					kjsonschema.Prop("permission", kjsonschema.Ref("#/$defs/ToolPermissionSelection")),
					kjsonschema.Prop("description", kjsonschema.String(kjsonschema.MinLength(1))),
					kjsonschema.Prop("input_schema", kjsonschema.Ref("#/$defs/AgentToolInputSchema")),
					kjsonschema.AdditionalProps(false),
				)
				def.If = kjsonschema.Object(
					kjsonschema.Prop("type", kjsonschema.Const(toolcatalog.ToolTypeCustom)),
					kjsonschema.Required("type"),
				)
				def.Then = &kjsonschema.Schema{
					Required: []string{"description", "input_schema"},
				}
				def.Else = kjsonschema.Not(kjsonschema.AnyOf(
					kjsonschema.Object(kjsonschema.Required("description")),
					kjsonschema.Object(kjsonschema.Required("input_schema")),
				))
				return def
			}(),
			"AgentToolInputSchema": kjsonschema.Object(
				kjsonschema.Prop("type", kjsonschema.Const(toolcatalog.ToolInputSchemaObject)),
				kjsonschema.Prop("properties", kjsonschema.Object(
					kjsonschema.AdditionalPropsSchema(kjsonschema.Object()),
				)),
				kjsonschema.Prop("required", kjsonschema.Array(
					kjsonschema.Items(kjsonschema.String(kjsonschema.MinLength(1))),
				)),
				kjsonschema.Required("type"),
			),
			"AgentConfigMCPSource": kjsonschema.Object(
				kjsonschema.Prop("url", kjsonschema.String(kjsonschema.MinLength(1))),
				kjsonschema.Prop("auth", kjsonschema.Ref("#/$defs/AgentConfigMCPAuthSource")),
				kjsonschema.Prop("default_enabled", kjsonschema.AnyOf(kjsonschema.Boolean(), kjsonschema.Null())),
				kjsonschema.Prop("permission", kjsonschema.Ref("#/$defs/ToolPermissionSelection")),
				kjsonschema.Prop("tools", kjsonschema.Object(
					kjsonschema.PropertyNames(kjsonschema.String(kjsonschema.Pattern(toolcatalog.MCPRemoteToolNamePattern))),
					kjsonschema.AdditionalPropsSchema(kjsonschema.Ref("#/$defs/AgentConfigMCPToolSource")),
				)),
				kjsonschema.Required("url"),
				kjsonschema.AdditionalProps(false),
			),
			"AgentConfigMCPAuthSource": kjsonschema.Object(
				kjsonschema.Prop(
					"type",
					kjsonschema.Enum(MCPAuthTypeBearer, MCPAuthTypeOAuth, MCPAuthTypeSigV4),
				),
				kjsonschema.Prop(
					"secret_id",
					kjsonschema.String(kjsonschema.MinLength(1), kjsonschema.Pattern(`^sec_[a-z2-7]{26}$`)),
				),
				kjsonschema.Prop("service", kjsonschema.String(kjsonschema.MinLength(1))),
				kjsonschema.Prop("region", kjsonschema.String(kjsonschema.MinLength(1))),
				kjsonschema.Required("type", "secret_id"),
				kjsonschema.AdditionalProps(false),
			),
			"AgentConfigMCPToolSource": kjsonschema.Object(
				kjsonschema.Prop("enabled", kjsonschema.AnyOf(kjsonschema.Boolean(), kjsonschema.Null())),
				kjsonschema.Prop("permission", kjsonschema.Ref("#/$defs/ToolPermissionSelection")),
				kjsonschema.AdditionalProps(false),
			),
			"ToolPermissionSelection": kjsonschema.Object(
				kjsonschema.Prop("mode", kjsonschema.String(kjsonschema.MinLength(1), kjsonschema.Pattern(`\S`))),
				kjsonschema.Prop("parameters", kjsonschema.Object()),
				kjsonschema.Required("mode"),
				kjsonschema.AdditionalProps(false),
			),
		}),
	)
	return schema
}

func resourceNameReferenceSchema() *kjsonschema.Schema {
	return kjsonschema.String(
		kjsonschema.MinLength(1),
		kjsonschema.MaxLength(resourcename.MaxCodePoints),
		kjsonschema.Pattern(`^\S(?:.*\S)?$`),
	)
}

func envNameSchema() *kjsonschema.Schema {
	return kjsonschema.String(kjsonschema.MinLength(1), kjsonschema.Pattern("^[^=\x00]+$"))
}

func validationErrors(result *kjsonschema.EvaluationResult) string {
	detailed := result.DetailedErrors()
	messages := make([]string, 0, len(detailed))
	for location, message := range detailed {
		if location == "" {
			location = "/"
		}
		messages = append(messages, location+": "+message)
	}
	if len(messages) == 0 {
		return "validation failed"
	}
	sort.Strings(messages)
	return strings.Join(messages, "; ")
}
