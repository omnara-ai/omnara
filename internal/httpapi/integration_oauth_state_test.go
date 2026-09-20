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
		FlowID:            uuid.Must(uuid.NewV7()),
		OrgID:             uuid.New(),
		ProjectID:         uuid.New(),
		InstalledByUserID: uuid.New(),
		Provider:          integrationstore.IntegrationProviderSlack,
		ClientID:          "client",
		ClientSecret:      "secret",
		SigningSecret:     "signing",
		ExpiresAt:         now.Add(integrationOAuthStateTTL),
	}
	require.Error(
		t,
		validateIntegrationOAuthState(state, now),
		"missing app must not infer another setup scope",
	)
	state.AppID = uuid.New()
	require.Error(t, validateIntegrationOAuthState(state, now), "setup revision is required")
	state.SetupRevision = 1
	require.NoError(t, validateIntegrationOAuthState(state, now))
	require.Error(t, validateIntegrationOAuthState(state, state.ExpiresAt))
	require.Error(t, validateIntegrationOAuthState(state, state.ExpiresAt.Add(time.Second)))

	wrapper, err := secrets.NewLocalKeyWrapper(
		"test",
		map[string][]byte{"test": bytes.Repeat([]byte{1}, 32)},
	)
	require.NoError(t, err)
	server := &Server{secretKeyWrapper: wrapper}
	state.ReturnTo = "/projects/proj_example/apps/new/slack"
	token, err := server.encodeIntegrationOAuthState(t.Context(), state)
	require.NoError(t, err)
	decoded, err := server.decodeIntegrationOAuthState(t.Context(), token)
	require.NoError(t, err)
	require.Equal(t, state, decoded)
	legacy, err := secrets.SealToken(
		t.Context(),
		wrapper,
		integrationOAuthStatePurpose,
		[]byte(
			`{"connection_only":true,"agent_profile_id":"00000000-0000-0000-0000-000000000000"}`,
		),
	)
	require.NoError(t, err)
	_, err = server.decodeIntegrationOAuthState(t.Context(), legacy)
	require.Error(t, err, "removed setup scopes are not silently reinterpreted")
	state.ClientSecret = strings.Repeat("x", integrationOAuthStateBytes)
	_, err = server.encodeIntegrationOAuthState(t.Context(), state)
	require.ErrorIs(t, err, errIntegrationOAuthStateTooLarge)
}
