package logent

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
)

func TestModelCallRecordCacheRatio(t *testing.T) {
	for _, tc := range []struct {
		usage modelenvelope.Usage
		ratio float64
		known bool
	}{
		{modelenvelope.Usage{}, 0, false},
		{modelenvelope.Usage{OutputTokens: 3}, 0, false},
		{modelenvelope.Usage{InputTokens: 10}, 0, false},
		{modelenvelope.Usage{InputTokens: 10, CacheReported: true}, 0, true},
		{modelenvelope.Usage{InputTokens: 10, CacheReadTokens: 4, CacheReported: true}, 0.4, true},
	} {
		ratio, known := (ModelCallRecord{Usage: tc.usage}).CacheRatio()
		if ratio != tc.ratio || known != tc.known {
			t.Errorf("CacheRatio(%+v) = %v, %v; want %v, %v", tc.usage, ratio, known, tc.ratio, tc.known)
		}
	}
}
