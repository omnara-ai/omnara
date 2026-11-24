package openaichatcompletions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
)

const (
	openRouterGenerationTimeout   = 10 * time.Second
	openRouterGenerationBodyLimit = 1024 * 1024
)

type openRouterGeneration struct {
	TotalCost              json.RawMessage `json:"total_cost"`
	UpstreamInferenceCost  json.RawMessage `json:"upstream_inference_cost"`
	IsBYOK                 json.RawMessage `json:"is_byok"`
	NativeTokensPrompt     lenientCount    `json:"native_tokens_prompt"`
	NativeTokensCached     lenientCount    `json:"native_tokens_cached"`
	NativeTokensCompletion lenientCount    `json:"native_tokens_completion"`
	NativeTokensReasoning  lenientCount    `json:"native_tokens_reasoning"`
}

func (c Client) LookupGenerationUsage(ctx context.Context, generationID string) (model.GenerationUsage, error) {
	ctx, cancel := context.WithTimeout(ctx, openRouterGenerationTimeout)
	defer cancel()
	source := protocol{client: c}.errorSource()
	endpoint := c.endpoint().ResolvedBaseURL() + "/generation?" + url.Values{"id": {generationID}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return model.GenerationUsage{}, err
	}
	if c.Auth != nil {
		if err := c.Auth.Apply(req); err != nil {
			return model.GenerationUsage{}, err
		}
	}
	resp, err := c.generationHTTPClient().Do(req)
	if err != nil {
		return model.GenerationUsage{}, model.ProviderError{
			Kind:    model.ErrorKindTransient,
			Source:  source,
			Message: fmt.Sprintf("look up OpenRouter generation: %v", err),
			Cause:   err,
		}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return model.GenerationUsage{}, model.ErrGenerationUsageNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return model.GenerationUsage{}, model.ProviderError{
			Kind:       model.ErrorKindFromHTTPStatus(resp.StatusCode),
			Source:     source,
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("OpenRouter generation lookup returned HTTP status %d", resp.StatusCode),
			RequestID:  model.RequestIDFromHeader(resp.Header),
			RetryAfter: model.RetryAfterFromHeader(resp.Header),
		}
	}
	body, exceeded, err := route.ReadAllAndClose(route.StreamingResponse{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       resp.Body,
	}, openRouterGenerationBodyLimit)
	if err != nil {
		return model.GenerationUsage{}, model.ProviderError{
			Kind:       model.ErrorKindTransient,
			Source:     source,
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("read OpenRouter generation: %v", err),
			Cause:      err,
		}
	}
	if exceeded {
		return model.GenerationUsage{}, errors.New("OpenRouter generation response exceeds the supported size")
	}
	var payload struct {
		Data openRouterGeneration `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return model.GenerationUsage{}, fmt.Errorf("decode OpenRouter generation: %w", err)
	}
	return payload.Data.usage()
}

func (c Client) generationHTTPClient() *http.Client {
	if c.HTTPClient == nil {
		return outboundhttp.NewPublicClient(outboundhttp.PublicClientOptions{Timeout: openRouterGenerationTimeout})
	}
	return outboundhttp.CloneWithoutRedirects(c.HTTPClient)
}

func (g openRouterGeneration) usage() (model.GenerationUsage, error) {
	cost := g.cost()
	if cost == "" {
		return model.GenerationUsage{}, model.ErrGenerationUsageNotFound
	}
	tokens := model.Usage{
		InputTokens:     g.NativeTokensPrompt.count,
		CacheReadTokens: g.NativeTokensCached.count,
		OutputTokens:    g.NativeTokensCompletion.count,
		ReasoningTokens: g.NativeTokensReasoning.count,
		Incomplete:      !g.NativeTokensPrompt.valid || !g.NativeTokensCompletion.valid,
	}
	if tokens.ReasoningTokens > tokens.OutputTokens {
		tokens.ReasoningTokens = 0
	}
	return model.GenerationUsage{Usage: modelenvelope.NormalizeUsage(tokens), ProviderReportedCostUSD: cost}, nil
}

func (g openRouterGeneration) cost() modelenvelope.ProviderReportedCostUSD {
	isBYOK, valid := optionalProviderBool(g.IsBYOK)
	total, totalValid := optionalProviderCost(g.TotalCost)
	if !valid || isBYOK == nil || !totalValid || total.raw == "" {
		return ""
	}
	if !*isBYOK {
		return total.normalized
	}
	upstream, upstreamValid := optionalProviderCost(g.UpstreamInferenceCost)
	if !upstreamValid || upstream.raw == "" {
		return ""
	}
	if sum, ok := modelenvelope.SumProviderReportedCostUSD(total.raw, upstream.raw); ok {
		return sum
	}
	return ""
}
