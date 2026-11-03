package testutil

import (
	"reflect"
	"strings"
	"testing"
)

func AssertSchemaMatchesStruct(t testing.TB, name string, schema map[string]any, structType reflect.Type) {
	t.Helper()
	if extra, ok := schema["additionalProperties"].(bool); !ok || extra {
		t.Errorf("%s: schema must set additionalProperties=false to reject unknown fields before Go decode", name)
	}
	properties, _ := schema["properties"].(map[string]any)
	structFields := make(map[string]bool, structType.NumField())
	for i := range structType.NumField() {
		tagName, _, _ := strings.Cut(structType.Field(i).Tag.Get("json"), ",")
		if tagName == "" || tagName == "-" {
			t.Errorf("%s: struct field %s needs an explicit JSON tag", name, structType.Field(i).Name)
			continue
		}
		structFields[tagName] = true
	}
	for field := range properties {
		if !structFields[field] {
			t.Errorf(
				"%s: schema property %q has no Go struct field; its value would be silently dropped on decode",
				name,
				field,
			)
		}
	}
	for field := range structFields {
		if _, ok := properties[field]; !ok {
			t.Errorf(
				"%s: Go struct field %q is not declared in the schema; configs using it would be rejected",
				name,
				field,
			)
		}
	}
}
