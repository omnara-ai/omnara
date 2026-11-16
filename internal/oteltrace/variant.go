package oteltrace

import "encoding/json"

type Variant string

const (
	VariantOTel       Variant = "otel"
	VariantLangfuse   Variant = "langfuse"
	VariantBraintrust Variant = "braintrust"
	VariantLangSmith  Variant = "langsmith"
)

func ParseVariant(value string) (Variant, bool) {
	switch variant := Variant(value); variant {
	case VariantOTel, VariantLangfuse, VariantBraintrust, VariantLangSmith:
		return variant, true
	case "":
		return VariantOTel, true
	default:
		return "", false
	}
}

type spanRole int

const (
	spanRoleAgent spanRole = iota
	spanRoleModel
	spanRoleTool
)

type spanContent struct {
	input, output             []message
	toolArguments, toolResult string
}

func (content spanContent) inputText() string {
	if content.toolArguments != "" {
		return content.toolArguments
	}
	return messagesText(content.input)
}

func (content spanContent) outputText() string {
	if content.toolResult != "" {
		return content.toolResult
	}
	return messagesText(content.output)
}

func (variant Variant) attributes(role spanRole, turn Turn, content spanContent, costUSD string) []KeyValue {
	var attributes []KeyValue
	if len(content.input) > 0 {
		attributes = append(attributes, stringAttr("gen_ai.input.messages", messagesJSON(content.input)))
	}
	if len(content.output) > 0 {
		attributes = append(attributes, stringAttr("gen_ai.output.messages", messagesJSON(content.output)))
	}
	input, output := content.inputText(), content.outputText()
	switch variant {
	case VariantOTel:
	case VariantLangfuse:
		observationType := [...]string{spanRoleAgent: "agent", spanRoleModel: "generation", spanRoleTool: "tool"}[role]
		attributes = append(attributes,
			stringAttr("langfuse.observation.type", observationType),
			stringAttr("langfuse.session.id", turn.RootAgentID),
		)
		if role == spanRoleAgent {
			attributes = append(attributes, stringAttr("langfuse.trace.name", turn.AgentName))
		}
		if input != "" {
			attributes = append(attributes, stringAttr("langfuse.observation.input", input))
		}
		if output != "" {
			attributes = append(attributes, stringAttr("langfuse.observation.output", output))
		}
		if costUSD != "" {
			attributes = append(attributes,
				stringAttr("langfuse.observation.cost_details", jsonObject(map[string]any{"total": json.Number(costUSD)})))
		}
	case VariantBraintrust:
		spanType := [...]string{spanRoleAgent: "task", spanRoleModel: "llm", spanRoleTool: "tool"}[role]
		attributes = append(attributes,
			stringAttr("braintrust.span_attributes", jsonObject(map[string]any{"type": spanType})),
			stringAttr("braintrust.metadata", jsonObject(map[string]any{
				"agent_id": turn.AgentID, "root_agent_id": turn.RootAgentID, "turn_id": turn.PublicTurnID,
			})),
		)
		if input != "" {
			attributes = append(attributes, stringAttr("braintrust.input_json", jsonValue(input)))
		}
		if output != "" {
			attributes = append(attributes, stringAttr("braintrust.output_json", jsonValue(output)))
		}
	case VariantLangSmith:
		runType := [...]string{spanRoleAgent: "chain", spanRoleModel: "llm", spanRoleTool: "tool"}[role]
		attributes = append(attributes,
			stringAttr("langsmith.span.kind", runType),
			stringAttr("langsmith.metadata.thread_id", turn.RootAgentID),
		)
		if role == spanRoleAgent {
			attributes = append(attributes, stringAttr("langsmith.trace.name", turn.AgentName))
		}
		if input != "" {
			attributes = append(attributes, stringAttr("gen_ai.prompt", input))
		}
		if output != "" {
			attributes = append(attributes, stringAttr("gen_ai.completion", output))
		}
	}
	return attributes
}

func jsonObject(value map[string]any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func jsonValue(text string) string {
	if json.Valid([]byte(text)) {
		return text
	}
	encoded, err := json.Marshal(text)
	if err != nil {
		return `""`
	}
	return string(encoded)
}
