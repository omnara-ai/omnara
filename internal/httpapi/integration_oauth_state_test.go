package httpapi

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func TestIntegrationOAuthStateScopeAndBounds(t *testing.T) {
	now := time.Now().UTC()
	state := integrationOAuthState{
		FlowID: uuid.New(), OrgID: uuid.New(), ProjectID: uuid.New(), InstalledByUserID: uuid.New(),
		Provider: integrationstore.IntegrationProviderSlack, ClientID: "client", ClientSecret: "secret",
		SigningSecret: "signing", ExpiresAt: now.Add(integrationOAuthStateTTL),
	}
	require.Error(t, validateIntegrationOAuthState(state, now), "missing profile must not imply connection-only")
	state.AgentProfileID = uuid.New()
	require.NoError(t, validateIntegrationOAuthState(state, now), "legacy state has no discriminator")
	state.ConnectionOnly = true
	require.Error(t, validateIntegrationOAuthState(state, now), "project state must not carry a profile")
	state.AgentProfileID = uuid.Nil
	require.NoError(t, validateIntegrationOAuthState(state, now))
	require.Error(t, validateIntegrationOAuthState(state, state.ExpiresAt.Add(time.Second)))

	wrapper, err := secrets.NewLocalKeyWrapper("test", map[string][]byte{"test": bytes.Repeat([]byte{1}, 32)})
	require.NoError(t, err)
	server := &Server{secretKeyWrapper: wrapper}
	state.ReturnTo = "/projects/proj_example/apps/new/slack"
	token, err := server.encodeIntegrationOAuthState(t.Context(), state)
	require.NoError(t, err)
	decoded, err := server.decodeIntegrationOAuthState(t.Context(), token)
	require.NoError(t, err)
	require.Equal(t, state, decoded)
	state.ClientSecret = strings.Repeat("x", integrationOAuthStateBytes)
	_, err = server.encodeIntegrationOAuthState(t.Context(), state)
	require.ErrorIs(t, err, errIntegrationOAuthStateTooLarge)
}
