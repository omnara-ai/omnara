package processaction

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeWritePayload(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		raw  json.RawMessage
		ok   bool
	}{
		{name: "data", raw: json.RawMessage(`{"data":"hello"}`), ok: true},
		{name: "close", raw: json.RawMessage(`{"close_stdin":true}`), ok: true},
		{name: "empty", raw: json.RawMessage(`{}`)},
		{name: "null", raw: json.RawMessage(`{"data":null}`)},
		{name: "unknown", raw: json.RawMessage(`{"extra":true}`)},
		{name: "too large", raw: json.RawMessage(`{"data":"` + strings.Repeat("x", MaxWriteBytes+1) + `"}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := DecodeWritePayload(test.raw)
			if (err == nil) != test.ok {
				t.Fatalf("DecodeWritePayload() error = %v, want success %t", err, test.ok)
			}
		})
	}
}

func TestDecodeReadPayload(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		raw  json.RawMessage
		ok   bool
	}{
		{name: "defaults", raw: json.RawMessage(`{}`), ok: true},
		{name: "explicit", raw: json.RawMessage(`{"cursor":4,"max_bytes":128,"wait_ms":20}`), ok: true},
		{name: "negative cursor", raw: json.RawMessage(`{"cursor":-1}`)},
		{name: "negative bytes", raw: json.RawMessage(`{"max_bytes":-1}`)},
		{name: "excess wait", raw: json.RawMessage(`{"wait_ms":10001}`)},
		{name: "null", raw: json.RawMessage(`{"cursor":null}`)},
		{name: "unknown", raw: json.RawMessage(`{"extra":true}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := DecodeReadPayload(test.raw)
			if (err == nil) != test.ok {
				t.Fatalf("DecodeReadPayload() error = %v, want success %t", err, test.ok)
			}
		})
	}
}

func TestKindClassification(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		kind     Kind
		valid    bool
		mutating bool
	}{
		{kind: KindWrite, valid: true, mutating: true},
		{kind: KindRead, valid: true},
		{kind: KindInterrupt, valid: true, mutating: true},
		{kind: KindTerminate, valid: true, mutating: true},
		{kind: Kind("invalid")},
	} {
		if test.kind.Valid() != test.valid || test.kind.Mutating() != test.mutating {
			t.Fatalf(
				"kind %q classification = valid %t, mutating %t; want valid %t, mutating %t",
				test.kind,
				test.kind.Valid(),
				test.kind.Mutating(),
				test.valid,
				test.mutating,
			)
		}
	}
}

func TestDecodeEmptyPayload(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		raw  json.RawMessage
		ok   bool
	}{
		{name: "object", raw: json.RawMessage(`{}`), ok: true},
		{name: "unknown", raw: json.RawMessage(`{"extra":true}`)},
		{name: "null", raw: json.RawMessage(`null`)},
		{name: "missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := DecodeEmptyPayload(test.raw)
			if (err == nil) != test.ok {
				t.Fatalf("DecodeEmptyPayload() error = %v, want success %t", err, test.ok)
			}
		})
	}
}
