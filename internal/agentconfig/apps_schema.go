package agentconfig

import (
	kjsonschema "github.com/kaptinlin/jsonschema"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func addAppSourceSchema(schema *kjsonschema.Schema) {
	schema.Defs["AgentConfigAppCapabilitySource"] = kjsonschema.Object(
		kjsonschema.Prop("config", kjsonschema.Object()), kjsonschema.AdditionalProps(false),
	)
	for name, pattern := range map[string]string{
		"listeners":            `^[a-zA-Z][a-zA-Z0-9-]{0,31}__[a-zA-Z][a-zA-Z0-9_-]{0,63}$`,
		"interaction_handlers": toolcatalog.AppNamePattern,
	} {
		(*schema.Properties)[name] = kjsonschema.Object(
			kjsonschema.PropertyNames(kjsonschema.String(kjsonschema.Pattern(pattern))),
			kjsonschema.AdditionalPropsSchema(kjsonschema.Ref("#/$defs/AgentConfigAppCapabilitySource")),
		)
	}
}
