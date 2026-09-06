package apivariantbody

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMarshalWithAPIVariantOptionsRejectsNonObjectAtRequestBuild(t *testing.T) {
	_, err := MarshalWithAPIVariantOptions(
		json.RawMessage(`["runtime","will","decide"]`),
		struct {
			Model string `json:"model"`
		}{Model: "gpt-test"},
	)
	if err == nil {
		t.Fatal("non-object api_variant_options was accepted")
	}
	if !strings.Contains(err.Error(), "api_variant_options") {
		t.Fatalf("error %q does not name api_variant_options", err)
	}
}

func TestMarshalWithAPIVariantOptionsTreatsNullAsNoop(t *testing.T) {
	body, err := MarshalWithAPIVariantOptions(
		json.RawMessage(`null`),
		struct {
			Model string `json:"model"`
		}{Model: "gpt-test"},
	)
	if err != nil {
		t.Fatalf("marshal with null api_variant_options: %v", err)
	}
	if string(body) != `{"model":"gpt-test"}` {
		t.Fatalf("body = %s, want base payload", body)
	}
}

func TestMarshalWithAPIVariantOptionsLetsUnownedOptionsOverrideBase(t *testing.T) {
	body, err := MarshalWithAPIVariantOptions(
		json.RawMessage(`{"store":true,"temperature":0.2}`),
		struct {
			Model string `json:"model"`
			Store bool   `json:"store"`
		}{Model: "gpt-test", Store: false},
	)
	if err != nil {
		t.Fatalf("marshal with api_variant_options: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if string(payload["store"]) != `true` {
		t.Fatalf("store = %s, want passthrough override", payload["store"])
	}
	if string(payload["temperature"]) != `0.2` {
		t.Fatalf("temperature = %s, want passthrough option", payload["temperature"])
	}
	if string(payload["model"]) != `"gpt-test"` {
		t.Fatalf("model = %s, want base model", payload["model"])
	}
}

func TestMarshalWithAPIVariantOptionsKeepsOwnedBaseFields(t *testing.T) {
	body, err := MarshalWithAPIVariantOptions(
		json.RawMessage(`{"model":"override","stream":true,"temperature":0.2}`),
		struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}{Model: "gpt-test", Stream: false},
		"model",
		"stream",
	)
	if err != nil {
		t.Fatalf("marshal with api_variant_options: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if string(payload["model"]) != `"gpt-test"` || string(payload["stream"]) != `false` {
		t.Fatalf("owned fields were overwritten: %s", body)
	}
	if string(payload["temperature"]) != `0.2` {
		t.Fatalf("temperature = %s, want passthrough option", payload["temperature"])
	}
}

func TestMarshalWithAPIVariantOptionsSkipsOwnedFieldsAbsentFromBase(t *testing.T) {
	body, err := MarshalWithAPIVariantOptions(
		json.RawMessage(`{"tools":[{"type":"function"}],"temperature":0.2}`),
		struct {
			Model string `json:"model"`
		}{Model: "gpt-test"},
		"tools",
	)
	if err != nil {
		t.Fatalf("marshal with api_variant_options: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if _, ok := payload["tools"]; ok {
		t.Fatalf("owned absent field was injected: %s", body)
	}
	if string(payload["temperature"]) != `0.2` {
		t.Fatalf("temperature = %s, want passthrough option", payload["temperature"])
	}
}

func TestSetsReportsTopLevelKeys(t *testing.T) {
	options := json.RawMessage(`{"provider":{"only":["moonshotai"]},"prompt_cache_key":"pinned"}`)
	if !Sets(options, "session_id", "prompt_cache_key") {
		t.Fatal("expected prompt_cache_key to be reported as set")
	}
	if Sets(options, "session_id") || Sets(nil, "provider") || Sets(json.RawMessage(`[]`), "provider") {
		t.Fatal("unset, empty, and non-object options must not report keys")
	}
}
