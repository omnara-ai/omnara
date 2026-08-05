package publicid

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestRoundTripEveryKind(t *testing.T) {
	id := uuid.MustParse("019535d9-3df7-79fb-b466-fa907fa17f9e")
	seen := map[string]Kind{}
	for kind := range kindPrefixes {
		encoded, err := Encode(kind, id)
		if err != nil {
			t.Fatalf("encode %s: %v", kind, err)
		}
		prefix, ok := Prefix(kind)
		if !ok {
			t.Fatalf("missing prefix for kind %s", kind)
		}
		if prior, exists := seen[prefix]; exists {
			t.Fatalf("prefix %q used by both %s and %s", prefix, prior, kind)
		}
		seen[prefix] = kind
		if !strings.HasPrefix(encoded, prefix+"_") {
			t.Fatalf("encoded %s = %q, missing prefix %q", kind, encoded, prefix)
		}
		if len(strings.TrimPrefix(encoded, prefix+"_")) != 26 {
			t.Fatalf("encoded %s = %q, want 26-byte token", kind, encoded)
		}
		got, err := Decode(kind, encoded)
		if err != nil {
			t.Fatalf("decode %s: %v", kind, err)
		}
		if got != id {
			t.Fatalf("round trip %s = %s, want %s", kind, got, id)
		}
	}
}

func TestEncodingIsStableLowercaseNoPadding(t *testing.T) {
	id := uuid.MustParse("019535d9-3df7-79fb-b466-fa907fa17f9e")
	encoded, err := Encode(KindAgent, id)
	if err != nil {
		t.Fatal(err)
	}
	if encoded != "agt_agktlwj56547xndg7kih7il7ty" {
		t.Fatalf("encoded = %q", encoded)
	}
}

func TestDecodeRejectsWrongPrefix(t *testing.T) {
	id := uuid.MustParse("019535d9-3df7-79fb-b466-fa907fa17f9e")
	encoded, err := Encode(KindAgent, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Decode(KindProject, encoded)
	var wrong WrongPrefixError
	if !errors.As(err, &wrong) {
		t.Fatalf("decode wrong prefix error = %v", err)
	}
	if wrong.Want != "proj" || wrong.Got != "agt" {
		t.Fatalf("wrong prefix = %+v", wrong)
	}
}

func TestDecodeRejectsMalformed(t *testing.T) {
	for _, value := range []string{"", "agt", "agt_", "_abc", "agt_short", "agt_!!!!!!!!!!!!!!!!!!!!!!!!!!"} {
		if _, err := Decode(KindAgent, value); !errors.Is(err, ErrMalformed) {
			t.Fatalf("Decode(%q) error = %v, want malformed", value, err)
		}
	}
}

func TestNilUUIDRejected(t *testing.T) {
	if _, err := Encode(KindAgent, uuid.Nil); !errors.Is(err, ErrNilUUID) {
		t.Fatalf("Encode nil error = %v", err)
	}
	if _, err := Decode(KindAgent, "agt_aaaaaaaaaaaaaaaaaaaaaaaaaa"); !errors.Is(err, ErrNilUUID) {
		t.Fatalf("Decode nil error = %v", err)
	}
}
