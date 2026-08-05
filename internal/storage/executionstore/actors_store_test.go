package executionstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestValidateActorMetadata(t *testing.T) {
	tooManyEntries := make(map[string]string, MaxActorMetadataEntries+1)
	for i := range MaxActorMetadataEntries + 1 {
		tooManyEntries[fmt.Sprintf("key-%d", i)] = "value"
	}
	tooManyBody, err := json.Marshal(tooManyEntries)
	if err != nil {
		t.Fatalf("marshal oversized metadata: %v", err)
	}

	valid := []struct {
		name     string
		metadata string
	}{
		{name: "empty", metadata: ""},
		{name: "empty object", metadata: "{}"},
		{name: "string values", metadata: `{"team":"support","region":"us-east-1"}`},
		{
			name: "boundary sizes",
			metadata: fmt.Sprintf(
				`{%q:%q}`,
				strings.Repeat("k", MaxActorMetadataKeyLength),
				strings.Repeat("v", MaxActorMetadataValueLength),
			),
		},
	}
	for _, tt := range valid {
		t.Run("valid/"+tt.name, func(t *testing.T) {
			if err := validateActorMetadata(json.RawMessage(tt.metadata)); err != nil {
				t.Fatalf("validate %q: %v", tt.metadata, err)
			}
		})
	}

	invalid := []struct {
		name     string
		metadata string
	}{
		{name: "null", metadata: "null"},
		{name: "array", metadata: `["a"]`},
		{name: "number value", metadata: `{"count":3}`},
		{name: "nested object value", metadata: `{"nested":{"a":"b"}}`},
		{name: "empty key", metadata: `{"":"value"}`},
		{name: "oversized key", metadata: fmt.Sprintf(`{%q:"v"}`, strings.Repeat("k", MaxActorMetadataKeyLength+1))},
		{name: "oversized value", metadata: fmt.Sprintf(`{"k":%q}`, strings.Repeat("v", MaxActorMetadataValueLength+1))},
		{name: "too many entries", metadata: string(tooManyBody)},
	}
	for _, tt := range invalid {
		t.Run("invalid/"+tt.name, func(t *testing.T) {
			if err := validateActorMetadata(json.RawMessage(tt.metadata)); !errors.Is(err, storeerr.ErrInvalidActorRequest) {
				t.Fatalf("validate %q error = %v, want ErrInvalidActorRequest", tt.metadata, err)
			}
		})
	}
}
