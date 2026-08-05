package secrets

import (
	"testing"
	"time"
)

func TestCanonicalizeMaterial(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		material Material
		wantKind Kind
		wantErr  bool
	}{
		{
			name:     "generic",
			material: GenericMaterial{Value: "secret"},
			wantKind: KindGeneric,
		},
		{
			name: "oauth access token",
			material: OAuthTokenSetMaterial{
				AccessToken:         "access",
				AccessTokenLifetime: FixedOAuthAccessTokenLifetime(time.Hour),
			},
			wantKind: KindOAuthTokenSet,
		},
		{
			name: "oauth refresh material",
			material: OAuthTokenSetMaterial{
				AccessToken: "access",
				Refresh: &OAuthRefreshMaterial{
					RefreshToken:  "refresh",
					TokenEndpoint: "https://auth.example.com/token",
					ClientID:      "client",
					Resource:      "https://api.example.com",
				},
			},
			wantKind: KindOAuthTokenSet,
		},
		{
			name: "incomplete oauth refresh material",
			material: OAuthTokenSetMaterial{
				AccessToken: "access",
				Refresh:     &OAuthRefreshMaterial{RefreshToken: "refresh"},
			},
			wantErr: true,
		},
		{
			name: "public HTTP oauth refresh endpoint",
			material: OAuthTokenSetMaterial{
				AccessToken: "access",
				Refresh: &OAuthRefreshMaterial{
					RefreshToken:  "refresh",
					TokenEndpoint: "http://auth.example.com/token",
					ClientID:      "client",
					Resource:      "https://api.example.com",
				},
			},
			wantErr: true,
		},
		{
			name: "slack credentials",
			material: SlackAppCredentialsMaterial{
				AccessToken:   "xoxb-token",
				ClientID:      "client",
				ClientSecret:  "client-secret",
				SigningSecret: "signing-secret",
			},
			wantKind: KindSlackAppCredentials,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			canonical, err := CanonicalizeMaterial(test.material)
			if test.wantErr {
				if err == nil {
					t.Fatal("expected material validation error")
				}
				return
			}
			if err != nil {
				t.Fatalf("canonicalize material: %v", err)
			}
			if canonical.Kind != test.wantKind {
				t.Fatalf("kind = %q, want %q", canonical.Kind, test.wantKind)
			}
		})
	}
}
