package agentconfig

import (
	kjsonschema "github.com/kaptinlin/jsonschema"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func addIntegrationSourceSchema(schema *kjsonschema.Schema) {
	schema.Defs["GitCredentialsSource"] = kjsonschema.Object(
		kjsonschema.Prop("integration", kjsonschema.String(kjsonschema.Pattern(toolcatalog.IntegrationNamePattern))),
		kjsonschema.Required("integration"),
		kjsonschema.AdditionalProps(false),
	)
	(*schema.Properties)["git_credentials"] = kjsonschema.Ref("#/$defs/GitCredentialsSource")
	schema.Defs["AgentConfigIntegrationCapabilitySource"] = kjsonschema.Object(
		kjsonschema.AdditionalProps(false),
	)
	(*schema.Properties)["interaction_handlers"] = kjsonschema.Object(
		kjsonschema.PropertyNames(kjsonschema.String(kjsonschema.Pattern(toolcatalog.IntegrationNamePattern))),
		kjsonschema.AdditionalPropsSchema(kjsonschema.Ref("#/$defs/AgentConfigIntegrationCapabilitySource")),
	)
}
