package oteltrace

import (
	"encoding/json"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

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

func (variant Variant) putAttributes(
	attributes pcommon.Map, role spanRole, turn Turn, content spanContent, costUSD string,
) {
	if len(content.input) > 0 {
		attributes.PutStr("gen_ai.input.messages", messagesJSON(content.input))
	}
	if len(content.output) > 0 {
		attributes.PutStr("gen_ai.output.messages", messagesJSON(content.output))
	}
	input, output := content.inputText(), content.outputText()
	switch variant {
	case VariantOTel:
	case VariantLangfuse:
		observationType := [...]string{spanRoleAgent: "agent", spanRoleModel: "generation", spanRoleTool: "tool"}[role]
		attributes.PutStr("langfuse.observation.type", observationType)
		attributes.PutStr("langfuse.session.id", turn.AgentID)
		if role == spanRoleAgent {
			attributes.PutStr("langfuse.trace.name", turn.AgentName)
		}
		if input != "" {
			attributes.PutStr("langfuse.observation.input", input)
		}
		if output != "" {
			attributes.PutStr("langfuse.observation.output", output)
		}
		if costUSD != "" {
			attributes.PutStr("langfuse.observation.cost_details",
				jsonObject(map[string]any{"total": json.Number(costUSD)}))
		}
	case VariantBraintrust:
		spanType := [...]string{spanRoleAgent: "task", spanRoleModel: "llm", spanRoleTool: "tool"}[role]
		attributes.PutStr("braintrust.span_attributes", jsonObject(map[string]any{"type": spanType}))
		attributes.PutStr("braintrust.metadata", jsonObject(map[string]any{
			"agent_id": turn.AgentID, "root_agent_id": turn.RootAgentID, "turn_id": turn.PublicTurnID,
		}))
		if input != "" {
			attributes.PutStr("braintrust.input_json", jsonValue(input))
		}
		if output != "" {
			attributes.PutStr("braintrust.output_json", jsonValue(output))
		}
	case VariantLangSmith:
		runType := [...]string{spanRoleAgent: "chain", spanRoleModel: "llm", spanRoleTool: "tool"}[role]
		attributes.PutStr("langsmith.span.kind", runType)
		attributes.PutStr("langsmith.metadata.thread_id", turn.AgentID)
		if role == spanRoleAgent {
			attributes.PutStr("langsmith.trace.name", turn.AgentName)
		}
		if input != "" {
			attributes.PutStr("gen_ai.prompt", input)
		}
		if output != "" {
			attributes.PutStr("gen_ai.completion", output)
		}
	}
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
	return jsonString(text)
}

func jsonString(text string) string {
	encoded, err := json.Marshal(text)
	if err != nil {
		return `""`
	}
	return string(encoded)
}
