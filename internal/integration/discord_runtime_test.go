package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestDiscordHeartbeatLogsUnexpectedRenewalOncePerEpisode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var output bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&output, nil))
		lease := integrationstore.IntegrationRuntimeLease{
			IntegrationRuntimeRevision: integrationstore.IntegrationRuntimeRevision{IntegrationID: uuid.New()},
		}
		first := fmt.Errorf("renew ownership: %w", errors.New("database unavailable"))
		second := fmt.Errorf("renew ownership: %w", context.DeadlineExceeded)
		third := fmt.Errorf("renew ownership: %w", context.Canceled)
		sequence := []error{
			first, first, nil, second, second, nil, third, third, integrationstore.ErrIntegrationRuntimeLeaseLost,
		}
		calls := 0
		heartbeatDiscordRuntime(ctx, cancel, lease, time.Now().Add(discordRuntimeLease), logger,
			func(callCtx context.Context, got integrationstore.IntegrationRuntimeLease, duration time.Duration) error {
				require.Equal(t, lease, got)
				require.Equal(t, discordRuntimeLease, duration)
				require.NoError(t, callCtx.Err())
				require.Less(t, calls, len(sequence))
				err := sequence[calls]
				calls++
				return err
			})
		require.ErrorIs(t, ctx.Err(), context.Canceled)
		require.Equal(t, len(sequence), calls)
		entries := strings.Split(strings.TrimSpace(output.String()), "\n")
		require.Len(t, entries, 3, "only a successful renewal starts a new failure episode")
		for i, cause := range []error{first, second, third} {
			var entry map[string]any
			require.NoError(t, json.Unmarshal([]byte(entries[i]), &entry))
			require.Equal(t, "WARN", entry["level"])
			require.Equal(t, "renew Discord connection lease", entry["msg"])
			require.Equal(t, lease.IntegrationID.String(), entry["integration_id"])
			require.Equal(t, cause.Error(), entry["error"])
		}
	})
}

func TestDiscordHeartbeatUnexpectedRenewalStopsAtLeaseDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var output bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&output, nil))
		deadline := time.Now().Add(discordRuntimeLease)
		calls := 0
		heartbeatDiscordRuntime(ctx, cancel, integrationstore.IntegrationRuntimeLease{}, deadline, logger,
			func(context.Context, integrationstore.IntegrationRuntimeLease, time.Duration) error {
				calls++
				return errors.New("database unavailable")
			})
		require.Greater(t, calls, 1)
		require.Equal(t, deadline, time.Now())
		require.ErrorIs(t, ctx.Err(), context.Canceled)
		require.Equal(t, 1, strings.Count(output.String(), "renew Discord connection lease"))
		require.Contains(t, output.String(), "database unavailable")
	})
}

func TestDiscordHeartbeatExpectedStopsStayQuiet(t *testing.T) {
	for _, test := range []struct {
		name         string
		err          error
		cancelBefore bool
		cancelDuring bool
	}{
		{name: "lease lost", err: integrationstore.ErrIntegrationRuntimeLeaseLost},
		{name: "authorization revoked", err: storeerr.ErrUnauthorized},
		{name: "integration deleted", err: storeerr.ErrNotFound},
		{name: "shutdown while waiting", cancelBefore: true},
		{name: "shutdown during renewal", cancelDuring: true, err: context.Canceled},
		{name: "driver error during shutdown", cancelDuring: true, err: errors.New("connection closed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if test.cancelBefore {
					cancel()
				}
				var output bytes.Buffer
				logger := slog.New(slog.NewJSONHandler(&output, nil))
				calls := 0
				heartbeatDiscordRuntime(ctx, cancel, integrationstore.IntegrationRuntimeLease{},
					time.Now().Add(discordRuntimeLease), logger,
					func(context.Context, integrationstore.IntegrationRuntimeLease, time.Duration) error {
						calls++
						if test.cancelDuring {
							cancel()
						}
						return fmt.Errorf("renew: %w", test.err)
					})
				require.ErrorIs(t, ctx.Err(), context.Canceled)
				require.Empty(t, output.String())
				if test.cancelBefore {
					require.Zero(t, calls)
				} else {
					require.Positive(t, calls)
				}
			})
		})
	}
}

func TestDiscordRuntimeFailureMessage(t *testing.T) {
	t.Parallel()
	untrusted := "private token=do-not-expose\x00" + strings.Repeat("界", 2000) + "\xff"
	const credentials = "Discord rejected the bot token. Check this integration's credentials."
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
			"Discord denied access. Check the bot's permissions and integration setup."},
		{"identity", &discord.APIError{Code: discord.ScopeMismatch},
			"Discord bot identity does not match this integration's setup. Check the application ID, bot user ID and token."},
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

func TestDiscordReconnectDelayHonorsProviderFailures(t *testing.T) {
	for _, test := range []struct {
		name    string
		err     error
		minimum time.Duration
	}{
		{"revoked token", &discord.APIError{Code: discord.PermanentFailure, StatusCode: 401}, time.Hour},
		{"missing permissions", &discord.APIError{Code: discord.PermanentFailure, StatusCode: 403}, time.Hour},
		{"READY identity mismatch", &discord.APIError{Code: discord.ScopeMismatch}, time.Hour},
		{"rate limit", &discord.APIError{Code: discord.RateLimited, RetryAfter: 2 * time.Hour}, 2 * time.Hour},
		{"disabled intent", &discord.GatewayError{Fatal: true}, time.Hour},
		{"session budget", discordIdentifyWaitError{After: 20 * time.Hour}, 20 * time.Hour},
		{"reconnect with permit wait", errors.Join(
			&discord.GatewayError{RetryAfter: time.Second}, discordIdentifyWaitError{After: 20 * time.Hour},
		), 20 * time.Hour},
		{"reconnect with API rate limit", errors.Join(
			&discord.GatewayError{RetryAfter: time.Second},
			&discord.APIError{Code: discord.RateLimited, RetryAfter: 2 * time.Hour},
		), 2 * time.Hour},
		{"network", errors.New("network unavailable"), time.Second},
	} {
		t.Run(
			test.name,
			func(t *testing.T) { require.GreaterOrEqual(t, discordReconnectDelay(test.err), test.minimum) },
		)
	}
}
