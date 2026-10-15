//go:build integration

package httpapi

import (
	"github.com/benbjohnson/clock"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/github"
)

func WithTimer(timer clock.Clock) Option {
	return func(s *Server) {
		s.timer = timer
	}
}

func WithDiscordClientConfig(config discord.Config) Option {
	return func(s *Server) { s.discordClientConfig = config }
}

func WithGitHubClientConfig(config github.Config) Option {
	return func(s *Server) { s.githubClientConfig = config }
}
