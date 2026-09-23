package httpapi

import "github.com/omnara-ai/omnara/internal/apps/discord"

func WithDiscordClientConfig(config discord.Config) Option {
	return func(s *Server) { s.discordClientConfig = config }
}
