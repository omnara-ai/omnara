package tools

import (
	"fmt"
	"testing"

	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/stretchr/testify/require"
)

func TestGitHubRepositoryPageContinuesAcrossWindows(t *testing.T) {
	repositories := func(start, count int) []github.Repository {
		result := make([]github.Repository, count)
		for i := range result {
			result[i] = github.Repository{FullName: fmt.Sprintf("acme/repo-%d", start+i)}
		}
		return result
	}
	names := func(start, count int) []githubRepositoryPayload {
		result := make([]githubRepositoryPayload, count)
		for i := range result {
			result[i] = githubRepositoryPayload{FullName: fmt.Sprintf("acme/repo-%d", start+i)}
		}
		return result
	}
	for _, test := range []struct {
		name        string
		window      []github.Repository
		windowStart int
		nextStart   int
		offset      int
		limit       int
		want        []githubRepositoryPayload
		page        listing.OffsetPage
	}{
		{"within GitHub page", repositories(0, 100), 0, 100, 0, 50, names(0, 50), listing.ContinueAt(50, true)},
		{"stops at GitHub page", repositories(100, 100), 100, 200, 190, 50, names(190, 10), listing.ContinueAt(200, true)},
		{"last GitHub page", repositories(100, 10), 100, 0, 105, 50, names(105, 5), listing.OffsetPage{}},
		{"short GitHub page", repositories(0, 1), 0, 100, 0, 50, names(0, 1), listing.ContinueAt(100, true)},
		{"past short GitHub page", repositories(0, 1), 0, 100, 1, 50, names(0, 0), listing.ContinueAt(100, true)},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := githubRepositoryPage(test.window, test.windowStart, test.nextStart, test.offset, test.limit)
			require.Equal(t, test.want, result.Repositories)
			require.Equal(t, test.page, result.OffsetPage)
		})
	}
}
