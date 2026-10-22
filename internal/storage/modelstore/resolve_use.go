package modelstore

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

type ModelSecretVersion struct {
	VersionID uuid.UUID    `json:"version_id"`
	Kind      secrets.Kind `json:"kind"`
}

type ResolvedModelUse struct {
	Revision       ConfiguredModelRevisionRecord
	Provider       ModelProviderConfigRecord
	Grant          *ProjectModelGrantRecord
	SecretVersions map[uuid.UUID]ModelSecretVersion
}

func (s *Store) ResolveModelForUse(ctx context.Context, orgID, projectID,
	revisionID uuid.UUID) (ResolvedModelUse, error) {
	row, err := s.q.ResolveModelForUse(ctx, dbsqlc.ResolveModelForUseParams{OrgID: orgID, ProjectID: projectID,
		RevisionID: revisionID})
	if err != nil {
		return ResolvedModelUse{}, err
	}
	result := ResolvedModelUse{Revision: configuredModelRevisionRecordFromSQLC(dbsqlc.ConfiguredModelRevision{
		ID:                        row.RevisionID,
		OrgID:                     row.RevisionOrgID,
		ConfiguredModelID:         row.RevisionConfiguredModelID,
		ModelProviderConfigID:     row.RevisionModelProviderConfigID,
		ProviderModelSlug:         row.RevisionProviderModelSlug,
		ContextWindowTokens:       row.RevisionContextWindowTokens,
		MaxOutputTokens:           row.RevisionMaxOutputTokens,
		DefaultMaxOutputTokens:    row.RevisionDefaultMaxOutputTokens,
		DefaultCacheRetention:     row.RevisionDefaultCacheRetention,
		SupportsTools:             row.RevisionSupportsTools,
		SupportsReasoning:         row.RevisionSupportsReasoning,
		DefaultReasoningEffort:    row.RevisionDefaultReasoningEffort,
		SupportedReasoningEfforts: row.RevisionSupportedReasoningEfforts,
		InputModalities:           row.RevisionInputModalities,
		OutputModalities:          row.RevisionOutputModalities,
		ApiVariantOptions:         row.RevisionApiVariantOptions,
		CreatedAt:                 row.RevisionCreatedAt,
	}),
		Provider: modelProviderConfigRecordFromSQLC(dbsqlc.ModelProviderConfig{ID: row.ProviderID,
			OrgID:              row.ProviderOrgID,
			ManagementKind:     row.ProviderManagementKind,
			Name:               row.ProviderName,
			ApiFormat:          row.ProviderApiFormat,
			ApiVariant:         row.ProviderApiVariant,
			BaseUrl:            row.ProviderBaseUrl,
			EndpointPath:       row.ProviderEndpointPath,
			RequestTimeoutMs:   row.ProviderRequestTimeoutMs,
			AuthKind:           row.ProviderAuthKind,
			AuthOptions:        row.ProviderAuthOptions,
			CredentialSecretID: row.ProviderCredentialSecretID,
			DeletedAt:          row.ProviderDeletedAt,
			CreatedAt:          row.ProviderCreatedAt,
			UpdatedAt:          row.ProviderUpdatedAt,
			IdleTimeoutMs:      row.ProviderIdleTimeoutMs,
			Headers:            row.ProviderHeaders,
			SecretHeaders:      row.ProviderSecretHeaders,
		})}
	if len(row.GrantPolicy) > 0 {
		if err = json.Unmarshal(row.GrantPolicy, &result.Grant); err != nil {
			return ResolvedModelUse{}, err
		}
	}
	if err = json.Unmarshal(row.SecretVersions, &result.SecretVersions); err != nil {
		return ResolvedModelUse{}, err
	}
	return result, nil
}
