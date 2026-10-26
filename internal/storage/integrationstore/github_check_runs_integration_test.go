//go:build integration

package integrationstore_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestGitHubCheckRunRestartMarksAlwaysAdvance(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	address := integrationstore.ConversationAddress{Kind: "pull_request", Ref: "1001#42"}
	agentID := uuid.New()
	mark := func(restart bool) {
		t.Helper()
		require.NoError(t, f.store.MarkGitHubCheckRun(f.ctx, f.project, f.integrationID, agentID, address, restart))
	}
	tracked := func() integrationstore.GitHubCheckRun {
		t.Helper()
		runs, err := f.store.ListGitHubCheckRuns(f.ctx, uuid.Nil, 10)
		require.NoError(t, err)
		require.Len(t, runs, 1)
		return runs[0]
	}
	mark(true)
	pending := tracked()
	mark(true)
	require.ErrorIs(t, f.store.SaveGitHubCheckRun(f.ctx, pending), storeerr.ErrConflict,
		"a second restart while one is pending must invalidate the in-flight save")
	current := tracked()
	require.True(t, current.Restart)
	mark(false)
	require.NoError(t, f.store.SaveGitHubCheckRun(f.ctx, current), "a comment mark must not invalidate the save")
	finished := tracked()
	require.False(t, finished.Restart)
	finished.Done = true
	require.NoError(t, f.store.SaveGitHubCheckRun(f.ctx, finished))
	done := tracked()
	mark(false)
	require.ErrorIs(t, f.store.DeleteGitHubCheckRun(f.ctx, done), storeerr.ErrConflict,
		"new work must invalidate deleting a finished row")
	require.False(t, tracked().Done)
}

func TestRestartGitHubCheckRunsFlagsTrackedAgentsOnThatPullRequest(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	address := integrationstore.ConversationAddress{Kind: "pull_request", Ref: "1001#42"}
	other := integrationstore.ConversationAddress{Kind: "pull_request", Ref: "1001#43"}
	require.NoError(t, f.store.MarkGitHubCheckRun(f.ctx, f.project, f.integrationID, uuid.New(), address, false))
	require.NoError(t, f.store.MarkGitHubCheckRun(f.ctx, f.project, f.integrationID, uuid.New(), other, false))
	require.NoError(t, f.store.RestartGitHubCheckRuns(f.ctx, f.project, f.integrationID, address))
	runs, err := f.store.ListGitHubCheckRuns(f.ctx, uuid.Nil, 10)
	require.NoError(t, err)
	require.Len(t, runs, 2)
	for _, run := range runs {
		require.Equal(t, run.Address == address, run.Restart, "pull request %s", run.Address.Ref)
	}
}
