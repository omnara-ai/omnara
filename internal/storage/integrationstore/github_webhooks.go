package integrationstore

import (
	"context"
	"fmt"
	"strconv"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// GitHubWebhookCredentialLimit bounds App-level signature verification work.
const GitHubWebhookCredentialLimit = 16

// ListGitHubWebhookCredentialConnections selects one existing connection per
// live credential available to its project, including disabled installations.
// Callers must authorize and read the secret again before signature verification.
// This method neither provisions an App nor chooses a recipient installation.
func (s *Store) ListGitHubWebhookCredentialConnections(
	ctx context.Context, appID string, limit int,
) ([]IntegrationConnectionRecord, error) {
	id, err := strconv.ParseInt(appID, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != appID {
		return nil, storeerr.InvalidRequest(fmt.Errorf("GitHub App ID must be a canonical positive integer"))
	}
	if limit <= 0 || limit > GitHubWebhookCredentialLimit {
		return nil, storeerr.InvalidRequest(fmt.Errorf(
			"GitHub webhook credential limit must be between 1 and %d", GitHubWebhookCredentialLimit,
		))
	}
	rows, err := s.q.ListGitHubWebhookCredentialConnections(ctx, dbsqlc.ListGitHubWebhookCredentialConnectionsParams{
		AppID: appID, RowLimit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list GitHub webhook credential connections: %w", err)
	}
	result := make([]IntegrationConnectionRecord, 0, len(rows))
	for _, row := range rows {
		result = append(result, integrationConnectionRecordFromSQLC(row))
	}
	return result, nil
}
