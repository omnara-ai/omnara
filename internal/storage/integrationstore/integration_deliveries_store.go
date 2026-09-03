package integrationstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) CreateIntegrationDelivery(
	ctx context.Context,
	input CreateIntegrationDeliveryInput,
) (IntegrationDeliveryRecord, error) {
	var err error
	input, err = normalizeCreateIntegrationDeliveryInput(input)
	if err != nil {
		return IntegrationDeliveryRecord{}, err
	}
	params := dbsqlc.InsertIntegrationDeliveryParams{
		ProjectID:                  input.ProjectID,
		AgentID:                    input.AgentID,
		IntegrationTargetBindingID: input.IntegrationTargetBindingID,
		Transport:                  string(input.Transport),
		DeliveryKind:               input.DeliveryKind,
		PayloadVersion:             input.PayloadVersion,
		Payload:                    input.Payload,
		IdempotencyScope:           input.IdempotencyScope,
		IdempotencyKey:             input.IdempotencyKey,
		NotifyRef:                  sqlcIDFromNil(input.NotifyRef),
	}
	row, err := s.q.InsertIntegrationDelivery(ctx, params)
	if err == nil {
		record := integrationDeliveryRecordFromSQLC(row)
		record.Created = true
		return record, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return IntegrationDeliveryRecord{}, integrationChannelWriteError(
			"create integration delivery",
			err,
		)
	}

	existing, getErr := s.q.GetIntegrationDeliveryByIdempotency(
		ctx,
		dbsqlc.GetIntegrationDeliveryByIdempotencyParams{
			ProjectID: input.ProjectID, AgentID: input.AgentID,
			IdempotencyScope: input.IdempotencyScope, IdempotencyKey: input.IdempotencyKey,
		},
	)
	if errors.Is(getErr, pgx.ErrNoRows) {
		return IntegrationDeliveryRecord{}, storeerr.ErrUnauthorized
	}
	if getErr != nil {
		return IntegrationDeliveryRecord{}, fmt.Errorf("load idempotent integration delivery: %w", getErr)
	}
	if existing.IntegrationTargetBindingID != input.IntegrationTargetBindingID ||
		existing.Transport != string(input.Transport) ||
		existing.DeliveryKind != input.DeliveryKind ||
		existing.PayloadVersion != input.PayloadVersion ||
		idFromSQLCPtr(existing.NotifyRef) != input.NotifyRef ||
		!jsoncanonical.Equal(existing.Payload, input.Payload) {
		return IntegrationDeliveryRecord{}, storeerr.ErrConflict
	}
	return integrationDeliveryRecordFromSQLC(existing), nil
}

func (s *Store) GetIntegrationDelivery(
	ctx context.Context,
	projectID, id ID,
) (IntegrationDeliveryRecord, error) {
	if isNilID(projectID) || isNilID(id) {
		return IntegrationDeliveryRecord{}, errors.New("project and integration delivery are required")
	}
	row, err := s.q.GetIntegrationDelivery(
		ctx,
		dbsqlc.GetIntegrationDeliveryParams{ProjectID: projectID, ID: id},
	)
	if err != nil {
		return IntegrationDeliveryRecord{}, integrationChannelReadError("get integration delivery", err)
	}
	return integrationDeliveryRecordFromSQLC(row), nil
}

func (s *Store) ClaimIntegrationDeliveries(
	ctx context.Context,
	input ClaimIntegrationDeliveriesInput,
) ([]IntegrationDeliveryRecord, error) {
	var err error
	input.ClaimedBy = strings.TrimSpace(input.ClaimedBy)
	if input.ClaimedBy == "" {
		return nil, errors.New("delivery claimant is required")
	}
	if len(input.ClaimedBy) > 256 {
		return nil, errors.New("delivery claimant exceeds its size limit")
	}
	if err := validateLeaseAndLimit(input.LeaseDuration, input.Limit); err != nil {
		return nil, err
	}
	capability, err := normalizedClaimCapability(input.Capability)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ClaimIntegrationDeliveries(ctx, dbsqlc.ClaimIntegrationDeliveriesParams{
		ClaimedBy: input.ClaimedBy, LeaseMicroseconds: input.LeaseDuration.Microseconds(),
		ConnectorKey: capability.ConnectorKey, Provider: capability.Provider,
		RowLimit: int32(input.Limit),
	})
	if err != nil {
		return nil, fmt.Errorf("claim integration deliveries: %w", err)
	}
	out := make([]IntegrationDeliveryRecord, 0, len(rows))
	for _, row := range rows {
		record := integrationDeliveryRecordFromClaimSQLC(row)
		record.AppConfigurationRevision = row.AppConfigurationRevision
		record.InstallConfigurationRevision = row.InstallConfigurationRevision
		out = append(out, record)
	}
	return out, nil
}

func (s *Store) CompleteIntegrationDelivery(
	ctx context.Context,
	input CompleteIntegrationDeliveryInput,
) (IntegrationDeliveryRecord, error) {
	var err error
	input, err = normalizeCompleteIntegrationDeliveryInput(input)
	if err != nil {
		return IntegrationDeliveryRecord{}, err
	}
	connectorKeys, providers, err := normalizedCapabilityColumns(input.Capabilities)
	if err != nil {
		return IntegrationDeliveryRecord{}, err
	}
	row, err := s.q.CompleteIntegrationDelivery(ctx, dbsqlc.CompleteIntegrationDeliveryParams{
		ID: input.ID, ClaimToken: input.ClaimToken, ClaimGeneration: input.ClaimGeneration,
		State: string(input.State), RetryMicroseconds: input.RetryAfter.Microseconds(),
		ProviderMessageRef: input.ProviderMessageRef, LastError: input.LastError,
		ConnectorKeys: connectorKeys, Providers: providers,
		MaxAttempts: MaxIntegrationDeliveryClaims,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationDeliveryRecord{}, storeerr.ErrStateTransitionConflict
	}
	if err != nil {
		return IntegrationDeliveryRecord{}, integrationChannelWriteError(
			"complete integration delivery",
			err,
		)
	}
	return integrationDeliveryRecordFromSQLC(row), nil
}

func (s *Store) ExpireIntegrationDeliveryClaims(
	ctx context.Context,
	limit int,
) ([]IntegrationDeliveryUpdate, error) {
	if err := validateRowLimit(limit); err != nil {
		return nil, err
	}
	rows, err := s.q.ExpireIntegrationDeliveryClaims(
		ctx,
		dbsqlc.ExpireIntegrationDeliveryClaimsParams{RowLimit: int32(limit)},
	)
	if err != nil {
		return nil, fmt.Errorf("expire integration delivery claims: %w", err)
	}
	out := make([]IntegrationDeliveryUpdate, 0, len(rows))
	for _, row := range rows {
		out = append(out, IntegrationDeliveryUpdate{
			ID: row.ID, ProjectID: row.ProjectID, NotifyRef: idFromSQLCPtr(row.NotifyRef),
		})
	}
	return out, nil
}

func (s *Store) CancelUnavailableIntegrationDeliveries(
	ctx context.Context,
	limit int,
) ([]IntegrationDeliveryUpdate, error) {
	if err := validateRowLimit(limit); err != nil {
		return nil, err
	}
	rows, err := s.q.CancelUnavailableIntegrationDeliveries(
		ctx,
		dbsqlc.CancelUnavailableIntegrationDeliveriesParams{RowLimit: int32(limit)},
	)
	if err != nil {
		return nil, fmt.Errorf("cancel unavailable integration deliveries: %w", err)
	}
	out := make([]IntegrationDeliveryUpdate, 0, len(rows))
	for _, row := range rows {
		out = append(out, IntegrationDeliveryUpdate{
			ID: row.ID, ProjectID: row.ProjectID, NotifyRef: idFromSQLCPtr(row.NotifyRef),
		})
	}
	return out, nil
}

func (s *Store) DeleteRetainedIntegrationDeliveries(
	ctx context.Context,
	input DeleteRetainedIntegrationDeliveriesInput,
) (int64, error) {
	retentionMicroseconds := input.Retention.Microseconds()
	if retentionMicroseconds <= 0 {
		return 0, errors.New("positive delivery retention is required")
	}
	if err := validateRowLimit(input.Limit); err != nil {
		return 0, err
	}
	rows, err := s.q.DeleteRetainedIntegrationDeliveries(
		ctx,
		dbsqlc.DeleteRetainedIntegrationDeliveriesParams{
			RetentionMicroseconds: retentionMicroseconds, RowLimit: int32(input.Limit),
		},
	)
	if err != nil {
		return 0, fmt.Errorf("delete retained integration deliveries: %w", err)
	}
	return rows, nil
}

func normalizeCreateIntegrationDeliveryInput(
	input CreateIntegrationDeliveryInput,
) (CreateIntegrationDeliveryInput, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) ||
		isNilID(input.IntegrationTargetBindingID) {
		return CreateIntegrationDeliveryInput{}, errors.New(
			"project, agent, and integration target binding are required",
		)
	}
	if input.Transport != IntegrationDeliveryTransportConnector &&
		input.Transport != IntegrationDeliveryTransportNative {
		return CreateIntegrationDeliveryInput{}, fmt.Errorf("unsupported delivery transport %q", input.Transport)
	}
	input.DeliveryKind = strings.TrimSpace(input.DeliveryKind)
	input.PayloadVersion = strings.TrimSpace(input.PayloadVersion)
	input.IdempotencyScope = strings.TrimSpace(input.IdempotencyScope)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.DeliveryKind == "" || input.PayloadVersion == "" ||
		input.IdempotencyScope == "" || input.IdempotencyKey == "" {
		return CreateIntegrationDeliveryInput{}, errors.New(
			"delivery kind, payload version, idempotency scope, and idempotency key are required",
		)
	}
	payload, err := normalizedJSONObject(input.Payload, "payload")
	if err != nil {
		return CreateIntegrationDeliveryInput{}, err
	}
	input.Payload = payload
	return input, nil
}

func normalizeCompleteIntegrationDeliveryInput(
	input CompleteIntegrationDeliveryInput,
) (CompleteIntegrationDeliveryInput, error) {
	if isNilID(input.ID) || isNilID(input.ClaimToken) || input.ClaimGeneration <= 0 {
		return CompleteIntegrationDeliveryInput{}, storeerr.InvalidRequest(errors.New(
			"delivery, claim token, and positive claim generation are required",
		))
	}
	switch input.State {
	case IntegrationDeliveryStateRetryWait:
		if input.RetryAfter <= 0 {
			return CompleteIntegrationDeliveryInput{}, storeerr.InvalidRequest(
				errors.New("retry delay must be positive"),
			)
		}
	case IntegrationDeliveryStateDelivered,
		IntegrationDeliveryStateFailed,
		IntegrationDeliveryStateUnknown,
		IntegrationDeliveryStateCanceled:
		if input.RetryAfter != 0 {
			return CompleteIntegrationDeliveryInput{}, storeerr.InvalidRequest(
				errors.New("retry delay is only valid for retry outcomes"),
			)
		}
	default:
		return CompleteIntegrationDeliveryInput{}, storeerr.InvalidRequest(fmt.Errorf(
			"unsupported delivery outcome %q",
			input.State,
		))
	}
	input.ProviderMessageRef = strings.TrimSpace(input.ProviderMessageRef)
	if len(input.ProviderMessageRef) > MaxProviderMessageRefBytes {
		return CompleteIntegrationDeliveryInput{}, storeerr.InvalidRequest(fmt.Errorf(
			"provider message reference exceeds %d UTF-8 bytes",
			MaxProviderMessageRefBytes,
		))
	}
	if err := dbsafe.Text(input.ProviderMessageRef); err != nil {
		return CompleteIntegrationDeliveryInput{}, storeerr.InvalidRequest(fmt.Errorf(
			"provider message reference %w",
			err,
		))
	}
	lastError, err := normalizedJSONObject(input.LastError, "last_error")
	if err != nil {
		return CompleteIntegrationDeliveryInput{}, err
	}
	input.LastError = lastError
	input.Capabilities, err = channelconnector.NormalizeCapabilities(input.Capabilities)
	if err != nil {
		return CompleteIntegrationDeliveryInput{}, err
	}
	return input, nil
}

func integrationDeliveryRecordFromSQLC(row dbsqlc.IntegrationDelivery) IntegrationDeliveryRecord {
	return IntegrationDeliveryRecord{
		ID:                         row.ID,
		ProjectID:                  row.ProjectID,
		AgentID:                    row.AgentID,
		IntegrationAppID:           row.IntegrationAppID,
		IntegrationInstallID:       row.IntegrationInstallID,
		IntegrationTargetID:        row.IntegrationTargetID,
		IntegrationTargetBindingID: row.IntegrationTargetBindingID,
		Provider:                   row.Provider,
		ConnectorKey:               row.ConnectorKey,
		Transport:                  IntegrationDeliveryTransport(row.Transport),
		DeliveryKind:               row.DeliveryKind,
		PayloadVersion:             row.PayloadVersion,
		Payload:                    row.Payload,
		IdempotencyScope:           row.IdempotencyScope,
		IdempotencyKey:             row.IdempotencyKey,
		State:                      IntegrationDeliveryState(row.State),
		AttemptCount:               int(row.AttemptCount),
		AvailableAt:                row.AvailableAt,
		ClaimToken:                 idFromSQLCPtr(row.ClaimToken),
		ClaimGeneration:            row.ClaimGeneration,
		ClaimedBy:                  stringFromPtr(row.ClaimedBy),
		ClaimedAt:                  row.ClaimedAt,
		ClaimExpiresAt:             row.ClaimExpiresAt,
		NotifyRef:                  idFromSQLCPtr(row.NotifyRef),
		ProviderMessageRef:         stringFromPtr(row.ProviderMessageRef),
		LastError:                  row.LastError,
		CompletedAt:                row.CompletedAt,
		CreatedAt:                  row.CreatedAt,
		UpdatedAt:                  row.UpdatedAt,
	}
}

func integrationDeliveryRecordFromClaimSQLC(
	row dbsqlc.ClaimIntegrationDeliveriesRow,
) IntegrationDeliveryRecord {
	return IntegrationDeliveryRecord{
		ID:                         row.ID,
		ProjectID:                  row.ProjectID,
		AgentID:                    row.AgentID,
		IntegrationAppID:           row.IntegrationAppID,
		IntegrationInstallID:       row.IntegrationInstallID,
		IntegrationTargetID:        row.IntegrationTargetID,
		IntegrationTargetBindingID: row.IntegrationTargetBindingID,
		Provider:                   row.Provider,
		ConnectorKey:               row.ConnectorKey,
		Transport:                  IntegrationDeliveryTransport(row.Transport),
		DeliveryKind:               row.DeliveryKind,
		PayloadVersion:             row.PayloadVersion,
		Payload:                    row.Payload,
		IdempotencyScope:           row.IdempotencyScope,
		IdempotencyKey:             row.IdempotencyKey,
		State:                      IntegrationDeliveryState(row.State),
		AttemptCount:               int(row.AttemptCount),
		AvailableAt:                row.AvailableAt,
		ClaimToken:                 idFromSQLCPtr(row.ClaimToken),
		ClaimGeneration:            row.ClaimGeneration,
		ClaimedBy:                  stringFromPtr(row.ClaimedBy),
		ClaimedAt:                  row.ClaimedAt,
		ClaimExpiresAt:             row.ClaimExpiresAt,
		NotifyRef:                  idFromSQLCPtr(row.NotifyRef),
		ProviderMessageRef:         stringFromPtr(row.ProviderMessageRef),
		LastError:                  row.LastError,
		CompletedAt:                row.CompletedAt,
		CreatedAt:                  row.CreatedAt,
		UpdatedAt:                  row.UpdatedAt,
	}
}

func stringFromPtr(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func validateLeaseAndLimit(duration time.Duration, limit int) error {
	if duration < time.Millisecond {
		return errors.New("lease duration must be at least one millisecond")
	}
	return validateRowLimit(limit)
}

func validateRowLimit(limit int) error {
	if limit <= 0 || limit > 1000 {
		return errors.New("row limit must be between 1 and 1000")
	}
	return nil
}
