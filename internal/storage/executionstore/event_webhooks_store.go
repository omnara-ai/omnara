package executionstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	webhookconfig "github.com/omnara-ai/omnara/internal/eventwebhook/config"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/resourceguard"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const MaxEventWebhooksPerScope = 16

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

type UpdateEventWebhookInput struct {
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
	if err := validateEventWebhookScopeTx(ctx, tx, input.ProjectID, input.Scope); err != nil {
		return EventWebhookRecord{}, err
	}
	if err := lockResourceCreation(ctx, q, "event_webhooks",
		fmt.Sprintf("%s/%s/%s", input.ProjectID, input.Scope.Type, input.Scope.ID)); err != nil {
		return EventWebhookRecord{}, err
	}
	settings, err := s.validateEventWebhookSettingsTx(ctx, tx, project.OrgID, input.ProjectID, input.Settings)
	if err != nil {
		return EventWebhookRecord{}, err
	}
	params := eventWebhookListParams(input.ProjectID, input.Scope)
	existing, err := q.ListEventWebhooks(ctx, params)
	if err != nil {
		return EventWebhookRecord{}, err
	}
	if len(existing) >= MaxEventWebhooksPerScope {
		return EventWebhookRecord{}, resourceguard.LimitExceeded("event webhooks per scope", MaxEventWebhooksPerScope)
	}
	row, err := q.InsertEventWebhook(ctx, dbsqlc.InsertEventWebhookParams{
		OrgID: project.OrgID, ProjectID: input.ProjectID,
		AgentProfileID: params.AgentProfileID, AgentID: params.AgentID,
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

func eventWebhookListParams(projectID uuid.UUID, scope EventWebhookScope) dbsqlc.ListEventWebhooksParams {
	params := dbsqlc.ListEventWebhooksParams{ProjectID: projectID}
	switch scope.Type {
	case EventWebhookScopeProject:
	case EventWebhookScopeAgentProfile:
		params.AgentProfileID = &scope.ID
	case EventWebhookScopeAgent:
		params.AgentID = &scope.ID
	}
	return params
}

func (s *Store) ListEventWebhooks(
	ctx context.Context, projectID uuid.UUID, scope EventWebhookScope,
) ([]EventWebhookRecord, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	var err error
	switch scope.Type {
	case EventWebhookScopeProject:
	case EventWebhookScopeAgentProfile:
		_, err = s.q.GetAgentProfile(ctx, dbsqlc.GetAgentProfileParams{ProjectID: projectID, ID: scope.ID})
	case EventWebhookScopeAgent:
		_, err = s.q.GetAgentInProject(ctx, dbsqlc.GetAgentInProjectParams{ProjectID: projectID, ID: scope.ID})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, storeerr.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListEventWebhooks(ctx, eventWebhookListParams(projectID, scope))
	if err != nil {
		return nil, err
	}
	result := make([]EventWebhookRecord, 0, len(rows))
	for _, row := range rows {
		result = append(result, eventWebhookRecordFromSQLC(row))
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
	if err := validateEventWebhookScopeTx(ctx, tx, input.ProjectID, record.Scope); err != nil {
		return EventWebhookRecord{}, err
	}
	q := dbsqlc.New(tx)
	current, err := q.GetEventWebhookForUpdate(ctx, dbsqlc.GetEventWebhookForUpdateParams{
		ProjectID: input.ProjectID, ID: input.WebhookID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return EventWebhookRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
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
	row, err := q.UpdateEventWebhook(ctx, dbsqlc.UpdateEventWebhookParams{
		ProjectID: input.ProjectID, ID: input.WebhookID, Url: settings.URL, EventTypes: settings.EventTypes,
		SigningSecretID: storeutil.IDFromNil(settings.SigningSecretID),
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
