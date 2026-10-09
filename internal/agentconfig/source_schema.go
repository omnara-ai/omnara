package agentconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	kjsonschema "github.com/kaptinlin/jsonschema"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"gopkg.in/yaml.v3"
)

const MaxMachineCwdLength = 4096

type SourceFormat string

const (
	SourceFormatJSON SourceFormat = "json"
	SourceFormatYAML SourceFormat = "yaml"
)

type EventWebhook struct {
	Events          []string `json:"events"`
	SigningSecretID string   `json:"signing_secret_id,omitempty"`
	URL             string   `json:"url"`
}

type AgentConfigSource struct {
	Version             string                                            `json:"version,omitempty"`
	Instruction         string                                            `json:"instruction"`
	Model               AgentConfigModelSource                            `json:"model"`
	MachineSources      []AgentConfigMachineSource                        `json:"machine_sources,omitempty"`
	Tools               map[string]AgentConfigToolSource                  `json:"tools,omitempty"`
	MCP                 map[string]AgentConfigMCPSource                   `json:"mcp,omitempty"`
	InteractionHandlers map[string]AgentConfigIntegrationCapabilitySource `json:"interaction_handlers,omitempty"`
	GitCredentials      *GitCredentialsSource                             `json:"git_credentials,omitempty"`
	Skills              []string                                          `json:"skills,omitempty"`
	Subagents           map[string]AgentConfigSubagentSource              `json:"subagents,omitempty"`
	MaxSubagents        *int                                              `json:"max_subagents,omitempty"`
	MaxDepth            *int                                              `json:"max_depth,omitempty"`
	EventWebhook        *EventWebhook                                     `json:"event_webhook,omitempty"`
	MemoryStores        []MemoryStoreSource                               `json:"memory_stores,omitempty"`
}

type MemoryStoreAccess string

const (
	MemoryStoreAccessRead      MemoryStoreAccess = "read"
	MemoryStoreAccessReadWrite MemoryStoreAccess = "read_write"
)

func (access MemoryStoreAccess) Valid() bool {
	return access == MemoryStoreAccessRead || access == MemoryStoreAccessReadWrite
}

type MemoryStoreSource struct {
	Name   string            `json:"name"`
	Access MemoryStoreAccess `json:"access"`
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
	Deferred    bool                      `json:"deferred,omitempty"`
	Description string                    `json:"description,omitempty"`
	InputSchema map[string]any            `json:"input_schema,omitempty"`
}

type AgentConfigMCPSource struct {
	URL            string                              `json:"url,omitempty"`
	Auth           *AgentConfigMCPAuthSource           `json:"auth,omitempty"`
	DefaultEnabled *bool                               `json:"default_enabled,omitempty"`
	Permission     *toolpermission.Selection           `json:"permission,omitempty"`
	Deferred       bool                                `json:"deferred,omitempty"`
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
	Deferred   *bool                     `json:"deferred,omitempty"`
}

func ParseSource(format SourceFormat, raw []byte) (AgentConfigSource, error) {
	parsed, _, err := parseSource(format, raw)
	return parsed, err
}

// CanonicalDefinitionJSON compacts JSON and sorts object keys. Invalid JSON is
// returned unchanged for the compiler to report.
func CanonicalDefinitionJSON(raw []byte) []byte {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return raw
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return raw
	}
	var canonical bytes.Buffer
	encoder := json.NewEncoder(&canonical)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return raw
	}
	return bytes.TrimSuffix(canonical.Bytes(), []byte("\n"))
}

func parseSource(format SourceFormat, raw []byte) (AgentConfigSource, *yaml.Node, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return AgentConfigSource{}, nil, errors.New("agent config source is required")
	}
	jsonSource, root, err := sourceJSON(format, raw)
	if err != nil {
		return AgentConfigSource{}, nil, validationErrorFrom(err, root)
	}
	jsonSource, err = canonicalizeSourceResourceReferences(jsonSource)
	if err != nil {
		return AgentConfigSource{}, nil, validationErrorFrom(err, root)
	}
	schema, err := compiledSourceSchema()
	if err != nil {
		return AgentConfigSource{}, nil, err
	}
	if err := validateSourceSchema(schema, jsonSource, root); err != nil {
		return AgentConfigSource{}, nil, err
	}
	var parsed AgentConfigSource
	if err := json.Unmarshal(jsonSource, &parsed); err != nil {
		return AgentConfigSource{}, nil, fmt.Errorf("decode agent config source: %w", err)
	}
	return parsed, root, nil
}

func canonicalizeSourceResourceReferences(raw []byte) ([]byte, error) {
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
	if !ok {
		return raw, nil
	}
	changed, err := canonicalizeJSONResourceReferences(root)
	if err != nil {
		return nil, err
	}
	if !changed {
		return raw, nil
	}
	normalized, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("encode canonical agent config JSON: %w", err)
	}
	return normalized, nil
}

func canonicalizeJSONResourceReferences(root map[string]any) (bool, error) {
	changed := false
	canonicalize := func(object map[string]any, key string, path string) error {
		value, ok := object[key].(string)
		if !ok {
			return nil
		}
		canonical, err := resourcename.CanonicalizeRequired(key, value)
		if err != nil {
			return NewIssue(path, err)
		}
		if canonical != value {
			object[key] = canonical
			changed = true
		}
		return nil
	}
	if model, ok := root["model"].(map[string]any); ok {
		for _, key := range []string{"provider_config", "name"} {
			if err := canonicalize(model, key, jsonPointer("model", key)); err != nil {
				return false, err
			}
		}
	}
	if subagents, ok := root["subagents"].(map[string]any); ok {
		for key, value := range subagents {
			subagent, ok := value.(map[string]any)
			if !ok {
				continue
			}
			if err := canonicalize(subagent, "profile", jsonPointer("subagents", key, "profile")); err != nil {
				return false, err
			}
			if model, ok := subagent["model"].(map[string]any); ok {
				for _, field := range []string{"provider_config", "name"} {
					if err := canonicalize(model, field, jsonPointer("subagents", key, "model", field)); err != nil {
						return false, err
					}
				}
			}
		}
	}
	if machineSources, ok := root["machine_sources"].([]any); ok {
		for index, value := range machineSources {
			machine, ok := value.(map[string]any)
			if !ok {
				continue
			}
			for _, key := range []string{"machine_name", "machine_pool_name"} {
				if err := canonicalize(machine, key, jsonPointer("machine_sources", index, key)); err != nil {
					return false, err
				}
			}
		}
	}
	return changed, nil
}

func sourceJSON(format SourceFormat, raw []byte) ([]byte, *yaml.Node, error) {
	switch format {
	case SourceFormatJSON:
		return raw, nil, nil
	case SourceFormatYAML:
		var root yaml.Node
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		if err := decoder.Decode(&root); err != nil {
			return nil, nil, yamlSyntaxIssue(err)
		}
		var extra yaml.Node
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			if err == nil {
				line, column := yamlLocation(&extra, "")
				return nil, &root, issueError{issue: Issue{Message: "trailing document", Line: line, Column: column}}
			}
			return nil, &root, yamlSyntaxIssue(err)
		}
		var value any
		if err := root.Decode(&value); err != nil {
			return nil, &root, yamlSyntaxIssue(err)
		}
		normalized, err := normalizeYAMLValue(value)
		if err != nil {
			return nil, &root, NewIssue("", err)
		}
		out, err := json.Marshal(normalized)
		if err != nil {
			return nil, &root, fmt.Errorf("marshal agent config YAML as JSON: %w", err)
		}
		return out, &root, nil
	default:
		return nil, nil, fmt.Errorf("unsupported agent config source format %q", format)
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

var compiledSourceSchema = sync.OnceValues(newCompiledSourceSchema)

func newCompiledSourceSchema() (*kjsonschema.Schema, error) {
	schemaJSON, err := SourceJSONSchema()
	if err != nil {
		return nil, err
	}
	compiled, err := kjsonschema.NewCompiler().Compile(schemaJSON)
	if err != nil {
		return nil, fmt.Errorf("compile agent config JSON schema: %w", err)
	}
	return compiled, nil
}

func validateSourceSchema(schema *kjsonschema.Schema, jsonSource []byte, root *yaml.Node) error {
	result := schema.Validate(jsonSource)
	if !result.IsValid() {
		return newValidationError(schemaIssues(result, schema), root)
	}
	return nil
}

// SourceJSONSchema returns the OpenAPI agent config definition as a JSON Schema.
func SourceJSONSchema() ([]byte, error) {
	return specJSONSchema(OpenAPIDefinitionComponent)
}
