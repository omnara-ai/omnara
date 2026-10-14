package httpapi

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/stretchr/testify/require"
)

func TestIntegrationOAuthStateScopeAndBounds(t *testing.T) {
	now := time.Now().UTC()
	state := integrationOAuthState{
		FlowID:            uuid.Must(uuid.NewV7()),
		OrgID:             uuid.New(),
		ProjectID:         uuid.New(),
		InstalledByUserID: uuid.New(),
		Provider:          integrationdefinition.ProviderSlack,
		ClientID:          "client",
		ClientSecret:      "secret",
		SigningSecret:     "signing",
		ExpiresAt:         now.Add(integrationOAuthStateTTL),
	}
	require.Error(
		t,
		validateIntegrationOAuthState(state, now),
		"missing integration must not infer another setup scope",
	)
	state.IntegrationID = uuid.New()
	require.Error(t, validateIntegrationOAuthState(state, now), "setup revision is required")
	state.SetupRevision = 1
	require.NoError(t, validateIntegrationOAuthState(state, now))
	require.ErrorIs(t, validateIntegrationOAuthState(state, state.ExpiresAt), errIntegrationOAuthStateExpired)
	require.ErrorIs(t,
		validateIntegrationOAuthState(state, state.ExpiresAt.Add(time.Second)), errIntegrationOAuthStateExpired)
	invalid := state
	invalid.ProjectID = uuid.Nil
	err := validateIntegrationOAuthState(invalid, state.ExpiresAt)
	require.Error(t, err)
	require.NotErrorIs(t, err, errIntegrationOAuthStateExpired)

	wrapper, err := secrets.NewLocalKeyWrapper(
		"test",
		map[string][]byte{"test": bytes.Repeat([]byte{1}, 32)},
	)
	require.NoError(t, err)
	server := &Server{secretKeyWrapper: wrapper}
	state.ReturnTo = "/projects/proj_example/integrations/new/slack"
	token, err := server.encodeIntegrationOAuthState(t.Context(), state)
	require.NoError(t, err)
	_, err = secrets.OpenToken(t.Context(), wrapper, "integration-oauth-state", token)
	require.NoError(t, err)
	decoded, err := server.decodeIntegrationOAuthState(t.Context(), token)
	require.NoError(t, err)
	require.Equal(t, state, decoded)
	legacy, err := secrets.SealToken(
		t.Context(),
		wrapper,
		integrationOAuthStatePurpose,
		[]byte(
			`{"agent_profile_id":"00000000-0000-0000-0000-000000000000"}`,
		),
	)
	require.NoError(t, err)
	_, err = server.decodeIntegrationOAuthState(t.Context(), legacy)
	require.Error(t, err, "removed setup scopes are not silently reinterpreted")
	state.ClientSecret = strings.Repeat("x", integrationOAuthStateBytes)
	_, err = server.encodeIntegrationOAuthState(t.Context(), state)
	require.ErrorIs(t, err, errIntegrationOAuthStateTooLarge)
}
