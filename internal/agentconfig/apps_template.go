package agentconfig

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sync"

	kjsonschema "github.com/kaptinlin/jsonschema"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

var appTemplateSchema = sync.OnceValues(func() (*kjsonschema.Schema, error) {
	schema := kjsonschema.Ref("#/$defs/AgentConfigAppResourceSource")
	schema.Defs = agentConfigSourceSchema().Defs
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	return kjsonschema.NewCompiler().Compile(raw)
})

// ValidateAppResourceTemplate validates project app setup without constructing a
// model/agent config, resolving references, or requiring event-derived scope.
// Every supplied scope is concrete and valid. Storage owns project/provider
// authorization of connection and secret references and launcher validation.
// Optional compile options let the owner validate MCP secret references.
func ValidateAppResourceTemplate(source AgentConfigAppResourceSource, options ...CompileOptions) error {
	var opts CompileOptions
	switch len(options) {
	case 0:
	case 1:
		opts = options[0]
	default:
		return fmt.Errorf("app resource template accepts at most one compile options value")
	}
	if source.Definition == "" || source.AppInstance != "" {
		return fmt.Errorf("app resource template must use an inline definition")
	}
	schema, err := appTemplateSchema()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(source)
	if err != nil {
		return err
	}
	if err := validateSourceSchema(schema, raw, nil); err != nil {
		return err
	}
	definition, ok := appdefinition.Lookup(source.Definition)
	if !ok {
		return issuef("/definition", "unknown app definition %q", source.Definition)
	}
	if source.Scope != nil {
		if err := source.Scope.Validate(definition.Provider); err != nil {
			return issueAt("/scope", err)
		}
	}
	if err := definition.ValidateSelection(source.Listener, source.InteractionHandler); err != nil {
		return err
	}
	catalog, err := toolcatalog.Default()
	if err != nil {
		return err
	}
	needsConnection := source.Listener != nil || source.Follow != nil || source.InteractionHandler != nil
	for name, tool := range source.Tools {
		enabled := tool.Enabled == nil || *tool.Enabled
		if tool.Type == toolcatalog.ToolTypeCustom {
			_, err = compileCustomTool(name, tool, enabled, catalog)
		} else {
			_, err = compileBuiltInTool(name, tool, enabled, catalog)
		}
		if err != nil {
			return err
		}
		if provider := toolcatalog.AppToolProvider(name); provider != "" && provider != definition.Provider {
			return issuef(jsonPointer("tools", name), "tool does not belong to provider %s", definition.Provider)
		}
		needsConnection = needsConnection || enabled
	}
	if needsConnection && source.Connection == "" {
		return issuef("/connection", "selected capabilities require a connection")
	}
	if source.Connection != "" {
		if _, err := publicid.Decode(publicid.KindIntegrationConnection, source.Connection); err != nil {
			return issueAt("/connection", err)
		}
	}
	if err := validateAppMCPCredentials(source.MCP); err != nil {
		return err
	}
	_, err = compileMCPServers(source.MCP, opts)
	return err
}

func validateAppMCPCredentials(servers map[string]AgentConfigMCPSource) error {
	for name, server := range servers {
		parsed, err := url.Parse(server.URL)
		if err != nil {
			return issuef(jsonPointer("mcp", name, "url"), "invalid MCP URL")
		}
		if parsed.User != nil {
			return issuef(jsonPointer("mcp", name, "url"), "credentials must use an auth secret reference")
		}
	}
	return nil
}
