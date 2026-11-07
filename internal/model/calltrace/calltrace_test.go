package calltrace

import (
	"testing"
	"time"
)

func TestTrackerSeparatesFirstContentFromFirstVisibleText(t *testing.T) {
	now := time.Unix(0, 0)
	tracker := Start(func() time.Time { return now })
	advance := func(d time.Duration) { now = now.Add(d) }

	advance(time.Second)
	tracker.MarkContent(ContentText, "")
	advance(time.Second)
	tracker.MarkContent(ContentReasoning, "thinking")
	advance(time.Second)
	tracker.MarkContent(ContentText, "\n ")
	advance(time.Second)
	tracker.MarkContent(ContentText, "Hi")
	advance(time.Second)
	tracker.MarkContent(ContentText, "again")

	var record Record
	tracker.Fill(&record)
	if record.FirstContent == nil || *record.FirstContent != 2*time.Second || record.FirstContentKind != ContentReasoning {
		t.Fatalf("first content = %v %q", record.FirstContent, record.FirstContentKind)
	}
	if record.FirstText == nil || *record.FirstText != 4*time.Second {
		t.Fatalf("first text = %v", record.FirstText)
	}
	if record.UploadComplete != nil || record.Headers != nil || record.StreamComplete != nil {
		t.Fatalf("unreached phases must stay nil: %+v", record)
	}
	if record.Duration != 5*time.Second {
		t.Fatalf("duration = %s", record.Duration)
	}
}

func TestRecordCache(t *testing.T) {
	for _, tc := range []struct {
		usage *Usage
		want  Cache
	}{
		{nil, CacheUnknown},
		{&Usage{OutputTokens: 3}, CacheUnknown},
		{&Usage{InputTokens: 10}, CacheMiss},
		{&Usage{InputTokens: 10, CachedInputTokens: 4}, CacheHit},
	} {
		if got := (Record{Usage: tc.usage}).Cache(); got != tc.want {
			t.Errorf("Cache(%+v) = %s, want %s", tc.usage, got, tc.want)
		}
	}
}
