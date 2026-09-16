//go:build integration

package httpapi

import (
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/bearertoken"
	"github.com/omnara-ai/omnara/internal/channelconnector"
)

func testSlackChannelGateway(t testing.TB) Option {
	t.Helper()
	token, err := bearertoken.Generate(bearertoken.KindChannelConnector)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := channelconnector.NewAuthenticator([]channelconnector.Config{{
		ID: "slack-test-gateway", Token: token,
		Capabilities: []channelconnector.Capability{{ConnectorKey: channelconnector.BuiltInConnectorKey, Provider: "slack"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return WithChannelConnectorAuthenticator(auth)
}

func unitSlackSignedHeaders(body, signingSecret string) map[string]string {
	return slackSignedHeadersAt(body, signingSecret, time.Now().UTC())
}
