//go:build live

package model_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/testutil/modeltest"
)

func TestLiveAnthropicReasoningEffort(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY is not set")
	}
	client := func(apiVariantOptions string) anthropicmessages.Client {
		return anthropicmessages.Client{
			Auth:              route.HeaderAuth{Header: "x-api-key", Value: apiKey},
			BaseURL:           os.Getenv("ANTHROPIC_BASE_URL"),
			EndpointPath:      modelstore.DefaultModelProviderEndpointPath(modelprotocol.APIFormatAnthropicMessages),
			ProviderModelSlug: modeltest.LiveAnthropicProviderModelSlug,
			APIVariantOptions: json.RawMessage(apiVariantOptions),
		}
	}

	for _, effort := range []string{"low", "high"} {
		t.Run("effort "+effort, func(t *testing.T) {
			prepared, response := liveReasoningRespond(t, client(""), effort,
				"What is 17 * 23? Reply with only the number.")
			requireLiveReasoningRequest(t, prepared, effort, `{"type":"adaptive"}`)
			if !strings.Contains(response.Text(), "391") {
				t.Fatalf("response text = %q, want 391", response.Text())
			}
			t.Logf("reasoning parts=%d usage=%+v", liveReasoningParts(response), response.Usage)
		})
	}

	t.Run("effort keeps api variant options", func(t *testing.T) {
		userID := uuid.NewString()
		apiVariantOptions := `{
			"metadata": {"user_id": "` + userID + `"},
			"output_config": {
				"effort": "max",
				"format": {
					"type": "json_schema",
					"schema": {
						"type": "object",
						"properties": {"product": {"type": "integer"}},
						"required": ["product"],
						"additionalProperties": false
					}
				}
			}
		}`
		prepared, response := liveReasoningRespond(t, client(apiVariantOptions), "low",
			"What is 17 * 23?")
		body := requireLiveReasoningRequest(t, prepared, "low", `{"type":"adaptive"}`)
		var metadata struct {
			UserID string `json:"user_id"`
		}
		if err := json.Unmarshal(body["metadata"], &metadata); err != nil || metadata.UserID != userID {
			t.Fatalf("metadata = %s, want user_id %s", body["metadata"], userID)
		}
		// The provider only returns schema-shaped JSON when output_config.format
		// survived alongside the adapter-owned effort.
		var answer struct {
			Product *int `json:"product"`
		}
		if err := json.Unmarshal([]byte(response.Text()), &answer); err != nil || answer.Product == nil {
			t.Fatalf("response text = %q, want JSON matching the output_config.format schema (%v)", response.Text(), err)
		}
		if *answer.Product != 391 {
			t.Fatalf("product = %d, want 391", *answer.Product)
		}
	})

	t.Run("effort keeps api variant thinking", func(t *testing.T) {
		thinking := `{"type":"adaptive","display":"summarized"}`
		prepared, response := liveReasoningRespond(t, client(`{"thinking":`+thinking+`}`), "high",
			"How many positive integers below 1000 are divisible by 7 but not by 11? "+
				"Reply with only the number.")
		requireLiveReasoningRequest(t, prepared, "high", thinking)
		if !strings.Contains(response.Text(), "130") {
			t.Fatalf("response text = %q, want 130", response.Text())
		}
		t.Logf("reasoning parts=%d usage=%+v", liveReasoningParts(response), response.Usage)
	})
}

func liveReasoningRespond(
	t *testing.T,
	client model.Client,
	effort string,
	prompt string,
) (model.PreparedRequest, model.Response) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	supportsReasoning := true
	prepared, err := client.Prepare(ctx, model.PrepareInput{
		Context: modelcontext.Bundle{
			AgentID:      uuid.New(),
			SystemPrompt: "You are a careful arithmetic assistant.",
			Messages:     []modelcontext.Message{liveTextMessage(modelprotocol.RoleUser, 10, prompt)},
		},
		Policy: model.RequestPolicy{
			MaxOutputTokens:   4096,
			SupportsReasoning: &supportsReasoning,
			ReasoningEffort:   effort,
		},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	response, err := client.Respond(ctx, model.Request{ProviderRequest: prepared.Body})
	if err != nil {
		t.Fatalf("respond: %v\nrequest: %s", err, prepared.Body)
	}
	return prepared, response
}

func requireLiveReasoningRequest(
	t *testing.T,
	prepared model.PreparedRequest,
	effort string,
	wantThinking string,
) map[string]json.RawMessage {
	t.Helper()
	var body map[string]json.RawMessage
	if err := json.Unmarshal(prepared.Body, &body); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	var outputConfig struct {
		Effort string `json:"effort"`
	}
	if err := json.Unmarshal(body["output_config"], &outputConfig); err != nil || outputConfig.Effort != effort {
		t.Fatalf("output_config = %s, want effort %q", body["output_config"], effort)
	}
	if got := string(body["thinking"]); got != wantThinking {
		t.Fatalf("thinking = %s, want %s", got, wantThinking)
	}
	return body
}

func liveReasoningParts(response model.Response) int {
	count := 0
	for _, part := range response.Content {
		if part.Type == model.ResponsePartTypeReasoning {
			count++
		}
	}
	return count
}
