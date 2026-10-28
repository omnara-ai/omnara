package httpapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	logpkg "github.com/omnara-ai/omnara/observability/wideevent"
)

// Credential workflows can return URLs, response bodies or database details containing
// secrets. Log provider diagnostics and error types without arbitrary error text.
func logIntegrationCredentialError(ctx context.Context, operation string, err error) {
	var githubErr *github.APIError
	var discordErr *discord.APIError
	var queryErr *storeerr.QueryError
	var diagnostic error
	switch {
	case errors.As(err, &githubErr):
		diagnostic = githubErr
	case errors.As(err, &discordErr):
		diagnostic = discordErr
	case errors.As(err, &queryErr):
		diagnostic = queryErr
	case errors.Is(err, context.DeadlineExceeded):
		diagnostic = context.DeadlineExceeded
	case errors.Is(err, context.Canceled):
		diagnostic = context.Canceled
	default:
		diagnostic = fmt.Errorf("credential operation failed (%T)", err)
	}
	logpkg.Error(ctx, fmt.Errorf("%s: %w", operation, diagnostic))
}
