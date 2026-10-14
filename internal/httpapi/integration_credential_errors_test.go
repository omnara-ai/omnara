package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/github"
	logpkg "github.com/omnara-ai/omnara/observability/wideevent"
	"github.com/stretchr/testify/require"
)

func TestIntegrationCredentialFailureDiagnosticsDoNotLogSecrets(t *testing.T) {
	t.Parallel()
	const sensitive = "private-key webhook-secret token manifest-code provider-body"
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{
			name: "GitHub setup",
			err:  fmt.Errorf(sensitive+": %w", &github.APIError{Code: github.TransientFailure, StatusCode: 503}),
			want: "github transient_failure (HTTP 503)",
		},
		{
			name: "Discord setup",
			err:  fmt.Errorf(sensitive+": %w", &discord.APIError{Code: discord.InvalidResponse, StatusCode: 502}),
			want: "discord invalid_response (HTTP 502",
		},
		{
			name: "Git credentials",
			err:  fmt.Errorf(sensitive+": %w", &github.APIError{Code: github.TransientFailure, StatusCode: 500}),
			want: "github transient_failure (HTTP 500)",
		},
		{"database save", &pgconn.PgError{Code: "23505", Message: sensitive, Detail: sensitive}, "SQLSTATE 23505"},
		{"unknown save", errors.New(sensitive), "*errors.errorString"},
		{"conversion timeout", fmt.Errorf(sensitive+": %w", context.DeadlineExceeded), "context deadline exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			ctx := logpkg.WithLogger(t.Context(), slog.New(slog.NewJSONHandler(&output, nil)))
			event := logpkg.NewEvent(ctx, "http.request")
			ctx = logpkg.WithEvent(ctx, event)
			logIntegrationCredentialError(ctx, tc.name, tc.err)
			event.Done(ctx)
			require.Contains(t, output.String(), tc.want)
			for _, secret := range []string{"private-key", "webhook-secret", "token", "manifest-code", "provider-body"} {
				require.NotContains(t, output.String(), secret)
			}
		})
	}
}
