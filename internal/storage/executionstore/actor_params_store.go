package executionstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/resourcemeta"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

type ActorParams struct {
	Provider         string                `json:"provider"`
	ProviderTenantID string                `json:"provider_tenant_id"`
	ProviderUserID   string                `json:"provider_user_id"`
	DisplayName      *string               `json:"display_name,omitempty"`
	Metadata         resourcemeta.Metadata `json:"metadata,omitempty"`
}

func AppActorParams(appID uuid.UUID, userID string, displayName *string) (ActorParams, error) {
	tenantID, err := publicid.Encode(publicid.KindProjectApp, appID)
	if err != nil {
		return ActorParams{}, err
	}
	return ActorParams{
		Provider: ActorProviderApp, ProviderTenantID: tenantID,
		ProviderUserID: userID, DisplayName: displayName,
	}, nil
}

func OmnaraActorParams(orgID uuid.UUID, principal identitystore.PrincipalRecord) (*ActorParams, error) {
	tenantID, err := publicid.Encode(publicid.KindOrganization, orgID)
	if err != nil {
		return nil, fmt.Errorf("encode omnara actor tenant: %w", err)
	}
	if principal.ID == uuid.Nil {
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
	projectID uuid.UUID,
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
	projectID uuid.UUID,
	params *ActorParams,
) (uuid.UUID, error) {
	if projectID == uuid.Nil {
		return uuid.Nil, errors.New("project id is required for input actor")
	}
	if params == nil {
		return uuid.Nil, nil
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
				return uuid.Nil, err
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
		return uuid.Nil, err
	}
	return actor.ID, nil
}

func lookupActorIDTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID uuid.UUID,
	params *ActorParams,
) (uuid.UUID, bool, error) {
	if params == nil {
		return uuid.Nil, true, nil
	}
	row, err := qtx.GetActorByIdentity(ctx, dbsqlc.GetActorByIdentityParams{
		ProjectID:        projectID,
		Provider:         strings.TrimSpace(params.Provider),
		ProviderTenantID: storeutil.TextFromEmpty(strings.TrimSpace(params.ProviderTenantID)),
		ProviderUserID:   strings.TrimSpace(params.ProviderUserID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("look up input actor: %w", err)
	}
	return row.ID, true, nil
}
