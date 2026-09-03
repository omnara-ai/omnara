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
			{ConnectorKey: "chat_sdk_v1", Provider: "discord"},
			{ConnectorKey: "chat_sdk_v1", Provider: "discord"},
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
			ConnectorKey: "chat_sdk_v1", Provider: "discord",
		}) {
		t.Fatalf("identity = %+v", identity)
	}
	identity.Capabilities[0].ConnectorKey = "mutated"
	again, err := authenticator.Authenticate(token)
	if err != nil || again.Capabilities[0].ConnectorKey != "chat_sdk_v1" {
		t.Fatalf("authenticator leaked mutable scopes: %+v, %v", again, err)
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
		Capabilities: []Capability{{ConnectorKey: "chat_sdk_v1", Provider: "discord"}},
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
		{ID: "", Token: token, Capabilities: []Capability{{ConnectorKey: "chat_sdk_v1", Provider: "discord"}}},
		{ID: "gateway-a", Token: token, Capabilities: []Capability{{ConnectorKey: "native_slack_v1", Provider: "slack"}}},
		{ID: "gateway-a", Token: token},
		{ID: "gateway-a", Token: token, Capabilities: []Capability{{ConnectorKey: "chat_sdk_v1"}}},
	}
	for _, config := range tests {
		if _, err := NewAuthenticator([]Config{config}); err == nil {
			t.Fatalf("NewAuthenticator(%+v) succeeded", config)
		}
	}
}
