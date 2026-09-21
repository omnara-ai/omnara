package agentconfig

import (
	kjsonschema "github.com/kaptinlin/jsonschema"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func addAppSourceSchema(schema *kjsonschema.Schema) {
	schema.Defs["AgentConfigAppCapabilitySource"] = kjsonschema.Object(
		kjsonschema.AdditionalProps(false),
	)
	(*schema.Properties)["interaction_handlers"] = kjsonschema.Object(
		kjsonschema.PropertyNames(kjsonschema.String(kjsonschema.Pattern(toolcatalog.AppNamePattern))),
		kjsonschema.AdditionalPropsSchema(kjsonschema.Ref("#/$defs/AgentConfigAppCapabilitySource")),
	)
}
