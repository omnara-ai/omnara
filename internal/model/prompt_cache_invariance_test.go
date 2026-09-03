package model_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	"github.com/omnara-ai/omnara/internal/model/openairesponses"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type promptCacheRoute struct {
	name   string
	client model.Client
}

func promptCacheRoutes() []promptCacheRoute {
	return []promptCacheRoute{
		{name: "anthropic", client: anthropicmessages.Client{EndpointPath: "/messages", ProviderModelSlug: "claude-sonnet-5"}},
		{name: "openrouter claude", client: openaichatcompletions.Client{
			EndpointPath:      "/chat/completions",
			ProviderModelSlug: "anthropic/claude-sonnet-5",
			APIVariant:        modelprotocol.APIVariantOpenRouter,
		}},
		{name: "openrouter automatic", client: openaichatcompletions.Client{
			EndpointPath:      "/chat/completions",
			ProviderModelSlug: "moonshotai/kimi-k3",
			APIVariant:        modelprotocol.APIVariantOpenRouter,
		}},
		{name: "openai chat", client: openaichatcompletions.Client{EndpointPath: "/chat/completions", ProviderModelSlug: "gpt-test"}},
		{name: "openai responses", client: openairesponses.Client{EndpointPath: "/responses", ProviderModelSlug: "gpt-test"}},
	}
}

func conversationStates(systemPrompt string) []modelcontext.Bundle {
	text := func(value string) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`[{"type":"text","text":%q}]`, value))
	}
	base := modelcontext.Bundle{
		AgentID:      uuid.MustParse("0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"),
		SystemPrompt: systemPrompt,
		ToolSpecs: []modelcontext.ToolSpec{
			{Name: toolcatalog.ToolNameRunCommand, InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`)},
			{Name: toolcatalog.ToolNameSendIntegrationMessage},
		},
		IntegrationTargets: []modelcontext.IntegrationTargetRef{{
			TargetRef:       "slack-abcd",
			DurableID:       "internal-target-id",
			Provider:        "slack",
			ProviderRefKind: "thread",
			Label:           "slack thread C123",
			IsCurrent:       true,
		}},
	}
	user1 := modelcontext.Message{Role: modelprotocol.RoleUser, Sequence: 10, Content: text("list the files")}
	toolCall := modelcontext.Message{
		Role:               modelprotocol.RoleAssistant,
		Sequence:           20,
		ModelCallContextID: "mcc_1",
		Content:            json.RawMessage(`[{"type":"text","text":"Running ls."},{"type":"tool_call","tool_call_id":"tcl_1"}]`),
	}
	toolResult := modelcontext.ToolResultRef{
		ToolCallID:          "tcl_1",
		ModelCallContextID:  "mcc_1",
		ProviderCallID:      "call_1",
		Name:                toolcatalog.ToolNameRunCommand,
		Input:               json.RawMessage(`{"command":"ls"}`),
		ContentParts:        text("a.txt\nb.txt"),
		SourceEventSequence: 20,
		ResultEventSequence: 30,
	}
	answer := modelcontext.Message{Role: modelprotocol.RoleAssistant, Sequence: 40, Content: text("Two files: a.txt and b.txt.")}
	user2 := modelcontext.Message{Role: modelprotocol.RoleUser, Sequence: 50, Content: text("delete b.txt")}

	first := base
	first.Messages = []modelcontext.Message{user1}
	first.InputEventSequence = 10
	second := base
	second.Messages = []modelcontext.Message{user1, toolCall}
	second.ToolResults = []modelcontext.ToolResultRef{toolResult}
	second.InputEventSequence = 30
	third := base
	third.Messages = []modelcontext.Message{user1, toolCall, answer, user2}
	third.ToolResults = []modelcontext.ToolResultRef{toolResult}
	third.InputEventSequence = 50
	return []modelcontext.Bundle{first, second, third}
}

type preparedPrefix struct {
	static   map[string]any
	messages []any
}

func preparePrefix(t *testing.T, client model.Client, bundle modelcontext.Bundle) preparedPrefix {
	t.Helper()
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{
		Context: bundle,
		Policy:  model.RequestPolicy{MaxOutputTokens: 64},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(prepared.Body, &body); err != nil {
		t.Fatalf("decode prepared body: %v", err)
	}
	canonicalize(body)
	messagesKey := "messages"
	if client.APIFormat() == modelprotocol.APIFormatOpenAIResponses {
		messagesKey = "input"
	}
	messages, _ := body[messagesKey].([]any)
	delete(body, messagesKey)
	return preparedPrefix{static: body, messages: withoutTrailingSystemContext(messages)}
}

func canonicalize(value any) {
	switch value := value.(type) {
	case map[string]any:
		delete(value, "cache_control")
		if text, ok := value["content"].(string); ok {
			value["content"] = []any{map[string]any{"type": "text", "text": text}}
		}
		for _, child := range value {
			canonicalize(child)
		}
	case []any:
		for _, child := range value {
			canonicalize(child)
		}
	}
}

func withoutTrailingSystemContext(messages []any) []any {
	end := len(messages)
	for end > 1 {
		message, _ := messages[end-1].(map[string]any)
		if message["role"] != "system" {
			break
		}
		end--
	}
	return messages[:end]
}

func prefixViolation(previous, next preparedPrefix) string {
	if !reflect.DeepEqual(previous.static, next.static) {
		return "request fields outside the conversation changed"
	}
	if len(next.messages) < len(previous.messages) {
		return "conversation shrank"
	}
	for index := range previous.messages {
		if !reflect.DeepEqual(previous.messages[index], next.messages[index]) {
			return fmt.Sprintf("message %d changed", index)
		}
	}
	return ""
}

func TestPreparedRequestsExtendThePreviousTurnsPrefix(t *testing.T) {
	for _, route := range promptCacheRoutes() {
		t.Run(route.name, func(t *testing.T) {
			states := conversationStates("You are a careful assistant.")
			previous := preparePrefix(t, route.client, states[0])
			for index, state := range states[1:] {
				next := preparePrefix(t, route.client, state)
				if len(next.messages) <= len(previous.messages) {
					t.Fatalf("turn %d did not extend the conversation: %+v", index+1, next.messages)
				}
				if violation := prefixViolation(previous, next); violation != "" {
					t.Fatalf("turn %d broke the cached prefix: %s", index+1, violation)
				}
				previous = next
			}
		})
	}
}

func TestPrefixCheckerDetectsVolatileSystemPrompt(t *testing.T) {
	for _, route := range promptCacheRoutes() {
		t.Run(route.name, func(t *testing.T) {
			previous := preparePrefix(t, route.client, conversationStates("You are a careful assistant. Now: 12:00")[0])
			next := preparePrefix(t, route.client, conversationStates("You are a careful assistant. Now: 12:01")[1])
			if prefixViolation(previous, next) == "" {
				t.Fatal("a system prompt that changes between turns must be reported as a broken prefix")
			}
		})
	}
}
