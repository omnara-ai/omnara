package maintenance

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/stretchr/testify/require"
)

func TestCoreMaintenanceJobsReportOutcomes(t *testing.T) {
	failure := errors.New("store failed")
	for _, test := range []struct {
		name, success, failure string
		job                    func(log *slog.Logger, err error) func(context.Context) bool
	}{
		{"reap runtime locks", "reaped expired runtime locks", "reap expired runtime locks",
			func(log *slog.Logger, err error) func(context.Context) bool {
				return reapRuntimeLocks(log, func(context.Context, int32) (int64, error) { return 1, err })
			}},
		{"expire daemon runtimes", "expired daemon runtimes", "expire daemon runtimes",
			func(log *slog.Logger, err error) func(context.Context) bool {
				return expireDaemonRuntimes(log, func(context.Context, int32) ([]executionstore.DaemonRuntimeRecord, error) {
					return []executionstore.DaemonRuntimeRecord{{}}, err
				})
			}},
		{"expire process tools", "expired process tool calls", "expire process tool calls",
			func(log *slog.Logger, err error) func(context.Context) bool {
				return expireProcessTools(log, func(context.Context, time.Duration) (int64, error) { return 1, err })
			}},
		{"delete expired webhook deliveries",
			"cleaned expired event webhook deliveries", "cleanup expired event webhook deliveries",
			func(log *slog.Logger, err error) func(context.Context) bool {
				return deleteExpiredWebhookDeliveries(log, func(context.Context, int32) (int64, error) { return 1, err })
			}},
		{"cleanup auth state", "cleaned inactive auth state", "cleanup inactive auth state",
			func(log *slog.Logger, err error) func(context.Context) bool {
				return cleanupAuthState(log, func(context.Context) (identitystore.AuthStateCleanupResult, error) {
					return identitystore.AuthStateCleanupResult{DeletedBrowserSessions: 1}, err
				})
			}},
		{"cleanup integration states", "cleaned integration states", "cleanup integration states",
			func(log *slog.Logger, err error) func(context.Context) bool {
				return cleanupIntegrationStates(log, func(context.Context, time.Duration, int) (int64, error) {
					return 1, err
				})
			}},
		{"cleanup completed inbox", "cleaned completed integration inbox", "cleanup completed integration inbox",
			func(log *slog.Logger, err error) func(context.Context) bool {
				return cleanupCompletedInbox(log, func(context.Context, time.Duration, int) (int64, error) {
					return 1, err
				})
			}},
		{"cleanup deleted inbox", "cleaned deleted integration inbox", "cleanup deleted integration inbox",
			func(log *slog.Logger, err error) func(context.Context) bool {
				return cleanupDeletedInbox(log, func(context.Context, int) (int64, error) { return 1, err })
			}},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := func(ctx context.Context, err error) string {
				var output bytes.Buffer
				test.job(slog.New(slog.NewJSONHandler(&output, nil)), err)(ctx)
				return output.String()
			}
			require.Contains(t, run(t.Context(), nil), test.success)
			failed := run(t.Context(), failure)
			require.Contains(t, failed, test.failure)
			require.Contains(t, failed, failure.Error())
			canceled, cancel := context.WithCancel(t.Context())
			cancel()
			require.Empty(t, run(canceled, context.Canceled), "shutdown must suppress maintenance messages")
		})
	}
}

func TestCleanupJobsRerunOnlyWhileBehind(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	full := int64(integrationInboxCleanupBatch)
	webhooks := func(count int64) bool {
		return deleteExpiredWebhookDeliveries(log, func(context.Context, int32) (int64, error) {
			return count, nil
		})(t.Context())
	}
	states := func(count int64) bool {
		return cleanupIntegrationStates(log, func(context.Context, time.Duration, int) (int64, error) {
			return count, nil
		})(t.Context())
	}
	auth := func(deleted int64) bool {
		return cleanupAuthState(log, func(context.Context) (identitystore.AuthStateCleanupResult, error) {
			return identitystore.AuthStateCleanupResult{DeletedInactiveTokens: deleted}, nil
		})(t.Context())
	}
	require.True(t, webhooks(int64(eventWebhookDeliveryCleanupBatchSize)))
	require.False(t, webhooks(int64(eventWebhookDeliveryCleanupBatchSize)-1))
	require.True(t, states(full))
	require.False(t, states(full-1))
	require.True(t, auth(500))
	require.False(t, auth(499))
	require.True(t, cleanupCompletedInbox(log, func(context.Context, time.Duration, int) (int64, error) {
		return full, nil
	})(t.Context()))
	require.True(t, cleanupDeletedInbox(log, func(context.Context, int) (int64, error) { return full, nil })(t.Context()))
	require.False(t, cleanupDeletedInbox(log, func(context.Context, int) (int64, error) { return 1, nil })(t.Context()))
}
