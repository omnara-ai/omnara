package httpapi

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
)

func TestPublicModelUsageOmitsUnreportedCounts(t *testing.T) {
	usage := publicModelUsage(modelenvelope.Usage{InputTokens: 12, UncachedInputTokens: 12, OutputTokens: 3})
	if usage == nil || *usage.InputTokensTotal != 12 || *usage.OutputTokensTotal != 3 ||
		usage.CacheReadInputTokens != nil || usage.CacheWriteInputTokens != nil || usage.ReasoningOutputTokens != nil {
		t.Fatalf("usage = %+v, want totals only", usage)
	}
	if publicModelUsage(modelenvelope.Usage{}) != nil {
		t.Fatal("empty usage should be omitted entirely")
	}
}
