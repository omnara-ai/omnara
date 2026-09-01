package listing

import (
	"testing"
	"time"
)

func TestSortAllowed(t *testing.T) {
	if !SortAllowed("name", "created_at", "name") {
		t.Fatal("expected name to be allowed")
	}
	if SortAllowed("owner", "created_at", "name") {
		t.Fatal("expected owner to be rejected")
	}
	if SortAllowed("") {
		t.Fatal("empty field with no allowed list should be false")
	}
}

func TestNormalizeDefaultsCreatedAtDescending(t *testing.T) {
	got := Normalize(Options{})
	if got.SortField != "created_at" || !got.SortDesc {
		t.Fatalf("got %#v", got)
	}
}

func TestNormalizePreservesExplicitSort(t *testing.T) {
	got := Normalize(Options{SortField: "name", SortDesc: false, NamePattern: "om%"})
	if got.SortField != "name" || got.SortDesc || got.NamePattern != "om%" {
		t.Fatalf("got %#v", got)
	}
}

func TestTimestampKeyRoundTripUTC(t *testing.T) {
	loc := time.FixedZone("UTC-5", -5*60*60)
	in := time.Date(2026, 9, 1, 13, 31, 0, 123456000, loc)
	key := TimestampKey(in)
	wantKey := "2026-09-01T18:31:00.123456"
	if key != wantKey {
		t.Fatalf("TimestampKey = %q, want %q", key, wantKey)
	}
	parsed, err := ParseTimestampKey(key)
	if err != nil {
		t.Fatalf("ParseTimestampKey: %v", err)
	}
	if !parsed.Equal(in.UTC()) {
		t.Fatalf("parsed %v, want %v", parsed, in.UTC())
	}
	if parsed.Location() != time.UTC {
		t.Fatalf("location = %v, want UTC", parsed.Location())
	}
}

func TestParseTimestampKeyRejectsRFC3339(t *testing.T) {
	if _, err := ParseTimestampKey("2026-09-01T18:31:00Z"); err == nil {
		t.Fatal("expected layout mismatch")
	}
}
