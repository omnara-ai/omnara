package agentconfig

import (
	kjsonschema "github.com/kaptinlin/jsonschema"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func addAppSourceSchema(schema *kjsonschema.Schema) {
	ref := func(name string) *kjsonschema.Schema { return kjsonschema.Ref("#/$defs/" + name) }
	text := func() *kjsonschema.Schema {
		return kjsonschema.String(kjsonschema.MinLength(1), kjsonschema.Pattern(`\S`))
	}
	toolMap := func() *kjsonschema.Schema {
		return kjsonschema.Object(
			kjsonschema.PropertyNames(kjsonschema.AllOf(
				kjsonschema.String(kjsonschema.Pattern(toolcatalog.ToolNamePattern)),
				kjsonschema.Not(kjsonschema.String(kjsonschema.Pattern(`^`+toolcatalog.MCPRuntimeToolPrefix))),
			)),
			kjsonschema.AdditionalPropsSchema(ref("AgentConfigAppToolSource")),
		)
	}
	// App selections may omit a bundled definition. Compilation validates the
	// complete definition after instance resolution; base declarations stay strict.
	tool := *schema.Defs["AgentConfigToolSource"]
	tool.If, tool.Then, tool.Else = nil, nil, nil
	schema.Defs["AgentConfigAppToolSource"] = &tool
	mcp := *schema.Defs["AgentConfigMCPSource"]
	mcp.Required = nil
	schema.Defs["AgentConfigAppMCPSource"] = &mcp
	schema.Defs["AgentConfigAppResourceSource"] = kjsonschema.Object(
		kjsonschema.Prop("definition", text()),
		kjsonschema.Prop("app_instance", kjsonschema.String(kjsonschema.Pattern(`^app_[a-z2-7]{26}$`))),
		kjsonschema.Prop("enabled", kjsonschema.Boolean()),
		kjsonschema.Prop("connection", kjsonschema.String(kjsonschema.Pattern(`^iin_[a-z2-7]{26}$`))),
		kjsonschema.Prop("scope", ref("AppScope")),
		kjsonschema.Prop("tools", toolMap()),
		kjsonschema.Prop("mcp", kjsonschema.Object(
			kjsonschema.PropertyNames(kjsonschema.String(kjsonschema.Pattern(toolcatalog.MCPServerKeyPattern))),
			kjsonschema.AdditionalPropsSchema(ref("AgentConfigAppMCPSource")),
		)),
		kjsonschema.Prop("listener", ref("AppListener")),
		kjsonschema.Prop("follow", ref("AppFollow")),
		kjsonschema.Prop("interaction_handler", ref("AppInteractionHandler")),
		kjsonschema.AdditionalProps(false),
		kjsonschema.Keyword(func(s *kjsonschema.Schema) {
			s.OneOf = []*kjsonschema.Schema{
				kjsonschema.Object(kjsonschema.Required("definition")),
				kjsonschema.Object(kjsonschema.Required("app_instance")),
			}
		}),
	)
	schema.Defs["AppScope"] = kjsonschema.Object(
		kjsonschema.Prop("slack", ref("AppSlackScope")),
		kjsonschema.Prop("github", ref("AppGitHubScope")),
		kjsonschema.Prop("discord", ref("AppDiscordScope")),
		kjsonschema.AdditionalProps(false),
		kjsonschema.Keyword(func(s *kjsonschema.Schema) {
			for _, provider := range []string{"slack", "github", "discord"} {
				s.OneOf = append(s.OneOf, kjsonschema.Object(kjsonschema.Required(provider)))
			}
		}),
	)
	schema.Defs["AppSlackScope"] = kjsonschema.Object(
		kjsonschema.Prop("channel_id", text()), kjsonschema.Prop("thread_ts", text()),
		kjsonschema.Required("channel_id"), kjsonschema.AdditionalProps(false),
	)
	schema.Defs["AppGitHubScope"] = kjsonschema.Object(
		kjsonschema.Prop("repository_id", kjsonschema.Integer(kjsonschema.Min(1))),
		kjsonschema.Prop("pull_request", kjsonschema.Integer(kjsonschema.Min(1))),
		kjsonschema.Required("repository_id", "pull_request"), kjsonschema.AdditionalProps(false),
	)
	schema.Defs["AppDiscordScope"] = kjsonschema.Object(
		kjsonschema.Prop(
			"guild_id",
			text(),
		),
		kjsonschema.Prop("channel_id", text()),
		kjsonschema.Prop("thread_id", text()),
		kjsonschema.Required("channel_id"),
		kjsonschema.AdditionalProps(false),
	)
	schema.Defs["AppListener"] = kjsonschema.Object(
		kjsonschema.Prop(
			"events",
			kjsonschema.Array(kjsonschema.Items(text()), kjsonschema.MinItems(1), kjsonschema.UniqueItems(true)),
		),
		kjsonschema.Required("events"),
		kjsonschema.AdditionalProps(false),
	)
	schema.Defs["AppFollow"] = kjsonschema.Object(
		kjsonschema.Prop(
			"replies",
			kjsonschema.Boolean(),
		),
		kjsonschema.Required("replies"),
		kjsonschema.AdditionalProps(false),
	)
	schema.Defs["AppInteractionHandler"] = kjsonschema.Object(
		kjsonschema.Prop("definition", text()), kjsonschema.Required("definition"), kjsonschema.AdditionalProps(false),
	)
	(*schema.Properties)["app_resources"] = kjsonschema.Object(
		kjsonschema.PropertyNames(kjsonschema.String(kjsonschema.Pattern(toolcatalog.ToolNamePattern))),
		kjsonschema.AdditionalPropsSchema(ref("AgentConfigAppResourceSource")),
	)
}
