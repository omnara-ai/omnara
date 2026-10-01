package httpapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/github"
	logpkg "github.com/omnara-ai/omnara/internal/log"
)

// Credential workflows can return URLs, response bodies or database details containing
// secrets. Log provider diagnostics and error types without arbitrary error text.
func logIntegrationCredentialError(ctx context.Context, operation string, err error) {
	var githubErr *github.APIError
	var discordErr *discord.APIError
	var sqlErr interface{ SQLState() string }
	var diagnostic error
	switch {
	case errors.As(err, &githubErr):
		diagnostic = githubErr
	case errors.As(err, &discordErr):
		diagnostic = discordErr
	case errors.Is(err, context.DeadlineExceeded):
		diagnostic = context.DeadlineExceeded
	case errors.Is(err, context.Canceled):
		diagnostic = context.Canceled
	case errors.As(err, &sqlErr):
		diagnostic = fmt.Errorf("database error (SQLSTATE %s)", sqlErr.SQLState())
	default:
		diagnostic = fmt.Errorf("credential operation failed (%T)", err)
	}
	logpkg.Error(ctx, fmt.Errorf("%s: %w", operation, diagnostic))
}
