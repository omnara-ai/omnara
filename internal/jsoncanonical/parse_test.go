package jsoncanonical

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseObjectRejectsAmbiguousOrInvalidJSON(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		raw, diagnostic string
	}{
		"empty":          {``, "EOF"},
		"null":           {`null`, "root must be an object"},
		"array":          {`[]`, "root must be an object"},
		"string":         {`"{}"`, "root must be an object"},
		"number":         {`1`, "root must be an object"},
		"boolean":        {`true`, "root must be an object"},
		"duplicate":      {`{"a":1,"a":2}`, `duplicate JSON object key "a" at "/a"`},
		"escaped":        {`{"omnara_channel":"a","\u006fmnara_channel":"b"}`, "duplicate"},
		"escaped slash":  {`{"a/b":{"~":1,"\u007e":2}}`, `"/a~1b/~0"`},
		"nested array":   {`{"items":[{"a":null,"\u0061":2}]}`, `"/items/0/a"`},
		"surrogate pair": {`{"𝄞":1,"\ud834\udd1e":2}`, "duplicate"},
		"trailing value": {`{} {}`, "trailing JSON value"},
		"trailing null":  {`{} null`, "trailing JSON value"},
		"trailing junk":  {`{} garbage`, "trailing JSON data"},
		"bad utf8":       {"{\"a\":\"\xff\"}", "invalid UTF-8"},
		"bad number":     {`{"a":01}`, "invalid character"},
		"missing close":  {`{"a":[]`, "unexpected end"},
		"wrong close":    {`{"a":[1}}`, "invalid character"},
		"trailing comma": {`{"a":1,}`, "invalid character"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			object, err := ParseObject(json.RawMessage(test.raw), 4096)
			if err == nil || !strings.Contains(err.Error(), test.diagnostic) || object != nil {
				t.Fatalf("got (%v, %v), want nil and diagnostic %q", object, err, test.diagnostic)
			}
		})
	}
}

func TestParseObjectPreservesNumbersAndNestedBusinessFields(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(` {"large":9007199254740993,"exponent":1.234567890123456789e1000,"negative":-0,"nested":{"omnara_channel":"business"},"items":[{},[],true,null,"x"]} `)
	before := bytes.Clone(raw)
	object, err := ParseObject(raw, len(raw))
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"large":    "9007199254740993",
		"exponent": "1.234567890123456789e1000",
		"negative": "-0",
	} {
		number, ok := object[key].(json.Number)
		if !ok || number.String() != want {
			t.Fatalf("%s: got %#v, want json.Number(%q)", key, object[key], want)
		}
	}
	nested, ok := object["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested: got %#v, want an object", object["nested"])
	}
	nested["omnara_channel"] = "changed"
	if !bytes.Equal(raw, before) {
		t.Fatal("modifying decoded object changed original JSON")
	}
	// Identical names in different objects are not duplicates.
	if _, err := ParseObject(json.RawMessage(`{"a":{"x":1},"b":[{"x":2},{"x":3}]}`), 4096); err != nil {
		t.Fatal(err)
	}
}

func TestParseObjectBounds(t *testing.T) {
	t.Parallel()
	for _, limit := range []int{-1, 0, 1} {
		if _, err := ParseObject(json.RawMessage(`{}`), limit); err == nil {
			t.Fatalf("accepted invalid/insufficient limit %d", limit)
		}
	}
	if _, err := ParseObject(json.RawMessage(` {} `), 2); err == nil {
		t.Fatal("whitespace bypassed byte limit")
	}
	for _, depth := range []int{MaxDepth, MaxDepth + 1} {
		raw := json.RawMessage(`{"a":` + strings.Repeat("[", depth-1) + `0` + strings.Repeat("]", depth-1) + `}`)
		_, err := ParseObject(raw, len(raw))
		if (err == nil) != (depth == MaxDepth) {
			t.Fatalf("depth %d: unexpected error %v", depth, err)
		}
	}
}
