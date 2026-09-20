package httpapi

import "github.com/omnara-ai/omnara/internal/integration/github"

// WithGitHubClientConfig sets trusted provider transport settings for app
// setup. Credentials and installation identity always come from the authorized
// project secret and setup request, never this option.
func WithGitHubClientConfig(config github.Config) Option {
	return func(s *Server) { s.githubClientConfig = config }
}
