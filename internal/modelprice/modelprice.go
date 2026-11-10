package modelprice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	genai "github.com/pydantic/genai-prices/packages/go"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	log "github.com/omnara-ai/omnara/observability/wideevent"
)

const (
	Off             = "off"
	refreshInterval = time.Hour
	fetchTimeout    = 30 * time.Second
	maxDataBytes    = 16 * 1024 * 1024
	builtinSource   = "builtin"
	minPricedModels = 50
)

// genai-prices prices mistral-large-latest as legacy Mistral Large ($2/$6), but Mistral points it at
// mistral-large-2512 ($0.50/$1.50). Remove an entry once the bundled price data maps it correctly.
var knownBadAliases = map[string]bool{
	"mistral-large-latest": true,
}

var pricedServiceTiers = map[string]bool{"flex": true, "priority": true, "fast": true}

type Input struct {
	BaseURL         string
	APIFormat       modelprotocol.APIFormat
	APIVariant      modelprotocol.APIVariant
	ConfiguredModel string
	ServedModel     string
	Usage           modelenvelope.Usage
	Metadata        modelenvelope.ProviderMetadata
	CreatedAt       time.Time
}

type Estimator interface {
	Estimate(ctx context.Context, input Input) modelenvelope.ProviderReportedCostUSD
}

type Result struct {
	CostUSD    modelenvelope.ProviderReportedCostUSD
	Provider   string
	Model      string
	Assumption string
	Reason     string
}

type priceCalculator interface {
	Calculate(request genai.PriceRequest) (genai.PriceCalculation, error)
}

type snapshot struct {
	calculator   priceCalculator
	source       string
	pricedModels int
}

type Pricer struct {
	url     string
	client  *http.Client
	current atomic.Pointer[snapshot]
}

func New(dataURL string) (*Pricer, error) {
	calculator, err := genai.NewCalculator()
	if err != nil {
		return nil, fmt.Errorf("load built-in model prices: %w", err)
	}
	if dataURL == "" {
		dataURL = genai.RemoteDataURL
	}
	pricer := &Pricer{url: dataURL, client: &http.Client{Timeout: fetchTimeout}}
	pricer.current.Store(&snapshot{calculator: calculator, source: builtinSource})
	return pricer, nil
}

func (p *Pricer) Run(ctx context.Context) {
	if p.url == Off {
		return
	}
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	for {
		event := log.NewEvent(ctx, "model_prices.refresh", log.Fields{"model_prices.url": p.url})
		err := p.refresh(ctx)
		if ctx.Err() == nil {
			event.Attach(log.Fields{"model_prices.price_data": p.current.Load().source})
			if err != nil {
				event.Level(log.WarnLevel)
				event.Error(err)
			} else {
				event.Level(log.DebugLevel)
			}
			event.Done(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *Pricer) refresh(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDataBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxDataBytes {
		return fmt.Errorf("price data exceeds %d bytes", maxDataBytes)
	}
	next, err := parseSnapshot(data, p.current.Load().pricedModels)
	if err != nil {
		return err
	}
	p.current.Store(next)
	return nil
}

func parseSnapshot(data []byte, previousPricedModels int) (next *snapshot, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			next, err = nil, fmt.Errorf("price data panicked: %v", recovered)
		}
	}()
	calculator, err := genai.NewCalculatorFromJSON(data)
	if err != nil {
		return nil, err
	}
	priced, err := countPricedModels(calculator, data)
	if err != nil {
		return nil, err
	}
	if priced < minPricedModels {
		return nil, fmt.Errorf("price data has %d priced models, want at least %d", priced, minPricedModels)
	}
	if priced*2 < previousPricedModels {
		return nil, fmt.Errorf("price data shrank from %d to %d priced models", previousPricedModels, priced)
	}
	digest := sha256.Sum256(data)
	return &snapshot{calculator: calculator, source: hex.EncodeToString(digest[:6]), pricedModels: priced}, nil
}

func countPricedModels(calculator *genai.Calculator, data []byte) (int, error) {
	var providers []struct {
		ID     string `json:"id"`
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &providers); err != nil {
		return 0, err
	}
	usage := genai.Usage{genai.UsageInputTokens: 1_000_000, genai.UsageOutputTokens: 1_000_000}
	now := time.Now()
	count := 0
	for _, provider := range providers {
		for _, model := range provider.Models {
			result, err := calculator.Calculate(genai.PriceRequest{
				ProviderID: provider.ID, Model: model.ID, Usage: usage, Timestamp: now,
			})
			if err == nil && validPrice(result.TotalPrice) {
				count++
			}
		}
	}
	return count, nil
}

func (p *Pricer) Estimate(ctx context.Context, input Input) modelenvelope.ProviderReportedCostUSD {
	current := p.current.Load()
	result := estimate(current.calculator, input)
	fields := log.Fields{"model_response.estimated_cost_usd.price_data": current.source}
	if result.CostUSD == "" {
		fields["model_response.estimated_cost_usd.unavailable_reason"] = result.Reason
	} else {
		fields["model_response.estimated_cost_usd.provider"] = result.Provider
		fields["model_response.estimated_cost_usd.model"] = result.Model
		if result.Assumption != "" {
			fields["model_response.estimated_cost_usd.assumption"] = result.Assumption
		}
	}
	log.Attach(ctx, fields)
	return result.CostUSD
}

func estimate(calculator priceCalculator, input Input) (result Result) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = Result{Reason: "price_lookup_panic"}
		}
	}()
	usage := input.Usage
	if usage.Incomplete {
		return Result{Reason: "incomplete_usage"}
	}
	if usage.InputTokens <= 0 && usage.OutputTokens <= 0 {
		return Result{Reason: "no_usage"}
	}
	anthropic := input.Metadata.Anthropic
	if anthropic.Speed != "" || anthropic.InferenceGeo != "" || anthropic.ServiceTier != "" ||
		input.Metadata.OpenAI.CacheWrite12hInputTokens > 0 {
		return Result{Reason: "unpriced_rate_class"}
	}
	tier := input.Metadata.OpenAI.ServiceTier
	if tier != "" && !pricedServiceTiers[tier] {
		return Result{Reason: "unpriced_service_tier"}
	}

	request := genai.PriceRequest{Timestamp: input.CreatedAt, Usage: genai.Usage{
		genai.UsageInputTokens:      float64(usage.InputTokens),
		genai.UsageCacheReadTokens:  float64(usage.CacheReadTokens),
		genai.UsageCacheWriteTokens: float64(usage.CacheWriteTokens),
		genai.UsageOutputTokens:     float64(usage.OutputTokens),
	}}
	bedrock := input.APIVariant == modelprotocol.APIVariantBedrock
	if bedrock {
		request.ProviderID = "aws"
	} else {
		request.ProviderAPIURL = input.BaseURL
	}
	if usage.CacheWriteTokens > 0 && input.APIFormat == modelprotocol.APIFormatAnthropicMessages &&
		(bedrock || anthropicHost(input.BaseURL)) {
		if oneHour := anthropic.CacheWrite1hInputTokens; oneHour != nil {
			request.Usage[genai.UsageCacheWrite1HTokens] = float64(*oneHour)
		} else {
			request.Usage[genai.UsageCacheWrite1HTokens] = float64(usage.CacheWriteTokens)
			result.Assumption = "cache_write_split_missing_priced_as_1h"
		}
	}

	reason := "unknown_model"
	for _, name := range candidateNames(input) {
		if knownBadAliases[name] {
			reason = "known_bad_alias"
			continue
		}
		request.Model = name
		request.PriceContext = nil
		calculation, err := calculator.Calculate(request)
		if errors.Is(err, genai.ErrModelNotFound) {
			continue
		}
		if errors.Is(err, genai.ErrProviderNotFound) {
			return Result{Reason: "unknown_provider"}
		}
		if err != nil {
			return Result{Reason: "price_lookup_error"}
		}
		if tier != "" {
			standard := calculation.TotalPrice
			request.PriceContext = map[string]string{"service_tier": tier}
			calculation, err = calculator.Calculate(request)
			if err != nil || calculation.TotalPrice == standard {
				return Result{Reason: "unpriced_service_tier"}
			}
		}
		cost, ok := decimalCost(calculation.TotalPrice)
		if !ok {
			return Result{Reason: "zero_price"}
		}
		result.CostUSD = cost
		result.Provider = calculation.ProviderID
		result.Model = calculation.ModelID
		return result
	}
	return Result{Reason: reason}
}

func candidateNames(input Input) []string {
	served := normalizedName(input.ServedModel)
	configured := normalizedName(input.ConfiguredModel)
	names := []string{served, configured}
	if input.APIVariant == modelprotocol.APIVariantBedrock {
		names = []string{configured, served}
	}
	if index := strings.LastIndex(configured, "/"); index >= 0 {
		names = append(names, configured[index+1:])
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name != "" && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

func normalizedName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func validPrice(value float64) bool {
	return value > 0 && !math.IsInf(value, 0) && !math.IsNaN(value)
}

func decimalCost(value float64) (modelenvelope.ProviderReportedCostUSD, bool) {
	if !validPrice(value) {
		return "", false
	}
	cost, ok := modelenvelope.ParseProviderReportedCostUSD(strconv.FormatFloat(value, 'f', -1, 64))
	if !ok || cost == "0" {
		return "", false
	}
	return cost, true
}

func anthropicHost(baseURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "api.anthropic.com" || strings.HasSuffix(host, ".api.anthropic.com")
}
