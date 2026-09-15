package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestValidateBuiltinToolInputUsesRegisteredValidator(t *testing.T) {
	t.Parallel()
	implementation, found, err := toolImplementationFor(toolcatalog.ToolNameAskQuestion)
	require.NoError(t, err)
	require.True(t, found)
	call := model.ToolCall{Name: toolcatalog.ToolNameAskQuestion,
		Input: json.RawMessage(`{"questions":[{"prompt":"Proceed?","options":[{"label":"Yes"}]}]}`)}
	// Built-ins need no per-request definition. A runtime schema cannot replace
	// the registered validator either.
	for _, turn := range []Turn{{}, {Tools: map[string]ToolSpec{call.Name: {
		InputSchema: json.RawMessage(`{"type":"object","required":["unrelated"]}`),
	}}}} {
		require.NoError(t, validateToolInput(turn, call, implementation))
	}
	call.Input = json.RawMessage(`{"questions":[]}`)
	require.Error(t, validateToolInput(Turn{}, call, implementation))

	implementation, found, err = toolImplementationFor(toolcatalog.ToolNameSendChannelMessage)
	require.NoError(t, err)
	require.True(t, found)
	call = model.ToolCall{Name: toolcatalog.ToolNameSendChannelMessage, Input: json.RawMessage(`{"text":"hello"}`)}
	require.Error(t, validateToolInput(Turn{}, call, implementation), "channel_id remains required")
}

func TestValidateToolInputPreservesSemanticChecks(t *testing.T) {
	t.Parallel()
	schema, err := jsonschema.Compile(json.RawMessage(`{"type":"object"}`))
	require.NoError(t, err)
	semanticErr := errors.New("semantic rejection")
	implementation := toolImplementation{
		inputSchemaValidator: schema,
		toolRegistration: toolRegistration{semanticInputValidator: func(input json.RawMessage) error {
			require.Equal(t, json.RawMessage(`{"field":true}`), input)
			return semanticErr
		}},
	}
	call := model.ToolCall{Name: "test", Input: json.RawMessage(`{"field":true}`)}
	require.ErrorIs(t, validateToolInput(Turn{}, call, implementation), semanticErr)
}

func TestValidateCustomAndMCPInputPreservesPermissionArguments(t *testing.T) {
	t.Parallel()
	for _, toolType := range []string{toolcatalog.ToolTypeCustom, toolcatalog.ToolTypeMCP} {
		t.Run(toolType, func(t *testing.T) {
			t.Parallel()
			name := "custom_lookup"
			if toolType == toolcatalog.ToolTypeMCP {
				name = toolcatalog.MCPRuntimeToolName("docs", "lookup")
			}
			implementation, implemented, err := toolImplementationFor(name)
			require.NoError(t, err)
			selection := toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)
			turn := Turn{Tools: map[string]ToolSpec{name: {
				Type: toolType, Permission: selection,
				InputSchema: json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer","minimum":2},"omnara_channel":{"type":"null"}},"required":["count","omnara_channel"],"additionalProperties":false}`),
			}}}
			call := model.ToolCall{ID: "call", Name: name,
				Input: json.RawMessage(` {"count":9007199254740993,"omnara_channel":null} `)}
			before := bytes.Clone(call.Input)
			require.NoError(t, validateToolInput(turn, call, implementation))
			require.Equal(t, before, []byte(call.Input))
			handler, descriptor, supported, err := permissionModeForTool(toolType, selection, implementation, implemented)
			require.NoError(t, err)
			require.True(t, supported)
			result, err := handler(context.Background(), Executor{}, turn, call,
				permissionModeContext{selection: selection, descriptor: descriptor})
			require.NoError(t, err)
			require.Equal(t, permissionModeAsk, result.kind)
			require.True(t, jsoncanonical.Equal(call.Input, result.request.Authorization.Input))
			call.Input = json.RawMessage(`{"count":1,"omnara_channel":null}`)
			require.Error(t, validateToolInput(turn, call, implementation))
			require.NoError(t, validateToolInput(Turn{}, call, implementation), "missing tools defer to availability")
			turn.Tools[name] = ToolSpec{Type: toolType, Permission: selection}
			require.ErrorContains(t, validateToolInput(turn, call, implementation), "no runtime input schema")
		})
	}
}

func TestValidateToolInputRejectsMalformedJSONAndUsesSchemaComposition(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{"type":"object","properties":{"kind":{"enum":["a","b"]},"count":{"type":"integer","minimum":2}},"required":["kind"],"if":{"properties":{"kind":{"const":"a"}}},"then":{"required":["count"]},"additionalProperties":false}`)
	turn := Turn{Tools: map[string]ToolSpec{"test": {InputSchema: schema}}}
	for input, valid := range map[string]bool{
		`{"kind":"a","count":2}`: true,
		`{"kind":"b"}`:           true,
		`{"kind":"a"}`:           false,
		`{"kind":"a","count":1}`: false,
	} {
		err := validateToolInput(turn, model.ToolCall{Name: "test", Input: json.RawMessage(input)}, toolImplementation{})
		if valid {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
		}
	}
	for _, input := range []string{
		`null`, `[]`, `1`, `"object"`, ``, `{} {}`, `{} junk`,
		"{\"message\":\"before\xffafter\"}",
		`{"omnara_channel":"a","\u006fmnara_channel":"b"}`,
		`{"nested":[{"key":1,"\u006bey":2}]}`,
		`{"nested":` + strings.Repeat("[", jsoncanonical.MaxDepth) + `0` + strings.Repeat("]", jsoncanonical.MaxDepth) + `}`,
	} {
		call := model.ToolCall{Name: "test", Input: json.RawMessage(input)}
		require.ErrorContains(t, validateToolInput(turn, call, toolImplementation{}), "arguments")
		require.ErrorContains(t, validateToolInput(Turn{}, call, toolImplementation{}), "arguments",
			"an unavailable tool must still reject malformed arguments")
	}
}
