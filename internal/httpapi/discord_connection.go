package httpapi

import "github.com/omnara-ai/omnara/internal/integration/discord"

// WithDiscordClientConfig sets trusted provider transport settings for connection
// setup. Credentials always come from the authorized project secret and request.
func WithDiscordClientConfig(config discord.Config) Option {
	return func(s *Server) { s.discordClientConfig = config }
}
