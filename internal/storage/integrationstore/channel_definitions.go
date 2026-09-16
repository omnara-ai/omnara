package integrationstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/omnara-ai/omnara/internal/registryname"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ChannelKind string

const (
	ChannelKindSlackChannel       ChannelKind = "SLACK_CHANNEL"
	ChannelKindSlackThread        ChannelKind = "SLACK_THREAD"
	ChannelKindDiscordChannel     ChannelKind = "DISCORD_CHANNEL"
	ChannelKindDiscordThread      ChannelKind = "DISCORD_THREAD"
	ChannelKindGitHubPR           ChannelKind = "GITHUB_PR"
	ChannelKindGitHubReviewThread ChannelKind = "GITHUB_REVIEW_THREAD"
	ChannelKindExternal           ChannelKind = "EXTERNAL"
	MaxChannelSchemaBytes                     = 256 * 1024
)

// ChannelCapabilities describe implemented operations, independently of an agent's
// grants. Effective access requires both, and a live connection.
type ChannelCapabilities struct {
	Read                bool `json:"read"`
	Send                bool `json:"send"`
	Text                bool `json:"text"`
	Artifacts           bool `json:"artifacts"`
	Permissions         bool `json:"permissions"`
	Questions           bool `json:"questions"`
	CreatesReplyChannel bool `json:"creates_reply_channel"`
}

type ChannelDefinition struct {
	ID                   uuid.UUID
	ProjectID            uuid.UUID
	IntegrationInstallID uuid.UUID
	ImplementationKey    string
	Kind                 ChannelKind
	Description          string
	SendParamsSchema     json.RawMessage
	Capabilities         ChannelCapabilities
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type PublishChannelDefinitionInput struct {
	ProjectID             uuid.UUID
	IntegrationInstallID  uuid.UUID
	ImplementationKey     string
	Kind                  ChannelKind
	Description           string
	SendParamsSchema      json.RawMessage
	Capabilities          ChannelCapabilities
	ConnectorCapabilities []channelconnector.Capability
}

// PublishConnectorChannelDefinition updates the current contract for this
// connection. The connector's exact capability pair must own its real app.
func (s *Store) PublishConnectorChannelDefinition(
	ctx context.Context,
	input PublishChannelDefinitionInput,
) (ChannelDefinition, error) {
	input, err := normalizeChannelDefinition(input)
	if err != nil {
		return ChannelDefinition{}, storeerr.InvalidRequest(err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ChannelDefinition{}, fmt.Errorf("begin channel definition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	authority, err := s.lockConnectorIntegrationAuthority(
		ctx, tx, input.ProjectID, input.IntegrationInstallID, input.ConnectorCapabilities,
	)
	if err != nil {
		return ChannelDefinition{}, err
	}
	if !input.Kind.MatchesProvider(authority.Provider) {
		return ChannelDefinition{}, storeerr.InvalidRequest(errors.New("channel kind does not match connection provider"))
	}
	definition, err := upsertChannelDefinition(ctx, s.q.WithTx(tx), input)
	if err != nil {
		return ChannelDefinition{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ChannelDefinition{}, fmt.Errorf("commit channel definition: %w", err)
	}
	return definition, nil
}

// MatchesProvider keeps the public definition and storage authorization checks
// on the same provider domain. Customer-owned definitions use External.
func (kind ChannelKind) MatchesProvider(provider string) bool {
	switch provider {
	case IntegrationProviderSlack:
		return kind == ChannelKindSlackChannel || kind == ChannelKindSlackThread
	case IntegrationProviderDiscord:
		return kind == ChannelKindDiscordChannel || kind == ChannelKindDiscordThread
	case IntegrationProviderGitHub:
		return kind == ChannelKindGitHubPR || kind == ChannelKindGitHubReviewThread
	case "":
		return kind == ChannelKindExternal
	default:
		return false
	}
}

func upsertChannelDefinition(
	ctx context.Context,
	q *dbsqlc.Queries,
	input PublishChannelDefinitionInput,
) (ChannelDefinition, error) {
	capabilities, err := json.Marshal(input.Capabilities)
	if err != nil {
		return ChannelDefinition{}, err
	}
	row, err := q.UpsertChannelDefinition(ctx, dbsqlc.UpsertChannelDefinitionParams{
		ProjectID: input.ProjectID, IntegrationInstallID: input.IntegrationInstallID,
		ImplementationKey: input.ImplementationKey, Kind: string(input.Kind), Description: input.Description,
		SendParamsSchema: input.SendParamsSchema, Capabilities: capabilities,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ChannelDefinition{}, storeerr.ErrConflict
	}
	if err != nil {
		return ChannelDefinition{}, integrationChannelWriteError("publish channel definition", err)
	}
	return channelDefinitionFromSQLC(row)
}

func (s *Store) GetChannelDefinition(
	ctx context.Context,
	projectID, installID, id uuid.UUID,
) (ChannelDefinition, error) {
	if projectID == uuid.Nil || installID == uuid.Nil || id == uuid.Nil {
		return ChannelDefinition{}, storeerr.InvalidRequest(errors.New("project, connection, and definition are required"))
	}
	row, err := s.q.GetChannelDefinition(ctx, dbsqlc.GetChannelDefinitionParams{
		ProjectID: projectID, IntegrationInstallID: installID, ID: id,
	})
	if err != nil {
		return ChannelDefinition{}, integrationChannelReadError("get channel definition", err)
	}
	return channelDefinitionFromSQLC(row)
}

func normalizeChannelDefinition(input PublishChannelDefinitionInput) (PublishChannelDefinitionInput, error) {
	if input.ProjectID == uuid.Nil || input.IntegrationInstallID == uuid.Nil {
		return input, errors.New("project and connection are required")
	}
	if !registryname.Valid(input.ImplementationKey) {
		return input, errors.New("invalid implementation key")
	}
	switch input.Kind {
	case ChannelKindSlackChannel, ChannelKindSlackThread, ChannelKindDiscordChannel, ChannelKindDiscordThread,
		ChannelKindGitHubPR, ChannelKindGitHubReviewThread, ChannelKindExternal:
	default:
		return input, errors.New("unsupported channel kind")
	}
	if len(input.Description) > 16*1024 {
		return input, errors.New("channel description exceeds 16 KiB")
	}
	if err := dbsafe.Text(input.Description); err != nil {
		return input, err
	}
	if _, err := jsoncanonical.ParseObject(input.SendParamsSchema, MaxChannelSchemaBytes); err != nil {
		return input, fmt.Errorf("send params schema: %w", err)
	}
	if err := jsonschema.ValidateSchema(input.SendParamsSchema); err != nil {
		return input, fmt.Errorf("send params schema: %w", err)
	}
	if err := dbsafe.JSONB(input.SendParamsSchema, MaxChannelSchemaBytes); err != nil {
		return input, err
	}
	if input.Capabilities.Send && !input.Capabilities.Text && !input.Capabilities.Artifacts {
		return input, errors.New("sending requires supported text or artifacts")
	}
	if input.Capabilities.CreatesReplyChannel && !input.Capabilities.Send {
		return input, errors.New("creating a reply channel requires sending support")
	}
	return input, nil
}

func channelDefinitionFromSQLC(row dbsqlc.IntegrationChannelDefinition) (ChannelDefinition, error) {
	var capabilities ChannelCapabilities
	if err := json.Unmarshal(row.Capabilities, &capabilities); err != nil {
		return ChannelDefinition{}, fmt.Errorf("decode channel capabilities: %w", err)
	}
	return ChannelDefinition{
		ID: row.ID, ProjectID: row.ProjectID, IntegrationInstallID: row.IntegrationInstallID,
		ImplementationKey: row.ImplementationKey, Kind: ChannelKind(row.Kind), Description: row.Description,
		SendParamsSchema: row.SendParamsSchema, Capabilities: capabilities,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}
