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
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const MaxOtelExportersPerOrg = 100

type OtelExporterScopeType string

const (
	OtelExporterScopeProject OtelExporterScopeType = "project"
	OtelExporterScopeAgent   OtelExporterScopeType = "agent"
)

type OtelExporterScope struct {
	Type      OtelExporterScopeType
	ProjectID uuid.UUID
	AgentID   uuid.UUID
}

func (scope OtelExporterScope) Validate() error {
	valid := false
	switch scope.Type {
	case OtelExporterScopeProject:
		valid = scope.ProjectID != uuid.Nil && scope.AgentID == uuid.Nil
	case OtelExporterScopeAgent:
		valid = scope.ProjectID != uuid.Nil && scope.AgentID != uuid.Nil
	}
	if !valid {
		return storeerr.InvalidRequest(errors.New("invalid exporter scope"))
	}
	return nil
}

func otelExporterScopeFromColumns(projectID uuid.UUID, agentID *uuid.UUID) OtelExporterScope {
	scope := OtelExporterScope{
		Type: OtelExporterScopeProject, ProjectID: projectID, AgentID: storeutil.IDFromPtr(agentID),
	}
	if agentID != nil {
		scope.Type = OtelExporterScopeAgent
	}
	return scope
}

type OtelExporterRecord struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	Scope     OtelExporterScope
	Settings  otelconfig.Settings
	CreatedAt time.Time
	UpdatedAt time.Time
}

type CreateOtelExporterInput struct {
	OrgID    uuid.UUID
	Scope    OtelExporterScope
	Settings otelconfig.Settings
}

type ListOtelExportersInput struct {
	OrgID uuid.UUID
	Scope *OtelExporterScope
	Limit int
	After listing.KeysetCursor
}

type ListOtelExportersResult struct {
	Exporters []OtelExporterRecord
	HasMore   bool
}

type UpdateOtelExporterInput struct {
	OrgID                 uuid.UUID
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
		ID: row.ID, OrgID: row.OrgID, Scope: otelExporterScopeFromColumns(row.ProjectID, row.AgentID),
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

func (s *Store) readOtelExporterSecret(ctx context.Context, orgID, secretID uuid.UUID) (string, error) {
	if s.secrets == nil {
		return "", errors.New("secret store is required")
	}
	record, err := s.secrets.ReadOrgOwnedSecretPayload(ctx, secretstore.ReadOrgOwnedSecretPayloadInput{
		OrgID: orgID, SecretID: secretID, ManagementKind: management.Tenant, Kind: secrets.KindGeneric,
	})
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

func (s *Store) lockAndValidateOtelExporterSecretsTx(
	ctx context.Context, tx pgx.Tx, orgID uuid.UUID, settings otelconfig.Settings,
) error {
	referenced := otelExporterReferencedSecretIDs(settings)
	if len(referenced) == 0 {
		return nil
	}
	values := make(map[uuid.UUID]string, len(referenced))
	for _, secretID := range referenced {
		facts, err := secretops.LockReference(ctx, tx, orgID, secretID)
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.InvalidRequest(errors.New("referenced secret must be an organization-owned generic secret"))
		}
		if err != nil {
			return err
		}
		if facts.OwnerKind != secretstore.SecretOwnerOrg ||
			facts.ManagementKind != management.Tenant ||
			facts.Kind != secrets.KindGeneric {
			return storeerr.InvalidRequest(errors.New("referenced secret must be an organization-owned generic secret"))
		}
		value, err := s.readOtelExporterSecret(ctx, orgID, secretID)
		if errors.Is(err, storeerr.ErrNotFound) {
			return storeerr.InvalidRequest(errors.New("referenced secret must be an organization-owned generic secret"))
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
	if storeutil.IsUniqueViolationOnConstraint(err, "otel_exporters_active_scope_endpoint_idx") {
		return storeerr.Tag(storeerr.ErrConflict,
			errors.New("an exporter with this endpoint already exists on this resource"))
	}
	return err
}

func (s *Store) CreateOtelExporter(ctx context.Context, input CreateOtelExporterInput) (OtelExporterRecord, error) {
	if err := input.Scope.Validate(); err != nil {
		return OtelExporterRecord{}, err
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
	project, err := q.GetProjectByID(ctx, dbsqlc.GetProjectByIDParams{ID: input.Scope.ProjectID})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && project.OrgID != input.OrgID) {
		return OtelExporterRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return OtelExporterRecord{}, err
	}
	if err := lifecyclelock.EnterActiveProject(ctx, tx, input.OrgID, input.Scope.ProjectID); err != nil {
		return OtelExporterRecord{}, err
	}
	if input.Scope.Type == OtelExporterScopeAgent {
		_, err := q.GetAgentInProject(ctx, dbsqlc.GetAgentInProjectParams{
			ProjectID: input.Scope.ProjectID, ID: input.Scope.AgentID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return OtelExporterRecord{}, storeerr.ErrNotFound
		}
		if err != nil {
			return OtelExporterRecord{}, err
		}
	}
	if err := lockResourceCreation(ctx, q, "otel_exporters", input.OrgID.String()); err != nil {
		return OtelExporterRecord{}, err
	}
	if err := s.lockAndValidateOtelExporterSecretsTx(ctx, tx, input.OrgID, settings); err != nil {
		return OtelExporterRecord{}, err
	}
	existing, err := q.CountOrgOtelExporters(ctx, dbsqlc.CountOrgOtelExportersParams{OrgID: input.OrgID})
	if err != nil {
		return OtelExporterRecord{}, err
	}
	if existing >= MaxOtelExportersPerOrg {
		return OtelExporterRecord{}, resourceguard.LimitExceeded(
			"OpenTelemetry exporters per organization", MaxOtelExportersPerOrg)
	}
	authOptions, headers, secretHeaders, err := encodeOtelExporterJSON(settings)
	if err != nil {
		return OtelExporterRecord{}, err
	}
	row, err := q.InsertOtelExporter(ctx, dbsqlc.InsertOtelExporterParams{
		OrgID: input.OrgID, ProjectID: input.Scope.ProjectID,
		AgentID: storeutil.IDFromNil(input.Scope.AgentID), Endpoint: settings.Endpoint, Variant: string(settings.Variant),
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
	if input.OrgID == uuid.Nil || input.Limit < 1 || input.Limit > 100 {
		return ListOtelExportersResult{}, storeerr.InvalidRequest(
			errors.New("organization and limit between 1 and 100 are required"))
	}
	params := dbsqlc.ListOtelExportersParams{OrgID: input.OrgID, RowLimit: int32(input.Limit) + 1}
	if scope := input.Scope; scope != nil {
		if err := scope.Validate(); err != nil {
			return ListOtelExportersResult{}, err
		}
		params.FilterScope = true
		params.ProjectID, params.AgentID = storeutil.IDFromNil(scope.ProjectID), storeutil.IDFromNil(scope.AgentID)
	}
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

func (s *Store) GetOtelExporter(ctx context.Context, orgID, id uuid.UUID) (OtelExporterRecord, error) {
	row, err := s.q.GetOtelExporter(ctx, dbsqlc.GetOtelExporterParams{OrgID: orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return OtelExporterRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return OtelExporterRecord{}, err
	}
	return otelExporterRecordFromSQLC(row)
}

func (s *Store) UpdateOtelExporter(ctx context.Context, input UpdateOtelExporterInput) (OtelExporterRecord, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OtelExporterRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lifecyclelock.EnterActiveOrganization(ctx, tx, input.OrgID); err != nil {
		return OtelExporterRecord{}, err
	}
	q := dbsqlc.New(tx)
	if err := lockResourceCreation(ctx, q, "otel_exporters", input.OrgID.String()); err != nil {
		return OtelExporterRecord{}, err
	}
	row, err := q.GetOtelExporter(ctx, dbsqlc.GetOtelExporterParams{OrgID: input.OrgID, ID: input.ExporterID})
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
	if err := s.lockAndValidateOtelExporterSecretsTx(ctx, tx, input.OrgID, settings); err != nil {
		return OtelExporterRecord{}, err
	}
	authOptions, headers, secretHeaders, err := encodeOtelExporterJSON(settings)
	if err != nil {
		return OtelExporterRecord{}, err
	}
	row, err = q.UpdateOtelExporter(ctx, dbsqlc.UpdateOtelExporterParams{
		OrgID: input.OrgID, ID: input.ExporterID, Endpoint: settings.Endpoint, Variant: string(settings.Variant),
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
func (s *Store) DeleteOtelExporter(ctx context.Context, orgID, id uuid.UUID) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	count, err := q.DeleteOtelExporter(ctx, dbsqlc.DeleteOtelExporterParams{OrgID: orgID, ID: id})
	if err != nil {
		return err
	}
	if count == 0 {
		return storeerr.ErrNotFound
	}
	if err := q.DeleteOtelExporterDeliveries(ctx, dbsqlc.DeleteOtelExporterDeliveriesParams{
		OrgID: orgID, ExporterID: id,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
