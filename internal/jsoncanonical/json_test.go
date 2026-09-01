package jsoncanonical

import (
	"encoding/json"
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

func TestNormalizeOrdersObjectKeysAndRejectsTrailingValues(t *testing.T) {
	normalized, err := Normalize(json.RawMessage(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatalf("normalize valid object: %v", err)
	}
	if string(normalized) != `{"a":1,"b":2}` {
		t.Fatalf("normalized = %s, want keys in lexical order", normalized)
	}
	if _, err := Normalize(json.RawMessage(`{"value":1} trailing`)); err == nil {
		t.Fatal("expected trailing JSON value to be rejected")
	}
	equivalent, err := Normalize(json.RawMessage(`{"n":1.0}`))
	if err != nil {
		t.Fatalf("normalize number: %v", err)
	}
	if string(equivalent) != `{"n":1}` {
		t.Fatalf("normalized number = %s, want compact encoding", equivalent)
	}
}
