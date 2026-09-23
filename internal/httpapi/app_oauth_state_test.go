package httpapi

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/stretchr/testify/require"
)

func TestAppOAuthStateScopeAndBounds(t *testing.T) {
	now := time.Now().UTC()
	state := appOAuthState{
		FlowID:            uuid.Must(uuid.NewV7()),
		OrgID:             uuid.New(),
		ProjectID:         uuid.New(),
		InstalledByUserID: uuid.New(),
		Provider:          appstore.AppProviderSlack,
		ClientID:          "client",
		ClientSecret:      "secret",
		SigningSecret:     "signing",
		ExpiresAt:         now.Add(appOAuthStateTTL),
	}
	require.Error(
		t,
		validateAppOAuthState(state, now),
		"missing app must not infer another setup scope",
	)
	state.AppID = uuid.New()
	require.Error(t, validateAppOAuthState(state, now), "setup revision is required")
	state.SetupRevision = 1
	require.NoError(t, validateAppOAuthState(state, now))
	require.Error(t, validateAppOAuthState(state, state.ExpiresAt))
	require.Error(t, validateAppOAuthState(state, state.ExpiresAt.Add(time.Second)))

	wrapper, err := secrets.NewLocalKeyWrapper(
		"test",
		map[string][]byte{"test": bytes.Repeat([]byte{1}, 32)},
	)
	require.NoError(t, err)
	server := &Server{secretKeyWrapper: wrapper}
	state.ReturnTo = "/projects/proj_example/apps/new/slack"
	token, err := server.encodeAppOAuthState(t.Context(), state)
	require.NoError(t, err)
	_, err = secrets.OpenToken(t.Context(), wrapper, "integration-oauth-state", token)
	require.NoError(t, err)
	decoded, err := server.decodeAppOAuthState(t.Context(), token)
	require.NoError(t, err)
	require.Equal(t, state, decoded)
	legacy, err := secrets.SealToken(
		t.Context(),
		wrapper,
		appOAuthStatePurpose,
		[]byte(
			`{"agent_profile_id":"00000000-0000-0000-0000-000000000000"}`,
		),
	)
	require.NoError(t, err)
	_, err = server.decodeAppOAuthState(t.Context(), legacy)
	require.Error(t, err, "removed setup scopes are not silently reinterpreted")
	state.ClientSecret = strings.Repeat("x", appOAuthStateBytes)
	_, err = server.encodeAppOAuthState(t.Context(), state)
	require.ErrorIs(t, err, errAppOAuthStateTooLarge)
}
