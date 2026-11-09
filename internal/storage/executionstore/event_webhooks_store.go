package executionstore

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	webhookconfig "github.com/omnara-ai/omnara/internal/eventwebhook/config"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/resourceguard"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const MaxEventWebhooksPerProject = 100

type EventWebhookScopeType string

const (
	EventWebhookScopeProject      EventWebhookScopeType = "project"
	EventWebhookScopeAgentProfile EventWebhookScopeType = "profile"
	EventWebhookScopeAgent        EventWebhookScopeType = "agent"
)

type EventWebhookScope struct {
	Type EventWebhookScopeType
	ID   uuid.UUID
}

func (scope EventWebhookScope) Validate() error {
	switch scope.Type {
	case EventWebhookScopeProject:
		if scope.ID == uuid.Nil {
			return nil
		}
	case EventWebhookScopeAgentProfile, EventWebhookScopeAgent:
		if scope.ID != uuid.Nil {
			return nil
		}
	}
	return storeerr.InvalidRequest(errors.New("invalid webhook scope"))
}

type EventWebhookRecord struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	ProjectID uuid.UUID
	Scope     EventWebhookScope
	Settings  webhookconfig.Settings
	CreatedAt time.Time
	UpdatedAt time.Time
}

type CreateEventWebhookInput struct {
	ProjectID uuid.UUID
	Scope     EventWebhookScope
	Settings  webhookconfig.Settings
}

type ListEventWebhooksInput struct {
	ProjectID uuid.UUID
	Scope     *EventWebhookScope
	Limit     int
	After     listing.KeysetCursor
}

type ListEventWebhooksResult struct {
	Webhooks []EventWebhookRecord
	HasMore  bool
}

type UpdateEventWebhookInput struct {
	Scope           *EventWebhookScope
	ProjectID       uuid.UUID
	WebhookID       uuid.UUID
	URL             *string
	EventTypes      []string
	SigningSecretID *uuid.UUID
}

func eventWebhookRecordFromSQLC(row dbsqlc.EventWebhook) EventWebhookRecord {
	return EventWebhookRecord{
		ID: row.ID, OrgID: row.OrgID, ProjectID: row.ProjectID,
		Scope: eventWebhookScopeFromColumns(row.AgentProfileID, row.AgentID),
		Settings: webhookconfig.Settings{URL: row.Url, EventTypes: row.EventTypes,
			SigningSecretID: storeutil.IDFromPtr(row.SigningSecretID)},
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func eventWebhookScopeFromColumns(agentProfileID, agentID *uuid.UUID) EventWebhookScope {
	if agentProfileID != nil {
		return EventWebhookScope{Type: EventWebhookScopeAgentProfile, ID: *agentProfileID}
	}
	if agentID != nil {
		return EventWebhookScope{Type: EventWebhookScopeAgent, ID: *agentID}
	}
	return EventWebhookScope{Type: EventWebhookScopeProject}
}

func validateEventWebhookScopeTx(ctx context.Context, tx pgx.Tx, projectID uuid.UUID, scope EventWebhookScope) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	q := dbsqlc.New(tx)
	if scope.Type == EventWebhookScopeAgentProfile {
		_, err := lockAgentProfileTx(ctx, q, projectID, scope.ID)
		return err
	}
	if scope.Type == EventWebhookScopeAgent {
		_, err := q.GetAgentInProject(ctx, dbsqlc.GetAgentInProjectParams{ProjectID: projectID, ID: scope.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrNotFound
		}
		return err
	}
	return nil
}

func (s *Store) validateEventWebhookSettingsTx(
	ctx context.Context,
	tx pgx.Tx,
	orgID, projectID uuid.UUID,
	settings webhookconfig.Settings,
) (webhookconfig.Settings, error) {
	settings, err := webhookconfig.Validate(settings)
	if err != nil {
		return webhookconfig.Settings{}, storeerr.InvalidRequest(err)
	}
	if settings.SigningSecretID != uuid.Nil {
		if s.secrets == nil {
			return webhookconfig.Settings{}, errors.New("secret store is required")
		}
		secret, err := s.secrets.ReadProjectAvailableSecretPayloadTx(ctx, tx,
			secretstore.ReadProjectAvailableSecretPayloadInput{
				OrgID: orgID, ProjectID: projectID,
				SecretID: settings.SigningSecretID, Kind: secrets.KindGeneric,
			})
		if err != nil {
			return webhookconfig.Settings{}, err
		}
		if _, err := webhookconfig.DecodeSigningKey(secret.Payload[secrets.KeyValue]); err != nil {
			return webhookconfig.Settings{}, storeerr.InvalidRequest(err)
		}
	}
	return settings, nil
}

func (s *Store) CreateEventWebhook(ctx context.Context, input CreateEventWebhookInput) (EventWebhookRecord, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return EventWebhookRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	project, err := loadProjectTx(ctx, q, input.ProjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return EventWebhookRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return EventWebhookRecord{}, err
	}
	if err := lifecyclelock.EnterActiveProject(ctx, tx, project.OrgID, input.ProjectID); err != nil {
		return EventWebhookRecord{}, err
	}
	if err := lockResourceCreation(ctx, q, "event_webhooks", input.ProjectID.String()); err != nil {
		return EventWebhookRecord{}, err
	}
	if err := validateEventWebhookScopeTx(ctx, tx, input.ProjectID, input.Scope); err != nil {
		return EventWebhookRecord{}, err
	}
	settings, err := s.validateEventWebhookSettingsTx(ctx, tx, project.OrgID, input.ProjectID, input.Settings)
	if err != nil {
		return EventWebhookRecord{}, err
	}
	existing, err := q.CountProjectEventWebhooks(ctx, dbsqlc.CountProjectEventWebhooksParams{ProjectID: input.ProjectID})
	if err != nil {
		return EventWebhookRecord{}, err
	}
	if existing >= MaxEventWebhooksPerProject {
		return EventWebhookRecord{}, resourceguard.LimitExceeded("event webhooks per project", MaxEventWebhooksPerProject)
	}
	profileID, agentID := input.Scope.columns()
	row, err := q.InsertEventWebhook(ctx, dbsqlc.InsertEventWebhookParams{
		OrgID: project.OrgID, ProjectID: input.ProjectID,
		AgentProfileID: profileID, AgentID: agentID,
		Url: settings.URL, EventTypes: settings.EventTypes, SigningSecretID: storeutil.IDFromNil(settings.SigningSecretID),
	})
	if storeutil.IsUniqueViolationOnConstraint(err, "event_webhooks_active_scope_url_idx") {
		return EventWebhookRecord{}, storeerr.Tag(storeerr.ErrConflict,
			errors.New("a webhook with this URL already exists on this resource"))
	}
	if err != nil {
		return EventWebhookRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return EventWebhookRecord{}, err
	}
	return eventWebhookRecordFromSQLC(row), nil
}

func (scope EventWebhookScope) columns() (profileID, agentID *uuid.UUID) {
	switch scope.Type {
	case EventWebhookScopeProject:
	case EventWebhookScopeAgentProfile:
		profileID = &scope.ID
	case EventWebhookScopeAgent:
		agentID = &scope.ID
	}
	return
}

func (s *Store) ListEventWebhooks(
	ctx context.Context, input ListEventWebhooksInput,
) (ListEventWebhooksResult, error) {
	if input.ProjectID == uuid.Nil || input.Limit < 1 || input.Limit > 100 {
		return ListEventWebhooksResult{}, storeerr.InvalidRequest(
			errors.New("project and limit between 1 and 100 are required"))
	}
	params := dbsqlc.ListEventWebhooksParams{ProjectID: input.ProjectID, RowLimit: int32(input.Limit) + 1}
	if scope := input.Scope; scope != nil {
		if err := scope.Validate(); err != nil {
			return ListEventWebhooksResult{}, err
		}
		var err error
		switch scope.Type {
		case EventWebhookScopeProject:
		case EventWebhookScopeAgentProfile:
			_, err = s.q.GetAgentProfile(ctx, dbsqlc.GetAgentProfileParams{ProjectID: input.ProjectID, ID: scope.ID})
		case EventWebhookScopeAgent:
			_, err = s.q.GetAgentInProject(ctx, dbsqlc.GetAgentInProjectParams{ProjectID: input.ProjectID, ID: scope.ID})
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return ListEventWebhooksResult{}, storeerr.ErrNotFound
		}
		if err != nil {
			return ListEventWebhooksResult{}, err
		}
		params.FilterScope = true
		params.AgentProfileID, params.AgentID = scope.columns()
	}
	if input.After.Set {
		params.CursorCreatedAt = &input.After.CreatedAt
		params.CursorID = &input.After.ID
	}
	rows, err := s.q.ListEventWebhooks(ctx, params)
	if err != nil {
		return ListEventWebhooksResult{}, err
	}
	result := ListEventWebhooksResult{HasMore: len(rows) > input.Limit}
	if result.HasMore {
		rows = rows[:input.Limit]
	}
	result.Webhooks = make([]EventWebhookRecord, 0, len(rows))
	for _, row := range rows {
		result.Webhooks = append(result.Webhooks, eventWebhookRecordFromSQLC(row))
	}
	return result, nil
}

func (s *Store) GetEventWebhook(ctx context.Context, projectID, id uuid.UUID) (EventWebhookRecord, error) {
	row, err := s.q.GetEventWebhook(ctx, dbsqlc.GetEventWebhookParams{ProjectID: projectID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return EventWebhookRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return EventWebhookRecord{}, err
	}
	return eventWebhookRecordFromSQLC(row), nil
}

func (s *Store) UpdateEventWebhook(
	ctx context.Context,
	input UpdateEventWebhookInput,
) (EventWebhookRecord, error) {
	record, err := s.GetEventWebhook(ctx, input.ProjectID, input.WebhookID)
	if err != nil {
		return EventWebhookRecord{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return EventWebhookRecord{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lifecyclelock.EnterActiveProject(ctx, tx, record.OrgID, input.ProjectID); err != nil {
		return EventWebhookRecord{}, err
	}
	q := dbsqlc.New(tx)
	// Share the project's creation lock so selection changes, duplicate checks,
	// and partial updates observe the latest committed subscription.
	if err := lockResourceCreation(ctx, q, "event_webhooks", input.ProjectID.String()); err != nil {
		return EventWebhookRecord{}, err
	}
	current, err := q.GetEventWebhook(ctx, dbsqlc.GetEventWebhookParams{
		ProjectID: input.ProjectID, ID: input.WebhookID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return EventWebhookRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return EventWebhookRecord{}, err
	}
	scope := eventWebhookScopeFromColumns(current.AgentProfileID, current.AgentID)
	if input.Scope != nil {
		scope = *input.Scope
	}
	// Profile deletion locks the profile before its subscriptions. Validate the
	// chosen profile before updating the webhook row in the same order.
	if err := validateEventWebhookScopeTx(ctx, tx, input.ProjectID, scope); err != nil {
		return EventWebhookRecord{}, err
	}
	settings := eventWebhookRecordFromSQLC(current).Settings
	if input.URL != nil {
		settings.URL = *input.URL
	}
	if input.EventTypes != nil {
		settings.EventTypes = input.EventTypes
	}
	if input.SigningSecretID != nil {
		settings.SigningSecretID = *input.SigningSecretID
	}
	settings, err = s.validateEventWebhookSettingsTx(ctx, tx, record.OrgID, input.ProjectID, settings)
	if err != nil {
		return EventWebhookRecord{}, err
	}
	profileID, agentID := scope.columns()
	row, err := q.UpdateEventWebhook(ctx, dbsqlc.UpdateEventWebhookParams{
		ProjectID: input.ProjectID, ID: input.WebhookID, Url: settings.URL, EventTypes: settings.EventTypes,
		SigningSecretID: storeutil.IDFromNil(settings.SigningSecretID),
		AgentProfileID:  profileID, AgentID: agentID,
	})
	if storeutil.IsUniqueViolationOnConstraint(err, "event_webhooks_active_scope_url_idx") {
		return EventWebhookRecord{}, storeerr.Tag(storeerr.ErrConflict,
			errors.New("a webhook with this URL already exists on this resource"))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return EventWebhookRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return EventWebhookRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return EventWebhookRecord{}, err
	}
	return eventWebhookRecordFromSQLC(row), nil
}

func (s *Store) DeleteEventWebhook(ctx context.Context, projectID, id uuid.UUID) error {
	count, err := s.q.DeleteEventWebhook(ctx, dbsqlc.DeleteEventWebhookParams{ProjectID: projectID, ID: id})
	if err != nil {
		return err
	}
	if count == 0 {
		return storeerr.ErrNotFound
	}
	return nil
}
