package integrationstore

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/resourceguard"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func (s *Store) CreateIntegration(
	ctx context.Context,
	input SaveIntegrationInput,
) (IntegrationRecord, error) {
	return s.saveIntegration(ctx, uuid.Nil, input)
}

func (s *Store) UpdateIntegration(
	ctx context.Context,
	id uuid.UUID,
	input SaveIntegrationInput,
) (IntegrationRecord, error) {
	if id == uuid.Nil {
		return IntegrationRecord{}, storeerr.InvalidRequest(errors.New("integration id is required"))
	}
	return s.saveIntegration(ctx, id, input)
}

func (s *Store) saveIntegration(
	ctx context.Context,
	id uuid.UUID,
	input SaveIntegrationInput,
) (IntegrationRecord, error) {
	var err error
	if id == uuid.Nil {
		input, err = normalizeIntegration(input)
		if err != nil {
			return IntegrationRecord{}, storeerr.InvalidRequest(err)
		}
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	if err := lifecyclelock.EnterActiveProject(ctx, tx, input.OrgID, input.ProjectID); err != nil {
		return IntegrationRecord{}, err
	}
	if id != uuid.Nil {
		if err := q.LockIntegrationLifecycleShared(ctx, dbsqlc.LockIntegrationLifecycleSharedParams{
			IntegrationID: id,
		}); err != nil {
			return IntegrationRecord{}, err
		}
	}
	var row dbsqlc.Integration
	if id == uuid.Nil {
		if err := resourceguard.Lock(ctx, q, "integrations", input.ProjectID.String()); err != nil {
			return IntegrationRecord{}, err
		}
		limits, limitErr := resourceguard.ResolveLimits(ctx, q, input.OrgID)
		if limitErr != nil {
			return IntegrationRecord{}, limitErr
		}
		count, countErr := q.CountIntegrations(ctx, dbsqlc.CountIntegrationsParams{ProjectID: input.ProjectID})
		if countErr != nil {
			return IntegrationRecord{}, countErr
		}
		if count >= limits.MaxActiveIntegrationsPerProject {
			return IntegrationRecord{}, fmt.Errorf(
				"project integrations limit of %d reached: %w",
				limits.MaxActiveIntegrationsPerProject,
				storeerr.ErrConflict,
			)
		}
		row, err = q.InsertIntegration(
			ctx,
			dbsqlc.InsertIntegrationParams{
				OrgID:           input.OrgID,
				ProjectID:       input.ProjectID,
				Name:            input.Name,
				IntegrationKind: string(input.IntegrationKind),
				Settings:        input.Settings,
			},
		)
	} else {
		current, lockErr := q.LockIntegration(ctx, dbsqlc.LockIntegrationParams{
			ProjectID: input.ProjectID, ID: id,
		})
		if errors.Is(lockErr, pgx.ErrNoRows) {
			return IntegrationRecord{}, storeerr.ErrNotFound
		}
		if lockErr != nil {
			return IntegrationRecord{}, lockErr
		}
		if (input.Name != "" && input.Name != current.Name) ||
			(input.IntegrationKind != "" && string(input.IntegrationKind) != current.IntegrationKind) {
			return IntegrationRecord{}, storeerr.InvalidRequest(errors.New("integration name and kind are immutable"))
		}
		input.Name, input.IntegrationKind = current.Name, integrationdefinition.Kind(current.IntegrationKind)
		input, err = normalizeIntegration(input)
		if err != nil {
			return IntegrationRecord{}, storeerr.InvalidRequest(err)
		}
		row, err = q.UpdateIntegrationSettings(
			ctx,
			dbsqlc.UpdateIntegrationSettingsParams{ProjectID: input.ProjectID, ID: id, Settings: input.Settings},
		)
	}
	if storeutil.IsUniqueViolationOnConstraint(err, "integrations_active_name_idx") {
		return IntegrationRecord{}, storeerr.Tag(storeerr.ErrConflict,
			fmt.Errorf("an integration named %q already exists in this project; choose a different name", input.Name))
	}
	if err != nil {
		return IntegrationRecord{}, fmt.Errorf("save integration: %w", err)
	}
	record, err := integrationRecord(row)
	if err != nil {
		return IntegrationRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IntegrationRecord{}, err
	}
	return record, nil
}

func (s *Store) GetIntegration(ctx context.Context, projectID, id uuid.UUID) (IntegrationRecord, error) {
	return getIntegration(ctx, s.q, projectID, id)
}

func getIntegration(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, id uuid.UUID,
) (IntegrationRecord, error) {
	row, err := q.GetIntegration(ctx, dbsqlc.GetIntegrationParams{ProjectID: projectID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return IntegrationRecord{}, err
	}
	return integrationRecord(row)
}

func (s *Store) GetIntegrationByName(
	ctx context.Context,
	projectID uuid.UUID,
	name string,
) (IntegrationRecord, error) {
	row, err := s.q.GetIntegrationByName(ctx, dbsqlc.GetIntegrationByNameParams{
		ProjectID: projectID, Name: name,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return IntegrationRecord{}, err
	}
	return integrationRecord(row)
}

func (s *Store) GetIntegrationByID(ctx context.Context, id uuid.UUID) (IntegrationRecord, error) {
	return getIntegrationByID(ctx, s.q, id)
}

func (s *Store) GetIntegrationByIDTx(
	ctx context.Context,
	tx dbsqlc.DBTX,
	id uuid.UUID,
) (IntegrationRecord, error) {
	return getIntegrationByID(ctx, dbsqlc.New(tx), id)
}

func getIntegrationByID(ctx context.Context, q *dbsqlc.Queries, id uuid.UUID) (IntegrationRecord, error) {
	row, err := q.GetIntegrationByID(ctx, dbsqlc.GetIntegrationByIDParams{ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return IntegrationRecord{}, err
	}
	return integrationRecord(row)
}

func integrationRecord(row dbsqlc.Integration) (IntegrationRecord, error) {
	definition, ok := integrationdefinition.Lookup(integrationdefinition.Kind(row.IntegrationKind))
	if !ok {
		return IntegrationRecord{}, fmt.Errorf("integration %s has unregistered type %q", row.ID, row.IntegrationKind)
	}
	record := IntegrationRecord{
		ID:                       row.ID,
		OrgID:                    row.OrgID,
		ProjectID:                row.ProjectID,
		InstalledByUserID:        storeutil.IDFromPtr(row.InstalledByUserID),
		Name:                     row.Name,
		IntegrationKind:          definition.IntegrationKind,
		Provider:                 definition.Provider,
		State:                    IntegrationState(row.State),
		SetupRevision:            row.SetupRevision,
		ProviderTenantID:         "",
		ProviderAccountRef:       "",
		ProviderAgentDisplayName: row.ProviderAgentDisplayName,
		CredentialSecretID:       storeutil.IDFromPtr(row.CredentialSecretID),
		ProviderConfig:           row.ProviderConfig,
		ProviderIdentity:         row.ProviderIdentity,
		ProviderMetadata:         row.ProviderMetadata,
		LastOAuthFlowID:          storeutil.IDFromPtr(row.LastOauthFlowID),
		DeletedAt:                row.DeletedAt,
		CreatedAt:                row.CreatedAt,
		UpdatedAt:                row.UpdatedAt,
	}
	if row.ProviderTenantID != nil {
		record.ProviderTenantID = *row.ProviderTenantID
	}
	if row.ProviderAccountRef != nil {
		record.ProviderAccountRef = *row.ProviderAccountRef
	}
	record.Settings = row.Settings
	return record, nil
}

func normalizeIntegration(input SaveIntegrationInput) (SaveIntegrationInput, error) {
	if input.OrgID == uuid.Nil || input.ProjectID == uuid.Nil {
		return input, errors.New("organization and project are required")
	}
	if err := toolcatalog.ValidateIntegrationName(input.Name); err != nil {
		return input, err
	}
	settings, err := integrationdefinition.ValidateSettings(input.IntegrationKind, input.Settings)
	if err != nil {
		return input, err
	}
	input.Settings = settings
	return input, nil
}

func (s *Store) ListIntegrations(
	ctx context.Context,
	input ListIntegrationsInput,
) (ListIntegrationsResult, error) {
	if input.ProjectID == uuid.Nil || input.Limit < 1 || input.Limit > 100 {
		return ListIntegrationsResult{}, storeerr.InvalidRequest(
			errors.New("project and limit between 1 and 100 are required"),
		)
	}
	rows, err := s.q.ListIntegrations(ctx, dbsqlc.ListIntegrationsParams{
		ProjectID: input.ProjectID, NamePattern: input.NamePattern, RowLimit: int32(input.Limit + 1),
		CursorSet: input.After.Set, CursorCreatedAt: input.After.CreatedAt, CursorID: input.After.ID,
	})
	if err != nil {
		return ListIntegrationsResult{}, err
	}
	result := ListIntegrationsResult{
		Integrations: make([]IntegrationRecord, 0, min(len(rows), input.Limit)),
		HasMore:      len(rows) > input.Limit,
	}
	if result.HasMore {
		rows = rows[:input.Limit]
	}
	for _, row := range rows {
		record, err := integrationRecord(row)
		if err != nil {
			return ListIntegrationsResult{}, err
		}
		result.Integrations = append(result.Integrations, record)
	}
	if result.HasMore {
		last := rows[len(rows)-1]
		result.Next = listing.KeysetCursor{Set: true, CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return result, nil
}

func (s *Store) ListIntegrationsByProviderIdentity(
	ctx context.Context, provider integrationdefinition.Provider, tenant, account string, after uuid.UUID, limit int,
) ([]IntegrationRecord, error) {
	if provider == "" || tenant == "" || account == "" || limit < 1 || limit > 100 {
		return nil, storeerr.InvalidRequest(errors.New("provider identity and limit between 1 and 100 are required"))
	}
	return s.listIntegrationsByProviderIdentity(ctx, provider, tenant, &account, after, limit, false)
}

func (s *Store) ListIntegrationsForProviderEventVerification(
	ctx context.Context, provider integrationdefinition.Provider, tenant, account string, after uuid.UUID, limit int,
) ([]IntegrationRecord, error) {
	// Disconnected integrations retain credentials so ingress can authenticate acknowledgements.
	if provider == "" || tenant == "" || account == "" || limit < 1 || limit > 100 {
		return nil, storeerr.InvalidRequest(errors.New("provider identity and limit between 1 and 100 are required"))
	}
	return s.listIntegrationsByProviderIdentity(ctx, provider, tenant, &account, after, limit, true)
}

func (s *Store) SharesProviderIdentity(ctx context.Context, integration IntegrationRecord) (bool, error) {
	active, err := s.listIntegrationsByProviderIdentity(ctx, integration.Provider,
		integration.ProviderTenantID, &integration.ProviderAccountRef, uuid.Nil, 2, false)
	return slices.ContainsFunc(active, func(other IntegrationRecord) bool { return other.ID != integration.ID }), err
}

func (s *Store) ListIntegrationsByProviderTenant(
	ctx context.Context,
	provider integrationdefinition.Provider, tenant string,
	after uuid.UUID,
	limit int,
) ([]IntegrationRecord, error) {
	if provider == "" || tenant == "" || limit < 1 || limit > 100 {
		return nil, storeerr.InvalidRequest(errors.New("provider tenant and limit between 1 and 100 are required"))
	}
	return s.listIntegrationsByProviderIdentity(ctx, provider, tenant, nil, after, limit, false)
}

func (s *Store) listIntegrationsByProviderIdentity(
	ctx context.Context,
	provider integrationdefinition.Provider, tenant string,
	account *string,
	after uuid.UUID,
	limit int,
	includeDisconnected bool,
) ([]IntegrationRecord, error) {
	rows, err := s.q.ListIntegrationsByProviderIdentity(ctx, dbsqlc.ListIntegrationsByProviderIdentityParams{
		IntegrationKinds: integrationdefinition.IntegrationKindsForProvider(provider),
		ProviderTenantID: &tenant, ProviderAccountRef: account,
		AfterID: storeutil.IDFromNil(after), RowLimit: int32(limit),
		IncludeDisconnected: includeDisconnected,
	})
	if err != nil {
		return nil, err
	}
	integrations := make([]IntegrationRecord, 0, len(rows))
	for _, row := range rows {
		integration, err := integrationRecord(row)
		if err != nil {
			return nil, err
		}
		integrations = append(integrations, integration)
	}
	return integrations, nil
}
