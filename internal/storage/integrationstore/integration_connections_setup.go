package integrationstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// CompleteSlackConnectionSetup atomically redeems OAuth to create or reauthorize
// a project connection without reading or writing apps or profile destinations.
// It uses the same account/lifecycle locks, installer and credential checks,
// resource limits, and monotonic OAuth flow fence as CompleteSlackAppSetup.
func (s *Store) CompleteSlackConnectionSetup(
	ctx context.Context,
	connection SaveIntegrationConnectionInput,
) (IntegrationConnectionRecord, error) {
	if connection.Provider != IntegrationProviderSlack || connection.State != IntegrationConnectionStateActive ||
		connection.OAuthFlowID == uuid.Nil {
		return IntegrationConnectionRecord{}, storeerr.InvalidRequest(
			errors.New("slack connection setup requires an active Slack connection and OAuth flow"),
		)
	}
	return s.saveIntegrationConnection(ctx, uuid.Nil, connection, false)
}

// CompleteSlackAppSetup atomically consumes OAuth and persists usable app setup.
// The caller supplies provider behavior. Storage binds its connection and supplies
// a stable default name when empty. Reconnect validates and preserves existing
// configured setup (including disabled state), rather than resetting its slots.
func (s *Store) CompleteSlackAppSetup(
	ctx context.Context,
	connection SaveIntegrationConnectionInput,
	defaults SaveProjectAppInput,
) (IntegrationConnectionRecord, ProjectAppRecord, error) {
	var saved IntegrationConnectionRecord
	var app ProjectAppRecord
	var err error
	connection, err = normalizeSaveIntegrationConnectionInput(connection)
	if err != nil {
		return saved, app, storeerr.InvalidRequest(err)
	}
	if connection.Provider != IntegrationProviderSlack || connection.State != IntegrationConnectionStateActive ||
		defaults.DefinitionID != appdefinition.Slack ||
		defaults.OrgID != connection.OrgID || defaults.ProjectID != connection.ProjectID {
		return saved, app, storeerr.InvalidRequest(
			errors.New("slack setup requires an active Slack connection and same-project Slack app defaults"),
		)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return saved, app, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	connection, existingConnection, err := s.lockIntegrationConnectionSaveTx(ctx, tx, uuid.Nil, connection, false)
	if err != nil {
		return saved, app, err
	}
	q := dbsqlc.New(tx)
	setup := defaults
	var existing *ProjectAppRecord
	if existingConnection != uuid.Nil {
		ref, err := publicid.Encode(publicid.KindIntegrationConnection, existingConnection)
		if err != nil {
			return saved, app, err
		}
		row, err := q.GetConnectionProjectAppForSetup(
			ctx,
			dbsqlc.GetConnectionProjectAppForSetupParams{
				ProjectID:     connection.ProjectID,
				DefinitionID:  appdefinition.Slack,
				ConnectionRef: ref,
			},
		)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return saved, app, err
		}
		if err == nil {
			record, err := projectAppRecord(row)
			if err != nil {
				return saved, app, err
			}
			existing = &record
			setup = SaveProjectAppInput{
				OrgID:        connection.OrgID,
				ProjectID:    record.ProjectID,
				Name:         record.Name,
				DefinitionID: record.DefinitionID,
				Settings:     record.Settings,
				Enabled:      record.Enabled,
			}
		}
	}
	// Profiles and agents must be locked before the connection's credential. The
	// ordinary app-save path uses the same destination ordering. Revalidation below
	// only re-enters these already-held locks; the connection is exclusively gated
	// (or newly inserted and invisible to other transactions).
	if err := s.lockProjectAppDestinations(ctx, tx, setup); err != nil {
		return saved, app, err
	}
	saved, err = s.writeIntegrationConnectionTx(ctx, tx, connection)
	if err != nil {
		return saved, app, err
	}
	ref, err := publicid.Encode(publicid.KindIntegrationConnection, saved.ID)
	if err != nil {
		return saved, app, err
	}
	if setup.Settings.Resource.Connection != "" && setup.Settings.Resource.Connection != ref {
		return saved, app, storeerr.InvalidRequest(errors.New("setup resource must reference its connection"))
	}
	setup.Settings.Resource.Connection = ref
	if setup.Name == "" {
		setup.Name = "slack-" + saved.ID.String()
	}
	var references []projectAppSecretReference
	setup, err = normalizeProjectApp(setup, agentconfig.CompileOptions{ValidateSecretID: func(
		ref string,
		kind secrets.Kind,
	) error {
		id, err := publicid.Decode(publicid.KindSecret, ref)
		if err == nil {
			references = append(references, projectAppSecretReference{id: id, kind: kind})
		}
		return err
	}})
	if err != nil {
		return saved, app, storeerr.InvalidRequest(err)
	}
	connectionID, err := s.validateProjectAppReferences(ctx, tx, setup, references)
	if err != nil {
		return saved, app, err
	}
	settings, err := json.Marshal(setup.Settings)
	if err != nil {
		return saved, app, err
	}
	if existing == nil {
		app, err = writeProjectAppTx(ctx, q, uuid.Nil, setup, settings, connectionID)
		if err != nil {
			return saved, app, err
		}
	} else {
		// Deletion and unchanged-disable may proceed without a connection gate. Take
		// the app row only after its dependencies; reject a concurrent edit rather
		// than overwriting it or consuming OAuth against an unvalidated setup.
		if _, err = q.LockProjectApp(
			ctx,
			dbsqlc.LockProjectAppParams{ProjectID: setup.ProjectID, ID: existing.ID},
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				err = storeerr.ErrNotFound
			}
			return saved, app, err
		}
		current, err := q.GetProjectApp(ctx, dbsqlc.GetProjectAppParams{ProjectID: setup.ProjectID, ID: existing.ID})
		if err != nil {
			return saved, app, err
		}
		if current.Name != setup.Name || current.DefinitionID != setup.DefinitionID || current.Enabled != setup.Enabled ||
			!jsoncanonical.Equal(
				current.Settings,
				settings,
			) {
			return saved, app, storeerr.ErrConflict
		}
		app, err = projectAppRecord(current)
		if err != nil {
			return saved, app, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return saved, app, fmt.Errorf("commit Slack app setup: %w", err)
	}
	return saved, app, nil
}
