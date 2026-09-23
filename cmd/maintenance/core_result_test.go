package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/maintenance"
	"github.com/stretchr/testify/require"
)

func TestReportCoreMaintenanceResultPreservesCleanupOutcomes(t *testing.T) {
	for _, test := range []struct {
		name     string
		canceled bool
	}{
		{name: "running"},
		{name: "canceled", canceled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&output, nil))
			ctx, cancel := context.WithCancel(logpkg.WithLogger(t.Context(), logger))
			defer cancel()
			if test.canceled {
				cancel()
			}
			ctx, event := logent.MaintenanceLoop(ctx, time.Second, time.Now())
			reportCoreMaintenanceResult(ctx, logger, maintenance.CoreResult{
				DeletedAppStates: 1, DeletedWebhooks: 2,
				CompletedInbox: 100, CompletedInboxBudgetExhausted: true,
				DeletedInbox: 100, DeletedInboxBudgetExhausted: true,
			})
			event.Done(ctx)
			lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
			var loop map[string]any
			require.NoError(t, json.Unmarshal(lines[len(lines)-1], &loop))
			require.Equal(t, true, loop["maintenance.loop.worked"])
			require.NotContains(t, loop, "error.message")
			if test.canceled {
				require.Len(t, lines, 1, "shutdown must suppress cleanup success messages")
			} else {
				require.Contains(t, output.String(), "cleaned app states")
				require.Contains(t, output.String(), "cleaned expired event webhooks")
				require.Contains(t, output.String(), "cleaned completed app inbox")
				require.Contains(t, output.String(), "cleaned deleted app inbox")
				require.Contains(t, output.String(), `"budget_exhausted":true`)
				require.Contains(t, output.String(), `"retention":604800000000000`)
			}
		})
	}
}

func TestReportCoreMaintenanceResultJoinsCleanupErrors(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	ctx, event := logent.MaintenanceLoop(logpkg.WithLogger(t.Context(), logger), time.Second, time.Now())
	reportCoreMaintenanceResult(ctx, logger, maintenance.CoreResult{
		WebhookCleanupErr:   errors.New("webhook cleanup failed"),
		AppStatesCleanupErr: errors.New("app state cleanup failed"),
		CompletedInbox:      100, CompletedInboxCleanupErr: context.DeadlineExceeded,
		DeletedInboxCleanupErr: errors.New("deleted scope cleanup failed"),
	})
	event.Done(ctx)
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
	var loop map[string]any
	require.NoError(t, json.Unmarshal(lines[len(lines)-1], &loop))
	for _, message := range []string{
		"webhook cleanup failed", "app state cleanup failed", "context deadline exceeded", "deleted scope cleanup failed",
	} {
		require.Contains(t, loop["error.message"], message)
	}
	require.Contains(t, output.String(), `"count":100`)
	require.NotContains(t, output.String(), "cleaned completed app inbox")
}
