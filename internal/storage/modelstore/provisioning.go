package modelstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/management"
)

const (
	maxDefaultModelProviderProvisionerBytes = 128
)

type DefaultModelProviderTemplate struct {
	Provisioner          string                           `json:"provisioner"`
	Name                 string                           `json:"name"`
	CredentialSecretName string                           `json:"credential_secret_name"`
	APIFormat            modelprotocol.APIFormat          `json:"api_format"`
	APIVariant           modelprotocol.APIVariant         `json:"api_variant"`
	BaseURL              string                           `json:"base_url"`
	EndpointPath         string                           `json:"endpoint_path"`
	RequestTimeoutMS     int                              `json:"request_timeout_ms"`
	AuthKind             string                           `json:"auth_kind"`
	AuthOptions          json.RawMessage                  `json:"auth_options"`
	Models               []DefaultConfiguredModelTemplate `json:"models"`
}

type DefaultConfiguredModelTemplate struct {
	Name                      string          `json:"name"`
	ProviderModelSlug         string          `json:"provider_model_slug"`
	ContextWindowTokens       int             `json:"context_window_tokens"`
	MaxOutputTokens           int             `json:"max_output_tokens"`
	DefaultMaxOutputTokens    *int            `json:"default_max_output_tokens"`
	DefaultCacheRetention     string          `json:"default_cache_retention"`
	SupportsTools             *bool           `json:"supports_tools"`
	SupportsReasoning         bool            `json:"supports_reasoning"`
	DefaultReasoningEffort    string          `json:"default_reasoning_effort"`
	SupportedReasoningEfforts []string        `json:"supported_reasoning_efforts"`
	InputModalities           []string        `json:"input_modalities"`
	OutputModalities          []string        `json:"output_modalities"`
	APIVariantOptions         json.RawMessage `json:"api_variant_options"`
}

type ProvisionedDefaultModelProvider struct {
	Template        DefaultModelProviderTemplate
	CredentialValue string
}

func PrepareDefaultModelProviderTemplate(
	template DefaultModelProviderTemplate,
) (DefaultModelProviderTemplate, error) {
	template = cloneDefaultModelProviderTemplate(template)
	template.Provisioner = strings.TrimSpace(template.Provisioner)
	template.Name = strings.TrimSpace(template.Name)
	template.CredentialSecretName = strings.TrimSpace(template.CredentialSecretName)
	template.APIFormat = modelprotocol.APIFormat(strings.TrimSpace(string(template.APIFormat)))
	template.APIVariant = modelprotocol.APIVariant(strings.TrimSpace(string(template.APIVariant)))
	if template.APIVariant == "" {
		template.APIVariant = modelprotocol.APIVariantDefault
	}
	baseURL, err := normalizeModelProviderBaseURL(template.BaseURL)
	if err != nil {
		return DefaultModelProviderTemplate{}, err
	}
	template.BaseURL = baseURL
	apiFormat := template.APIFormat
	template.EndpointPath = normalizeModelProviderEndpointPath(apiFormat, template.EndpointPath)
	template.RequestTimeoutMS = normalizeModelProviderRequestTimeoutMS(template.RequestTimeoutMS)
	template.AuthKind = normalizeModelProviderAuthKind(apiFormat, template.AuthKind)
	template.AuthOptions = normalizeModelProviderAuthOptions(
		apiFormat,
		template.AuthKind,
		template.AuthOptions,
	)
	for i := range template.Models {
		template.Models[i] = normalizeDefaultConfiguredModelTemplate(template.Models[i])
	}
	if err := validatePreparedDefaultModelProviderTemplate(template); err != nil {
		return DefaultModelProviderTemplate{}, err
	}
	return template, nil
}

func validatePreparedDefaultModelProviderTemplate(template DefaultModelProviderTemplate) error {
	if template.Provisioner == "" {
		return errors.New("provisioner is required")
	}
	if len(template.Provisioner) > maxDefaultModelProviderProvisionerBytes {
		return fmt.Errorf("provisioner exceeds %d bytes", maxDefaultModelProviderProvisionerBytes)
	}
	for _, character := range template.Provisioner {
		if unicode.IsControl(character) {
			return errors.New("provisioner cannot contain control characters")
		}
	}
	if template.Name == "" {
		return errors.New("name is required")
	}
	if template.CredentialSecretName == "" {
		return errors.New("credential secret name is required")
	}
	if len(template.Models) == 0 {
		return errors.New("at least one model is required")
	}
	apiFormat := template.APIFormat
	if err := validateModelProviderAPIFormat(apiFormat); err != nil {
		return err
	}
	if err := validateModelProviderEndpointPath(template.EndpointPath); err != nil {
		return err
	}
	if err := ValidateModelProviderAuth(template.AuthKind, template.AuthOptions); err != nil {
		return err
	}
	if err := validateModelProviderAPIVariant(
		apiFormat,
		template.APIVariant,
	); err != nil {
		return err
	}
	if err := validateModelProviderRequestTimeoutMS(template.RequestTimeoutMS); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(template.Models))
	for _, model := range template.Models {
		if model.Name == "" || model.ProviderModelSlug == "" {
			return errors.New("model name and provider model slug are required")
		}
		if _, ok := seen[model.Name]; ok {
			return fmt.Errorf("model name %q is duplicated", model.Name)
		}
		seen[model.Name] = struct{}{}
		input := configuredModelInputFromDefaultTemplate(model)
		if err := validateConfiguredModelOptions(
			apiFormat,
			configuredModelOptionsFromCreate(input),
		); err != nil {
			return err
		}
		if _, err := ValidateAPIVariantOptions(input.APIVariantOptions); err != nil {
			return err
		}
	}
	return nil
}

func cloneDefaultModelProviderTemplate(template DefaultModelProviderTemplate) DefaultModelProviderTemplate {
	template.AuthOptions = cloneRawMessage(template.AuthOptions)
	template.Models = append([]DefaultConfiguredModelTemplate(nil), template.Models...)
	for i := range template.Models {
		model := &template.Models[i]
		model.DefaultMaxOutputTokens = cloneIntPtr(model.DefaultMaxOutputTokens)
		model.SupportsTools = cloneBoolPtr(model.SupportsTools)
		model.SupportedReasoningEfforts = append([]string(nil), model.SupportedReasoningEfforts...)
		model.InputModalities = append([]string(nil), model.InputModalities...)
		model.OutputModalities = append([]string(nil), model.OutputModalities...)
		model.APIVariantOptions = cloneRawMessage(model.APIVariantOptions)
	}
	return template
}

func cloneRawMessage(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func normalizeDefaultConfiguredModelTemplate(model DefaultConfiguredModelTemplate) DefaultConfiguredModelTemplate {
	input := normalizeCreateConfiguredModelInput(configuredModelInputFromDefaultTemplate(model))
	model.Name = input.Name
	model.ProviderModelSlug = input.ProviderModelSlug
	model.DefaultCacheRetention = input.DefaultCacheRetention
	model.DefaultReasoningEffort = input.DefaultReasoningEffort
	model.SupportedReasoningEfforts = input.SupportedReasoningEfforts
	model.InputModalities = input.InputModalities
	model.OutputModalities = input.OutputModalities
	model.APIVariantOptions = input.APIVariantOptions
	return model
}

func configuredModelInputFromDefaultTemplate(model DefaultConfiguredModelTemplate) CreateConfiguredModelInput {
	return CreateConfiguredModelInput{
		Name:                      model.Name,
		ProviderModelSlug:         model.ProviderModelSlug,
		ContextWindowTokens:       model.ContextWindowTokens,
		MaxOutputTokens:           model.MaxOutputTokens,
		DefaultMaxOutputTokens:    model.DefaultMaxOutputTokens,
		DefaultCacheRetention:     model.DefaultCacheRetention,
		SupportsTools:             model.SupportsTools,
		SupportsReasoning:         model.SupportsReasoning,
		DefaultReasoningEffort:    model.DefaultReasoningEffort,
		SupportedReasoningEfforts: model.SupportedReasoningEfforts,
		InputModalities:           model.InputModalities,
		OutputModalities:          model.OutputModalities,
		APIVariantOptions:         model.APIVariantOptions,
	}
}

func (s *Store) ProvisionDefaultTx(
	ctx context.Context,
	tx pgx.Tx,
	orgID, defaultProjectID, createdByUserID, credentialSecretID ID,
	template DefaultModelProviderTemplate,
) error {
	if isNilID(orgID) || isNilID(defaultProjectID) || isNilID(createdByUserID) || isNilID(credentialSecretID) {
		return errors.New("org, default project, creator, and credential are required")
	}
	prepared, err := PrepareDefaultModelProviderTemplate(template)
	if err != nil {
		return fmt.Errorf("default model provider %q: %w", template.Name, err)
	}
	qtx := s.q.WithTx(tx)
	provider, err := s.createModelProviderConfigTx(ctx, tx, qtx, CreateModelProviderConfigInput{
		OrgID:              orgID,
		Name:               prepared.Name,
		APIFormat:          prepared.APIFormat,
		APIVariant:         prepared.APIVariant,
		BaseURL:            prepared.BaseURL,
		EndpointPath:       prepared.EndpointPath,
		RequestTimeoutMS:   prepared.RequestTimeoutMS,
		AuthKind:           prepared.AuthKind,
		AuthOptions:        prepared.AuthOptions,
		CredentialSecretID: credentialSecretID,
		managementKind:     management.Cluster,
	})
	if err != nil {
		return fmt.Errorf("create default model provider: %w", err)
	}
	for _, modelTemplate := range prepared.Models {
		modelInput := configuredModelInputFromDefaultTemplate(modelTemplate)
		modelInput.OrgID = orgID
		modelInput.ModelProviderConfigID = provider.ID
		model, err := s.createConfiguredModelTx(
			ctx,
			tx,
			qtx,
			modelInput,
			management.Cluster,
		)
		if err != nil {
			return fmt.Errorf("create default configured model %q: %w", modelTemplate.Name, err)
		}
		if err := grantDefaultConfiguredModelToProjectTx(
			ctx,
			qtx,
			orgID,
			defaultProjectID,
			prepared.APIFormat,
			model,
		); err != nil {
			return err
		}
	}
	return nil
}

func grantDefaultConfiguredModelToProjectTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	orgID, projectID ID,
	apiFormat modelprotocol.APIFormat,
	model ConfiguredModelRecord,
) error {
	input := normalizeProjectModelGrantInput(CreateProjectModelGrantInput{
		OrgID:             orgID,
		ProjectID:         projectID,
		ConfiguredModelID: model.ID,
	})
	if err := validateProjectModelGrantForConfiguredModel(apiFormat, model, input); err != nil {
		return fmt.Errorf("validate default configured model grant: %w", err)
	}
	if _, err := qtx.UpsertProjectModelGrant(ctx, dbsqlc.UpsertProjectModelGrantParams{
		OrgID:                     input.OrgID,
		ProjectID:                 input.ProjectID,
		ConfiguredModelID:         input.ConfiguredModelID,
		ContextWindowTokens:       storeutil.Int32Ptr(input.ContextWindowTokens),
		MaxOutputTokens:           storeutil.Int32Ptr(input.MaxOutputTokens),
		DefaultMaxOutputTokens:    storeutil.Int32Ptr(input.DefaultMaxOutputTokens),
		DefaultCacheRetention:     storeutil.TextFromEmpty(input.DefaultCacheRetention),
		SupportsTools:             input.SupportsTools,
		SupportsReasoning:         input.SupportsReasoning,
		DefaultReasoningEffort:    input.DefaultReasoningEffort,
		SupportedReasoningEfforts: input.SupportedReasoningEfforts,
		InputModalities:           input.InputModalities,
		OutputModalities:          input.OutputModalities,
	}); err != nil {
		return fmt.Errorf("grant default configured model to project: %w", err)
	}
	return nil
}
