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

type saveGenerationIDKey struct{}

func WithSaveGenerationID(ctx context.Context, save SaveGenerationIDFunc) context.Context {
	return context.WithValue(ctx, saveGenerationIDKey{}, save)
}

func SaveGenerationIDFromContext(ctx context.Context) SaveGenerationIDFunc {
	save, _ := ctx.Value(saveGenerationIDKey{}).(SaveGenerationIDFunc)
	return save
}
