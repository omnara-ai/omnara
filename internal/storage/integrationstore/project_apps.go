package integrationstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/resourceguard"
	"github.com/omnara-ai/omnara/internal/storage/internal/secretops"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const MaxAppLaunchSlots = 16

type projectAppSecretReference struct {
	id   uuid.UUID
	kind secrets.Kind
}

func (s *Store) CreateProjectApp(ctx context.Context, input SaveProjectAppInput) (ProjectAppRecord, error) {
	return s.saveProjectApp(ctx, uuid.Nil, input)
}

func (s *Store) UpdateProjectApp(
	ctx context.Context,
	id uuid.UUID,
	input SaveProjectAppInput,
) (ProjectAppRecord, error) {
	if id == uuid.Nil {
		return ProjectAppRecord{}, storeerr.InvalidRequest(errors.New("app id is required"))
	}
	return s.saveProjectApp(ctx, id, input)
}

func (s *Store) saveProjectApp(ctx context.Context, id uuid.UUID, input SaveProjectAppInput) (ProjectAppRecord, error) {
	var secretReferences []projectAppSecretReference
	input, err := normalizeProjectApp(input, agentconfig.CompileOptions{
		ValidateSecretID: func(ref string, kind secrets.Kind) error {
			id, err := publicid.Decode(publicid.KindSecret, ref)
			if err != nil {
				return err
			}
			secretReferences = append(secretReferences, projectAppSecretReference{id: id, kind: kind})
			return nil
		},
	})
	if err != nil {
		return ProjectAppRecord{}, storeerr.InvalidRequest(err)
	}
	settings, err := json.Marshal(input.Settings)
	if err != nil {
		return ProjectAppRecord{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ProjectAppRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	if err := lifecyclelock.EnterActiveProject(ctx, tx, input.OrgID, input.ProjectID); err != nil {
		return ProjectAppRecord{}, err
	}
	if id != uuid.Nil && !input.Enabled {
		current, err := q.GetProjectApp(ctx, dbsqlc.GetProjectAppParams{ProjectID: input.ProjectID, ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return ProjectAppRecord{}, storeerr.ErrNotFound
		}
		if err != nil {
			return ProjectAppRecord{}, fmt.Errorf("read app before disabling: %w", err)
		}
		if current.DefinitionID == input.DefinitionID && current.Name == input.Name &&
			jsoncanonical.Equal(current.Settings, settings) {
			// Revoking unchanged setup needs no live child references. The compare
			// in UPDATE closes the race with an editor; this branch never proceeds
			// to profile/secret locks after acquiring the app row.
			row, err := q.DisableUnchangedProjectApp(ctx, dbsqlc.DisableUnchangedProjectAppParams{
				ProjectID: input.ProjectID, ID: id, DefinitionID: input.DefinitionID, Name: input.Name, Settings: settings,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return ProjectAppRecord{}, storeerr.ErrConflict
			}
			if err != nil {
				return ProjectAppRecord{}, fmt.Errorf("disable unchanged app: %w", err)
			}
			record, err := projectAppRecord(row)
			if err != nil {
				return ProjectAppRecord{}, err
			}
			if err := tx.Commit(ctx); err != nil {
				return ProjectAppRecord{}, fmt.Errorf("commit app disable: %w", err)
			}
			return record, nil
		}
	}
	connectionID, err := s.validateProjectAppReferences(ctx, tx, input, secretReferences)
	if err != nil {
		return ProjectAppRecord{}, err
	}
	record, err := writeProjectAppTx(ctx, q, id, input, settings, connectionID)
	if err != nil {
		return ProjectAppRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProjectAppRecord{}, fmt.Errorf("commit project app: %w", err)
	}
	return record, nil
}

func writeProjectAppTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	id uuid.UUID,
	input SaveProjectAppInput,
	settings json.RawMessage,
	connectionID uuid.UUID,
) (ProjectAppRecord, error) {
	var err error
	var launchConnection *uuid.UUID
	var scopeKind, scopeRef *string
	if launcher := input.Settings.Launcher; launcher != nil {
		launchConnection, scopeKind, scopeRef = &connectionID, &launcher.ScopeKind, &launcher.ScopeRef
	}
	var row dbsqlc.ProjectApp
	if id == uuid.Nil {
		row, err = q.InsertProjectApp(ctx, dbsqlc.InsertProjectAppParams{
			ProjectID: input.ProjectID, Name: input.Name, DefinitionID: input.DefinitionID, Settings: settings,
			LaunchConnectionID: launchConnection, LaunchScopeKind: scopeKind, LaunchScopeRef: scopeRef, Enabled: input.Enabled,
		})
	} else {
		row, err = q.UpdateProjectApp(ctx, dbsqlc.UpdateProjectAppParams{
			ProjectID: input.ProjectID, ID: id, Name: input.Name, DefinitionID: input.DefinitionID, Settings: settings,
			LaunchConnectionID: launchConnection, LaunchScopeKind: scopeKind, LaunchScopeRef: scopeRef, Enabled: input.Enabled,
		})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectAppRecord{}, storeerr.ErrNotFound
	}
	if storeutil.IsUniqueViolation(err) {
		return ProjectAppRecord{}, storeerr.ErrConflict
	}
	if err != nil {
		return ProjectAppRecord{}, fmt.Errorf("save project app: %w", err)
	}
	if id == uuid.Nil {
		if err := resourceguard.Lock(ctx, q, "project_apps", input.ProjectID.String()); err != nil {
			return ProjectAppRecord{}, err
		}
		limits, err := resourceguard.ResolveLimits(ctx, q, input.OrgID)
		if err != nil {
			return ProjectAppRecord{}, err
		}
		count, err := q.CountProjectApps(ctx, dbsqlc.CountProjectAppsParams{ProjectID: input.ProjectID})
		if err != nil {
			return ProjectAppRecord{}, err
		}
		if count > limits.MaxActiveProjectAppsPerProject {
			return ProjectAppRecord{}, fmt.Errorf(
				"project apps limit of %d reached: %w",
				limits.MaxActiveProjectAppsPerProject,
				storeerr.ErrConflict,
			)
		}
	}
	record, err := projectAppRecord(row)
	if err != nil {
		return ProjectAppRecord{}, err
	}
	return record, nil
}

func normalizeProjectApp(input SaveProjectAppInput, opts agentconfig.CompileOptions) (SaveProjectAppInput, error) {
	if input.OrgID == uuid.Nil || input.ProjectID == uuid.Nil {
		return input, errors.New("organization and project are required")
	}
	var err error
	input.Name, err = resourcename.CanonicalizeRequired("app name", input.Name)
	if err != nil {
		return input, err
	}
	definition, ok := appdefinition.Lookup(input.DefinitionID)
	if !ok {
		return input, errors.New("unknown app definition")
	}
	if input.Settings.Resource.AppInstance != "" {
		return input, errors.New("app settings cannot reference another app instance")
	}
	if input.Settings.Resource.Definition != "" && input.Settings.Resource.Definition != input.DefinitionID {
		return input, errors.New("resource definition must match app definition")
	}
	input.Settings.Resource.Definition = input.DefinitionID
	if err := agentconfig.ValidateAppResourceTemplate(input.Settings.Resource, opts); err != nil {
		return input, err
	}
	if scope := input.Settings.Resource.Scope; scope != nil {
		if err := scope.Validate(definition.Provider); err != nil {
			return input, err
		}
	}
	launcher := input.Settings.Launcher
	if launcher == nil {
		return input, nil
	}
	if input.Settings.Resource.Connection == "" || launcher.ScopeKind == "" || launcher.ScopeRef == "" {
		return input, errors.New("launcher requires a connection and event scope")
	}
	kind, ref, err := appdefinition.CanonicalLauncherScope(definition.Provider, launcher.ScopeKind, launcher.ScopeRef)
	if err != nil {
		return input, err
	}
	canonical := *launcher
	canonical.ScopeKind, canonical.ScopeRef = kind, ref
	input.Settings.Launcher = &canonical
	launcher = &canonical
	if launcher.Trigger != "mention" &&
		!(definition.Provider == appdefinition.ProviderGitHub && launcher.Trigger == "pull_request_opened") {
		return input, errors.New("unsupported app launch trigger")
	}
	if len(launcher.Slots) == 0 || len(launcher.Slots) > MaxAppLaunchSlots {
		return input, fmt.Errorf("launcher must have between 1 and %d slots", MaxAppLaunchSlots)
	}
	keys := make(map[string]bool, len(launcher.Slots))
	for _, slot := range launcher.Slots {
		if slot.Key == "" || len(slot.Key) > 64 || strings.TrimSpace(slot.Key) != slot.Key || keys[slot.Key] {
			return input, errors.New("launcher slot keys must be nonempty, distinct and at most 64 bytes")
		}
		keys[slot.Key] = true
		if (slot.AgentProfileID == nil) == (slot.AgentID == nil) ||
			(slot.AgentProfileID != nil && *slot.AgentProfileID == uuid.Nil) ||
			(slot.AgentID != nil && *slot.AgentID == uuid.Nil) {
			return input, errors.New("each launch slot requires exactly one profile or agent")
		}
	}
	return input, nil
}

func (s *Store) validateProjectAppReferences(
	ctx context.Context,
	tx pgx.Tx,
	input SaveProjectAppInput,
	secretReferences []projectAppSecretReference,
) (uuid.UUID, error) {
	var connectionID uuid.UUID
	if ref := input.Settings.Resource.Connection; ref != "" {
		var err error
		connectionID, err = publicid.Decode(publicid.KindIntegrationConnection, ref)
		if err != nil {
			return uuid.Nil, storeerr.InvalidRequest(err)
		}
		q := dbsqlc.New(tx)
		if err := q.LockIntegrationConnectionLifecycleShared(
			ctx,
			dbsqlc.LockIntegrationConnectionLifecycleSharedParams{ConnectionID: connectionID},
		); err != nil {
			return uuid.Nil, err
		}
		connection, err := getIntegrationConnection(ctx, q, input.ProjectID, connectionID)
		if err != nil {
			return uuid.Nil, err
		}
		definition, _ := appdefinition.Lookup(input.DefinitionID)
		if connection.Provider != definition.Provider || connection.State != IntegrationConnectionStateActive {
			return uuid.Nil, storeerr.ErrUnauthorized
		}
	}
	if err := s.lockProjectAppDestinations(ctx, tx, input); err != nil {
		return uuid.Nil, err
	}
	// Use the existing secret reference lock and availability query, including
	// ordinary project grants. Grant revocation/deletion locks the same secret;
	// re-read availability after waiting. Compiler callbacks only collect these
	// references so map iteration cannot impose inconsistent lock order.
	slices.SortFunc(secretReferences, func(a, b projectAppSecretReference) int {
		return slices.Compare(a.id[:], b.id[:])
	})
	q := dbsqlc.New(tx)
	for _, reference := range secretReferences {
		if _, err := secretops.LockReference(ctx, tx, input.OrgID, reference.id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return uuid.Nil, storeerr.ErrNotFound
			}
			return uuid.Nil, fmt.Errorf("lock app MCP secret: %w", err)
		}
		available, err := q.GetProjectAvailableSecret(ctx, dbsqlc.GetProjectAvailableSecretParams{
			OrgID: input.OrgID, ProjectID: input.ProjectID, SecretID: reference.id,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, storeerr.ErrNotFound
		}
		if err != nil {
			return uuid.Nil, fmt.Errorf("authorize app MCP secret: %w", err)
		}
		if available.Kind != string(reference.kind) {
			return uuid.Nil, storeerr.InvalidRequest(
				fmt.Errorf("app MCP secret kind %q does not match expected kind %q", available.Kind, reference.kind),
			)
		}
	}
	return connectionID, nil
}

func (s *Store) lockProjectAppDestinations(ctx context.Context, tx pgx.Tx, input SaveProjectAppInput) error {
	if launcher := input.Settings.Launcher; launcher != nil {
		if len(launcher.Slots) == 0 || len(launcher.Slots) > MaxAppLaunchSlots {
			return storeerr.InvalidRequest(fmt.Errorf("launcher must have between 1 and %d slots", MaxAppLaunchSlots))
		}
		for _, slot := range launcher.Slots {
			if (slot.AgentProfileID == nil) == (slot.AgentID == nil) ||
				(slot.AgentProfileID != nil && *slot.AgentProfileID == uuid.Nil) ||
				(slot.AgentID != nil && *slot.AgentID == uuid.Nil) {
				return storeerr.InvalidRequest(errors.New("each launch slot requires exactly one profile or agent"))
			}
		}
		// Profile locks precede agent locks. Stable order prevents two app edits
		// with the same destinations in a different slot order from deadlocking.
		slots := slices.Clone(launcher.Slots)
		slices.SortFunc(slots, func(a, b AppLaunchSlot) int {
			if a.AgentProfileID != nil && b.AgentProfileID == nil {
				return -1
			}
			if a.AgentProfileID == nil && b.AgentProfileID != nil {
				return 1
			}
			if a.AgentProfileID != nil {
				return strings.Compare(a.AgentProfileID.String(), b.AgentProfileID.String())
			}
			return strings.Compare(a.AgentID.String(), b.AgentID.String())
		})
		for _, slot := range slots {
			destination := AppDestination{OrgID: input.OrgID, ProjectID: input.ProjectID}
			if slot.AgentProfileID != nil {
				destination.AgentProfileID = *slot.AgentProfileID
			}
			if slot.AgentID != nil {
				destination.AgentID = *slot.AgentID
			}
			if err := s.access.ValidateAppDestination(ctx, tx, destination); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) GetProjectApp(ctx context.Context, projectID, id uuid.UUID) (ProjectAppRecord, error) {
	row, err := s.q.GetProjectApp(ctx, dbsqlc.GetProjectAppParams{ProjectID: projectID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectAppRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return ProjectAppRecord{}, err
	}
	return projectAppRecord(row)
}

func (s *Store) ListProjectApps(ctx context.Context, input ListProjectAppsInput) (ListProjectAppsResult, error) {
	if input.ProjectID == uuid.Nil || input.Limit < 1 || input.Limit > 100 {
		return ListProjectAppsResult{}, storeerr.InvalidRequest(
			errors.New("project and limit between 1 and 100 are required"),
		)
	}
	rows, err := s.q.ListProjectApps(ctx, dbsqlc.ListProjectAppsParams{
		ProjectID: input.ProjectID, NamePattern: input.NamePattern, RowLimit: int32(input.Limit + 1),
		CursorSet: input.After.Set, CursorCreatedAt: input.After.CreatedAt, CursorID: input.After.ID,
	})
	if err != nil {
		return ListProjectAppsResult{}, err
	}
	result := ListProjectAppsResult{
		Apps:    make([]ProjectAppRecord, 0, min(len(rows), input.Limit)),
		HasMore: len(rows) > input.Limit,
	}
	if result.HasMore {
		rows = rows[:input.Limit]
	}
	for _, row := range rows {
		record, err := projectAppRecord(row)
		if err != nil {
			return ListProjectAppsResult{}, err
		}
		result.Apps = append(result.Apps, record)
	}
	if result.HasMore {
		last := rows[len(rows)-1]
		result.Next = listing.KeysetCursor{Set: true, CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return result, nil
}

func (s *Store) DeleteProjectApp(ctx context.Context, orgID, projectID, id uuid.UUID) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lifecyclelock.EnterActiveProject(ctx, tx, orgID, projectID); err != nil {
		return err
	}
	rows, err := dbsqlc.New(tx).DeleteProjectApp(ctx, dbsqlc.DeleteProjectAppParams{ProjectID: projectID, ID: id})
	if err != nil {
		return err
	}
	if rows == 0 {
		return storeerr.ErrNotFound
	}
	return tx.Commit(ctx)
}

func projectAppRecord(row dbsqlc.ProjectApp) (ProjectAppRecord, error) {
	record := ProjectAppRecord{ID: row.ID, ProjectID: row.ProjectID, Name: row.Name, DefinitionID: row.DefinitionID,
		Enabled: row.Enabled, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if err := json.Unmarshal(row.Settings, &record.Settings); err != nil {
		return ProjectAppRecord{}, fmt.Errorf("decode app %s settings: %w", row.ID, err)
	}
	return record, nil
}
