package openairesponses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/apivariantbody"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
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
		input.Policy,
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
		if input.Policy.ReasoningEffort != "" {
			payload.Reasoning = &responsesReasoning{Effort: input.Policy.ReasoningEffort}
		}
	}
	if input.Policy.MaxOutputTokens > 0 {
		payload.MaxOutputTokens = input.Policy.MaxOutputTokens
	}
	plan := model.PlanPromptCache(
		model.ProviderRoute{
			APIFormat:  modelprotocol.APIFormatOpenAIResponses,
			APIVariant: c.ModelAPIVariant(),
			BaseURL:    c.endpoint().ResolvedBaseURL(),
		},
		input.Context,
		input.Policy.CacheRetention,
	)
	payload.PromptCacheKey = plan.ConversationKey
	return apivariantbody.MarshalWithAPIVariantOptions(
		c.APIVariantOptions,
		payload,
		responsesOwnedFields(supportsReasoning, input.Policy.ReasoningEffort)...,
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

func validateToolResultProviderCallIDs(results []modelcontext.ToolResultRef) error {
	for _, result := range results {
		if result.ProviderCallID == "" {
			return fmt.Errorf("tool result %s is missing provider call id", result.DurableID)
		}
	}
	return nil
}

type responsesRequest struct {
	Model             string              `json:"model"`
	Stream            bool                `json:"stream"`
	Instructions      string              `json:"instructions,omitempty"`
	Input             []any               `json:"input"`
	Tools             []responsesTool     `json:"tools,omitempty"`
	ToolChoice        string              `json:"tool_choice,omitempty"`
	ParallelToolCalls bool                `json:"parallel_tool_calls,omitempty"`
	MaxOutputTokens   int                 `json:"max_output_tokens,omitempty"`
	PromptCacheKey    string              `json:"prompt_cache_key,omitempty"`
	Include           []string            `json:"include,omitempty"`
	Reasoning         *responsesReasoning `json:"reasoning,omitempty"`
	Store             bool                `json:"store"`
}

type responsesReasoning struct {
	Effort string `json:"effort,omitempty"`
}

type responsesTool struct {
	Type         string          `json:"type"`
	Name         string          `json:"name,omitempty"`
	Description  string          `json:"description,omitempty"`
	Parameters   json.RawMessage `json:"parameters"`
	Strict       *bool           `json:"strict,omitempty"`
	Execution    string          `json:"execution,omitempty"`
	DeferLoading bool            `json:"defer_loading,omitempty"`
}

func buildTools(specs []modelcontext.ToolSpec) []responsesTool {
	clientToolSearch := modelcontext.DeferredToolsEnabled(specs)
	tools := make([]responsesTool, 0, len(specs))
	for _, spec := range specs {
		if clientToolSearch && spec.Name == toolcatalog.ToolNameToolSearch {
			tools = append(tools, responsesTool{
				Type:        "tool_search",
				Execution:   "client",
				Description: spec.Description,
				Parameters:  toolParameters(spec),
			})
			continue
		}
		tools = append(tools, functionToolDefinition(spec))
	}
	return tools
}

func functionToolDefinition(spec modelcontext.ToolSpec) responsesTool {
	strict := false
	return responsesTool{
		Type:         "function",
		Name:         spec.Name,
		Description:  spec.Description,
		Parameters:   toolParameters(spec),
		Strict:       &strict,
		DeferLoading: spec.Deferred,
	}
}

func toolParameters(spec modelcontext.ToolSpec) json.RawMessage {
	if len(spec.InputSchema) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return spec.InputSchema
}
