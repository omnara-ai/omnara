package jsonschema

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
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
