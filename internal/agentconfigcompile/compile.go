package agentconfigcompile

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
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
	CompilerVersion    string
	DefinitionHash     string
}

func (body Body) CreateInput(projectID uuid.UUID) executionstore.CreateAgentConfigInput {
	return executionstore.CreateAgentConfigInput{
		ProjectID:               projectID,
		Source:                  body.Source,
		SourceFormat:            body.SourceFormat,
		ConfiguredModelID:       body.ConfiguredModelID,
		CompiledDefinition:      body.CompiledDefinition,
		CompilerVersion:         body.CompilerVersion,
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
	opts.ValidateSecretID = func(secretID string, expectedKind secrets.Kind) error {
		decoded, err := publicid.Decode(publicid.KindSecret, secretID)
		if err != nil {
			return err
		}
		return store.Secrets().ValidateProjectSecretReference(ctx, orgID, projectID, decoded, expectedKind)
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
	opts.ResolveMachineName = func(machineName string) (string, error) {
		machineID, err := store.Execution().ResolveAgentConfigMachineName(ctx, projectID, machineName)
		if err != nil {
			return "", err
		}
		return publicid.Encode(publicid.KindMachine, machineID)
	}
	opts.ResolveMachinePoolName = func(machinePoolName string) (string, error) {
		machinePoolID, err := store.Execution().ResolveAgentConfigMachinePoolName(
			ctx,
			orgID,
			projectID,
			machinePoolName,
		)
		if err != nil {
			return "", err
		}
		return publicid.Encode(publicid.KindMachinePool, machinePoolID)
	}
	opts.ResolveAgentProfileName = func(profileName string) (string, error) {
		profileID, err := store.Execution().ResolveAgentConfigProfileName(ctx, projectID, profileName)
		if err != nil {
			if storeerr.IsNotFound(err) {
				return "", fmt.Errorf("agent profile %q was not found: %w", profileName, storeerr.ErrNotFound)
			}
			return "", err
		}
		return publicid.Encode(publicid.KindAgentProfile, profileID)
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
		encoded, err := publicid.Encode(publicid.KindSkill, rec.ID)
		if err != nil {
			return agentconfig.SkillResolution{}, fmt.Errorf("encode skill public id: %w", err)
		}
		return agentconfig.SkillResolution{
			PublicID: encoded,
			Name:     rec.Name,
		}, nil
	}
	return opts
}

type ModelReads interface {
	GetConfiguredModel(ctx context.Context, orgID, id uuid.UUID) (modelstore.ConfiguredModelRecord, error)
	GetConfiguredModelByName(
		ctx context.Context, orgID, providerConfigID uuid.UUID, name string,
	) (modelstore.ConfiguredModelRecord, error)
	GetModelProviderConfig(ctx context.Context, orgID, id uuid.UUID) (modelstore.ModelProviderConfigRecord, error)
	GetModelProviderConfigByName(
		ctx context.Context, orgID uuid.UUID, name string,
	) (modelstore.ModelProviderConfigRecord, error)
	GetActiveProjectModelGrantForConfiguredModel(
		ctx context.Context, orgID, projectID, configuredModelID uuid.UUID,
	) (modelstore.ProjectModelGrantRecord, error)
}

func resolveGrantedModel(
	ctx context.Context,
	models ModelReads,
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
		ConfiguredModelID: configuredModel.ID.String(),
		SupportsTools:     &supportsTools,
	}, nil
}

// SubagentModelResolver resolves a subagent's model override against the
// organization's configured models and the project's grants through the
// given reads, so callers inside a transaction can keep resolution on their
// own connection.
func SubagentModelResolver(
	ctx context.Context,
	models ModelReads,
	orgID, projectID uuid.UUID,
) agentconfig.SubagentModelResolver {
	return func(
		baseConfiguredModelID string,
		override agentconfig.SubagentModelCompiled,
	) (agentconfig.ResolvedModelSelection, error) {
		baseModelID, err := uuid.Parse(baseConfiguredModelID)
		if err != nil {
			return agentconfig.ResolvedModelSelection{}, fmt.Errorf("parse base configured model id: %w", err)
		}
		baseModel, err := models.GetConfiguredModel(ctx, orgID, baseModelID)
		if err != nil {
			return agentconfig.ResolvedModelSelection{}, fmt.Errorf("load base configured model: %w", err)
		}
		var providerConfig modelstore.ModelProviderConfigRecord
		if override.ProviderConfig != "" {
			providerConfig, err = models.GetModelProviderConfigByName(ctx, orgID, override.ProviderConfig)
			if err != nil {
				if storeerr.IsNotFound(err) {
					return agentconfig.ResolvedModelSelection{}, agentconfig.NewIssue(
						"/model/provider_config",
						fmt.Errorf(
							"model provider config %q was not found: %w", override.ProviderConfig, storeerr.ErrNotFound,
						),
					)
				}
				return agentconfig.ResolvedModelSelection{}, err
			}
		} else {
			providerConfig, err = models.GetModelProviderConfig(ctx, orgID, baseModel.ModelProviderConfigID)
			if err != nil {
				return agentconfig.ResolvedModelSelection{}, fmt.Errorf("load base model provider config: %w", err)
			}
		}
		configuredModelName := override.Name
		if configuredModelName == "" {
			configuredModelName = baseModel.Name
		}
		return resolveGrantedModel(ctx, models, orgID, projectID, providerConfig, configuredModelName)
	}
}

func DeriveSubagentConfig(
	base executionstore.AgentConfigRecord,
	subagent agentconfig.SubagentCompiled,
	depth agentconfig.SubagentDepth,
	resolveModel agentconfig.SubagentModelResolver,
) (Body, error) {
	var baseCompiled agentconfig.Compiled
	if err := json.Unmarshal(base.CompiledDefinition, &baseCompiled); err != nil {
		return Body{}, fmt.Errorf("decode base compiled agent config: %w", err)
	}
	child, err := agentconfig.SubagentCompiledFrom(baseCompiled, subagent, depth, resolveModel)
	if err != nil {
		return Body{}, err
	}
	encoded, err := agentconfig.EncodeCompiled(child)
	if err != nil {
		return Body{}, err
	}
	configuredModelID, err := uuid.Parse(child.Model.ConfiguredModelID)
	if err != nil || configuredModelID == uuid.Nil {
		return Body{}, fmt.Errorf("subagent model must resolve to a configured project-granted model")
	}
	return Body{
		ConfiguredModelID:  configuredModelID,
		CompiledDefinition: json.RawMessage(encoded.CanonicalJSON),
		CompilerVersion:    agentconfig.CompilerVersion,
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
	if webhook := result.Compiled.EventWebhook; webhook != nil && webhook.SigningSecretID != "" {
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
	resolvedConfiguredModelID, err := uuid.Parse(result.Compiled.Model.ConfiguredModelID)
	if err != nil || resolvedConfiguredModelID == uuid.Nil {
		return Body{}, fmt.Errorf(
			"model.provider_config and model.name must resolve to a configured project-granted model",
		)
	}
	if err := store.Execution().ValidateAgentConfigMachineSources(
		ctx,
		projectID,
		json.RawMessage(result.CanonicalJSON),
		agentconfig.CompilerVersion,
		result.Hash,
	); err != nil {
		return Body{}, err
	}
	return Body{
		Source:             result.Source,
		SourceFormat:       string(result.SourceFormat),
		ConfiguredModelID:  resolvedConfiguredModelID,
		CompiledDefinition: json.RawMessage(result.CanonicalJSON),
		CompilerVersion:    agentconfig.CompilerVersion,
		DefinitionHash:     result.Hash,
	}, nil
}
