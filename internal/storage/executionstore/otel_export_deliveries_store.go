package executionstore

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	otelconfig "github.com/omnara-ai/omnara/internal/otelexport/config"
	"github.com/omnara-ai/omnara/internal/oteltrace"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	otelExportDeliveryWindow = time.Hour
	otelExportClaimDuration  = time.Minute
)

// OtelExportDelivery is one turn queued for one exporter.
type OtelExportDelivery struct {
	ID           uuid.UUID
	OrgID        uuid.UUID
	ExporterID   uuid.UUID
	AgentID      uuid.UUID
	TurnID       uuid.UUID
	AttemptCount int32
	ClaimToken   uuid.UUID
	CreatedAt    time.Time
}

type OtelExportDeliveryRetryResult struct {
	NextAttemptAt time.Time
	GaveUp        bool
}

type OtelExportDestination struct {
	Endpoint string
	Variant  oteltrace.Variant
	Encoding oteltrace.Encoding
	Headers  map[string]string
}

func (s *Store) ClaimOtelExportDelivery(ctx context.Context, perOrgLimit int) (OtelExportDelivery, error) {
	row, err := s.q.ClaimOtelExportDelivery(ctx, dbsqlc.ClaimOtelExportDeliveryParams{
		PerOrgLimit: int64(perOrgLimit), DeliveryWindowSeconds: otelExportDeliveryWindow.Seconds(),
		ClaimSeconds: otelExportClaimDuration.Seconds(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return OtelExportDelivery{}, storeerr.ErrNotFound
	}
	if err != nil {
		return OtelExportDelivery{}, fmt.Errorf("claim otel export delivery: %w", err)
	}
	return OtelExportDelivery{
		ID: row.ID, OrgID: row.OrgID, ExporterID: row.ExporterID, AgentID: row.AgentID, TurnID: row.TurnID,
		AttemptCount: row.AttemptCount, ClaimToken: *row.ClaimToken, CreatedAt: row.CreatedAt,
	}, nil
}

func (s *Store) CompleteOtelExportDelivery(ctx context.Context, id, claimToken uuid.UUID) error {
	return s.q.CompleteOtelExportDelivery(ctx, dbsqlc.CompleteOtelExportDeliveryParams{
		ID: id, ClaimToken: &claimToken,
	})
}

func (s *Store) RetryOtelExportDelivery(
	ctx context.Context, id, claimToken uuid.UUID, delay time.Duration,
) (OtelExportDeliveryRetryResult, error) {
	row, err := s.q.RetryOtelExportDelivery(ctx, dbsqlc.RetryOtelExportDeliveryParams{
		ID: id, ClaimToken: &claimToken, DelaySeconds: delay.Seconds(),
		DeliveryWindowSeconds: otelExportDeliveryWindow.Seconds(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return OtelExportDeliveryRetryResult{}, storeerr.ErrNotFound
	}
	if err != nil {
		return OtelExportDeliveryRetryResult{}, err
	}
	return OtelExportDeliveryRetryResult{NextAttemptAt: row.NextAttemptAt, GaveUp: row.GaveUp}, nil
}

func (s *Store) DeleteExpiredOtelExportDeliveries(ctx context.Context, limit int32) ([]OtelExportDelivery, error) {
	rows, err := s.q.DeleteExpiredOtelExportDeliveries(ctx, dbsqlc.DeleteExpiredOtelExportDeliveriesParams{
		LimitCount: limit, DeliveryWindowSeconds: otelExportDeliveryWindow.Seconds(),
	})
	if err != nil {
		return nil, err
	}
	expired := make([]OtelExportDelivery, 0, len(rows))
	for _, row := range rows {
		expired = append(expired, OtelExportDelivery{
			ID: row.ID, OrgID: row.OrgID, ExporterID: row.ExporterID,
			AgentID: row.AgentID, TurnID: row.TurnID,
			AttemptCount: row.AttemptCount, CreatedAt: row.CreatedAt,
		})
	}
	return expired, nil
}

func (s *Store) GetOtelExportDestination(
	ctx context.Context, orgID, exporterID uuid.UUID,
) (OtelExportDestination, error) {
	record, err := s.GetOtelExporter(ctx, orgID, exporterID)
	if err != nil {
		return OtelExportDestination{}, err
	}
	headerCap := len(record.Settings.Headers) + len(record.Settings.SecretHeaders)
	if record.Settings.CredentialSecretID != uuid.Nil {
		headerCap++
	}
	destination := OtelExportDestination{
		Endpoint: record.Settings.Endpoint, Variant: record.Settings.Variant, Encoding: record.Settings.Encoding,
		Headers: make(map[string]string, headerCap),
	}
	maps.Copy(destination.Headers, record.Settings.Headers)
	if record.Settings.AuthKind != otelconfig.AuthKindNone && record.Settings.AuthKind != "" {
		value, err := s.readOtelExporterSecret(ctx, orgID, record.Settings.CredentialSecretID)
		if errors.Is(err, storeerr.ErrNotFound) {
			return OtelExportDestination{}, fmt.Errorf(
				"%w: credential secret is unavailable", ErrOtelExporterMisconfigured)
		}
		if err != nil {
			return OtelExportDestination{}, fmt.Errorf("read exporter credential secret: %w", err)
		}
		headerName, headerValue, err := otelconfig.FormatAuthHeader(
			record.Settings.AuthKind, record.Settings.AuthOptions, value,
		)
		if err != nil {
			return OtelExportDestination{}, fmt.Errorf("%w: %w", ErrOtelExporterMisconfigured, err)
		}
		if headerName != "" {
			destination.Headers[headerName] = headerValue
		}
	}
	for name, secretID := range record.Settings.SecretHeaders {
		value, err := s.readOtelExporterSecret(ctx, orgID, secretID)
		if errors.Is(err, storeerr.ErrNotFound) {
			return OtelExportDestination{}, fmt.Errorf(
				"%w: secret header %s is unavailable", ErrOtelExporterMisconfigured, name)
		}
		if err != nil {
			return OtelExportDestination{}, fmt.Errorf("read exporter secret header: %w", err)
		}
		if err := otelconfig.ValidateHeaderValue(name, value); err != nil {
			return OtelExportDestination{}, fmt.Errorf("%w: %w", ErrOtelExporterMisconfigured, err)
		}
		destination.Headers[name] = value
	}
	return destination, nil
}

var ErrOtelExporterMisconfigured = errors.New("otel exporter is misconfigured")

func (s *Store) LoadTurnTraceForExport(ctx context.Context, agentID, turnID uuid.UUID) (oteltrace.Turn, error) {
	return s.loadTurnTrace(ctx, nil, agentID, turnID)
}
