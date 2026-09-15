package jsonschema

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReferencesStayInsideTheSubmittedSchema(t *testing.T) {
	t.Parallel()
	externalSchemaPath := filepath.Join(t.TempDir(), "schema.json")
	if err := os.WriteFile(externalSchemaPath, []byte(`{"type":"string"}`), 0o600); err != nil {
		t.Fatalf("write external schema: %v", err)
	}
	externalSchemaURL := url.URL{
		Scheme: "file",
		Path:   filepath.ToSlash(externalSchemaPath),
	}
	if runtime.GOOS == "windows" {
		externalSchemaURL.Path = "/" + externalSchemaURL.Path
	}
	externalSchemaRef, err := json.Marshal(map[string]string{
		"$ref": externalSchemaURL.String(),
	})
	if err != nil {
		t.Fatalf("marshal external schema reference: %v", err)
	}

	local := json.RawMessage(
		`{"$defs":{"value":{"type":"string"}},"$ref":"#/$defs/value"}`,
	)
	if err := Validate(local, json.RawMessage(`"ok"`)); err != nil {
		t.Fatalf("validate local reference: %v", err)
	}
	for name, schema := range map[string]json.RawMessage{
		"missing local target": json.RawMessage(`{"$ref":"#/$defs/missing"}`),
		"network target":       json.RawMessage(`{"$ref":"https://example.com/schema.json"}`),
		"local file target":    externalSchemaRef,
	} {
		err := ValidateSchema(schema)
		if err == nil {
			t.Fatalf("%s schema unexpectedly compiled", name)
		}
	}
}

func TestValidatorRejectsAmbiguousJSONBeforeValidation(t *testing.T) {
	t.Parallel()
	for _, schema := range []string{
		`{"type":"object","type":"string"}`,
		`{"type":"object","properties":{"a":{"type":"integer","\u0074ype":"string"}}}`,
		`{"type":"object"} null`,
	} {
		if _, err := Compile(json.RawMessage(schema)); err == nil {
			t.Fatalf("compiled ambiguous schema %s", schema)
		}
	}
	validator, err := Compile(json.RawMessage(`{"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		`{"a":1,"\u0061":2}`,
		`{"a":[{"nested":1,"nested":2}]}`,
		`{} []`,
	} {
		err := validator.Validate(json.RawMessage(value))
		if err == nil || !strings.Contains(err.Error(), "decode JSON value") {
			t.Fatalf("want pre-validation decoding error for %s, got %v", value, err)
		}
	}
	// The general validator still accepts non-object JSON for existing callers.
	if err := Validate(json.RawMessage(`{"type":"integer","minimum":9007199254740993}`), json.RawMessage(`9007199254740993`)); err != nil {
		t.Fatal(err)
	}
	if err := Validate(json.RawMessage(`{"type":"integer","minimum":9007199254740993}`), json.RawMessage(`9007199254740992`)); err == nil {
		t.Fatal("validator lost integer precision")
	}
}

func TestCompiledValidatorCanBeReused(t *testing.T) {
	t.Parallel()
	validator, err := Compile(json.RawMessage(
		`{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}`,
	))
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	if err := validator.Validate(json.RawMessage(`{"name":"Omnara"}`)); err != nil {
		t.Fatalf("validate matching value: %v", err)
	}
	if err := validator.Validate(json.RawMessage(`{}`)); err == nil {
		t.Fatal("schema-invalid value was accepted")
	}
}
