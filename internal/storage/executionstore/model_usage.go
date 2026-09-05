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
		InputTokens:         modelenvelope.OptionalCount(usage.InputTokens),
		UncachedInputTokens: modelenvelope.OptionalCount(usage.UncachedInputTokens),
		OutputTokens:        modelenvelope.OptionalCount(usage.OutputTokens),
		ReasoningTokens:     modelenvelope.OptionalCount(usage.ReasoningTokens),
		CacheReadTokens:     modelenvelope.OptionalCount(usage.CacheReadTokens),
		CacheWriteTokens:    modelenvelope.OptionalCount(usage.CacheWriteTokens),
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

func intFromSQLCPtr(value *int32) int {
	if value == nil {
		return 0
	}
	return int(*value)
}
