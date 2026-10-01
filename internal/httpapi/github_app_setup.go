package httpapi

import "github.com/omnara-ai/omnara/internal/integration/github"

func WithGitHubClientConfig(config github.Config) Option {
	return func(s *Server) { s.githubClientConfig = config }
}
