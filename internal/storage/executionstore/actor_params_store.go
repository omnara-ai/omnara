package executionstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/resourcemeta"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ActorParams struct {
	Provider         string
	ProviderTenantID string
	ProviderUserID   string
	DisplayName      *string
	Metadata         resourcemeta.Metadata
}

func OmnaraActorParams(orgID ID, principal identitystore.PrincipalRecord) (*ActorParams, error) {
	tenantID, err := publicid.Encode(publicid.KindOrganization, orgID)
	if err != nil {
		return nil, fmt.Errorf("encode omnara actor tenant: %w", err)
	}
	if isNilID(principal.ID) {
		return nil, errors.New("omnara actor principal id is required")
	}
	var providerUserID string
	switch principal.Type {
	case identitystore.PrincipalTypeUser:
		providerUserID, err = publicid.Encode(publicid.KindUser, principal.ID)
	case identitystore.PrincipalTypeOrgAPIKey:
		providerUserID, err = publicid.Encode(publicid.KindOrgAPIKey, principal.ID)
	default:
		return nil, fmt.Errorf("unsupported omnara actor principal type %q", principal.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("encode omnara actor principal: %w", err)
	}
	return &ActorParams{
		Provider:         ActorProviderOmnara,
		ProviderTenantID: tenantID,
		ProviderUserID:   providerUserID,
	}, nil
}

func omnaraActorDisplayNameTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID ID,
	providerUserID string,
) (string, error) {
	if userID, err := publicid.Decode(publicid.KindUser, providerUserID); err == nil {
		user, err := qtx.GetUser(ctx, dbsqlc.GetUserParams{ID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("load omnara actor user: %w", err)
		}
		return user.DisplayName, nil
	}
	if agentID, err := publicid.Decode(publicid.KindAgent, providerUserID); err == nil {
		agent, err := qtx.GetAgentInProject(ctx, dbsqlc.GetAgentInProjectParams{ProjectID: projectID, ID: agentID})
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("load omnara actor agent: %w", err)
		}
		return subagentDisplayName(agentRecordFromProjectSQLC(agent)), nil
	}
	keyID, err := publicid.Decode(publicid.KindOrgAPIKey, providerUserID)
	if err != nil {
		return "", fmt.Errorf("decode omnara actor principal: %w", err)
	}
	project, err := loadProjectTx(ctx, qtx, projectID)
	if err != nil {
		return "", err
	}
	key, err := qtx.GetOrgAPIKey(ctx, dbsqlc.GetOrgAPIKeyParams{OrgID: project.OrgID, ID: keyID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load omnara actor org api key: %w", err)
	}
	return key.Name, nil
}

func resolveActorTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID ID,
	params *ActorParams,
	integrationTargetID ID,
) (ID, error) {
	if isNilID(projectID) {
		return NilID, errors.New("project id is required for input actor")
	}
	if params == nil {
		if !isNilID(integrationTargetID) {
			return NilID, errors.New("integration target input requires an actor")
		}
		return NilID, nil
	}
	var actor ActorRecord
	var err error
	switch params.Provider {
	case ActorProviderExternal:
		actor, err = putActorTx(ctx, qtx, PutActorInput{
			ProjectID:        projectID,
			ProviderTenantID: params.ProviderTenantID,
			ProviderUserID:   params.ProviderUserID,
			DisplayName:      params.DisplayName,
			Metadata:         params.Metadata,
		})
	default:
		displayName := ""
		if params.DisplayName != nil {
			displayName = *params.DisplayName
		}
		if displayName == "" && params.Provider == ActorProviderOmnara {
			displayName, err = omnaraActorDisplayNameTx(ctx, qtx, projectID, params.ProviderUserID)
			if err != nil {
				return NilID, err
			}
		}
		actor, err = upsertActorIdentityTx(ctx, qtx, UpsertActorIdentityInput{
			ProjectID:        projectID,
			Provider:         params.Provider,
			ProviderTenantID: params.ProviderTenantID,
			ProviderUserID:   params.ProviderUserID,
			DisplayName:      displayName,
		})
	}
	if err != nil {
		return NilID, err
	}
	if !isNilID(integrationTargetID) {
		if isNilID(agentID) {
			return NilID, errors.New("agent is required for integration-target input actor")
		}
		matches, err := qtx.ActorMatchesIntegrationTarget(
			ctx,
			dbsqlc.ActorMatchesIntegrationTargetParams{
				ProjectID:           projectID,
				AgentID:             agentID,
				IntegrationTargetID: integrationTargetID,
				ActorID:             actor.ID,
			},
		)
		if err != nil {
			return NilID, fmt.Errorf("validate integration target actor: %w", err)
		}
		if !matches {
			return NilID, storeerr.ErrUnauthorized
		}
	}
	return actor.ID, nil
}

func lookupActorIDTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID ID,
	params *ActorParams,
) (ID, bool, error) {
	if params == nil {
		return NilID, true, nil
	}
	row, err := qtx.GetActorByIdentity(ctx, dbsqlc.GetActorByIdentityParams{
		ProjectID:        projectID,
		Provider:         strings.TrimSpace(params.Provider),
		ProviderTenantID: sqlcTextFromEmpty(strings.TrimSpace(params.ProviderTenantID)),
		ProviderUserID:   strings.TrimSpace(params.ProviderUserID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return NilID, false, nil
	}
	if err != nil {
		return NilID, false, fmt.Errorf("look up input actor: %w", err)
	}
	return row.ID, true, nil
}
