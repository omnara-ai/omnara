package model_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	"github.com/omnara-ai/omnara/internal/model/openairesponses"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type promptCacheRoute struct {
	name   string
	client model.Client
}

func promptCacheRoutes() []promptCacheRoute {
	return []promptCacheRoute{
		{name: "anthropic", client: anthropicmessages.Client{
			EndpointPath:      "/messages",
			ProviderModelSlug: "claude-sonnet-5",
		}},
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
		{name: "openai chat", client: openaichatcompletions.Client{
			EndpointPath:      "/chat/completions",
			ProviderModelSlug: "gpt-test",
		}},
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
			{
				Name:        toolcatalog.ToolNameRunCommand,
				InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`),
			},
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
		Content: json.RawMessage(
			`[{"type":"text","text":"Running ls."},{"type":"tool_call","tool_call_id":"tcl_1"}]`,
		),
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
	answer := modelcontext.Message{
		Role:     modelprotocol.RoleAssistant,
		Sequence: 40,
		Content:  text("Two files: a.txt and b.txt."),
	}
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

func TestPreparedRequestsExtendThePreviousTurnsPrefix(t *testing.T) {
	for _, route := range promptCacheRoutes() {
		t.Run(route.name, func(t *testing.T) {
			states := conversationStates("You are a careful assistant.")
			previous := modeltest.PreparePrefix(t, route.client, states[0])
			for index, state := range states[1:] {
				next := modeltest.PreparePrefix(t, route.client, state)
				if violation := modeltest.PrefixViolation(previous, next); violation != "" {
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
			states := conversationStates("You are a careful assistant. Now: 12:00")
			previous := modeltest.PreparePrefix(t, route.client, states[0])
			next := modeltest.PreparePrefix(t, route.client, conversationStates("You are a careful assistant. Now: 12:01")[1])
			if modeltest.PrefixViolation(previous, next) == "" {
				t.Fatal("a system prompt that changes between turns must be reported as a broken prefix")
			}
		})
	}
}
