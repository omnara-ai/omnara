package jsoncanonical

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestEqualComparesJSONValuesWithoutLosingNumberPrecision(t *testing.T) {
	if !Equal(
		json.RawMessage(`{"nested":[1.0,true,null],"small":1.230e-5,"large":9007199254740993}`),
		json.RawMessage(`{"large":9007199254740993,"small":0.00001230,"nested":[1,true,null]}`),
	) {
		t.Fatal("semantically equal JSON values did not compare equal")
	}
	if Equal(
		json.RawMessage(`{"large":9007199254740993}`),
		json.RawMessage(`{"large":9007199254740992}`),
	) {
		t.Fatal("different large integers compared equal")
	}
	if Equal(json.RawMessage(`{"value":1} trailing`), json.RawMessage(`{"value":1}`)) {
		t.Fatal("invalid JSON compared equal")
	}
	if !Equal(
		json.RawMessage(`1e1000`),
		json.RawMessage(`10e999`),
	) {
		t.Fatal("equivalent numbers with large exponents did not compare equal")
	}
}

func TestCanonicalOperationsPreserveStoredOutputStringNormalization(t *testing.T) {
	t.Parallel()
	want := json.RawMessage(`{"large":9007199254740993,"nested":[{"�":"before�after"}]}`)
	for name, raw := range map[string]json.RawMessage{
		"invalid UTF8":        json.RawMessage("{\"nested\":[{\"\xff\":\"before\xfeafter\"}],\"large\":9007199254740993}"),
		"unpaired surrogates": json.RawMessage(`{"nested":[{"\ud800":"before\udfffafter"}],"large":9007199254740993}`),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			before := bytes.Clone(raw)
			got, err := Normalize(raw)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("normalized = %s, want %s", got, want)
			}
			if !Equal(raw, want) || !Equal(want, raw) {
				t.Fatal("replacement-normalized stored output did not compare equal")
			}
			wrongNumber := json.RawMessage(strings.ReplaceAll(string(want), "9007199254740993", "9007199254740992"))
			if Equal(raw, wrongNumber) {
				t.Fatal("string normalization lost numeric precision")
			}
			if !bytes.Equal(raw, before) {
				t.Fatal("normalization changed source bytes")
			}
		})
	}
	raw := json.RawMessage("{\"message\":\"before\xffafter\"}")
	if _, err := Decode(raw); err == nil || !strings.Contains(err.Error(), "invalid UTF-8") {
		t.Fatalf("Decode must still reject invalid UTF-8: %v", err)
	}
	if _, err := ParseObject(raw, len(raw)); err == nil || !strings.Contains(err.Error(), "invalid UTF-8") {
		t.Fatalf("ParseObject must still reject invalid UTF-8: %v", err)
	}
}

func TestCanonicalOperationsPreserveLegacyDuplicateKeysAndDepth(t *testing.T) {
	t.Parallel()
	deep := strings.Repeat("[", MaxDepth+1) + "0" + strings.Repeat("]", MaxDepth+1)
	for name, test := range map[string]struct{ raw, normalized string }{
		"duplicate keys": {`{"value":1,"value":9007199254740993}`, `{"value":9007199254740993}`},
		"decoded keys":   {`{"value":1,"\u0076alue":2}`, `{"value":2}`},
		"deep value":     {deep, deep},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			raw := json.RawMessage(test.raw)
			normalized, err := Normalize(raw)
			if err != nil {
				t.Fatal(err)
			}
			if string(normalized) != test.normalized || !Equal(raw, normalized) {
				t.Fatalf("changed existing normalization: got %s, want %s", normalized, test.normalized)
			}
			if _, err := Decode(raw); err == nil {
				t.Fatal("strict input decoder must still reject duplicate keys or excessive depth")
			}
		})
	}
}
