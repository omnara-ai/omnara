package agentexecution

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/modelprice"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
)

type catalogEstimator struct {
	prices bool
	input  modelprice.Input
}

func (*catalogEstimator) Estimate(context.Context, modelprice.Input) modelenvelope.ProviderReportedCostUSD {
	return ""
}

func (e *catalogEstimator) Prices(input modelprice.Input) bool {
	e.input = input
	return e.prices
}

func TestModelPriced(t *testing.T) {
	pricing := modelprice.Pricing{Input: "1", Output: "2"}
	listed := executiondb.ExecutionModelRevisionRow{
		ProviderModelSlug: "gpt-x", BaseUrl: "https://api.openai.com/v1",
		ApiVariant: "default",
	}
	custom, openRouter := listed, listed
	custom.Pricing = &pricing
	openRouter.ApiVariant = "openrouter"
	tests := []struct {
		name      string
		estimator modelprice.Estimator
		revision  executiondb.ExecutionModelRevisionRow
		priced    bool
	}{
		{name: "custom prices", estimator: &catalogEstimator{}, revision: custom, priced: true},
		{name: "openrouter reports cost", estimator: &catalogEstimator{}, revision: openRouter, priced: true},
		{name: "listed in the catalog", estimator: &catalogEstimator{prices: true}, revision: listed, priced: true},
		{name: "not in the catalog", estimator: &catalogEstimator{}, revision: listed},
		{name: "no catalog", revision: listed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.priced, modelPriced(test.estimator, test.revision))
		})
	}
	estimator := &catalogEstimator{}
	modelPriced(estimator, listed)
	require.Equal(t, modelprice.Input{
		BaseURL: "https://api.openai.com/v1", APIVariant: "default",
		ConfiguredModel: "gpt-x",
	}, estimator.input)
}
