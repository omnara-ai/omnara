package httpapi

import (
	"errors"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/github"
)

// WithGitHubClientConfig sets trusted provider transport settings for connection
// setup. Credentials and installation identity always come from the authorized
// project secret and setup request, never this option.
func WithGitHubClientConfig(config github.Config) Option {
	return func(s *Server) { s.githubClientConfig = config }
}

func integrationConnectionInputError(err error) error {
	var discordErr *discord.APIError
	if errors.As(err, &discordErr) {
		switch discordErr.Code {
		case discord.ScopeMismatch, discord.PermanentFailure:
			return apierror.FromCode(openapi.ErrorCodeInvalidRequest,
				"Discord application or bot identity could not be verified")
		case discord.RateLimited:
			return apierror.FromCode(openapi.ErrorCodeRateLimited, "Discord setup rate limited; retry later")
		default:
			return apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "Discord identity verification unavailable")
		}
	}
	var providerErr *github.APIError
	if !errors.As(err, &providerErr) {
		return apierror.ProjectScoped(err)
	}
	switch providerErr.Code {
	case github.ScopeMismatch, github.PermanentFailure:
		return apierror.FromCode(openapi.ErrorCodeInvalidRequest,
			"GitHub App, installation or bot identity could not be verified")
	case github.RateLimited:
		return apierror.FromCode(openapi.ErrorCodeRateLimited, "GitHub setup rate limited; retry later")
	default:
		return apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "GitHub identity verification unavailable")
	}
}
