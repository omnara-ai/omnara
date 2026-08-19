package anthropicmessages

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"

	"github.com/omnara-ai/omnara/internal/model"
)

const minimumManualThinkingBudgetTokens = 1_024

var _ model.OutputTokenLimitProvider = Client{}

func (c Client) OutputTokenLimits() (model.OutputTokenLimits, error) {
	budget, enabled, err := anthropicManualThinkingBudget(c.APIVariantOptions)
	if err != nil {
		return model.OutputTokenLimits{}, fmt.Errorf("invalid Anthropic thinking configuration: %w", err)
	}
	if !enabled {
		return model.OutputTokenLimits{}, nil
	}
	// Manual thinking shares Anthropic's total output limit and, without the
	// interleaved-thinking beta, budget_tokens must be less than max_tokens.
	// https://platform.claude.com/docs/en/build-with-claude/extended-thinking#budget-rules-and-tuning
	return model.OutputTokenLimits{Minimum: budget + 1}, nil
}

func anthropicManualThinkingBudget(options json.RawMessage) (int, bool, error) {
	options = bytes.TrimSpace(options)
	if len(options) == 0 || bytes.Equal(options, []byte("null")) {
		return 0, false, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(options, &fields); err != nil || fields == nil {
		if err == nil {
			err = fmt.Errorf("api_variant_options must be a JSON object")
		}
		return 0, false, err
	}
	thinkingRaw, found := fields["thinking"]
	if !found || bytes.Equal(bytes.TrimSpace(thinkingRaw), []byte("null")) {
		return 0, false, nil
	}
	var thinking map[string]json.RawMessage
	if err := json.Unmarshal(thinkingRaw, &thinking); err != nil || thinking == nil {
		if err == nil {
			err = fmt.Errorf("thinking must be a JSON object")
		}
		return 0, false, err
	}
	typeRaw, found := thinking["type"]
	if !found {
		return 0, false, nil
	}
	var thinkingType string
	if err := json.Unmarshal(typeRaw, &thinkingType); err != nil {
		return 0, false, fmt.Errorf("thinking.type must be a string: %w", err)
	}
	if thinkingType != "enabled" {
		return 0, false, nil
	}
	budgetRaw, found := thinking["budget_tokens"]
	if !found {
		return 0, false, fmt.Errorf("thinking.budget_tokens is required when thinking.type is enabled")
	}
	var budget int
	if err := json.Unmarshal(budgetRaw, &budget); err != nil {
		return 0, false, fmt.Errorf("thinking.budget_tokens must be an integer: %w", err)
	}
	if budget < minimumManualThinkingBudgetTokens {
		return 0, false, fmt.Errorf(
			"thinking.budget_tokens must be at least %d",
			minimumManualThinkingBudgetTokens,
		)
	}
	if budget == math.MaxInt {
		return 0, false, fmt.Errorf("thinking.budget_tokens is too large")
	}
	return budget, true, nil
}
