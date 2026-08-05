package executionstore

import (
	"math"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
)

func TestModelUsageForStorageNormalizesProviderUsage(t *testing.T) {
	usage := modelUsageForStorage(modelenvelope.Usage{
		InputTokens:      20,
		OutputTokens:     7,
		ReasoningTokens:  3,
		CacheReadTokens:  8,
		CacheWriteTokens: 2,
	})
	if usage != (modelenvelope.Usage{
		InputTokens:         20,
		UncachedInputTokens: 10,
		OutputTokens:        7,
		ReasoningTokens:     3,
		CacheReadTokens:     8,
		CacheWriteTokens:    2,
	}) {
		t.Fatalf("normalized usage = %+v", usage)
	}
}

func TestModelUsageForStorageDropsInvalidOrUnrepresentableUsage(t *testing.T) {
	tests := []modelenvelope.Usage{
		{InputTokens: -1},
		{InputTokens: math.MaxInt32 + 1},
		{InputTokens: 10, OutputTokens: 2, ReasoningTokens: 3},
		{InputTokens: 10, CacheReadTokens: 11},
	}
	for _, usage := range tests {
		if got := modelUsageForStorage(usage); got != (modelenvelope.Usage{}) {
			t.Fatalf("invalid usage %+v normalized to %+v", usage, got)
		}
	}
}

func TestUsageColumnsOmitUnreportedZeroValues(t *testing.T) {
	columns := usageColumnsFromModelUsage(modelenvelope.Usage{
		InputTokens:         10,
		UncachedInputTokens: 10,
		OutputTokens:        4,
	})
	if columns.InputTokens == nil || *columns.InputTokens != 10 ||
		columns.UncachedInputTokens == nil || *columns.UncachedInputTokens != 10 ||
		columns.OutputTokens == nil || *columns.OutputTokens != 4 ||
		columns.CacheReadTokens != nil || columns.CacheWriteTokens != nil ||
		columns.ReasoningTokens != nil {
		t.Fatalf("usage columns = %+v", columns)
	}
}
