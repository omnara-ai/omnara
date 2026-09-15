package model_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	"github.com/omnara-ai/omnara/internal/model/openairesponses"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestCurrentChannelNoticeFollowsInputsAcrossProviders(t *testing.T) {
	clients := map[string]model.Client{
		"responses": &openairesponses.Client{EndpointPath: "/responses", ProviderModelSlug: "test"},
		"chat":      &openaichatcompletions.Client{EndpointPath: "/chat/completions", ProviderModelSlug: "test"},
		"anthropic": &anthropicmessages.Client{EndpointPath: "/messages", ProviderModelSlug: "test"},
	}
	for name, client := range clients {
		t.Run(name, func(t *testing.T) {
			for _, channelID := range []string{"itgt_aaaaaaaaaaaaaaaaaaaaaaaaae", ""} {
				bundle := modelcontext.Bundle{
					CurrentChannelID: channelID,
					Messages: []modelcontext.Message{{Sequence: 1,
						Role:    modelprotocol.RoleUser,
						Content: json.RawMessage(`[{"type":"text","text":"original input"}]`)}},
					ToolSpecs: []modelcontext.ToolSpec{{Name: toolcatalog.ToolNameSetCurrentChannel,
						InputSchema: json.RawMessage(`{"type":"object"}`)}},
				}
				prepared, err := client.Prepare(context.Background(),
					model.PrepareInput{Context: bundle,
						Policy: model.RequestPolicy{MaxOutputTokens: 1024}})
				require.NoError(t, err)
				var payload map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(prepared.Body, &payload))
				key := "messages"
				if name == "responses" {
					key = "input"
				}
				var messages []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				}
				require.NoError(t, json.Unmarshal(payload[key], &messages))
				require.NotEmpty(t, messages)
				last := messages[len(messages)-1]
				notice := "Current channel: " + channelID + "."
				if channelID == "" {
					notice = "Current channel: none."
				}
				if name == "anthropic" {
					var blocks []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					}
					require.NoError(t, json.Unmarshal(last.Content, &blocks))
					require.Equal(t, "original input", blocks[0].Text)
					require.Equal(t, notice, blocks[len(blocks)-1].Text)
				} else {
					require.Equal(t, "system", last.Role)
					var content string
					require.NoError(t, json.Unmarshal(last.Content, &content))
					require.Equal(t, notice, content)
					require.GreaterOrEqual(t, len(messages), 2)
					require.Contains(t, string(messages[len(messages)-2].Content), "original input")
				}
				require.JSONEq(t,
					`[{"type":"text","text":"original input"}]`,
					string(bundle.Messages[0].Content),
					"harness notice must not mutate the sender's content")
			}
		})
	}
}
