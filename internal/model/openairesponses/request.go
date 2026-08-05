package openairesponses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/apivariantbody"
	"github.com/omnara-ai/omnara/internal/modelcontext"
)

func (p protocol) BuildRequest(ctx context.Context, input model.PrepareInput) (json.RawMessage, error) {
	_ = ctx
	c := p.client
	if c.ProviderModelSlug == "" {
		return nil, errors.New("openai-responses provider model slug is required")
	}
	supportsTools := c.ModelCapabilities.SupportsTools
	if input.Policy.SupportsTools != nil {
		supportsTools = input.Policy.SupportsTools
	}
	if supportsTools != nil && !*supportsTools && len(input.Context.ToolSpecs) > 0 {
		return nil, errors.New("openai-responses model does not support tools")
	}
	if err := validateToolResultProviderCallIDs(input.Context.ToolResults); err != nil {
		return nil, err
	}
	requestInput, err := buildInput(
		input.Context,
		model.ProviderReplayIdentityForClient(c.ModelProviderConfigID, c),
		input.Policy.SuppressProviderReplay,
	)
	if err != nil {
		return nil, err
	}
	payload := responsesRequest{
		Model:        c.ProviderModelSlug,
		Instructions: modelcontext.ProjectedSystemPrompt(input.Context),
		Input:        requestInput,
		Tools:        buildTools(input.Context.ToolSpecs),
		Store:        false,
	}
	if len(payload.Tools) > 0 {
		payload.ToolChoice = "auto"
		payload.ParallelToolCalls = true
	}
	supportsReasoning := c.ModelCapabilities.SupportsReasoning
	if input.Policy.SupportsReasoning != nil {
		supportsReasoning = *input.Policy.SupportsReasoning
	}
	if supportsReasoning {
		payload.Include = []string{"reasoning.encrypted_content"}
		if input.Policy.DefaultReasoningEffort != "" {
			payload.Reasoning = &responsesReasoning{Effort: input.Policy.DefaultReasoningEffort}
		}
	}
	if input.Policy.MaxOutputTokens > 0 {
		payload.MaxOutputTokens = input.Policy.MaxOutputTokens
	}
	if retention := promptCacheRetention(input.Policy.CacheRetention); retention != "" {
		payload.PromptCacheRetention = retention
	}
	return apivariantbody.MarshalWithAPIVariantOptions(
		c.APIVariantOptions,
		payload,
		responsesOwnedFields(supportsReasoning, input.Policy.DefaultReasoningEffort)...,
	)
}

func responsesOwnedFields(supportsReasoning bool, reasoningEffort string) []string {
	fields := []string{
		"model",
		"stream",
		"instructions",
		"input",
		"tools",
		"tool_choice",
		"parallel_tool_calls",
		"max_output_tokens",
	}
	if supportsReasoning {
		fields = append(fields, "include")
	}
	if supportsReasoning && reasoningEffort != "" {
		fields = append(fields, "reasoning")
	}
	return fields
}

func promptCacheRetention(retention model.CacheRetention) string {
	switch retention {
	case model.CacheRetentionLong:
		return "24h"
	default:
		return ""
	}
}

func validateToolResultProviderCallIDs(results []modelcontext.ToolResultRef) error {
	for _, result := range results {
		if result.ProviderCallID == "" {
			return fmt.Errorf("tool result %s is missing provider call id", result.DurableID)
		}
	}
	return nil
}

type responsesRequest struct {
	Model                string              `json:"model"`
	Stream               bool                `json:"stream"`
	Instructions         string              `json:"instructions,omitempty"`
	Input                []any               `json:"input"`
	Tools                []responsesTool     `json:"tools,omitempty"`
	ToolChoice           string              `json:"tool_choice,omitempty"`
	ParallelToolCalls    bool                `json:"parallel_tool_calls,omitempty"`
	MaxOutputTokens      int                 `json:"max_output_tokens,omitempty"`
	PromptCacheRetention string              `json:"prompt_cache_retention,omitempty"`
	Include              []string            `json:"include,omitempty"`
	Reasoning            *responsesReasoning `json:"reasoning,omitempty"`
	Store                bool                `json:"store"`
}

type responsesReasoning struct {
	Effort string `json:"effort,omitempty"`
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

func buildTools(specs []modelcontext.ToolSpec) []responsesTool {
	tools := make([]responsesTool, 0, len(specs))
	for _, spec := range specs {
		parameters := spec.InputSchema
		if len(parameters) == 0 {
			parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		tools = append(
			tools,
			responsesTool{Type: "function", Name: spec.Name, Description: spec.Description, Parameters: parameters},
		)
	}
	return tools
}
