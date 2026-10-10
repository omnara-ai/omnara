package executionstore

import (
	"github.com/omnara-ai/omnara/internal/modelenvelope"
)

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
