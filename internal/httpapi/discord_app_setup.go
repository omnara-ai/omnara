package httpapi

import "github.com/omnara-ai/omnara/internal/integration/discord"

func WithDiscordClientConfig(config discord.Config) Option {
	return func(s *Server) { s.discordClientConfig = config }
}
