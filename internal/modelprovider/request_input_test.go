package modelprovider

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/stretchr/testify/require"
)

func TestInputRouteIdentityTracksMutableRoutingAndSecretVersions(t *testing.T) {
	config := modelstore.ModelProviderConfigRecord{ID: uuid.New(), APIFormat: modelprotocol.APIFormatAnthropicMessages,
		APIVariant: modelprotocol.APIVariantDefault, BaseURL: "https://provider.test", EndpointPath: "/messages",
		Headers: json.RawMessage(`{"x-route":"first"}`), SecretHeaders: json.RawMessage(`{}`),
		AuthOptions: json.RawMessage(`{}`), CredentialSecretID: uuid.New()}
	credential := uuid.New()
	headerVersion := uuid.New()
	original, err := providerInputIdentityScope(
		config, credential, map[string]uuid.UUID{"x-secret-route": headerVersion},
	)
	require.NoError(t, err)
	require.Len(t, original, 64)
	for _, mutation := range []func(*modelstore.ModelProviderConfigRecord){
		func(c *modelstore.ModelProviderConfigRecord) { c.BaseURL = "https://other.test" },
		func(c *modelstore.ModelProviderConfigRecord) { c.EndpointPath = "/other/messages" },
		func(c *modelstore.ModelProviderConfigRecord) { c.Headers = json.RawMessage(`{"x-route":"second"}`) },
		func(c *modelstore.ModelProviderConfigRecord) { c.AuthOptions = json.RawMessage(`{"region":"other"}`) },
	} {
		changed := config
		mutation(&changed)
		got, err := providerInputIdentityScope(
			changed, credential, map[string]uuid.UUID{"x-secret-route": headerVersion},
		)
		require.NoError(t, err)
		require.NotEqual(t, original, got)
	}
	for _, versions := range [][2]uuid.UUID{
		{uuid.New(), headerVersion}, {credential, uuid.New()}, {credential, uuid.Nil},
	} {
		got, err := providerInputIdentityScope(config, versions[0], map[string]uuid.UUID{"x-secret-route": versions[1]})
		require.NoError(t, err)
		require.NotEqual(t, original, got)
	}
	config.APIVariant = modelprotocol.APIVariantOpenRouter
	got, err := providerInputIdentityScope(config, credential, nil)
	require.NoError(t, err)
	require.Empty(t, got, "automatic upstream selection cannot certify the next request's input identity")
}
