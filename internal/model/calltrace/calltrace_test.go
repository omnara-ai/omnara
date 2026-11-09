package calltrace

import "testing"

func TestRecordCacheRatio(t *testing.T) {
	for _, tc := range []struct {
		usage *Usage
		ratio float64
		known bool
	}{
		{nil, 0, false},
		{&Usage{OutputTokens: 3}, 0, false},
		{&Usage{InputTokens: 10}, 0, false},
		{&Usage{InputTokens: 10, CacheReported: true}, 0, true},
		{&Usage{InputTokens: 10, CachedInputTokens: 4, CacheReported: true}, 0.4, true},
	} {
		ratio, known := (Record{Usage: tc.usage}).CacheRatio()
		if ratio != tc.ratio || known != tc.known {
			t.Errorf("CacheRatio(%+v) = %v, %v; want %v, %v", tc.usage, ratio, known, tc.ratio, tc.known)
		}
	}
}
