package auth

import (
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"testing"
)

func TestConnectorLoginURL(t *testing.T) {
	cases := []struct {
		name      string
		connector identitystore.AuthConnectorSummaryRecord
		want      string
	}{
		{
			name:      "github",
			connector: identitystore.AuthConnectorSummaryRecord{Slug: "github", Kind: identitystore.AuthConnectorKindGitHub},
			want:      "/api/auth/connectors/github/login",
		},
		{
			name:      "oidc",
			connector: identitystore.AuthConnectorSummaryRecord{Slug: "corp sso", Kind: identitystore.AuthConnectorKindOIDC},
			want:      "/api/auth/connectors/corp%20sso/login",
		},
		{
			name:      "unknown",
			connector: identitystore.AuthConnectorSummaryRecord{Slug: "future", Kind: "future"},
			want:      "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := connectorLoginURL(tc.connector); got != tc.want {
				t.Fatalf("connectorLoginURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGoogleEmailAuthoritative(t *testing.T) {
	cases := []struct {
		name         string
		email        string
		hostedDomain string
		want         bool
	}{
		{name: "gmail", email: "person@gmail.com", want: true},
		{name: "googlemail", email: "person@googlemail.com", want: true},
		{name: "workspace", email: "person@example.com", hostedDomain: "example.com", want: true},
		{name: "consumer external", email: "person@example.com", want: false},
		{name: "invalid", email: "person", hostedDomain: "example.com", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := googleEmailAuthoritative(tc.email, tc.hostedDomain); got != tc.want {
				t.Fatalf("googleEmailAuthoritative(%q, %q) = %v, want %v", tc.email, tc.hostedDomain, got, tc.want)
			}
		})
	}
}
