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
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/resourceguard"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const MaxIntegrationLaunchSlots = 16

func (s *Store) CreateProjectIntegration(
	ctx context.Context,
	input SaveProjectIntegrationInput,
) (ProjectIntegrationRecord, error) {
	return s.saveProjectIntegration(ctx, uuid.Nil, input)
}

func (s *Store) UpdateProjectIntegration(
	ctx context.Context,
	id uuid.UUID,
	input SaveProjectIntegrationInput,
) (ProjectIntegrationRecord, error) {
	if id == uuid.Nil {
		return ProjectIntegrationRecord{}, storeerr.InvalidRequest(errors.New("integration id is required"))
	}
	return s.saveProjectIntegration(ctx, id, input)
}

func (s *Store) saveProjectIntegration(
	ctx context.Context,
	id uuid.UUID,
	input SaveProjectIntegrationInput,
) (ProjectIntegrationRecord, error) {
	input, err := normalizeProjectIntegration(input)
	if err != nil {
		return ProjectIntegrationRecord{}, storeerr.InvalidRequest(err)
	}
	settings, err := json.Marshal(input.Settings)
	if err != nil {
		return ProjectIntegrationRecord{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ProjectIntegrationRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	if err := lifecyclelock.EnterActiveProject(ctx, tx, input.OrgID, input.ProjectID); err != nil {
		return ProjectIntegrationRecord{}, err
	}
	if id != uuid.Nil {
		if err := q.LockProjectIntegrationLifecycleShared(ctx, dbsqlc.LockProjectIntegrationLifecycleSharedParams{
			IntegrationID: id,
		}); err != nil {
			return ProjectIntegrationRecord{}, err
		}
	}
	if err := s.lockProjectIntegrationDestinations(ctx, tx, input); err != nil {
		return ProjectIntegrationRecord{}, err
	}
	var row dbsqlc.ProjectIntegration
	if id == uuid.Nil {
		if err := resourceguard.Lock(ctx, q, "project_integrations", input.ProjectID.String()); err != nil {
			return ProjectIntegrationRecord{}, err
		}
		limits, limitErr := resourceguard.ResolveLimits(ctx, q, input.OrgID)
		if limitErr != nil {
			return ProjectIntegrationRecord{}, limitErr
		}
		count, countErr := q.CountProjectIntegrations(ctx, dbsqlc.CountProjectIntegrationsParams{ProjectID: input.ProjectID})
		if countErr != nil {
			return ProjectIntegrationRecord{}, countErr
		}
		if count >= limits.MaxActiveProjectIntegrationsPerProject {
			return ProjectIntegrationRecord{}, fmt.Errorf(
				"project integrations limit of %d reached: %w",
				limits.MaxActiveProjectIntegrationsPerProject,
				storeerr.ErrConflict,
			)
		}
		row, err = q.InsertProjectIntegration(
			ctx,
			dbsqlc.InsertProjectIntegrationParams{
				OrgID:           input.OrgID,
				ProjectID:       input.ProjectID,
				Name:            input.Name,
				IntegrationType: string(input.IntegrationType),
				Settings:        settings,
			},
		)
	} else {
		current, lockErr := q.LockProjectIntegration(ctx, dbsqlc.LockProjectIntegrationParams{
			ProjectID: input.ProjectID, ID: id,
		})
		if errors.Is(lockErr, pgx.ErrNoRows) {
			return ProjectIntegrationRecord{}, storeerr.ErrNotFound
		}
		if lockErr != nil {
			return ProjectIntegrationRecord{}, lockErr
		}
		if current.Name != input.Name || current.IntegrationType != string(input.IntegrationType) {
			return ProjectIntegrationRecord{}, storeerr.InvalidRequest(errors.New("integration name and type are immutable"))
		}
		if current.ProviderTenantID != nil && current.ProviderAccountRef != nil {
			provider := integrationdefinition.ProviderForType(input.IntegrationType)
			if err := validateLauncherProviderScope(input.Settings.Launcher, provider,
				*current.ProviderTenantID, *current.ProviderAccountRef); err != nil {
				return ProjectIntegrationRecord{}, storeerr.InvalidRequest(err)
			}
		}
		row, err = q.UpdateProjectIntegrationSettings(
			ctx,
			dbsqlc.UpdateProjectIntegrationSettingsParams{ProjectID: input.ProjectID, ID: id, Settings: settings},
		)
	}
	if storeutil.IsUniqueViolationOnConstraint(err, "project_integrations_name_idx") {
		return ProjectIntegrationRecord{}, storeerr.Tag(storeerr.ErrConflict,
			fmt.Errorf("an integration named %q already exists in this project; choose a different name", input.Name))
	}
	if err != nil {
		return ProjectIntegrationRecord{}, fmt.Errorf("save integration: %w", err)
	}
	record, err := projectIntegrationRecord(row)
	if err != nil {
		return ProjectIntegrationRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProjectIntegrationRecord{}, err
	}
	return record, nil
}

func (s *Store) GetProjectIntegration(ctx context.Context, projectID, id uuid.UUID) (ProjectIntegrationRecord, error) {
	return getProjectIntegration(ctx, s.q, projectID, id)
}

func getProjectIntegration(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, id uuid.UUID,
) (ProjectIntegrationRecord, error) {
	row, err := q.GetProjectIntegration(ctx, dbsqlc.GetProjectIntegrationParams{ProjectID: projectID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectIntegrationRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return ProjectIntegrationRecord{}, err
	}
	return projectIntegrationRecord(row)
}

func (s *Store) GetProjectIntegrationByName(
	ctx context.Context,
	projectID uuid.UUID,
	name string,
) (ProjectIntegrationRecord, error) {
	row, err := s.q.GetProjectIntegrationByName(ctx, dbsqlc.GetProjectIntegrationByNameParams{
		ProjectID: projectID, Name: name,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectIntegrationRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return ProjectIntegrationRecord{}, err
	}
	return projectIntegrationRecord(row)
}

func (s *Store) GetProjectIntegrationByID(ctx context.Context, id uuid.UUID) (ProjectIntegrationRecord, error) {
	return getProjectIntegrationByID(ctx, s.q, id)
}

func (s *Store) GetProjectIntegrationByIDTx(
	ctx context.Context,
	tx pgx.Tx,
	id uuid.UUID,
) (ProjectIntegrationRecord, error) {
	return getProjectIntegrationByID(ctx, dbsqlc.New(tx), id)
}

func getProjectIntegrationByID(ctx context.Context, q *dbsqlc.Queries, id uuid.UUID) (ProjectIntegrationRecord, error) {
	row, err := q.GetProjectIntegrationByID(ctx, dbsqlc.GetProjectIntegrationByIDParams{ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectIntegrationRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return ProjectIntegrationRecord{}, err
	}
	return projectIntegrationRecord(row)
}

func projectIntegrationRecord(row dbsqlc.ProjectIntegration) (ProjectIntegrationRecord, error) {
	definition, ok := integrationdefinition.Lookup(integrationdefinition.Type(row.IntegrationType))
	if !ok {
		return ProjectIntegrationRecord{}, fmt.Errorf("integration %s has unregistered type %q", row.ID, row.IntegrationType)
	}
	record := ProjectIntegrationRecord{
		ID:                       row.ID,
		OrgID:                    row.OrgID,
		ProjectID:                row.ProjectID,
		InstalledByUserID:        storeutil.IDFromPtr(row.InstalledByUserID),
		Name:                     row.Name,
		IntegrationType:          definition.IntegrationType,
		Provider:                 definition.Provider,
		State:                    ProjectIntegrationState(row.State),
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
		return ProjectIntegrationRecord{}, fmt.Errorf("decode integration %s settings: %w", row.ID, err)
	}
	return record, nil
}

func normalizeProjectIntegration(input SaveProjectIntegrationInput) (SaveProjectIntegrationInput, error) {
	if input.OrgID == uuid.Nil || input.ProjectID == uuid.Nil {
		return input, errors.New("organization and project are required")
	}
	if err := toolcatalog.ValidateIntegrationName(input.Name); err != nil {
		return input, err
	}
	definition, ok := integrationdefinition.Lookup(input.IntegrationType)
	if !ok {
		return input, errors.New("unknown integration type")
	}
	launcher := input.Settings.Launcher
	if launcher == nil {
		return input, nil
	}
	kind, ref, err := integrationdefinition.CanonicalLauncherScope(
		definition.Provider, launcher.ScopeKind, launcher.ScopeRef,
	)
	if err != nil {
		return input, err
	}
	canonical := *launcher
	canonical.ScopeKind, canonical.ScopeRef = kind, ref
	input.Settings.Launcher = &canonical
	launcher = &canonical
	if !definition.SupportsLaunchTrigger(launcher.Trigger) {
		return input, errors.New("unsupported integration launch trigger")
	}
	if len(launcher.Slots) == 0 || len(launcher.Slots) > MaxIntegrationLaunchSlots {
		return input, fmt.Errorf("launcher must have between 1 and %d slots", MaxIntegrationLaunchSlots)
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

func (s *Store) lockProjectIntegrationDestinations(
	ctx context.Context,
	tx pgx.Tx,
	input SaveProjectIntegrationInput,
) error {
	if launcher := input.Settings.Launcher; launcher != nil {
		slots := slices.Clone(launcher.Slots)
		slices.SortFunc(slots, func(a, b IntegrationLaunchSlot) int {
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
			destination := IntegrationDestination{OrgID: input.OrgID, ProjectID: input.ProjectID}
			if slot.AgentProfileID != nil {
				destination.AgentProfileID = *slot.AgentProfileID
			}
			if slot.AgentID != nil {
				destination.AgentID = *slot.AgentID
			}
			if err := s.access.ValidateIntegrationDestination(ctx, tx, destination); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) ListProjectIntegrations(
	ctx context.Context,
	input ListProjectIntegrationsInput,
) (ListProjectIntegrationsResult, error) {
	if input.ProjectID == uuid.Nil || input.Limit < 1 || input.Limit > 100 {
		return ListProjectIntegrationsResult{}, storeerr.InvalidRequest(
			errors.New("project and limit between 1 and 100 are required"),
		)
	}
	rows, err := s.q.ListProjectIntegrations(ctx, dbsqlc.ListProjectIntegrationsParams{
		ProjectID: input.ProjectID, NamePattern: input.NamePattern, RowLimit: int32(input.Limit + 1),
		CursorSet: input.After.Set, CursorCreatedAt: input.After.CreatedAt, CursorID: input.After.ID,
	})
	if err != nil {
		return ListProjectIntegrationsResult{}, err
	}
	result := ListProjectIntegrationsResult{
		Integrations: make([]ProjectIntegrationRecord, 0, min(len(rows), input.Limit)),
		HasMore:      len(rows) > input.Limit,
	}
	if result.HasMore {
		rows = rows[:input.Limit]
	}
	for _, row := range rows {
		record, err := projectIntegrationRecord(row)
		if err != nil {
			return ListProjectIntegrationsResult{}, err
		}
		result.Integrations = append(result.Integrations, record)
	}
	if result.HasMore {
		last := rows[len(rows)-1]
		result.Next = listing.KeysetCursor{Set: true, CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return result, nil
}

func (s *Store) ListProjectIntegrationsByProviderIdentity(
	ctx context.Context, provider, tenant, account string, after uuid.UUID, limit int,
) ([]ProjectIntegrationRecord, error) {
	if provider == "" || tenant == "" || account == "" || limit < 1 || limit > 100 {
		return nil, storeerr.InvalidRequest(errors.New("provider identity and limit between 1 and 100 are required"))
	}
	return s.listProjectIntegrationsByProviderIdentity(ctx, provider, tenant, &account, after, limit, false)
}

func (s *Store) ListProjectIntegrationsForProviderEventVerification(
	ctx context.Context, provider, tenant, account string, after uuid.UUID, limit int,
) ([]ProjectIntegrationRecord, error) {
	// Disconnected integrations retain credentials so ingress can authenticate acknowledgements.
	if provider == "" || tenant == "" || account == "" || limit < 1 || limit > 100 {
		return nil, storeerr.InvalidRequest(errors.New("provider identity and limit between 1 and 100 are required"))
	}
	return s.listProjectIntegrationsByProviderIdentity(ctx, provider, tenant, &account, after, limit, true)
}

func (s *Store) ListProjectIntegrationsByProviderTenant(
	ctx context.Context,
	provider, tenant string,
	after uuid.UUID,
	limit int,
) ([]ProjectIntegrationRecord, error) {
	if provider == "" || tenant == "" || limit < 1 || limit > 100 {
		return nil, storeerr.InvalidRequest(errors.New("provider tenant and limit between 1 and 100 are required"))
	}
	return s.listProjectIntegrationsByProviderIdentity(ctx, provider, tenant, nil, after, limit, false)
}

func (s *Store) listProjectIntegrationsByProviderIdentity(
	ctx context.Context,
	provider, tenant string,
	account *string,
	after uuid.UUID,
	limit int,
	includeDisconnected bool,
) ([]ProjectIntegrationRecord, error) {
	rows, err := s.q.ListProjectIntegrationsByProviderIdentity(ctx, dbsqlc.ListProjectIntegrationsByProviderIdentityParams{
		IntegrationTypes: integrationdefinition.IntegrationTypesForProvider(provider),
		ProviderTenantID: &tenant, ProviderAccountRef: account,
		AfterID: storeutil.IDFromNil(after), RowLimit: int32(limit),
		IncludeDisconnected: includeDisconnected,
	})
	if err != nil {
		return nil, err
	}
	integrations := make([]ProjectIntegrationRecord, 0, len(rows))
	for _, row := range rows {
		integration, err := projectIntegrationRecord(row)
		if err != nil {
			return nil, err
		}
		integrations = append(integrations, integration)
	}
	return integrations, nil
}
