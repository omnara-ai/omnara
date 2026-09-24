package agentconfig

import (
	kjsonschema "github.com/kaptinlin/jsonschema"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func addIntegrationSourceSchema(schema *kjsonschema.Schema) {
	schema.Defs["AgentConfigIntegrationCapabilitySource"] = kjsonschema.Object(
		kjsonschema.AdditionalProps(false),
	)
	(*schema.Properties)["interaction_handlers"] = kjsonschema.Object(
		kjsonschema.PropertyNames(kjsonschema.String(kjsonschema.Pattern(toolcatalog.IntegrationNamePattern))),
		kjsonschema.AdditionalPropsSchema(kjsonschema.Ref("#/$defs/AgentConfigIntegrationCapabilitySource")),
	)
}
