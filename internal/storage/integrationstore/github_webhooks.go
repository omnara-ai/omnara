package integrationstore

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const GitHubWebhookCredentialLimit = 16

func (s *Store) ListGitHubWebhookCredentialApps(
	ctx context.Context, appID string, limit int,
) ([]ProjectAppRecord, error) {
	id, err := strconv.ParseInt(appID, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != appID {
		return nil, storeerr.InvalidRequest(errors.New("GitHub App ID must be a canonical positive integer"))
	}
	if limit <= 0 || limit > GitHubWebhookCredentialLimit {
		return nil, storeerr.InvalidRequest(fmt.Errorf(
			"GitHub webhook credential limit must be between 1 and %d", GitHubWebhookCredentialLimit,
		))
	}
	rows, err := s.q.ListGitHubWebhookCredentialApps(ctx, dbsqlc.ListGitHubWebhookCredentialAppsParams{
		AppTypes: appdefinition.AppTypesForProvider(appdefinition.ProviderGitHub), GithubAppID: appID, RowLimit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list GitHub webhook credential apps: %w", err)
	}
	result := make([]ProjectAppRecord, 0, len(rows))
	for _, row := range rows {
		app, err := projectAppRecord(row)
		if err != nil {
			return nil, err
		}
		result = append(result, app)
	}
	return result, nil
}
