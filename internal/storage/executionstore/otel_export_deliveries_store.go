package executionstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	otelconfig "github.com/omnara-ai/omnara/internal/otelexport/config"
	"github.com/omnara-ai/omnara/internal/oteltrace"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	// otelExportDeliveryWindow is how long a delivery is retried before the
	// maintenance job drops it.
	otelExportDeliveryWindow = time.Hour
	// otelExportClaimDuration must exceed the sender's whole attempt: loading
	// every trace in the batch plus one HTTP request.
	otelExportClaimDuration = 2 * time.Minute
)

// OtelExportDelivery is one turn's trace awaiting export to one exporter.
type OtelExportDelivery struct {
	ID           uuid.UUID
	AgentID      uuid.UUID
	TurnID       uuid.UUID
	AttemptCount int32
	CreatedAt    time.Time
}

// OtelExportBatch is a set of deliveries for one exporter claimed together.
type OtelExportBatch struct {
	ClaimToken uuid.UUID
	OrgID      uuid.UUID
	ExporterID uuid.UUID
	Deliveries []OtelExportDelivery
}

// OtelExportDestination is an exporter resolved for sending: Headers holds the
// plain headers and the decrypted secret headers.
type OtelExportDestination struct {
	Endpoint string
	Variant  oteltrace.Variant
	Headers  map[string]string
}

func (s *Store) ClaimOtelExportDeliveries(ctx context.Context, perOrgLimit, batchLimit int) (OtelExportBatch, error) {
	batch := OtelExportBatch{ClaimToken: uuid.New()}
	rows, err := s.q.ClaimOtelExportDeliveries(ctx, dbsqlc.ClaimOtelExportDeliveriesParams{
		ClaimToken: batch.ClaimToken, ClaimSeconds: otelExportClaimDuration.Seconds(),
		PerOrgLimit: int64(perOrgLimit), DeliveryWindowSeconds: otelExportDeliveryWindow.Seconds(),
		BatchLimit: int32(batchLimit),
	})
	if err != nil {
		return OtelExportBatch{}, fmt.Errorf("claim otel export deliveries: %w", err)
	}
	if len(rows) == 0 {
		return OtelExportBatch{}, storeerr.ErrNotFound
	}
	batch.OrgID, batch.ExporterID = rows[0].OrgID, rows[0].ExporterID
	batch.Deliveries = make([]OtelExportDelivery, 0, len(rows))
	for _, row := range rows {
		batch.Deliveries = append(batch.Deliveries, OtelExportDelivery{
			ID: row.ID, AgentID: row.AgentID, TurnID: row.TurnID,
			AttemptCount: row.AttemptCount, CreatedAt: row.CreatedAt,
		})
	}
	return batch, nil
}

func (s *Store) CompleteOtelExportDeliveries(ctx context.Context, claimToken uuid.UUID) error {
	_, err := s.q.CompleteOtelExportDeliveries(ctx, dbsqlc.CompleteOtelExportDeliveriesParams{ClaimToken: claimToken})
	return err
}

func (s *Store) RetryOtelExportDeliveries(ctx context.Context, claimToken uuid.UUID, delay time.Duration) error {
	_, err := s.q.RetryOtelExportDeliveries(ctx, dbsqlc.RetryOtelExportDeliveriesParams{
		ClaimToken: claimToken, DelaySeconds: delay.Seconds(),
	})
	return err
}

func (s *Store) DeleteExpiredOtelExportDeliveries(ctx context.Context, limit int32) (int64, error) {
	return s.q.DeleteExpiredOtelExportDeliveries(ctx, dbsqlc.DeleteExpiredOtelExportDeliveriesParams{
		LimitCount: limit, DeliveryWindowSeconds: otelExportDeliveryWindow.Seconds(),
	})
}

// GetOtelExportDestination resolves an exporter for sending. It returns
// storeerr.ErrNotFound when the exporter was deleted, and an error wrapping
// ErrOtelExporterMisconfigured when a secret header can no longer be read.
func (s *Store) GetOtelExportDestination(
	ctx context.Context, orgID, exporterID uuid.UUID,
) (OtelExportDestination, error) {
	record, err := s.GetOtelExporter(ctx, orgID, exporterID)
	if err != nil {
		return OtelExportDestination{}, err
	}
	destination := OtelExportDestination{
		Endpoint: record.Settings.Endpoint, Variant: record.Settings.Variant,
		Headers: make(map[string]string, len(record.Settings.Headers)+len(record.Settings.SecretHeaders)),
	}
	for name, value := range record.Settings.Headers {
		destination.Headers[name] = value
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

// LoadTurnTraceForExport loads a queued delivery's turn. The delivery row,
// not a caller, vouches for the agent and turn, so no project is required.
func (s *Store) LoadTurnTraceForExport(ctx context.Context, agentID, turnID uuid.UUID) (oteltrace.Turn, error) {
	return s.loadTurnTrace(ctx, nil, agentID, turnID)
}
