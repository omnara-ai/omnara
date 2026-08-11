package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/resourcemeta"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	ActorProviderOmnara   = "omnara"
	ActorProviderSlack    = "slack"
	ActorProviderExternal = "external"
)

type ActorRecord struct {
	ID               ID              `json:"id"`
	ProjectID        ID              `json:"project_id"`
	Provider         string          `json:"provider"`
	ProviderTenantID string          `json:"provider_tenant_id,omitempty"`
	ProviderUserID   string          `json:"provider_user_id"`
	DisplayName      string          `json:"display_name"`
	Metadata         json.RawMessage `json:"metadata"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

type UpsertActorIdentityInput struct {
	ProjectID        ID
	Provider         string
	ProviderTenantID string
	ProviderUserID   string
	DisplayName      string
}

func upsertActorIdentityTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input UpsertActorIdentityInput,
) (ActorRecord, error) {
	provider := strings.TrimSpace(input.Provider)
	providerTenantID := strings.TrimSpace(input.ProviderTenantID)
	providerUserID := strings.TrimSpace(input.ProviderUserID)
	if isNilID(input.ProjectID) || provider == "" || providerUserID == "" {
		return ActorRecord{}, errors.New("project, provider, and provider user id are required")
	}
	if provider != ActorProviderExternal && providerTenantID == "" {
		return ActorRecord{}, errors.New("provider tenant id is required for non-external actors")
	}
	row, err := qtx.UpsertActorIdentity(ctx, dbsqlc.UpsertActorIdentityParams{
		ProjectID:        input.ProjectID,
		Provider:         provider,
		ProviderTenantID: sqlcTextFromEmpty(providerTenantID),
		ProviderUserID:   providerUserID,
		DisplayName:      strings.TrimSpace(input.DisplayName),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The actor exists and nothing changed, so the upsert skipped the
		// write; read the current row instead.
		row, err = qtx.GetActorByIdentity(ctx, dbsqlc.GetActorByIdentityParams{
			ProjectID:        input.ProjectID,
			Provider:         provider,
			ProviderTenantID: sqlcTextFromEmpty(providerTenantID),
			ProviderUserID:   providerUserID,
		})
	}
	if err != nil {
		return ActorRecord{}, fmt.Errorf("upsert actor: %w", err)
	}
	return actorRecordFromSQLC(row), nil
}

type PutActorInput struct {
	ProjectID        ID
	ProviderTenantID string
	ProviderUserID   string
	// DisplayName and Metadata overwrite the stored attributes only when
	// set, including when set to an empty value; when unset the stored
	// values are kept.
	DisplayName *string
	Metadata    resourcemeta.Metadata
}

const (
	// MaxActorProviderTenantIDLength bounds actor provider tenant ids, in characters.
	MaxActorProviderTenantIDLength = 128
	// MaxActorProviderUserIDLength bounds actor provider user ids, in characters.
	MaxActorProviderUserIDLength = 128
	// MaxActorDisplayNameLength bounds actor display names, in characters.
	MaxActorDisplayNameLength = 256
)

// PutActor upserts an external actor by (provider_tenant_id,
// provider_user_id). Unset attributes keep their stored values, set
// attributes are overwritten, and an unchanged actor is not rewritten.
func (s *Store) PutActor(ctx context.Context, input PutActorInput) (ActorRecord, error) {
	if err := validatePutActorInput(input); err != nil {
		return ActorRecord{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ActorRecord{}, fmt.Errorf("begin put actor: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if err := enterActiveActorProjectTx(ctx, tx, qtx, input.ProjectID); err != nil {
		return ActorRecord{}, err
	}
	record, err := putActorTx(ctx, qtx, input)
	if err != nil {
		return ActorRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ActorRecord{}, fmt.Errorf("commit put actor: %w", err)
	}
	return record, nil
}

func enterActiveActorProjectTx(
	ctx context.Context,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	projectID ID,
) error {
	project, err := loadProjectTx(ctx, qtx, projectID)
	if err != nil {
		return err
	}
	return lifecyclelock.EnterActiveProject(ctx, tx, project.OrgID, projectID)
}

func putActorTx(ctx context.Context, q *dbsqlc.Queries, input PutActorInput) (ActorRecord, error) {
	if err := validatePutActorInput(input); err != nil {
		return ActorRecord{}, err
	}
	providerTenantID := strings.TrimSpace(input.ProviderTenantID)
	providerUserID := strings.TrimSpace(input.ProviderUserID)
	displayName := ""
	if input.DisplayName != nil {
		displayName = strings.TrimSpace(*input.DisplayName)
	}
	metadataSet := input.Metadata != nil
	metadata, err := input.Metadata.JSON()
	if err != nil {
		return ActorRecord{}, err
	}
	row, err := q.PutActor(ctx, dbsqlc.PutActorParams{
		ProjectID:        input.ProjectID,
		ProviderTenantID: sqlcTextFromEmpty(providerTenantID),
		ProviderUserID:   providerUserID,
		DisplayName:      displayName,
		DisplayNameSet:   input.DisplayName != nil,
		Metadata:         metadata,
		MetadataSet:      metadataSet,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The actor exists and nothing changed, so the upsert skipped the
		// write; read the current row instead.
		row, err = q.GetActorByIdentity(ctx, dbsqlc.GetActorByIdentityParams{
			ProjectID:        input.ProjectID,
			Provider:         ActorProviderExternal,
			ProviderTenantID: sqlcTextFromEmpty(providerTenantID),
			ProviderUserID:   providerUserID,
		})
	}
	if err != nil {
		return ActorRecord{}, fmt.Errorf("put actor: %w", err)
	}
	return actorRecordFromSQLC(row), nil
}

func validatePutActorInput(input PutActorInput) error {
	providerTenantID := strings.TrimSpace(input.ProviderTenantID)
	providerUserID := strings.TrimSpace(input.ProviderUserID)
	if isNilID(input.ProjectID) {
		return errors.New("project is required")
	}
	if providerUserID == "" {
		return fmt.Errorf("%w: provider user id is required", storeerr.ErrInvalidActorRequest)
	}
	if utf8.RuneCountInString(providerTenantID) > MaxActorProviderTenantIDLength {
		return fmt.Errorf(
			"%w: provider tenant id must be at most %d characters",
			storeerr.ErrInvalidActorRequest, MaxActorProviderTenantIDLength,
		)
	}
	if utf8.RuneCountInString(providerUserID) > MaxActorProviderUserIDLength {
		return fmt.Errorf(
			"%w: provider user id must be at most %d characters",
			storeerr.ErrInvalidActorRequest, MaxActorProviderUserIDLength,
		)
	}
	displayName := ""
	if input.DisplayName != nil {
		displayName = strings.TrimSpace(*input.DisplayName)
	}
	if utf8.RuneCountInString(displayName) > MaxActorDisplayNameLength {
		return fmt.Errorf(
			"%w: display name must be at most %d characters",
			storeerr.ErrInvalidActorRequest, MaxActorDisplayNameLength,
		)
	}
	if input.Metadata != nil {
		if err := input.Metadata.Validate(); err != nil {
			return fmt.Errorf("%w: %w", storeerr.ErrInvalidActorRequest, err)
		}
	}
	return nil
}

func (s *Store) GetActor(ctx context.Context, projectID, actorID ID) (ActorRecord, error) {
	if isNilID(projectID) || isNilID(actorID) {
		return ActorRecord{}, errors.New("project and actor are required")
	}
	row, err := s.q.GetActor(ctx, dbsqlc.GetActorParams{ProjectID: projectID, ID: actorID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ActorRecord{}, storeerr.ErrNotFound
		}
		return ActorRecord{}, fmt.Errorf("get actor: %w", err)
	}
	return actorRecordFromSQLC(row), nil
}

type ListActorsInput struct {
	ProjectID        ID
	Provider         string
	ProviderTenantID string
	ProviderUserID   string
	After            listing.KeysetCursor
	Limit            int
}

func (s *Store) ListActors(ctx context.Context, input ListActorsInput) ([]ActorRecord, error) {
	if isNilID(input.ProjectID) {
		return nil, errors.New("project is required")
	}
	limit := input.Limit
	if limit <= 0 {
		limit = 100
	}
	var cursorCreatedAt *time.Time
	var cursorID *ID
	if input.After.Set {
		cursorCreatedAt = &input.After.CreatedAt
		cursorID = &input.After.ID
	}
	rows, err := s.q.ListActors(ctx, dbsqlc.ListActorsParams{
		ProjectID:        input.ProjectID,
		Provider:         strings.TrimSpace(input.Provider),
		ProviderTenantID: strings.TrimSpace(input.ProviderTenantID),
		ProviderUserID:   strings.TrimSpace(input.ProviderUserID),
		CursorCreatedAt:  cursorCreatedAt,
		CursorID:         cursorID,
		RowLimit:         int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list actors: %w", err)
	}
	records := make([]ActorRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, actorRecordFromSQLC(row))
	}
	return records, nil
}

func (s *Store) ListActorDisplayNames(
	ctx context.Context,
	projectID ID,
	provider, providerTenantID string,
	providerUserIDs []string,
) (map[string]string, error) {
	if isNilID(projectID) || provider == "" {
		return nil, errors.New("project and provider are required")
	}
	if len(providerUserIDs) == 0 {
		return map[string]string{}, nil
	}
	rows, err := s.q.ListActorDisplayNames(
		ctx,
		dbsqlc.ListActorDisplayNamesParams{
			ProjectID:        projectID,
			Provider:         provider,
			ProviderTenantID: sqlcTextFromEmpty(providerTenantID),
			ProviderUserIds:  providerUserIDs,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list actor display names: %w", err)
	}
	names := make(map[string]string, len(rows))
	for _, row := range rows {
		names[row.ProviderUserID] = row.DisplayName
	}
	return names, nil
}

type UpdateActorDisplayNameInput struct {
	ProjectID        ID
	Provider         string
	ProviderTenantID string
	ProviderUserID   string
	DisplayName      string
}

func (s *Store) UpdateActorDisplayName(
	ctx context.Context,
	input UpdateActorDisplayNameInput,
) error {
	if isNilID(input.ProjectID) || input.Provider == "" || input.ProviderUserID == "" ||
		strings.TrimSpace(input.DisplayName) == "" {
		return errors.New("project, provider, provider user id, and display name are required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin update actor display name: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if err := enterActiveActorProjectTx(ctx, tx, qtx, input.ProjectID); err != nil {
		return err
	}
	_, err = qtx.UpdateActorDisplayName(
		ctx,
		dbsqlc.UpdateActorDisplayNameParams{
			ProjectID:        input.ProjectID,
			Provider:         input.Provider,
			ProviderTenantID: sqlcTextFromEmpty(input.ProviderTenantID),
			ProviderUserID:   input.ProviderUserID,
			DisplayName:      strings.TrimSpace(input.DisplayName),
		},
	)
	if err != nil {
		return fmt.Errorf("update actor display name: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit update actor display name: %w", err)
	}
	return nil
}

func actorRecordFromSQLC(row dbsqlc.Actor) ActorRecord {
	return ActorRecord{
		ID:               row.ID,
		ProjectID:        row.ProjectID,
		Provider:         row.Provider,
		ProviderTenantID: stringFromSQLCText(row.ProviderTenantID),
		ProviderUserID:   row.ProviderUserID,
		DisplayName:      stringFromSQLCText(row.DisplayName),
		Metadata:         row.Metadata,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
}
