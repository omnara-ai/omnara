package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/mcp"
	"github.com/omnara-ai/omnara/internal/resourcemeta"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

func TestStartMCPOAuthRejectsInvalidProspectiveSecretName(t *testing.T) {
	server := strictOpenAPIServer{server: &Server{}}
	_, apiErr, err := server.startMCPOAuth(
		context.Background(),
		mcpOAuthOwner{},
		identitystore.PrincipalRecord{},
		&openapi.MCPOAuthStartRequest{Name: " secret"},
	)
	if err != nil {
		t.Fatalf("start MCP OAuth: %v", err)
	}
	if apiErr == nil || apiErr.Code != openapi.ErrorCodeInvalidRequest {
		t.Fatalf("start MCP OAuth error = %+v, want invalid_request", apiErr)
	}
}

func TestMCPUpstreamFailureMapsErrorOrigins(t *testing.T) {
	for _, tt := range []struct {
		name       string
		err        error
		wantCode   openapi.ErrorCode
		wantStatus int
	}{
		{
			name:       "blocked address",
			err:        fmt.Errorf("dial: %w", ssrf.ErrBlockedAddress),
			wantCode:   openapi.ErrorCodeInvalidRequest,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "transient upstream failure",
			err:        &mcp.HTTPError{Status: http.StatusServiceUnavailable},
			wantCode:   openapi.ErrorCodeUpstreamUnavailable,
			wantStatus: http.StatusFailedDependency,
		},
		{
			name:       "rejected by upstream",
			err:        errors.New("registration rejected"),
			wantCode:   openapi.ErrorCodeUnprocessable,
			wantStatus: http.StatusUnprocessableEntity,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			apiErr := mcpUpstreamFailure(tt.err, "transient: ", "rejected: ")
			if apiErr.Code != tt.wantCode || apiErr.Status != tt.wantStatus {
				t.Fatalf("code=%q status=%d, want %q %d", apiErr.Code, apiErr.Status, tt.wantCode, tt.wantStatus)
			}
		})
	}
}

func TestMCPOAuthSecretMetadataMergesExistingAndProvided(t *testing.T) {
	cases := []struct {
		name     string
		existing json.RawMessage
		provided resourcemeta.Metadata
		want     resourcemeta.Metadata
	}{
		{
			name: "no existing no provided",
			want: resourcemeta.Metadata{},
		},
		{
			name:     "existing kept when provided is unset",
			existing: json.RawMessage(`{"team":"support","mcp_url":"https://old.example.com"}`),
			want:     resourcemeta.Metadata{"team": "support"},
		},
		{
			name:     "provided overlays existing per key",
			existing: json.RawMessage(`{"team":"support","region":"us-east-1"}`),
			provided: resourcemeta.Metadata{"team": "infra"},
			want:     resourcemeta.Metadata{"team": "infra", "region": "us-east-1"},
		},
		{
			name:     "oversized existing value is dropped",
			existing: json.RawMessage(`{"big":"` + strings.Repeat("v", resourcemeta.MaxValueLength+1) + `","ok":"kept"}`),
			want:     resourcemeta.Metadata{"ok": "kept"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mcpOAuthSecretMetadata(tc.existing, tc.provided)
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

func TestMCPOAuthSecretMetadataKeepsLabelsAtCapacity(t *testing.T) {
	labels := resourcemeta.Metadata{}
	for i := range resourcemeta.MaxEntries - 1 {
		labels[fmt.Sprintf("tag-%02d", i)] = "value"
	}
	existing := resourcemeta.Metadata{secrets.KeyMCPURL: "https://old.example.com/mcp"}
	maps.Copy(existing, labels)
	raw, err := existing.JSON()
	if err != nil {
		t.Fatalf("encode existing metadata: %v", err)
	}
	got, err := mcpOAuthSecretMetadata(raw, resourcemeta.Metadata{"provided": "wins"})
	if err != nil {
		t.Fatalf("merge metadata: %v", err)
	}
	want := resourcemeta.Metadata{"provided": "wins"}
	maps.Copy(want, labels)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merge metadata = %v, want %v", got, want)
	}
}
