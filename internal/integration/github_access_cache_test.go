package integration

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestGitHubInboxTokenReuseRetainsLiveAuthority(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"credential version", "revoked secret", "setup revision", "project"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			f, provider := newGitHubFeedbackFixture(t)
			saved := f.integration
			scope := github.Scope{RepositoryID: 1001, PullRequest: 42}
			for range 2 {
				client, err := provider.requestAccess(t.Context(), saved)
				require.NoError(t, err)
				_, err = client.GetPullRequest(t.Context(), scope)
				require.NoError(t, err)
			}
			client, err := provider.requestAccess(t.Context(), saved)
			require.NoError(t, err)
			f.mu.Lock()
			before, mints := len(f.requests), 0
			for _, request := range f.requests {
				if request == "POST /app/installations/456/access_tokens" {
					mints++
				}
			}
			switch change {
			case "credential version":
				f.version = uuid.New()
			case "revoked secret":
				f.revoked = true
			case "setup revision":
				f.integration.SetupRevision++
			case "project":
				f.integration.ProjectID = uuid.New()
			}
			f.mu.Unlock()
			require.Equal(t, 1, mints, "inbox clients should share only the token")
			_, err = client.GetPullRequest(t.Context(), scope)
			require.ErrorIs(t, err, storeerr.ErrUnauthorized)
			f.mu.Lock()
			after := len(f.requests)
			f.mu.Unlock()
			require.Equal(t, before, after, "cached tokens must not permit HTTP after authority changes")
			if change == "credential version" {
				client, err = provider.requestAccess(t.Context(), saved)
				require.NoError(t, err)
				_, err = client.GetPullRequest(t.Context(), scope)
				require.NoError(t, err)
				f.mu.Lock()
				require.Len(t, f.requests, before+3, "rotated credentials must mint a new token before the two reads")
				f.mu.Unlock()
			} else {
				_, err = provider.requestAccess(t.Context(), saved)
				require.ErrorIs(t, err, storeerr.ErrUnauthorized)
			}
		})
	}
}
