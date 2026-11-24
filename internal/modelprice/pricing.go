package modelprice

import (
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strconv"

	genai "github.com/pydantic/genai-prices/packages/go"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
)

const customProvider = "custom"

var priceDecimal = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]{1,15})?$`)

type Pricing struct {
	Input      string `json:"input_mtok"`
	CacheRead  string `json:"cache_read_mtok,omitempty"`
	CacheWrite string `json:"cache_write_mtok,omitempty"`
	Output     string `json:"output_mtok"`
}

func (p Pricing) Normalized() (Pricing, error) {
	if p.Input == "" || p.Output == "" {
		return Pricing{}, errors.New("input and output prices are required")
	}
	for _, price := range []*string{&p.Input, &p.CacheRead, &p.CacheWrite, &p.Output} {
		if *price == "" {
			continue
		}
		if !priceDecimal.MatchString(*price) {
			return Pricing{}, errors.New("prices must be non-negative decimals with at most 15 decimal places")
		}
		normalized, _ := modelenvelope.ParseCostTotalUSD(*price)
		*price = string(normalized)
	}
	for _, usage := range []modelenvelope.Usage{
		{InputTokens: math.MaxInt32, OutputTokens: math.MaxInt32},
		{InputTokens: math.MaxInt32, CacheReadTokens: math.MaxInt32, OutputTokens: math.MaxInt32},
		{InputTokens: math.MaxInt32, CacheWriteTokens: math.MaxInt32, OutputTokens: math.MaxInt32},
	} {
		if p.Cost(usage) == "" {
			return Pricing{}, errors.New("prices are too large to compute a call's cost")
		}
	}
	return p, nil
}

func (p Pricing) Free() bool {
	return p.Input == "0" && p.Output == "0" &&
		(p.CacheRead == "" || p.CacheRead == "0") && (p.CacheWrite == "" || p.CacheWrite == "0")
}

func (p Pricing) Cost(usage modelenvelope.Usage) modelenvelope.ProviderReportedCostUSD {
	if usage.Incomplete {
		return ""
	}
	prices := map[string]json.Number{"input_mtok": json.Number(p.Input), "output_mtok": json.Number(p.Output)}
	if p.CacheRead != "" {
		prices["cache_read_mtok"] = json.Number(p.CacheRead)
	}
	if p.CacheWrite != "" {
		prices["cache_write_mtok"] = json.Number(p.CacheWrite)
	}
	catalog, err := json.Marshal([]map[string]any{{
		"id": customProvider, "name": customProvider, "api_pattern": "^$",
		"models": []map[string]any{{
			"id": customProvider, "match": map[string]string{"equals": customProvider}, "prices": prices,
		}},
	}})
	if err != nil {
		return ""
	}
	calculator, err := genai.NewCalculatorFromJSON(catalog)
	if err != nil {
		return ""
	}
	calculation, err := calculator.Calculate(genai.PriceRequest{
		ProviderID: customProvider, Model: customProvider, Usage: genaiUsage(usage),
	})
	if err != nil {
		return ""
	}
	cost, ok := modelenvelope.ParseProviderReportedCostUSD(strconv.FormatFloat(calculation.TotalPrice, 'f', -1, 64))
	if !ok {
		return ""
	}
	return cost
}
