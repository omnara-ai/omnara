package logent

import "testing"

func TestModelCallRecordCacheRatio(t *testing.T) {
	for _, tc := range []struct {
		usage *ModelCallUsage
		ratio float64
		known bool
	}{
		{nil, 0, false},
		{&ModelCallUsage{OutputTokens: 3}, 0, false},
		{&ModelCallUsage{InputTokens: 10}, 0, false},
		{&ModelCallUsage{InputTokens: 10, CacheReported: true}, 0, true},
		{&ModelCallUsage{InputTokens: 10, CachedInputTokens: 4, CacheReported: true}, 0.4, true},
	} {
		ratio, known := (ModelCallRecord{Usage: tc.usage}).CacheRatio()
		if ratio != tc.ratio || known != tc.known {
			t.Errorf("CacheRatio(%+v) = %v, %v; want %v, %v", tc.usage, ratio, known, tc.ratio, tc.known)
		}
	}
}
