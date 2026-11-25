package executionstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	otelconfig "github.com/omnara-ai/omnara/internal/otelexport/config"
	"github.com/omnara-ai/omnara/internal/oteltrace"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/resourceguard"
	"github.com/omnara-ai/omnara/internal/storage/internal/secretops"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const MaxOtelExportersPerProject = 100

type OtelExporterRecord struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	ProjectID uuid.UUID
	Settings  otelconfig.Settings
	CreatedAt time.Time
	UpdatedAt time.Time
}

type CreateOtelExporterInput struct {
	ProjectID uuid.UUID
	Settings  otelconfig.Settings
}

type ListOtelExportersInput struct {
	// ProjectIDs is one project, or every project the caller can read for the
	// organization-wide list.
	ProjectIDs []uuid.UUID
	Limit      int
	After      listing.KeysetCursor
}

type ListOtelExportersResult struct {
	Exporters []OtelExporterRecord
	HasMore   bool
}

type UpdateOtelExporterInput struct {
	ProjectID             uuid.UUID
	ExporterID            uuid.UUID
	Endpoint              *string
	Variant               *oteltrace.Variant
	Encoding              *oteltrace.Encoding
	AuthKind              *otelconfig.AuthKind
	AuthOptions           *otelconfig.AuthOptions
	SetCredentialSecretID bool
	CredentialSecretID    uuid.UUID
	Headers               map[string]string
	SecretHeaders         map[string]uuid.UUID
	SamplePercent         *float64
}

func otelExporterRecordFromSQLC(row dbsqlc.OtelExporter) (OtelExporterRecord, error) {
	record := OtelExporterRecord{
		ID: row.ID, OrgID: row.OrgID, ProjectID: row.ProjectID,
		Settings: otelconfig.Settings{
			Endpoint: row.Endpoint, Variant: oteltrace.Variant(row.Variant),
			Encoding: oteltrace.Encoding(row.Encoding), AuthKind: otelconfig.AuthKind(row.AuthKind),
			CredentialSecretID: storeutil.IDFromPtr(row.CredentialSecretID),
			SamplePercent:      row.SamplePercent,
		},
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if len(row.AuthOptions) > 0 {
		if err := json.Unmarshal(row.AuthOptions, &record.Settings.AuthOptions); err != nil {
			return OtelExporterRecord{}, fmt.Errorf("decode exporter auth options: %w", err)
		}
	}
	if err := json.Unmarshal(row.Headers, &record.Settings.Headers); err != nil {
		return OtelExporterRecord{}, fmt.Errorf("decode exporter headers: %w", err)
	}
	if err := json.Unmarshal(row.SecretHeaders, &record.Settings.SecretHeaders); err != nil {
		return OtelExporterRecord{}, fmt.Errorf("decode exporter secret headers: %w", err)
	}
	return record, nil
}

// readOtelExporterSecret reads a generic secret available to the exporter's
// project. It reads through tx when the caller holds one, so an exporter
// mutation never waits on the pool for a second connection.
func (s *Store) readOtelExporterSecret(
	ctx context.Context, tx pgx.Tx, orgID, projectID, secretID uuid.UUID,
) (string, error) {
	if s.secrets == nil {
		return "", errors.New("secret store is required")
	}
	input := secretstore.ReadProjectAvailableSecretPayloadInput{
		OrgID: orgID, ProjectID: projectID, SecretID: secretID, Kind: secrets.KindGeneric,
	}
	var record secretstore.SecretPayloadRecord
	var err error
	if tx != nil {
		record, err = s.secrets.ReadProjectAvailableSecretPayloadTx(ctx, tx, input)
	} else {
		record, err = s.secrets.ReadProjectAvailableSecretPayload(ctx, input)
	}
	if err != nil {
		return "", err
	}
	return record.Payload[secrets.KeyValue], nil
}

func otelExporterReferencedSecretIDs(settings otelconfig.Settings) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(settings.SecretHeaders)+1)
	if settings.CredentialSecretID != uuid.Nil {
		ids = append(ids, settings.CredentialSecretID)
	}
	for _, secretID := range settings.SecretHeaders {
		ids = append(ids, secretID)
	}
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return slices.Compact(ids)
}

var errOtelExporterSecretUnavailable = storeerr.InvalidRequest(
	errors.New("referenced secret must be a generic secret available to the project"))

// lockAndValidateOtelExporterSecretsTx checks the secrets a mutation sets. An
// update that leaves secret_headers alone passes checkSecretHeaders false: a
// header secret can be deleted from under the exporter, and export then leaves
// that header out, so a stored reference to one must not fail unrelated edits.
func (s *Store) lockAndValidateOtelExporterSecretsTx(
	ctx context.Context, tx pgx.Tx, orgID, projectID uuid.UUID, settings otelconfig.Settings,
	checkSecretHeaders bool,
) error {
	if !checkSecretHeaders {
		settings.SecretHeaders = nil
	}
	referenced := otelExporterReferencedSecretIDs(settings)
	if len(referenced) == 0 {
		return nil
	}
	values := make(map[uuid.UUID]string, len(referenced))
	for _, secretID := range referenced {
		// The lock keeps a concurrent secret deletion from missing this reference.
		if _, err := secretops.LockReference(ctx, tx, orgID, secretID); errors.Is(err, pgx.ErrNoRows) {
			return errOtelExporterSecretUnavailable
		} else if err != nil {
			return err
		}
		value, err := s.readOtelExporterSecret(ctx, tx, orgID, projectID, secretID)
		if errors.Is(err, storeerr.ErrNotFound) || errors.Is(err, storeerr.ErrInvalidSecretRequest) {
			return errOtelExporterSecretUnavailable
		}
		if err != nil {
			return err
		}
		values[secretID] = value
	}
	if settings.CredentialSecretID != uuid.Nil {
		if _, _, err := otelconfig.FormatAuthHeader(
			settings.AuthKind, settings.AuthOptions, values[settings.CredentialSecretID],
		); err != nil {
			return storeerr.InvalidRequest(err)
		}
	}
	for name, secretID := range settings.SecretHeaders {
		if err := otelconfig.ValidateHeaderValue(name, values[secretID]); err != nil {
			return storeerr.InvalidRequest(err)
		}
	}
	return nil
}

func encodeOtelExporterJSON(
	settings otelconfig.Settings,
) (authOptions, headers, secretHeaders json.RawMessage, err error) {
	if authOptions, err = json.Marshal(settings.AuthOptions); err != nil {
		return nil, nil, nil, err
	}
	if headers, err = json.Marshal(settings.Headers); err != nil {
		return nil, nil, nil, err
	}
	secretHeaders, err = json.Marshal(settings.SecretHeaders)
	return authOptions, headers, secretHeaders, err
}

func otelExporterConflict(err error) error {
	if storeutil.IsUniqueViolationOnConstraint(err, "otel_exporters_active_project_endpoint_idx") {
		return storeerr.Tag(storeerr.ErrConflict,
			errors.New("an exporter with this endpoint already exists on this resource"))
	}
	return err
}

func (s *Store) CreateOtelExporter(ctx context.Context, input CreateOtelExporterInput) (OtelExporterRecord, error) {
	if input.ProjectID == uuid.Nil {
		return OtelExporterRecord{}, storeerr.InvalidRequest(errors.New("project is required"))
	}
	settings, err := otelconfig.Validate(input.Settings, s.endpointAllowlist)
	if err != nil {
		return OtelExporterRecord{}, storeerr.InvalidRequest(err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OtelExporterRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	project, err := loadProjectTx(ctx, q, input.ProjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return OtelExporterRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return OtelExporterRecord{}, err
	}
	if err := lifecyclelock.EnterActiveProject(ctx, tx, project.OrgID, input.ProjectID); err != nil {
		return OtelExporterRecord{}, err
	}
	if err := lockResourceCreation(ctx, q, "otel_exporters", input.ProjectID.String()); err != nil {
		return OtelExporterRecord{}, err
	}
	if err := s.lockAndValidateOtelExporterSecretsTx(ctx, tx, project.OrgID, input.ProjectID, settings, true); err != nil {
		return OtelExporterRecord{}, err
	}
	existing, err := q.CountProjectOtelExporters(ctx, dbsqlc.CountProjectOtelExportersParams{ProjectID: input.ProjectID})
	if err != nil {
		return OtelExporterRecord{}, err
	}
	if existing >= MaxOtelExportersPerProject {
		return OtelExporterRecord{}, resourceguard.LimitExceeded(
			"OpenTelemetry exporters per project", MaxOtelExportersPerProject)
	}
	authOptions, headers, secretHeaders, err := encodeOtelExporterJSON(settings)
	if err != nil {
		return OtelExporterRecord{}, err
	}
	row, err := q.InsertOtelExporter(ctx, dbsqlc.InsertOtelExporterParams{
		OrgID: project.OrgID, ProjectID: input.ProjectID,
		Endpoint: settings.Endpoint, Variant: string(settings.Variant),
		Encoding: string(settings.Encoding), AuthKind: string(settings.AuthKind), AuthOptions: authOptions,
		CredentialSecretID: storeutil.IDFromNil(settings.CredentialSecretID),
		Headers:            headers, SecretHeaders: secretHeaders, SamplePercent: settings.SamplePercent,
	})
	if err != nil {
		return OtelExporterRecord{}, otelExporterConflict(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return OtelExporterRecord{}, err
	}
	return otelExporterRecordFromSQLC(row)
}

func (s *Store) ListOtelExporters(ctx context.Context, input ListOtelExportersInput) (ListOtelExportersResult, error) {
	if input.Limit < 1 || input.Limit > 100 {
		return ListOtelExportersResult{}, storeerr.InvalidRequest(errors.New("limit between 1 and 100 is required"))
	}
	if len(input.ProjectIDs) == 0 {
		return ListOtelExportersResult{Exporters: []OtelExporterRecord{}}, nil
	}
	params := dbsqlc.ListOtelExportersParams{ProjectIds: input.ProjectIDs, RowLimit: int32(input.Limit) + 1}
	if input.After.Set {
		params.CursorCreatedAt = &input.After.CreatedAt
		params.CursorID = &input.After.ID
	}
	rows, err := s.q.ListOtelExporters(ctx, params)
	if err != nil {
		return ListOtelExportersResult{}, err
	}
	result := ListOtelExportersResult{HasMore: len(rows) > input.Limit}
	if result.HasMore {
		rows = rows[:input.Limit]
	}
	result.Exporters = make([]OtelExporterRecord, 0, len(rows))
	for _, row := range rows {
		record, err := otelExporterRecordFromSQLC(row)
		if err != nil {
			return ListOtelExportersResult{}, err
		}
		result.Exporters = append(result.Exporters, record)
	}
	return result, nil
}

func (s *Store) GetOtelExporter(ctx context.Context, projectID, id uuid.UUID) (OtelExporterRecord, error) {
	row, err := s.q.GetOtelExporter(ctx, dbsqlc.GetOtelExporterParams{ProjectID: projectID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return OtelExporterRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return OtelExporterRecord{}, err
	}
	return otelExporterRecordFromSQLC(row)
}

func (s *Store) UpdateOtelExporter(ctx context.Context, input UpdateOtelExporterInput) (OtelExporterRecord, error) {
	record, err := s.GetOtelExporter(ctx, input.ProjectID, input.ExporterID)
	if err != nil {
		return OtelExporterRecord{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OtelExporterRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lifecyclelock.EnterActiveProject(ctx, tx, record.OrgID, input.ProjectID); err != nil {
		return OtelExporterRecord{}, err
	}
	q := dbsqlc.New(tx)
	// Share the project's creation lock so duplicate checks and partial updates
	// observe the latest committed exporter.
	if err := lockResourceCreation(ctx, q, "otel_exporters", input.ProjectID.String()); err != nil {
		return OtelExporterRecord{}, err
	}
	row, err := q.GetOtelExporter(ctx, dbsqlc.GetOtelExporterParams{ProjectID: input.ProjectID, ID: input.ExporterID})
	if errors.Is(err, pgx.ErrNoRows) {
		return OtelExporterRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return OtelExporterRecord{}, err
	}
	current, err := otelExporterRecordFromSQLC(row)
	if err != nil {
		return OtelExporterRecord{}, err
	}
	settings := current.Settings
	if input.Endpoint != nil {
		settings.Endpoint = *input.Endpoint
	}
	if input.Variant != nil {
		settings.Variant = *input.Variant
	}
	if input.Encoding != nil {
		settings.Encoding = *input.Encoding
	}
	if input.AuthKind != nil {
		settings.AuthKind = *input.AuthKind
	}
	if input.AuthOptions != nil {
		settings.AuthOptions = *input.AuthOptions
	} else if input.AuthKind != nil && settings.AuthKind != current.Settings.AuthKind {
		settings.AuthOptions = otelconfig.AuthOptions{}
	}
	if input.SetCredentialSecretID {
		settings.CredentialSecretID = input.CredentialSecretID
	} else if input.AuthKind != nil && settings.AuthKind == otelconfig.AuthKindNone {
		settings.CredentialSecretID = uuid.Nil
	}
	if input.Headers != nil {
		settings.Headers = input.Headers
	}
	if input.SecretHeaders != nil {
		settings.SecretHeaders = input.SecretHeaders
	}
	if input.SamplePercent != nil {
		settings.SamplePercent = *input.SamplePercent
	}
	if settings, err = otelconfig.Validate(settings, s.endpointAllowlist); err != nil {
		return OtelExporterRecord{}, storeerr.InvalidRequest(err)
	}
	if err := s.lockAndValidateOtelExporterSecretsTx(
		ctx, tx, record.OrgID, input.ProjectID, settings, input.SecretHeaders != nil,
	); err != nil {
		return OtelExporterRecord{}, err
	}
	authOptions, headers, secretHeaders, err := encodeOtelExporterJSON(settings)
	if err != nil {
		return OtelExporterRecord{}, err
	}
	row, err = q.UpdateOtelExporter(ctx, dbsqlc.UpdateOtelExporterParams{
		ProjectID: input.ProjectID, ID: input.ExporterID, Endpoint: settings.Endpoint, Variant: string(settings.Variant),
		Encoding: string(settings.Encoding), AuthKind: string(settings.AuthKind), AuthOptions: authOptions,
		CredentialSecretID: storeutil.IDFromNil(settings.CredentialSecretID),
		Headers:            headers, SecretHeaders: secretHeaders, SamplePercent: settings.SamplePercent,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return OtelExporterRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return OtelExporterRecord{}, otelExporterConflict(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return OtelExporterRecord{}, err
	}
	return otelExporterRecordFromSQLC(row)
}

// DeleteOtelExporter deletes the exporter and drops the turns queued for it.
func (s *Store) DeleteOtelExporter(ctx context.Context, projectID, id uuid.UUID) error {
	record, err := s.GetOtelExporter(ctx, projectID, id)
	if err != nil {
		return err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	count, err := q.DeleteOtelExporter(ctx, dbsqlc.DeleteOtelExporterParams{ProjectID: projectID, ID: id})
	if err != nil {
		return err
	}
	if count == 0 {
		return storeerr.ErrNotFound
	}
	if err := q.DeleteOtelExporterDeliveries(ctx, dbsqlc.DeleteOtelExporterDeliveriesParams{
		OrgID: record.OrgID, ExporterID: id,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
