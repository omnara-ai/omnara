package integration

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/stretchr/testify/require"
)

func TestDiscordRuntimeFailureMessage(t *testing.T) {
	t.Parallel()
	untrusted := "private token=do-not-expose\x00" + strings.Repeat("界", 2000) + "\xff"
	const credentials = "Discord rejected the bot token. Check this app's credentials."
	const rateLimited = "Discord is limiting connection requests. Omnara will retry later."
	const fallback = "Omnara could not maintain the Discord connection. It will retry automatically."
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{"no error", nil, ""},
		{"canceled", fmt.Errorf("%s: %w", untrusted, context.Canceled), ""},
		{"unknown", errors.New(untrusted), fallback},
		{"deadline", context.DeadlineExceeded, fallback},
		{"invalid API response", &discord.APIError{Code: discord.InvalidResponse}, fallback},
		{"untrusted error code", &discord.APIError{Code: discord.ErrorCode(untrusted)}, fallback},
		{"HTTP token", &discord.APIError{Code: discord.PermanentFailure, StatusCode: http.StatusUnauthorized}, credentials},
		{"HTTP permissions", &discord.APIError{Code: discord.PermanentFailure, StatusCode: http.StatusForbidden},
			"Discord denied access. Check the bot's permissions and app setup."},
		{"identity", &discord.APIError{Code: discord.ScopeMismatch},
			"Discord bot identity does not match this app's setup. Check the application ID, bot user ID and token."},
		{"HTTP rate limit", &discord.APIError{Code: discord.RateLimited}, rateLimited},
		{"session start limit", discordIdentifyWaitError{After: time.Hour}, rateLimited},
		{"gateway token", &discord.GatewayError{CloseCode: 4004, Fatal: true}, credentials},
		{"gateway rate limit", &discord.GatewayError{CloseCode: 4008}, rateLimited},
		{"gateway intents", &discord.GatewayError{CloseCode: 4014, Fatal: true},
			"Discord denied a required gateway intent. Check the bot's enabled intents in the Discord Developer Portal."},
		{"gateway configuration", &discord.GatewayError{CloseCode: 4010, Fatal: true},
			"Discord rejected the gateway configuration. Contact your Omnara administrator."},
		{"gateway reconnect", &discord.GatewayError{CloseCode: 4009, ResetSession: true}, fallback},
		{"wrapped HTTP", fmt.Errorf("%s: %w", untrusted, &discord.APIError{Code: discord.RateLimited}), rateLimited},
		{"wrapped gateway", fmt.Errorf("%s: %w", untrusted, &discord.GatewayError{CloseCode: 4004}), credentials},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			message := discordRuntimeFailureMessage(test.err)
			require.Equal(t, test.want, message)
			require.True(t, utf8.ValidString(message))
			require.LessOrEqual(t, len(message), 256)
			require.NotContains(t, message, "private")
			require.NotContains(t, message, "\x00")
		})
	}
}
