package orglifecycle

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type AgentPanelScope struct {
	OrgID       uuid.UUID
	ProjectID   uuid.UUID
	OrgName     string
	ProjectName string
}

type AgentPanelSelection struct {
	AgentPanelScope
	AgentID    uuid.UUID
	Name       string
	CanOperate bool
}

type AgentPanelSetup struct {
	AgentPanelScope
	Ready bool
}

type AgentPanelRecord struct {
	Enabled   bool
	Revision  int64
	State     string
	Selection *AgentPanelSelection
	Setup     *AgentPanelSetup
}

func agentPanelPreference(
	ctx context.Context,
	q *dbsqlc.Queries,
	userID uuid.UUID,
) (dbsqlc.AgentPanelPreference, error) {
	row, err := q.GetAgentPanelPreference(ctx, dbsqlc.GetAgentPanelPreferenceParams{UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbsqlc.AgentPanelPreference{UserID: userID, Enabled: true}, nil
	}
	return row, err
}

func (s *Service) GetAgentPanel(
	ctx context.Context,
	principal identitystore.PrincipalRecord,
) (AgentPanelRecord, error) {
	if principal.Type != identitystore.PrincipalTypeUser || principal.ID == uuid.Nil {
		return AgentPanelRecord{}, storeerr.ErrUnauthorized
	}
	preference, err := agentPanelPreference(ctx, s.q, principal.ID)
	if err != nil {
		return AgentPanelRecord{}, err
	}
	result := AgentPanelRecord{Enabled: preference.Enabled, Revision: preference.Revision, State: "empty"}
	if preference.AgentID != nil {
		result.State = "unavailable"
		agent, err := s.q.GetAgent(ctx, dbsqlc.GetAgentParams{ID: *preference.AgentID})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return AgentPanelRecord{}, err
		}
		if err == nil {
			scope, found, err := s.agentPanelScope(ctx, principal, agent.ProjectID, false)
			if err != nil {
				return AgentPanelRecord{}, err
			}
			if found {
				result.State = "ready"
				if agent.State == "archived" {
					result.State = "archived"
				}
				allowed, err := identitystore.AuthorizeProject(ctx, s.q, identitystore.AuthorizeProjectInput{
					Principal: principal, OrgID: scope.OrgID, ProjectID: scope.ProjectID, Action: identitystore.AgentActionOperate,
				})
				if err != nil {
					return AgentPanelRecord{}, err
				}
				result.Selection = &AgentPanelSelection{
					AgentPanelScope: scope, AgentID: agent.ID, Name: agent.Name, CanOperate: allowed,
				}
			}
		}
	}
	if preference.PendingProjectID != nil && preference.Enabled {
		scope, found, err := s.agentPanelScope(ctx, principal, *preference.PendingProjectID, true)
		if err != nil {
			return AgentPanelRecord{}, err
		}
		if found {
			_, ready, err := assistantModel(ctx, s.q, scope.OrgID, scope.ProjectID, uuid.Nil)
			if err != nil {
				return AgentPanelRecord{}, err
			}
			result.Setup = &AgentPanelSetup{AgentPanelScope: scope, Ready: ready}
		}
	}
	return result, nil
}

func (s *Service) agentPanelScope(
	ctx context.Context, principal identitystore.PrincipalRecord, projectID uuid.UUID, owner bool,
) (AgentPanelScope, bool, error) {
	project, err := s.q.GetAgentPanelProject(ctx, dbsqlc.GetAgentPanelProjectParams{ProjectID: projectID})
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentPanelScope{}, false, nil
	}
	if err != nil {
		return AgentPanelScope{}, false, err
	}
	var allowed bool
	if owner {
		allowed, err = identitystore.AuthorizeOrg(ctx, s.q, identitystore.AuthorizeOrgInput{
			Principal: principal, OrgID: project.OrgID, Action: identitystore.OrgActionOwn,
		})
	} else {
		allowed, err = identitystore.AuthorizeProject(ctx, s.q, identitystore.AuthorizeProjectInput{
			Principal: principal, OrgID: project.OrgID, ProjectID: projectID, Action: identitystore.AgentActionRead,
		})
	}
	if err != nil || !allowed {
		return AgentPanelScope{}, false, err
	}
	return AgentPanelScope{
		OrgID: project.OrgID, ProjectID: projectID, OrgName: project.OrgName, ProjectName: project.Name,
	}, true, nil
}

type UpdateAgentPanelInput struct {
	Principal   identitystore.PrincipalRecord
	ChangeAgent bool
	AgentID     uuid.UUID
	Enabled     *bool
}

func (s *Service) UpdateAgentPanel(ctx context.Context, input UpdateAgentPanelInput) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	var agent dbsqlc.GetAgentRow
	if input.ChangeAgent && input.AgentID != uuid.Nil {
		agent, err = q.GetAgent(ctx, dbsqlc.GetAgentParams{ID: input.AgentID})
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := lifecyclelock.EnterActiveProject(ctx, tx, agent.OrgID, agent.ProjectID); err != nil {
			return err
		}
	}
	if err := s.lockAgentPanelUser(ctx, q, tx, input.Principal); err != nil {
		return err
	}
	preference, err := agentPanelPreference(ctx, q, input.Principal.ID)
	if err != nil {
		return err
	}
	if input.ChangeAgent {
		if input.AgentID != uuid.Nil {
			allowed, err := identitystore.AuthorizeProject(ctx, q, identitystore.AuthorizeProjectInput{
				Principal: input.Principal, OrgID: agent.OrgID, ProjectID: agent.ProjectID, Action: identitystore.AgentActionRead,
			})
			if err != nil {
				return err
			}
			if !allowed {
				return storeerr.ErrNotFound
			}
		}
		preference.AgentID = storeutil.IDFromNil(input.AgentID)
	}
	if input.Enabled != nil {
		preference.Enabled = *input.Enabled
	}
	if err := q.SetAgentPanelPreference(ctx, dbsqlc.SetAgentPanelPreferenceParams{
		UserID: input.Principal.ID, AgentID: preference.AgentID, Enabled: preference.Enabled,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) lockAgentPanelUser(
	ctx context.Context,
	q *dbsqlc.Queries,
	tx dbsqlc.DBTX,
	principal identitystore.PrincipalRecord,
) error {
	if principal.Type != identitystore.PrincipalTypeUser || principal.ID == uuid.Nil ||
		principal.BrowserSessionID == uuid.Nil {
		return storeerr.ErrUnauthorized
	}
	if _, err := q.LockUserForUpdate(ctx, dbsqlc.LockUserForUpdateParams{ID: principal.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrUnauthorized
		}
		return err
	}
	return s.identity.EnsureUserPrincipalStillActiveTx(ctx, tx, principal)
}

type CreateAssistantAgentInput struct {
	Principal          identitystore.PrincipalRecord
	OrgID              uuid.UUID
	ProjectID          uuid.UUID
	ModelID            uuid.UUID
	RequestID          uuid.UUID
	ExpectedRevision   int64
	Automatic          bool
	MCPURL             string
	ReadOnlyTools      []string
	AgentConfigOptions agentconfig.CompileOptions
}

func (s *Service) CreateAssistantAgent(ctx context.Context, input CreateAssistantAgentInput) (uuid.UUID, error) {
	if input.OrgID == uuid.Nil || input.ProjectID == uuid.Nil || input.RequestID == uuid.Nil ||
		input.ExpectedRevision < 0 {
		return uuid.Nil, storeerr.InvalidRequest(
			errors.New("organization, project, request ID and selection revision are required"),
		)
	}
	return agentexecution.Retry(ctx, "create_assistant_agent", func(ctx context.Context) (uuid.UUID, error) {
		return s.createAssistantAgentOnce(ctx, input)
	})
}

func (s *Service) createAssistantAgentOnce(ctx context.Context, input CreateAssistantAgentInput) (uuid.UUID, error) {
	unit, err := s.cell.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = unit.Rollback(ctx) }()
	tx := unit.DB()
	q := dbsqlc.New(tx)
	if err := lifecyclelock.EnterActiveProject(ctx, tx, input.OrgID, input.ProjectID); err != nil {
		return uuid.Nil, err
	}
	if err := s.lockAgentPanelUser(ctx, q, tx, input.Principal); err != nil {
		return uuid.Nil, err
	}
	if err := identitystore.RequireOrgOwner(ctx, q, input.OrgID, input.Principal); err != nil {
		return uuid.Nil, err
	}
	previous, err := q.GetAssistantAgentCreation(ctx, dbsqlc.GetAssistantAgentCreationParams{
		UserID:    input.Principal.ID,
		RequestID: input.RequestID,
	})
	if err == nil {
		if previous.ProjectID != input.ProjectID || previous.Automatic != input.Automatic ||
			storeutil.IDFromPtr(previous.RequestedModelID) != input.ModelID {
			return uuid.Nil, storeerr.ErrIdempotencyConflict
		}
		return previous.AgentID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, err
	}
	preference, err := agentPanelPreference(ctx, q, input.Principal.ID)
	if err != nil {
		return uuid.Nil, err
	}
	if preference.Revision != input.ExpectedRevision || (input.Automatic &&
		(!preference.Enabled || preference.AgentID != nil ||
			storeutil.IDFromPtr(preference.PendingProjectID) != input.ProjectID)) {
		return uuid.Nil, storeerr.Tag(storeerr.ErrConflict,
			errors.New("panel selection changed; refresh before creating an assistant"))
	}
	model, found, err := assistantModel(ctx, q, input.OrgID, input.ProjectID, input.ModelID)
	if err != nil {
		return uuid.Nil, err
	}
	if !found {
		return uuid.Nil, storeerr.Tag(storeerr.ErrConflict,
			errors.New("grant this project a valid model that supports tools before creating an assistant"))
	}
	token, err := s.identity.CreatePersonalAccessTokenTx(ctx, tx, identitystore.CreatePersonalAccessTokenInput{
		UserID: input.Principal.ID, ActorPrincipal: input.Principal,
		Name: "Omnara assistant (" + input.RequestID.String() + ")",
	})
	if err != nil {
		return uuid.Nil, err
	}
	secret, _, err := s.secrets.CreateTenantSecretInUnit(ctx, unit, secretstore.CreateSecretInput{
		OrgID: input.OrgID, OwnerKind: "project", OwnerProjectID: input.ProjectID,
		Name: "omnara-assistant-" + input.RequestID.String(), Actor: input.Principal,
		Material: secrets.GenericMaterial{Value: token.Token},
	})
	if err != nil {
		return uuid.Nil, err
	}
	compiled, err := compileAssistantPreset(input, model, secret.ID)
	if err != nil {
		return uuid.Nil, storeerr.InvalidRequest(err)
	}
	name := "Omnara assistant"
	launched, err := s.execution.LaunchAgentInUnit(ctx, unit, executionstore.LaunchAgentInput{
		ProjectID: input.ProjectID, LaunchedBy: input.Principal, Name: &name,
		DerivedConfig: &executionstore.CreateAgentConfigInput{
			OrgID: input.OrgID, ProjectID: input.ProjectID, Source: compiled.Source,
			SourceFormat: string(compiled.SourceFormat), ConfiguredModelID: model.ID,
			CompiledDefinition: compiled.CanonicalJSON, EffectiveDefinitionHash: compiled.Hash,
		},
	})
	if err != nil {
		return uuid.Nil, err
	}
	if err := q.RecordAssistantAgentCreation(ctx, dbsqlc.RecordAssistantAgentCreationParams{
		UserID: input.Principal.ID, RequestID: input.RequestID, OrgID: input.OrgID, ProjectID: input.ProjectID,
		AgentID: launched.Agent.ID, PersonalAccessTokenID: token.Record.ID, SecretID: secret.ID,
		RequestedModelID: storeutil.IDFromNil(input.ModelID), Automatic: input.Automatic,
	}); err != nil {
		return uuid.Nil, fmt.Errorf("record assistant creation: %w", err)
	}
	if err := q.SetAgentPanelPreference(ctx, dbsqlc.SetAgentPanelPreferenceParams{
		UserID: input.Principal.ID, AgentID: &launched.Agent.ID, Enabled: true,
	}); err != nil {
		return uuid.Nil, err
	}
	if err := unit.Commit(ctx, "create assistant agent"); err != nil {
		return uuid.Nil, err
	}
	return launched.Agent.ID, nil
}

// Inspect effective grants without locking each candidate. Ordinary config
// insertion locks and revalidates the single chosen model before commit.
func assistantModel(
	ctx context.Context,
	q *dbsqlc.Queries,
	orgID, projectID, modelID uuid.UUID,
) (modelstore.ConfiguredModelSummaryRecord, bool, error) {
	input := modelstore.ListProjectModelGrantsInput{OrgID: orgID, ProjectID: projectID, Limit: 100, List: listing.Options{
		SortField: "created_at",
	}}
	for {
		page, err := modelstore.ListProjectModelGrants(ctx, q, input)
		if err != nil {
			return modelstore.ConfiguredModelSummaryRecord{}, false, err
		}
		for _, grant := range page.Grants {
			if (modelID == uuid.Nil || grant.Model.ID == modelID) && grant.Effective != nil && grant.Effective.SupportsTools {
				return grant.Model, true, nil
			}
		}
		if !page.HasMore {
			return modelstore.ConfiguredModelSummaryRecord{}, false, nil
		}
		input.List.After = page.Next
	}
}
