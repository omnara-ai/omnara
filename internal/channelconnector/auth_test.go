package channelconnector

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/bearertoken"
)

func TestAuthenticatorScopesIdentity(t *testing.T) {
	token, err := bearertoken.Generate(bearertoken.KindChannelConnector)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := NewAuthenticator([]Config{{
		ID: "gateway-a", Token: token,
		Capabilities: []Capability{
			{ConnectorKey: "test_connector", Provider: "discord"},
			{ConnectorKey: "test_connector", Provider: "discord"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := authenticator.Authenticate(token)
	if err != nil {
		t.Fatal(err)
	}
	if identity.ID != "gateway-a" || len(identity.Capabilities) != 1 ||
		identity.Capabilities[0] != (Capability{
			ConnectorKey: "test_connector", Provider: "discord",
		}) {
		t.Fatalf("identity = %+v", identity)
	}
	identity.Capabilities[0].ConnectorKey = "mutated"
	again, err := authenticator.Authenticate(token)
	if err != nil || again.Capabilities[0].ConnectorKey != "test_connector" {
		t.Fatalf("authenticator leaked mutable scopes: %+v, %v", again, err)
	}
	for _, test := range []struct {
		capability Capability
		configured bool
	}{
		{Capability{ConnectorKey: "test_connector", Provider: "discord"}, true},
		{Capability{ConnectorKey: "test_connector", Provider: "slack"}, false},
		{Capability{ConnectorKey: "other", Provider: "discord"}, false},
	} {
		if got := authenticator.HasCapability(test.capability); got != test.configured {
			t.Errorf("HasCapability(%+v) = %t, want %t", test.capability, got, test.configured)
		}
	}
	var unconfigured *Authenticator
	if unconfigured.HasCapability(Capability{ConnectorKey: "test_connector", Provider: "discord"}) {
		t.Fatal("nil authenticator advertises a configured gateway")
	}
}

func TestAuthenticatorRejectsWrongOrUnknownTokens(t *testing.T) {
	token, err := bearertoken.Generate(bearertoken.KindChannelConnector)
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := bearertoken.Generate(bearertoken.KindChannelConnector)
	if err != nil {
		t.Fatal(err)
	}
	wrongKind, err := bearertoken.Generate(bearertoken.KindDaemon)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := NewAuthenticator([]Config{{
		ID: "gateway-a", Token: token,
		Capabilities: []Capability{{ConnectorKey: "test_connector", Provider: "discord"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, presented := range []string{"", unknown, wrongKind, token + "x"} {
		if _, err := authenticator.Authenticate(presented); err == nil {
			t.Fatalf("Authenticate(%q) succeeded", presented)
		}
	}
}

func TestAuthenticatorRejectsUnsafeConfiguration(t *testing.T) {
	token, err := bearertoken.Generate(bearertoken.KindChannelConnector)
	if err != nil {
		t.Fatal(err)
	}
	tests := []Config{
		{ID: "", Token: token, Capabilities: []Capability{{ConnectorKey: "test_connector", Provider: "discord"}}},
		{ID: "gateway-a", Token: token},
		{ID: "gateway-a", Token: token, Capabilities: []Capability{{ConnectorKey: "test_connector"}}},
	}
	for _, config := range tests {
		if _, err := NewAuthenticator([]Config{config}); err == nil {
			t.Fatalf("NewAuthenticator(%+v) succeeded", config)
		}
	}
}
