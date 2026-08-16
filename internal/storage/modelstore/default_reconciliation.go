package modelstore

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/management"
)

func (s *Store) ReconcileDefaultModelProviderTx(
	ctx context.Context,
	tx pgx.Tx,
	prepared *DefaultModelProviderTemplate,
	rows []dbsqlc.ModelProviderConfig,
	apply bool,
) ([]string, []string, error) {
	if prepared == nil {
		return nil, nil, nil
	}
	qtx := s.q.WithTx(tx)
	var changes []string
	var warnings []string
	for _, row := range rows {
		current := modelProviderConfigRecordFromSQLC(row)
		if apply {
			locked, err := qtx.LockModelProviderConfigForMutation(
				ctx,
				dbsqlc.LockModelProviderConfigForMutationParams{OrgID: current.OrgID, ID: current.ID},
			)
			if err != nil {
				return nil, nil, fmt.Errorf("lock default model provider %q: %w", prepared.Name, err)
			}
			current = modelProviderConfigRecordFromSQLC(locked)
		}
		if current.APIFormat != prepared.APIFormat || current.APIVariant != prepared.APIVariant {
			return nil, nil, fmt.Errorf(
				"default model provider %q cannot change api_format or api_variant",
				prepared.Name,
			)
		}
		desiredProvider := CreateModelProviderConfigInput{
			OrgID:              current.OrgID,
			Name:               prepared.Name,
			APIFormat:          prepared.APIFormat,
			APIVariant:         prepared.APIVariant,
			BaseURL:            prepared.BaseURL,
			EndpointPath:       prepared.EndpointPath,
			RequestTimeoutMS:   prepared.RequestTimeoutMS,
			AuthKind:           prepared.AuthKind,
			AuthOptions:        prepared.AuthOptions,
			CredentialSecretID: current.CredentialSecretID,
			managementKind:     management.Cluster,
		}
		if !sameModelProviderConfigIntent(current, desiredProvider) {
			changes = append(changes, fmt.Sprintf(
				"org %s: update model provider %q",
				current.OrgID,
				prepared.Name,
			))
			if apply {
				if _, err := updateModelProviderConfigTx(
					ctx,
					qtx,
					modelProviderConfigUpdate{
						OrgID:              current.OrgID,
						ID:                 current.ID,
						BaseURL:            prepared.BaseURL,
						EndpointPath:       prepared.EndpointPath,
						RequestTimeoutMS:   prepared.RequestTimeoutMS,
						AuthKind:           prepared.AuthKind,
						AuthOptions:        prepared.AuthOptions,
						CredentialSecretID: current.CredentialSecretID,
						APIFormat:          current.APIFormat,
						APIVariant:         current.APIVariant,
					},
					management.Cluster,
				); err != nil {
					return nil, nil, fmt.Errorf("update default model provider %q: %w", prepared.Name, err)
				}
			}
		}
		defaultProjectID := NilID
		project, err := qtx.GetProjectByIdempotencyKey(
			ctx,
			dbsqlc.GetProjectByIdempotencyKeyParams{
				OrgID: current.OrgID, IdempotencyKey: identitystore.DefaultProjectKey,
			},
		)
		if err == nil {
			defaultProjectID = project.ID
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, fmt.Errorf("load default project: %w", err)
		}
		modelRows, err := qtx.ListConfiguredModels(ctx, dbsqlc.ListConfiguredModelsParams{
			OrgID: current.OrgID, ModelProviderConfigID: current.ID, RowLimit: math.MaxInt64,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("list configured models for %q: %w", prepared.Name, err)
		}
		existing := make(map[string]ConfiguredModelRecord, len(modelRows))
		for _, modelRow := range modelRows {
			model := configuredModelRecordFromListSQLC(modelRow)
			existing[model.Name] = model
		}
		desiredNames := make(map[string]struct{}, len(prepared.Models))
		for _, modelTemplate := range prepared.Models {
			desiredNames[modelTemplate.Name] = struct{}{}
			desired := configuredModelInputFromDefaultTemplate(modelTemplate)
			desired.OrgID = current.OrgID
			desired.ModelProviderConfigID = current.ID
			model, found := existing[modelTemplate.Name]
			if !found {
				changes = append(changes, fmt.Sprintf(
					"org %s: add configured model %q",
					current.OrgID,
					modelTemplate.Name,
				))
				if apply {
					created, err := s.createConfiguredModelTx(ctx, qtx, desired, management.Cluster)
					if err != nil {
						return nil, nil, fmt.Errorf("add default configured model %q: %w", modelTemplate.Name, err)
					}
					if !isNilID(defaultProjectID) {
						if err := grantDefaultConfiguredModelToProjectTx(
							ctx,
							qtx,
							current.OrgID,
							defaultProjectID,
							current.APIFormat,
							created,
						); err != nil {
							return nil, nil, err
						}
					}
				}
				continue
			}
			if apply {
				locked, err := lockConfiguredModelCurrentRevisionForMutationTx(ctx, qtx, current.OrgID, model.ID)
				if err != nil {
					return nil, nil, fmt.Errorf("lock default configured model %q: %w", model.Name, err)
				}
				model = locked
			}
			if sameConfiguredModelIntent(model, desired) {
				continue
			}
			changes = append(changes, fmt.Sprintf(
				"org %s: update configured model %q",
				current.OrgID,
				modelTemplate.Name,
			))
			if apply {
				if _, err := updateConfiguredModelTx(
					ctx,
					qtx,
					configuredModelUpdateFromDefault(model.ID, desired),
					model.Name,
					management.Cluster,
				); err != nil {
					return nil, nil, fmt.Errorf("update default configured model %q: %w", modelTemplate.Name, err)
				}
			}
		}
		for _, modelRow := range modelRows {
			model := configuredModelRecordFromListSQLC(modelRow)
			name := model.Name
			if _, desired := desiredNames[name]; desired {
				continue
			}
			if apply {
				locked, err := lockConfiguredModelCurrentRevisionForDeleteTx(ctx, qtx, current.OrgID, model.ID)
				if err != nil {
					return nil, nil, fmt.Errorf("lock removed configured model %q: %w", name, err)
				}
				model = locked
			}
			var state dbsqlc.GetDefaultConfiguredModelRemovalStateRow
			if isNilID(defaultProjectID) {
				state.GrantedToOtherProject, err = qtx.ConfiguredModelHasActiveGrants(
					ctx,
					dbsqlc.ConfiguredModelHasActiveGrantsParams{OrgID: current.OrgID, ID: model.ID},
				)
			} else {
				state, err = qtx.GetDefaultConfiguredModelRemovalState(
					ctx,
					dbsqlc.GetDefaultConfiguredModelRemovalStateParams{
						TargetConfiguredModelID: model.ID,
						DefaultProjectID:        defaultProjectID,
					},
				)
			}
			if err != nil {
				return nil, nil, fmt.Errorf("check removed configured model %q: %w", name, err)
			}
			if state.GrantedToOtherProject {
				if state.GrantedToDefaultProject {
					changes = append(changes, fmt.Sprintf(
						"org %s: remove configured model %q from the default project",
						current.OrgID,
						name,
					))
					if apply {
						if err := deleteDefaultProjectModelGrantTx(ctx, qtx, current.OrgID, defaultProjectID, model.ID); err != nil {
							return nil, nil, fmt.Errorf("remove default grant for configured model %q: %w", name, err)
						}
					}
				}
				warnings = append(warnings, fmt.Sprintf(
					"org %s: keep configured model %q because another project grants it",
					current.OrgID,
					name,
				))
				continue
			}
			changes = append(changes, fmt.Sprintf(
				"org %s: remove configured model %q",
				current.OrgID,
				name,
			))
			if apply {
				if state.GrantedToDefaultProject {
					if err := deleteDefaultProjectModelGrantTx(ctx, qtx, current.OrgID, defaultProjectID, model.ID); err != nil {
						return nil, nil, fmt.Errorf("remove default grant for configured model %q: %w", name, err)
					}
				}
				if _, err := qtx.DeleteConfiguredModel(ctx, dbsqlc.DeleteConfiguredModelParams{
					OrgID: current.OrgID, ID: model.ID, ManagementKind: string(management.Cluster),
				}); err != nil {
					return nil, nil, fmt.Errorf("remove default configured model %q: %w", name, err)
				}
			}
		}
	}
	return changes, warnings, nil
}

func configuredModelUpdateFromDefault(id ID, input CreateConfiguredModelInput) configuredModelUpdate {
	return configuredModelUpdate{
		OrgID:                     input.OrgID,
		ModelProviderConfigID:     input.ModelProviderConfigID,
		ID:                        id,
		Name:                      input.Name,
		ProviderModelSlug:         input.ProviderModelSlug,
		ContextWindowTokens:       input.ContextWindowTokens,
		MaxOutputTokens:           input.MaxOutputTokens,
		DefaultMaxOutputTokens:    input.DefaultMaxOutputTokens,
		DefaultCacheRetention:     input.DefaultCacheRetention,
		SupportsTools:             input.SupportsTools,
		SupportsReasoning:         input.SupportsReasoning,
		DefaultReasoningEffort:    input.DefaultReasoningEffort,
		SupportedReasoningEfforts: input.SupportedReasoningEfforts,
		InputModalities:           input.InputModalities,
		OutputModalities:          input.OutputModalities,
		APIVariantOptions:         input.APIVariantOptions,
	}
}

func deleteDefaultProjectModelGrantTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	orgID, projectID, configuredModelID ID,
) error {
	grant, err := qtx.GetActiveProjectModelGrantForConfiguredModel(
		ctx,
		dbsqlc.GetActiveProjectModelGrantForConfiguredModelParams{
			OrgID: orgID, ProjectID: projectID, ConfiguredModelID: configuredModelID,
		},
	)
	if err != nil {
		return err
	}
	_, err = qtx.DeleteProjectModelGrant(ctx, dbsqlc.DeleteProjectModelGrantParams{
		OrgID: orgID, ProjectID: projectID, ID: grant.ID,
	})
	return err
}
