package agentconfigcompile

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/skillstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type Body struct {
	Source             string
	SourceFormat       string
	ConfiguredModelID  uuid.UUID
	CompiledDefinition json.RawMessage
	DefinitionHash     string
}

func (body Body) CreateInput(projectID uuid.UUID) executionstore.CreateAgentConfigInput {
	return executionstore.CreateAgentConfigInput{
		ProjectID:               projectID,
		Source:                  body.Source,
		SourceFormat:            body.SourceFormat,
		ConfiguredModelID:       body.ConfiguredModelID,
		CompiledDefinition:      body.CompiledDefinition,
		EffectiveDefinitionHash: body.DefinitionHash,
	}
}

func options(
	ctx context.Context,
	store *storage.Store,
	orgID, projectID uuid.UUID,
	base agentconfig.CompileOptions,
) agentconfig.CompileOptions {
	opts := base
	opts.ValidateSecretID = func(secretID uuid.UUID, expectedKind secrets.Kind) error {
		return store.Secrets().ValidateProjectSecretReference(ctx, orgID, projectID, secretID, expectedKind)
	}
	opts.ResolveModelSelection = func(
		providerConfigName string,
		configuredModelName string,
	) (agentconfig.ResolvedModelSelection, error) {
		providerConfig, err := store.Models().GetModelProviderConfigByName(ctx, orgID, providerConfigName)
		if err != nil {
			if storeerr.IsNotFound(err) {
				return agentconfig.ResolvedModelSelection{}, agentconfig.NewIssue(
					"/model/provider_config",
					fmt.Errorf("model provider config %q was not found: %w", providerConfigName, storeerr.ErrNotFound),
				)
			}
			return agentconfig.ResolvedModelSelection{}, err
		}
		return resolveGrantedModel(ctx, store.Models(), orgID, projectID, providerConfig, configuredModelName)
	}
	opts.ResolveMachineName = func(machineName string) (uuid.UUID, error) {
		return store.Execution().ResolveAgentConfigMachineName(ctx, projectID, machineName)
	}
	opts.ResolveMachinePoolName = func(machinePoolName string) (uuid.UUID, error) {
		return store.Execution().ResolveAgentConfigMachinePoolName(
			ctx,
			orgID,
			projectID,
			machinePoolName,
		)
	}
	opts.ResolveAgentProfileName = func(profileName string) (uuid.UUID, error) {
		profileID, err := store.Execution().ResolveAgentConfigProfileName(ctx, projectID, profileName)
		if err != nil {
			if storeerr.IsNotFound(err) {
				return uuid.Nil, fmt.Errorf("agent profile %q was not found: %w", profileName, storeerr.ErrNotFound)
			}
			return uuid.Nil, err
		}
		return profileID, nil
	}
	opts.ResolveMemoryStoreName = func(name string) (string, error) {
		record, err := store.Memories().Resolve(ctx, projectID, name)
		if err != nil {
			if storeerr.IsNotFound(err) {
				return "", fmt.Errorf("memory store %q was not found: %w", name, storeerr.ErrNotFound)
			}
			return "", err
		}
		return publicid.Encode(publicid.KindMemoryStore, record.ID)
	}
	opts.ResolveSkillID = func(skillID string) (agentconfig.SkillResolution, error) {
		records, missing, err := store.Skills().GetSkillsByIDsForCompile(ctx, skillstore.GetSkillsByIDsInput{
			OrgID:     orgID,
			ProjectID: projectID,
			IDs:       []string{skillID},
		})
		if err != nil {
			return agentconfig.SkillResolution{}, err
		}
		if len(missing) > 0 {
			return agentconfig.SkillResolution{}, fmt.Errorf("skill not found or not visible: %s", skillID)
		}
		if len(records) != 1 {
			return agentconfig.SkillResolution{}, fmt.Errorf("skill resolver returned %d records for %s", len(records), skillID)
		}
		rec := records[0]
		return agentconfig.SkillResolution{
			ID:   rec.ID,
			Name: rec.Name,
		}, nil
	}
	return opts
}

func resolveGrantedModel(
	ctx context.Context,
	models *modelstore.Store,
	orgID, projectID uuid.UUID,
	providerConfig modelstore.ModelProviderConfigRecord,
	configuredModelName string,
) (agentconfig.ResolvedModelSelection, error) {
	configuredModel, err := models.GetConfiguredModelByName(
		ctx,
		orgID,
		providerConfig.ID,
		configuredModelName,
	)
	if err != nil {
		if storeerr.IsNotFound(err) {
			return agentconfig.ResolvedModelSelection{}, agentconfig.NewIssue(
				"/model/name",
				fmt.Errorf(
					"configured model %q is not configured for model provider config %q: %w",
					configuredModelName,
					providerConfig.Name,
					storeerr.ErrNotFound,
				),
			)
		}
		return agentconfig.ResolvedModelSelection{}, err
	}
	grant, err := models.GetActiveProjectModelGrantForConfiguredModel(
		ctx,
		orgID,
		projectID,
		configuredModel.ID,
	)
	if err != nil {
		if storeerr.IsNotFound(err) {
			return agentconfig.ResolvedModelSelection{}, agentconfig.NewIssue(
				"/model/name",
				fmt.Errorf(
					"configured model %q on model provider config %q does not have an active project grant: %w",
					configuredModelName,
					providerConfig.Name,
					storeerr.ErrNotFound,
				),
			)
		}
		return agentconfig.ResolvedModelSelection{}, err
	}
	effectiveModel, err := modelstore.EffectiveConfiguredModelForProjectGrant(
		providerConfig.APIFormat,
		configuredModel,
		grant,
	)
	if err != nil {
		return agentconfig.ResolvedModelSelection{}, err
	}
	supportsTools := effectiveModel.SupportsTools
	return agentconfig.ResolvedModelSelection{
		ConfiguredModelID: configuredModel.ID,
		SupportsTools:     &supportsTools,
	}, nil
}

func DeriveSubagentConfig(
	base executionstore.AgentConfigRecord,
	subagent agentconfig.SubagentCompiled,
	depth agentconfig.SubagentDepth,
) (Body, error) {
	var baseCompiled agentconfig.Compiled
	if err := json.Unmarshal(base.CompiledDefinition, &baseCompiled); err != nil {
		return Body{}, fmt.Errorf("decode base compiled agent config: %w", err)
	}
	child := agentconfig.SubagentCompiledFrom(baseCompiled, subagent, depth)
	encoded, err := agentconfig.EncodeCompiled(child)
	if err != nil {
		return Body{}, err
	}
	if child.Model.ConfiguredModelID == uuid.Nil {
		return Body{}, fmt.Errorf("subagent model must resolve to a configured project-granted model")
	}
	return Body{
		ConfiguredModelID:  child.Model.ConfiguredModelID,
		CompiledDefinition: json.RawMessage(encoded.CanonicalJSON),
		DefinitionHash:     encoded.Hash,
	}, nil
}

func Compile(
	ctx context.Context,
	store *storage.Store,
	orgID, projectID uuid.UUID,
	base agentconfig.CompileOptions,
	sourceFormat agentconfig.SourceFormat,
	source string,
) (Body, error) {
	if source == "" {
		return Body{}, fmt.Errorf("source is required")
	}
	result, err := agentconfig.Compile(sourceFormat, []byte(source), options(ctx, store, orgID, projectID, base))
	if err != nil {
		return Body{}, err
	}
	if webhook := result.Compiled.EventWebhook; webhook != nil && webhook.SigningSecretID != uuid.Nil {
		secret, err := store.Execution().ReadEventWebhookSigningSecret(ctx, executionstore.EventWebhookTarget{
			OrgID: orgID, ProjectID: projectID, SigningSecretID: webhook.SigningSecretID,
		})
		if err != nil {
			return Body{}, err
		}
		if _, err := agentconfig.DecodeEventWebhookSigningKey(secret); err != nil {
			return Body{}, &agentconfig.ValidationError{Issues: []agentconfig.Issue{{
				Path: "/event_webhook/signing_secret_id", Message: err.Error(),
			}}}
		}
	}
	if result.Compiled.Model.ConfiguredModelID == uuid.Nil {
		return Body{}, fmt.Errorf(
			"model.provider_config and model.name must resolve to a configured project-granted model",
		)
	}
	if err := store.Execution().ValidateAgentConfigMachineSources(
		ctx,
		projectID,
		json.RawMessage(result.CanonicalJSON),
		result.Hash,
	); err != nil {
		return Body{}, err
	}
	return Body{
		Source:             result.Source,
		SourceFormat:       string(result.SourceFormat),
		ConfiguredModelID:  result.Compiled.Model.ConfiguredModelID,
		CompiledDefinition: json.RawMessage(result.CanonicalJSON),
		DefinitionHash:     result.Hash,
	}, nil
}
