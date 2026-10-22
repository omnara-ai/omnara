package main

import (
	"bytes"
	"log/slog"
	"testing"

	logpkg "github.com/omnara-ai/omnara/observability/wideevent"
	"github.com/stretchr/testify/require"
)

func TestWorkerLogLevel(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  slog.Level
	}{
		{"", slog.LevelInfo}, {"debug", slog.LevelDebug}, {"DEBUG", slog.LevelDebug}, {"info", slog.LevelInfo},
		{"warn", slog.LevelWarn}, {"warning", slog.LevelWarn}, {"error", slog.LevelError},
	} {
		t.Run(tc.value, func(t *testing.T) {
			level, err := parseLogLevel(tc.value)
			require.NoError(t, err)
			require.Equal(t, tc.want, level)
			var output bytes.Buffer
			logger := slog.New(logpkg.NewJSONHandler(&output, &slog.HandlerOptions{Level: level}))
			logger.InfoContext(t.Context(), "message")
			require.Equal(t, level <= slog.LevelInfo, output.Len() > 0)
		})
	}
	_, err := parseLogLevel("invalid")
	require.ErrorContains(t, err, "OMNARA_LOG_LEVEL")
}
