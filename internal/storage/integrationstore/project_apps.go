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
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/resourceguard"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const MaxAppLaunchSlots = 16

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
	input, err := normalizeProjectApp(input)
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
	if id != uuid.Nil {
		if err := q.LockProjectAppLifecycleShared(ctx, dbsqlc.LockProjectAppLifecycleSharedParams{AppID: id}); err != nil {
			return ProjectAppRecord{}, err
		}
	}
	// Profile/agent locks precede the app row, just as they do during launch.
	if err := s.lockProjectAppDestinations(ctx, tx, input); err != nil {
		return ProjectAppRecord{}, err
	}
	var row dbsqlc.ProjectApp
	if id == uuid.Nil {
		if err := resourceguard.Lock(ctx, q, "project_apps", input.ProjectID.String()); err != nil {
			return ProjectAppRecord{}, err
		}
		limits, limitErr := resourceguard.ResolveLimits(ctx, q, input.OrgID)
		if limitErr != nil {
			return ProjectAppRecord{}, limitErr
		}
		count, countErr := q.CountProjectApps(ctx, dbsqlc.CountProjectAppsParams{ProjectID: input.ProjectID})
		if countErr != nil {
			return ProjectAppRecord{}, countErr
		}
		if count >= limits.MaxActiveProjectAppsPerProject {
			return ProjectAppRecord{}, fmt.Errorf(
				"project apps limit of %d reached: %w",
				limits.MaxActiveProjectAppsPerProject,
				storeerr.ErrConflict,
			)
		}
		row, err = q.InsertProjectApp(
			ctx,
			dbsqlc.InsertProjectAppParams{
				OrgID:     input.OrgID,
				ProjectID: input.ProjectID,
				Name:      input.Name,
				AppType:   string(input.AppType),
				Settings:  settings,
			},
		)
	} else {
		current, lockErr := q.LockProjectApp(ctx, dbsqlc.LockProjectAppParams{ProjectID: input.ProjectID, ID: id})
		if errors.Is(lockErr, pgx.ErrNoRows) {
			return ProjectAppRecord{}, storeerr.ErrNotFound
		}
		if lockErr != nil {
			return ProjectAppRecord{}, lockErr
		}
		if current.Name != input.Name || current.AppType != string(input.AppType) {
			return ProjectAppRecord{}, storeerr.InvalidRequest(errors.New("app name and type are immutable"))
		}
		if current.ProviderTenantID != nil && current.ProviderAccountRef != nil {
			provider := appdefinition.ProviderForType(input.AppType)
			if err := validateLauncherProviderScope(input.Settings.Launcher, provider,
				*current.ProviderTenantID, *current.ProviderAccountRef); err != nil {
				return ProjectAppRecord{}, storeerr.InvalidRequest(err)
			}
		}
		row, err = q.UpdateProjectAppSettings(
			ctx,
			dbsqlc.UpdateProjectAppSettingsParams{ProjectID: input.ProjectID, ID: id, Settings: settings},
		)
	}
	if storeutil.IsUniqueViolationOnConstraint(err, "project_apps_name_idx") {
		return ProjectAppRecord{}, storeerr.Tag(storeerr.ErrConflict,
			fmt.Errorf("an app named %q already exists in this project; choose a different name", input.Name))
	}
	if err != nil {
		return ProjectAppRecord{}, fmt.Errorf("save app: %w", err)
	}
	record, err := projectAppRecord(row)
	if err != nil {
		return ProjectAppRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProjectAppRecord{}, err
	}
	return record, nil
}

func (s *Store) GetProjectApp(ctx context.Context, projectID, id uuid.UUID) (ProjectAppRecord, error) {
	return getProjectApp(ctx, s.q, projectID, id)
}

func getProjectApp(ctx context.Context, q *dbsqlc.Queries, projectID, id uuid.UUID) (ProjectAppRecord, error) {
	row, err := q.GetProjectApp(ctx, dbsqlc.GetProjectAppParams{ProjectID: projectID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectAppRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return ProjectAppRecord{}, err
	}
	return projectAppRecord(row)
}

func (s *Store) GetProjectAppByName(ctx context.Context, projectID uuid.UUID, name string) (ProjectAppRecord, error) {
	row, err := s.q.GetProjectAppByName(ctx, dbsqlc.GetProjectAppByNameParams{ProjectID: projectID, Name: name})
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectAppRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return ProjectAppRecord{}, err
	}
	return projectAppRecord(row)
}

func (s *Store) GetProjectAppByID(ctx context.Context, id uuid.UUID) (ProjectAppRecord, error) {
	return getProjectAppByID(ctx, s.q, id)
}

func (s *Store) GetProjectAppByIDTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (ProjectAppRecord, error) {
	return getProjectAppByID(ctx, dbsqlc.New(tx), id)
}

func getProjectAppByID(ctx context.Context, q *dbsqlc.Queries, id uuid.UUID) (ProjectAppRecord, error) {
	row, err := q.GetProjectAppByID(ctx, dbsqlc.GetProjectAppByIDParams{ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectAppRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return ProjectAppRecord{}, err
	}
	return projectAppRecord(row)
}

func projectAppRecord(row dbsqlc.ProjectApp) (ProjectAppRecord, error) {
	definition, ok := appdefinition.Lookup(appdefinition.Type(row.AppType))
	if !ok {
		return ProjectAppRecord{}, fmt.Errorf("app %s has unregistered type %q", row.ID, row.AppType)
	}
	record := ProjectAppRecord{
		ID:                       row.ID,
		OrgID:                    row.OrgID,
		ProjectID:                row.ProjectID,
		InstalledByUserID:        storeutil.IDFromPtr(row.InstalledByUserID),
		Name:                     row.Name,
		AppType:                  definition.AppType,
		Provider:                 definition.Provider,
		State:                    ProjectAppState(row.State),
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
	if err := json.Unmarshal(row.Settings, &record.Settings); err != nil {
		return ProjectAppRecord{}, fmt.Errorf("decode app %s settings: %w", row.ID, err)
	}
	return record, nil
}

func normalizeProjectApp(input SaveProjectAppInput) (SaveProjectAppInput, error) {
	if input.OrgID == uuid.Nil || input.ProjectID == uuid.Nil {
		return input, errors.New("organization and project are required")
	}
	if err := toolcatalog.ValidateAppName(input.Name); err != nil {
		return input, err
	}
	definition, ok := appdefinition.Lookup(input.AppType)
	if !ok {
		return input, errors.New("unknown app type")
	}
	launcher := input.Settings.Launcher
	if launcher == nil {
		return input, nil
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

func (s *Store) lockProjectAppDestinations(ctx context.Context, tx pgx.Tx, input SaveProjectAppInput) error {
	if launcher := input.Settings.Launcher; launcher != nil {
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

// ListProjectAppsByProviderIdentity is the private ingress lookup. Callers page
// through every active setup and verify each app's credential independently.
func (s *Store) ListProjectAppsByProviderIdentity(
	ctx context.Context, provider, tenant, account string, after uuid.UUID, limit int,
) ([]ProjectAppRecord, error) {
	if provider == "" || tenant == "" || account == "" || limit < 1 || limit > 100 {
		return nil, storeerr.InvalidRequest(errors.New("provider identity and limit between 1 and 100 are required"))
	}
	return s.listProjectAppsByProviderIdentity(ctx, provider, tenant, &account, after, limit, false)
}

// ListProjectAppsForProviderEventVerification also includes disconnected apps
// with retained credentials. These candidates permit authenticated acknowledgement,
// never admission or other mutations while disconnected.
func (s *Store) ListProjectAppsForProviderEventVerification(
	ctx context.Context, provider, tenant, account string, after uuid.UUID, limit int,
) ([]ProjectAppRecord, error) {
	if provider == "" || tenant == "" || account == "" || limit < 1 || limit > 100 {
		return nil, storeerr.InvalidRequest(errors.New("provider identity and limit between 1 and 100 are required"))
	}
	return s.listProjectAppsByProviderIdentity(ctx, provider, tenant, &account, after, limit, true)
}

// ListProjectAppsByProviderTenant resolves setup for provider callbacks that do
// not carry an account ID, such as Discord endpoint verification.
func (s *Store) ListProjectAppsByProviderTenant(
	ctx context.Context,
	provider, tenant string,
	after uuid.UUID,
	limit int,
) ([]ProjectAppRecord, error) {
	if provider == "" || tenant == "" || limit < 1 || limit > 100 {
		return nil, storeerr.InvalidRequest(errors.New("provider tenant and limit between 1 and 100 are required"))
	}
	return s.listProjectAppsByProviderIdentity(ctx, provider, tenant, nil, after, limit, false)
}

func (s *Store) listProjectAppsByProviderIdentity(
	ctx context.Context,
	provider, tenant string,
	account *string,
	after uuid.UUID,
	limit int,
	includeDisconnected bool,
) ([]ProjectAppRecord, error) {
	rows, err := s.q.ListProjectAppsByProviderIdentity(ctx, dbsqlc.ListProjectAppsByProviderIdentityParams{
		AppTypes: appdefinition.AppTypesForProvider(provider), ProviderTenantID: &tenant, ProviderAccountRef: account,
		AfterID: storeutil.IDFromNil(after), RowLimit: int32(limit),
		IncludeDisconnected: includeDisconnected,
	})
	if err != nil {
		return nil, err
	}
	apps := make([]ProjectAppRecord, 0, len(rows))
	for _, row := range rows {
		app, err := projectAppRecord(row)
		if err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return apps, nil
}
