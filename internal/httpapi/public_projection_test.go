package httpapi

import (
	"github.com/omnara-ai/omnara/internal/httpapi/publicevents"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
)

func TestPublicModelUsageOmitsUnreportedCounts(t *testing.T) {
	usage := publicevents.ModelUsage(modelenvelope.Usage{InputTokens: 12, UncachedInputTokens: 12, OutputTokens: 3})
	if usage == nil || *usage.InputTokensTotal != 12 || *usage.OutputTokensTotal != 3 ||
		usage.CacheReadInputTokens != nil || usage.CacheWriteInputTokens != nil || usage.ReasoningOutputTokens != nil {
		t.Fatalf("usage = %+v, want totals only", usage)
	}
	if publicevents.ModelUsage(modelenvelope.Usage{}) != nil {
		t.Fatal("empty usage should be omitted entirely")
	}
}
