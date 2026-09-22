package httpapi

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/stretchr/testify/require"
)

func TestGitHubManifestStatePurposeScopeAndExpiry(t *testing.T) {
	t.Parallel()
	wrapper, err := secrets.NewLocalKeyWrapper("test", map[string][]byte{"test": bytes.Repeat([]byte{1}, 32)})
	require.NoError(t, err)
	server := &Server{secretKeyWrapper: wrapper}
	state := githubManifestState{
		FlowID: uuid.New(), OrgID: uuid.New(), ProjectID: uuid.New(), AppID: uuid.New(), UserID: uuid.New(),
		SetupRevision: 1, ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	token, err := server.encodeGitHubManifestState(t.Context(), state)
	require.NoError(t, err)
	decoded, err := server.decodeGitHubManifestState(t.Context(), token)
	require.NoError(t, err)
	require.Equal(t, state, decoded)
	_, err = server.decodeIntegrationOAuthState(t.Context(), token)
	require.Error(t, err, "GitHub state must not authorize Slack OAuth")
	_, err = server.decodeGitHubManifestState(t.Context(), token+"tampered")
	require.Error(t, err)
	_, err = server.decodeGitHubManifestState(t.Context(), strings.Repeat("x", 12*1024+1))
	require.Error(t, err)
	for _, mutate := range []func(*githubManifestState){
		func(s *githubManifestState) { s.FlowID = uuid.Nil },
		func(s *githubManifestState) { s.OrgID = uuid.Nil },
		func(s *githubManifestState) { s.ProjectID = uuid.Nil },
		func(s *githubManifestState) { s.AppID = uuid.Nil },
		func(s *githubManifestState) { s.UserID = uuid.Nil },
		func(s *githubManifestState) { s.SetupRevision = 0 },
		func(s *githubManifestState) { s.ExpiresAt = time.Now().Add(-time.Second) },
		func(s *githubManifestState) { s.ExpiresAt = time.Now().Add(time.Hour + time.Minute) },
	} {
		invalid := state
		mutate(&invalid)
		token, err := server.encodeGitHubManifestState(t.Context(), invalid)
		require.NoError(t, err)
		_, err = server.decodeGitHubManifestState(t.Context(), token)
		require.Error(t, err)
	}
	body, err := json.Marshal(state)
	require.NoError(t, err)
	otherPurpose, err := secrets.SealToken(t.Context(), wrapper, integrationOAuthStatePurpose, body)
	require.NoError(t, err)
	_, err = server.decodeGitHubManifestState(t.Context(), otherPurpose)
	require.Error(t, err)
}

func TestGitHubManifestStateHasIndependentOneHourWindow(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	state := githubManifestState{
		FlowID: uuid.New(), OrgID: uuid.New(), ProjectID: uuid.New(), AppID: uuid.New(), UserID: uuid.New(),
		SetupRevision: 1, ExpiresAt: now.Add(time.Hour),
	}
	require.NoError(t, state.validate(now))
	require.NoError(t, state.validate(now.Add(45*time.Minute)), "long registration must survive the Slack OAuth window")
	require.Error(t, state.validate(state.ExpiresAt), "expiry is exclusive")
	state.ExpiresAt = now.Add(time.Hour + time.Nanosecond)
	require.Error(t, state.validate(now), "sealed state cannot extend the one-hour upper bound")
	require.Equal(t, 10*time.Minute, integrationOAuthStateTTL, "Slack OAuth keeps its existing lifetime")
}

func TestGitHubManifestWebhookRequiresPublicAPIOrigin(t *testing.T) {
	t.Parallel()
	for _, apiURL := range []string{"http://api.omnara.test/api/v1", "https://localhost/api/v1", "not-a-url"} {
		server := &Server{publicURL: "https://omnara.test", publicAPIURL: apiURL}
		_, err := server.githubManifestWebhookURL()
		require.Error(t, err, "invalid API origin must not silently use the browser origin: %s", apiURL)
	}
}

func TestGitHubGuidedSetupRestrictsTrustedOrigins(t *testing.T) {
	t.Parallel()
	wrapper, err := secrets.NewLocalKeyWrapper("test", map[string][]byte{"test": bytes.Repeat([]byte{1}, 32)})
	require.NoError(t, err)
	for _, tc := range []struct {
		public, api string
		valid       bool
	}{
		{"https://omnara.test", "", true},
		{"https://omnara.test", github.APIURL, true},
		{"https://omnara.test", "https://github.example/api/v3", false},
		{"https://omnara.test", "http://127.0.0.1:1234", false},
		{"http://omnara.test", "", false},
		{"https://user:password@omnara.test", "", false},
		{"https://omnara.test?redirect=elsewhere", "", false},
		{"https://omnara.test/path", "", false},
	} {
		server := &Server{publicURL: tc.public, secretKeyWrapper: wrapper, githubClientConfig: github.Config{APIURL: tc.api}}
		err := server.validateGitHubGuidedSetup()
		require.Equal(t, tc.valid, err == nil, "public %s api %s", tc.public, tc.api)
		if tc.api == "https://github.example/api/v3" {
			require.NotContains(t, err.Error(), "manual", "manual Enterprise setup is not established by the provider client")
		}
	}
}
