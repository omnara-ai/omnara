package model

import (
	"context"
	"errors"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
)

var ErrGenerationUsageNotFound = errors.New("provider generation usage is not available")

type GenerationUsage struct {
	Usage                   Usage
	ProviderReportedCostUSD modelenvelope.ProviderReportedCostUSD
}

type SaveGenerationIDFunc func(ctx context.Context, generationID string) error
