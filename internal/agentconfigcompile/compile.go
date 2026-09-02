package agentconfigcompile

import (
	"context"
	"encoding/json"
	"fmt"

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
	Definition         json.RawMessage
	Source             string
	SourceFormat       string
	ConfiguredModelID  storage.ID
	CompiledDefinition json.RawMessage
	CompilerVersion    string
	DefinitionHash     string
}

func (body Body) CreateInput(projectID storage.ID) executionstore.CreateAgentConfigInput {
	return executionstore.CreateAgentConfigInput{
		ProjectID:               projectID,
		Definition:              body.Definition,
		Source:                  body.Source,
		SourceFormat:            body.SourceFormat,
		ConfiguredModelID:       body.ConfiguredModelID,
		CompiledDefinition:      body.CompiledDefinition,
		CompilerVersion:         body.CompilerVersion,
		EffectiveDefinitionHash: body.DefinitionHash,
	}
}

// Options binds every compile-time resolver to the given project so a source
// document can be compiled outside the HTTP layer.
func Options(
	ctx context.Context,
	store *storage.Store,
	orgID, projectID storage.ID,
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
		configuredModel, err := store.Models().GetConfiguredModelByName(
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
						providerConfigName,
						storeerr.ErrNotFound,
					),
				)
			}
			return agentconfig.ResolvedModelSelection{}, err
		}
		grant, err := store.Models().GetActiveProjectModelGrantForConfiguredModel(
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
						providerConfigName,
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

func Compile(
	ctx context.Context,
	store *storage.Store,
	orgID, projectID storage.ID,
	base agentconfig.CompileOptions,
	sourceFormat agentconfig.SourceFormat,
	source string,
) (Body, error) {
	if source == "" {
		return Body{}, fmt.Errorf("source is required")
	}
	result, err := agentconfig.Compile(sourceFormat, []byte(source), Options(ctx, store, orgID, projectID, base))
	if err != nil {
		return Body{}, err
	}
	resolvedConfiguredModelID, err := storage.ParseID(result.Compiled.Model.ConfiguredModelID)
	if err != nil || resolvedConfiguredModelID == storage.NilID {
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
		Definition:         json.RawMessage(result.CanonicalJSON),
		Source:             result.Source,
		SourceFormat:       string(result.SourceFormat),
		ConfiguredModelID:  resolvedConfiguredModelID,
		CompiledDefinition: json.RawMessage(result.CanonicalJSON),
		CompilerVersion:    agentconfig.CompilerVersion,
		DefinitionHash:     result.Hash,
	}, nil
}
