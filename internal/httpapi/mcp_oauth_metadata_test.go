package httpapi

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/resourcemeta"
	"github.com/omnara-ai/omnara/internal/secrets"
)

func TestMCPOAuthSecretMetadataMergesExistingAndProvided(t *testing.T) {
	mcpURL := "https://mcp.example.com/mcp"
	cases := []struct {
		name     string
		existing json.RawMessage
		provided resourcemeta.Metadata
		want     resourcemeta.Metadata
	}{
		{
			name: "no existing no provided",
			want: resourcemeta.Metadata{secrets.KeyMCPURL: mcpURL},
		},
		{
			name:     "existing kept when provided is unset",
			existing: json.RawMessage(`{"team":"support","mcp_url":"https://old.example.com"}`),
			want: resourcemeta.Metadata{
				"team":            "support",
				secrets.KeyMCPURL: mcpURL,
			},
		},
		{
			name:     "provided overlays existing per key",
			existing: json.RawMessage(`{"team":"support","region":"us-east-1"}`),
			provided: resourcemeta.Metadata{"team": "infra"},
			want: resourcemeta.Metadata{
				"team":            "infra",
				"region":          "us-east-1",
				secrets.KeyMCPURL: mcpURL,
			},
		},
		{
			name:     "oversized existing value is dropped",
			existing: json.RawMessage(`{"big":"` + strings.Repeat("v", resourcemeta.MaxValueLength+1) + `","ok":"kept"}`),
			want: resourcemeta.Metadata{
				"ok":              "kept",
				secrets.KeyMCPURL: mcpURL,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mcpOAuthSecretMetadata(tc.existing, tc.provided, mcpURL)
			if err != nil {
				t.Fatalf("merge metadata: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("merge metadata = %v, want %v", got, tc.want)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("merged metadata invalid: %v", err)
			}
		})
	}
}

func TestMCPOAuthSecretMetadataTrimsExistingAtCapacity(t *testing.T) {
	mcpURL := "https://mcp.example.com/mcp"
	existing := resourcemeta.Metadata{}
	for i := range resourcemeta.MaxEntries {
		existing[fmt.Sprintf("key-%02d", i)] = "value"
	}
	raw, err := existing.JSON()
	if err != nil {
		t.Fatalf("encode existing metadata: %v", err)
	}
	provided := resourcemeta.Metadata{"provided": "wins"}
	got, err := mcpOAuthSecretMetadata(raw, provided, mcpURL)
	if err != nil {
		t.Fatalf("merge metadata: %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("merged metadata invalid: %v", err)
	}
	if len(got) != resourcemeta.MaxEntries {
		t.Fatalf("merged metadata has %d entries, want %d", len(got), resourcemeta.MaxEntries)
	}
	if got[secrets.KeyMCPURL] != mcpURL || got["provided"] != "wins" {
		t.Fatalf("merged metadata = %v", got)
	}
	for i := range resourcemeta.MaxEntries - 2 {
		key := fmt.Sprintf("key-%02d", i)
		if got[key] != "value" {
			t.Fatalf("merged metadata dropped %s: %v", key, got)
		}
	}
	for _, key := range []string{
		fmt.Sprintf("key-%02d", resourcemeta.MaxEntries-2),
		fmt.Sprintf("key-%02d", resourcemeta.MaxEntries-1),
	} {
		if _, ok := got[key]; ok {
			t.Fatalf("merged metadata kept %s past capacity: %v", key, got)
		}
	}
}
