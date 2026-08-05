package executionstore

import (
	"math"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
)

type normalizedUsageColumns struct {
	InputTokens         *int
	UncachedInputTokens *int
	OutputTokens        *int
	ReasoningTokens     *int
	CacheReadTokens     *int
	CacheWriteTokens    *int
}

func modelUsageForStorage(usage modelenvelope.Usage) modelenvelope.Usage {
	usage = modelenvelope.NormalizeUsage(usage)
	if usage.InputTokens > math.MaxInt32 ||
		usage.UncachedInputTokens > math.MaxInt32 ||
		usage.OutputTokens > math.MaxInt32 ||
		usage.ReasoningTokens > math.MaxInt32 ||
		usage.CacheReadTokens > math.MaxInt32 ||
		usage.CacheWriteTokens > math.MaxInt32 {
		return modelenvelope.Usage{}
	}
	return usage
}

func usageColumnsFromModelUsage(usage modelenvelope.Usage) normalizedUsageColumns {
	usage = modelUsageForStorage(usage)
	return normalizedUsageColumns{
		InputTokens:         positiveUsageTokenCountPtr(usage.InputTokens),
		UncachedInputTokens: positiveUsageTokenCountPtr(usage.UncachedInputTokens),
		OutputTokens:        positiveUsageTokenCountPtr(usage.OutputTokens),
		ReasoningTokens:     positiveUsageTokenCountPtr(usage.ReasoningTokens),
		CacheReadTokens:     positiveUsageTokenCountPtr(usage.CacheReadTokens),
		CacheWriteTokens:    positiveUsageTokenCountPtr(usage.CacheWriteTokens),
	}
}

func modelUsageFromSQLC(
	inputTokens,
	uncachedInputTokens,
	cacheReadTokens,
	cacheWriteTokens,
	outputTokens,
	reasoningTokens *int32,
) modelenvelope.Usage {
	return modelenvelope.NormalizeUsage(modelenvelope.Usage{
		InputTokens:         intFromSQLCPtr(inputTokens),
		UncachedInputTokens: intFromSQLCPtr(uncachedInputTokens),
		CacheReadTokens:     intFromSQLCPtr(cacheReadTokens),
		CacheWriteTokens:    intFromSQLCPtr(cacheWriteTokens),
		OutputTokens:        intFromSQLCPtr(outputTokens),
		ReasoningTokens:     intFromSQLCPtr(reasoningTokens),
	})
}

func positiveUsageTokenCountPtr(value int) *int {
	if value <= 0 {
		return nil
	}
	return &value
}

func intFromSQLCPtr(value *int32) int {
	if value == nil {
		return 0
	}
	return int(*value)
}
