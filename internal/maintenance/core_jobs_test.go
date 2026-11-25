package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/observability/metrics"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
)

func TestCoreMaintenanceJobsReportOutcomes(t *testing.T) {
	failure := errors.New("store failed")
	for _, test := range []struct {
		name, success, failure string
		job                    func(log *slog.Logger, err error) func(context.Context) maintenanceResult
	}{
		{"reap runtime locks", "reaped expired runtime locks", "reap expired runtime locks",
			func(log *slog.Logger, err error) func(context.Context) maintenanceResult {
				return reapRuntimeLocks(log, func(context.Context, int32) (int64, error) { return 1, err })
			}},
		{"expire daemon runtimes", "expired daemon runtimes", "expire daemon runtimes",
			func(log *slog.Logger, err error) func(context.Context) maintenanceResult {
				return expireDaemonRuntimes(log, func(context.Context, int32) ([]executionstore.DaemonRuntimeRecord, error) {
					return []executionstore.DaemonRuntimeRecord{{}}, err
				})
			}},
		{"expire process tools", "expired process tool calls", "expire process tool calls",
			func(log *slog.Logger, err error) func(context.Context) maintenanceResult {
				return expireProcessTools(log, func(context.Context, time.Duration) (int64, error) { return 1, err })
			}},
		{"expire custom tools", "expired custom tool calls", "expire custom tool calls",
			func(log *slog.Logger, err error) func(context.Context) maintenanceResult {
				return expireCustomTools(log, func(context.Context, int32) (int64, error) { return 1, err })
			}},
		{"delete expired webhook deliveries",
			"cleaned expired event webhook deliveries", "cleanup expired event webhook deliveries",
			func(log *slog.Logger, err error) func(context.Context) maintenanceResult {
				return deleteExpiredWebhookDeliveries(log, func(context.Context, int32) (int64, error) { return 1, err }, nil)
			}},
		{"delete expired otel export deliveries",
			"cleaned expired otel export deliveries", "cleanup expired otel export deliveries",
			func(log *slog.Logger, err error) func(context.Context) maintenanceResult {
				return deleteExpiredOtelExportDeliveries(
					log,
					func(context.Context, int32) ([]executionstore.OtelExportDelivery, error) {
						return []executionstore.OtelExportDelivery{{ID: uuid.New()}}, err
					},
					nil,
				)
			}},
		{"refresh model prices", "stored new model prices", "refresh model prices",
			func(log *slog.Logger, err error) func(context.Context) maintenanceResult {
				return refreshModelPrices(log, func(context.Context) (string, int, error) { return "v1", 50, err })
			}},
		{"cleanup auth state", "cleaned inactive auth state", "cleanup inactive auth state",
			func(log *slog.Logger, err error) func(context.Context) maintenanceResult {
				return cleanupAuthState(log, func(context.Context) (identitystore.AuthStateCleanupResult, error) {
					return identitystore.AuthStateCleanupResult{DeletedBrowserSessions: 1}, err
				})
			}},
		{"cleanup integration states", "cleaned integration states", "cleanup integration states",
			func(log *slog.Logger, err error) func(context.Context) maintenanceResult {
				return cleanupIntegrationStates(log, func(context.Context, time.Duration, int) (int64, error) {
					return 1, err
				})
			}},
		{"cleanup completed inbox", "cleaned completed integration inbox", "cleanup completed integration inbox",
			func(log *slog.Logger, err error) func(context.Context) maintenanceResult {
				return cleanupCompletedInbox(log, func(context.Context, time.Duration, int) (int64, error) {
					return 1, err
				})
			}},
		{"cleanup deleted inbox", "cleaned deleted integration inbox", "cleanup deleted integration inbox",
			func(log *slog.Logger, err error) func(context.Context) maintenanceResult {
				return cleanupDeletedInbox(log, func(context.Context, int) (int64, error) { return 1, err })
			}},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := func(ctx context.Context, err error) (string, maintenanceResult) {
				var output bytes.Buffer
				result := test.job(slog.New(slog.NewJSONHandler(&output, nil)), err)(ctx)
				return output.String(), result
			}
			succeeded, result := run(t.Context(), nil)
			require.Contains(t, succeeded, test.success)
			require.True(t, result.worked)
			require.NoError(t, result.err)
			failed, result := run(t.Context(), failure)
			require.Contains(t, failed, test.failure)
			require.Contains(t, failed, failure.Error())
			require.ErrorIs(t, result.err, failure)
			canceled, cancel := context.WithCancel(t.Context())
			cancel()
			stopped, result := run(canceled, context.Canceled)
			require.Empty(t, stopped, "shutdown must suppress maintenance messages")
			require.NoError(t, result.err, "shutdown is not a failure")
		})
	}
}

func TestCleanupJobsRerunOnlyWhileBehind(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	full := int64(integrationInboxCleanupBatch)
	webhooks := func(count int64) bool {
		return deleteExpiredWebhookDeliveries(log, func(context.Context, int32) (int64, error) {
			return count, nil
		}, nil)(t.Context()).more
	}
	states := func(count int64) bool {
		return cleanupIntegrationStates(log, func(context.Context, time.Duration, int) (int64, error) {
			return count, nil
		})(t.Context()).more
	}
	auth := func(deleted int64) bool {
		return cleanupAuthState(log, func(context.Context) (identitystore.AuthStateCleanupResult, error) {
			return identitystore.AuthStateCleanupResult{DeletedInactiveTokens: deleted}, nil
		})(t.Context()).more
	}
	require.True(t, webhooks(int64(deliveryCleanupBatchSize)))
	require.False(t, webhooks(int64(deliveryCleanupBatchSize)-1))
	require.True(t, states(full))
	require.False(t, states(full-1))
	require.True(t, auth(500))
	require.False(t, auth(499))
	require.True(t, cleanupCompletedInbox(log, func(context.Context, time.Duration, int) (int64, error) {
		return full, nil
	})(t.Context()).more)
	deleted := func(count int64) bool {
		return cleanupDeletedInbox(log, func(context.Context, int) (int64, error) { return count, nil })(t.Context()).more
	}
	require.True(t, deleted(full))
	require.False(t, deleted(1))
}

func TestDeleteExpiredOtelExportDeliveriesLogsMetadataEvents(t *testing.T) {
	delivery := executionstore.OtelExportDelivery{
		ID: uuid.New(), OrgID: uuid.New(), ExporterID: uuid.New(), AgentID: uuid.New(), TurnID: uuid.New(),
		AttemptCount: 5, CreatedAt: time.Now().Add(-2 * time.Hour),
	}
	var output bytes.Buffer
	set := metrics.New()
	job := deleteExpiredOtelExportDeliveries(
		slog.New(slog.NewJSONHandler(&output, nil)),
		func(context.Context, int32) ([]executionstore.OtelExportDelivery, error) {
			return []executionstore.OtelExportDelivery{delivery}, nil
		},
		metrics.NewOtelExportExpiryRecorder(set),
	)
	result := job(t.Context())
	require.True(t, result.worked)
	require.NoError(t, result.err)
	scraped := httptest.NewRecorder()
	set.Handler().ServeHTTP(scraped, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Contains(t, scraped.Body.String(), "omnara_otel_export_expired_total 1")

	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
	require.GreaterOrEqual(t, len(lines), 1)
	var expiredEvent map[string]any
	for _, line := range lines {
		var entry map[string]any
		require.NoError(t, json.Unmarshal(line, &entry))
		if entry["event.name"] == "otel_export.delivery_expired" {
			expiredEvent = entry
			break
		}
	}
	require.NotNil(t, expiredEvent)
	require.Equal(t, "WARN", expiredEvent["level"])
	require.Equal(t, delivery.OrgID.String(), expiredEvent["org.id"])
	require.Equal(t, delivery.AgentID.String(), expiredEvent["agent.id"])
	require.Equal(t, delivery.TurnID.String(), expiredEvent["turn.id"])
	require.Equal(t, delivery.ID.String(), expiredEvent["otel_export.delivery_id"])
	require.Equal(t, delivery.ExporterID.String(), expiredEvent["otel_export.exporter_id"])
	require.InDelta(t, 5, expiredEvent["otel_export.attempt"], 0)
	require.Equal(t, "expired", expiredEvent["otel_export.outcome"])
	require.GreaterOrEqual(t, expiredEvent["otel_export.age_seconds"], 3600.0)
}
