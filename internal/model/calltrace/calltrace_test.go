package calltrace

import (
	"testing"
	"time"
)

func TestTrackerTTFTIsFirstNonemptyOutputOfAnyKind(t *testing.T) {
	now := time.Unix(0, 0)
	tracker := Start(func() time.Time { return now })
	advance := func(d time.Duration) { now = now.Add(d) }

	advance(time.Second)
	tracker.MarkToken("")
	advance(time.Second)
	tracker.MarkToken("thinking")
	advance(time.Second)
	tracker.MarkToken("Hi")

	var record Record
	tracker.Fill(&record)
	if record.TTFT == nil || *record.TTFT != 2*time.Second {
		t.Fatalf("ttft = %v", record.TTFT)
	}
	if record.UploadComplete != nil || record.Headers != nil || record.StreamComplete != nil {
		t.Fatalf("unreached phases must stay nil: %+v", record)
	}
	if record.Duration != 3*time.Second {
		t.Fatalf("duration = %s", record.Duration)
	}
}

func TestTrackerTTFTCountsWhitespaceTokens(t *testing.T) {
	now := time.Unix(0, 0)
	tracker := Start(func() time.Time { return now })
	now = now.Add(time.Second)
	tracker.MarkToken("\n")
	now = now.Add(time.Second)
	tracker.MarkToken("Hi")

	var record Record
	tracker.Fill(&record)
	if record.TTFT == nil || *record.TTFT != time.Second {
		t.Fatalf("ttft = %v", record.TTFT)
	}
}

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
