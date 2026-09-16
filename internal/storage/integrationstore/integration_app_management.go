package integrationstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/secretops"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ListIntegrationAppsInput struct {
	OrgID, EligibleProjectID uuid.UUID
	After                    listing.KeysetCursor
	Limit                    int
}

type IntegrationAppsPage struct {
	Apps []IntegrationAppSummary
	Next listing.KeysetCursor
}

type IntegrationAppSummary struct {
	ID, OrgID, OwnerProjectID             uuid.UUID
	Provider, ProviderAppRef, DisplayName string
	State                                 IntegrationAppState
	CreatedAt, UpdatedAt                  time.Time
}

// ListIntegrationApps omits credentials and configuration from both inventory
// and project selectors. Full configuration requires a separate authorized read.
func (s *Store) ListIntegrationApps(ctx context.Context, input ListIntegrationAppsInput) (IntegrationAppsPage, error) {
	var page IntegrationAppsPage
	if input.OrgID == uuid.Nil || input.Limit < 1 || input.Limit > 100 ||
		(input.After.Set && (input.After.ID == uuid.Nil || input.After.CreatedAt.IsZero())) {
		return page, storeerr.InvalidRequest(errors.New("org and a valid bounded app page are required"))
	}
	rows, err := s.q.ListIntegrationApps(ctx, dbsqlc.ListIntegrationAppsParams{
		OrgID: input.OrgID, EligibleProjectID: storeutil.IDFromNil(input.EligibleProjectID),
		CursorSet: input.After.Set, CursorCreatedAt: input.After.CreatedAt, CursorID: input.After.ID,
		RowLimit: int32(input.Limit + 1),
	})
	if err != nil {
		return page, integrationChannelReadError("list integration apps", err)
	}
	if len(rows) > input.Limit {
		rows = rows[:input.Limit]
		last := rows[len(rows)-1]
		page.Next = listing.KeysetCursor{Set: true, ID: last.ID, CreatedAt: last.CreatedAt}
	}
	page.Apps = make([]IntegrationAppSummary, 0, len(rows))
	for _, row := range rows {
		page.Apps = append(page.Apps, IntegrationAppSummary{
			ID: row.ID, OrgID: row.OrgID, OwnerProjectID: storeutil.IDFromPtr(row.OwnerProjectID),
			Provider: row.Provider, ProviderAppRef: row.ProviderAppRef, DisplayName: row.DisplayName,
			State: IntegrationAppState(row.State), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		})
	}
	return page, nil
}

type UpdateIntegrationAppInput struct {
	OrgID, ID          uuid.UUID
	DisplayName        *string
	CredentialSecretID *uuid.UUID
	ProviderConfig     json.RawMessage
	State              *IntegrationAppState
}

func (s *Store) UpdateIntegrationApp(
	ctx context.Context, input UpdateIntegrationAppInput,
) (IntegrationAppRecord, error) {
	if input.OrgID == uuid.Nil || input.ID == uuid.Nil {
		return IntegrationAppRecord{}, storeerr.InvalidRequest(errors.New("org and app are required"))
	}
	if input.DisplayName != nil {
		name := strings.TrimSpace(*input.DisplayName)
		if len(name) > 512 || dbsafe.Text(name) != nil {
			return IntegrationAppRecord{}, storeerr.InvalidRequest(errors.New("invalid app display name"))
		}
		input.DisplayName = &name
	}
	var state *string
	if input.State != nil {
		if *input.State != IntegrationAppStateActive && *input.State != IntegrationAppStateDisabled {
			return IntegrationAppRecord{}, storeerr.InvalidRequest(errors.New("invalid app state"))
		}
		value := string(*input.State)
		state = &value
	}
	var err error
	var configuration *json.RawMessage
	if input.ProviderConfig != nil {
		input.ProviderConfig, err = normalizedJSONObject(input.ProviderConfig, "provider_config")
		if err != nil {
			return IntegrationAppRecord{}, storeerr.InvalidRequest(err)
		}
		configuration = &input.ProviderConfig
	}
	app, err := s.GetIntegrationApp(ctx, input.OrgID, input.ID)
	if err != nil {
		return IntegrationAppRecord{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationAppRecord{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if app.OwnerProjectID == uuid.Nil {
		err = lifecyclelock.EnterActiveOrganization(ctx, tx, input.OrgID)
	} else {
		err = lifecyclelock.EnterActiveProject(ctx, tx, input.OrgID, app.OwnerProjectID)
	}
	if err != nil {
		return IntegrationAppRecord{}, err
	}
	var secretID *uuid.UUID
	if input.CredentialSecretID != nil {
		secretID = storeutil.IDFromNil(*input.CredentialSecretID)
		if err := validateIntegrationAppCredential(
			ctx, tx, app.OrgID, app.OwnerProjectID, *input.CredentialSecretID,
		); err != nil {
			return IntegrationAppRecord{}, err
		}
	}
	row, err := s.q.WithTx(tx).UpdateIntegrationApp(ctx, dbsqlc.UpdateIntegrationAppParams{
		OrgID: input.OrgID, ID: input.ID, DisplayName: input.DisplayName, State: state,
		SetCredential: input.CredentialSecretID != nil, CredentialSecretID: secretID, ProviderConfig: configuration,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationAppRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return IntegrationAppRecord{}, integrationChannelWriteError("update integration app", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return IntegrationAppRecord{}, err
	}
	return integrationAppRecordFromSQLC(row), nil
}

func validateIntegrationAppCredential(ctx context.Context, tx pgx.Tx, orgID, projectID, secretID uuid.UUID) error {
	if secretID == uuid.Nil {
		return nil
	}
	// Rotation locks the secret before its dependent apps. Acquire the reference
	// before INSERT/UPDATE takes an app row lock; the DB trigger remains a backstop.
	credential, err := secretops.LockReference(ctx, tx, orgID, secretID)
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrNotFound
	}
	if err != nil {
		return err
	}
	if credential.ManagementKind != management.Tenant ||
		(projectID == uuid.Nil && credential.OwnerKind != secretstore.SecretOwnerOrg) ||
		(projectID != uuid.Nil && (credential.OwnerKind != secretstore.SecretOwnerProject ||
			credential.OwnerProjectID != projectID)) {
		return storeerr.ErrNotFound
	}
	return nil
}
