package openaichatcompletions

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const callDeferredToolDescription = "Call a tool that tool_search loaded. Use only tool names returned by " +
	"tool_search and follow that tool's input_schema exactly."

type deferredToolCall struct {
	ToolName  string          `json:"tool_name"`
	Arguments json.RawMessage `json:"arguments"`
}

type toolSearchOutput struct {
	Pattern            string                              `json:"pattern"`
	ToolNames          []string                            `json:"tool_names"`
	TotalDeferredTools int                                 `json:"total_deferred_tools"`
	Tools              []modelcontext.ToolSearchDefinition `json:"tools"`
}

func callDeferredToolDefinition(compat compat) chatToolDefinition {
	var strict *bool
	if compat.sendsStrictFalse {
		strict = new(false)
	}
	return chatToolDefinition{
		Type: "function",
		Function: chatFunctionDefinition{
			Name:        toolcatalog.ToolNameCallDeferredTool,
			Description: callDeferredToolDescription,
			Parameters: json.RawMessage(`{"type":"object","properties":{` +
				`"tool_name":{"type":"string","description":"Exact name of a tool returned by tool_search."},` +
				`"arguments":{"type":"object","additionalProperties":true,` +
				`"description":"Arguments matching the tool's input_schema."}},` +
				`"required":["tool_name","arguments"],"additionalProperties":false}`),
			Strict: strict,
		},
	}
}

func unwrapDeferredToolCall(name string, arguments json.RawMessage) (string, json.RawMessage) {
	if name != toolcatalog.ToolNameCallDeferredTool {
		return name, arguments
	}
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	var call deferredToolCall
	if decoder.Decode(&call) != nil || strings.TrimSpace(call.ToolName) == "" {
		return name, arguments
	}
	input := bytes.TrimSpace(call.Arguments)
	var encoded string
	if json.Unmarshal(input, &encoded) == nil {
		input = bytes.TrimSpace([]byte(encoded))
	}
	if len(input) == 0 || bytes.Equal(input, []byte("null")) {
		return call.ToolName, json.RawMessage(`{}`)
	}
	return call.ToolName, json.RawMessage(input)
}

func wrapDeferredToolCall(name string, input json.RawMessage) (json.RawMessage, error) {
	return json.Marshal(deferredToolCall{ToolName: name, Arguments: input})
}

func deferredToolNames(specs []modelcontext.ToolSpec) map[string]bool {
	names := map[string]bool{}
	for _, spec := range modelcontext.DeferredToolSpecs(specs) {
		names[spec.Name] = true
	}
	return names
}

func toolSearchOutputContent(specs []modelcontext.ToolSpec, search modelcontext.ToolSearchResult) (string, error) {
	output := toolSearchOutput{
		Pattern:            search.Pattern,
		ToolNames:          search.ToolNames,
		TotalDeferredTools: search.TotalDeferredTools,
		Tools:              modelcontext.DeferredToolSearchDefinitions(specs, search),
	}
	if output.ToolNames == nil {
		output.ToolNames = []string{}
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
