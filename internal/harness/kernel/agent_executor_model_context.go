package kernel

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func ensureModelSupportsTools(
	client model.Client,
	capabilities model.Capabilities,
	required bool,
) error {
	if !required || capabilities.SupportsTools == nil || *capabilities.SupportsTools {
		return nil
	}
	return fmt.Errorf(
		"model %q does not support tools required by the agent contract",
		client.RequestedProviderModelSlug(),
	)
}

func modelSelectionForContext(
	contextRow executionstore.ModelCallContextRecord,
	runtimeModel agentconfig.ModelCompiled,
) model.Selection {
	return model.Selection{
		OrgID:                     contextRow.OrgID.String(),
		ProjectID:                 contextRow.ProjectID.String(),
		ConfiguredModelRevisionID: contextRow.ConfiguredModelRevisionID.String(),
		Overrides:                 runtimeModel.Overrides(),
	}
}

func validateResolvedModelContext(
	resolved model.ResolvedClient,
	contextRow executionstore.ModelCallContextRecord,
) error {
	if resolved.Client == nil {
		return model.ProviderError{
			Kind:    model.ErrorKindInvalidRequest,
			Source:  "model_resolver",
			Code:    "missing_client",
			Message: "The configured model resolver returned no client.",
		}
	}
	revisionID, err := storage.ParseID(resolved.ConfiguredModelRevisionID)
	if err != nil || revisionID != contextRow.ConfiguredModelRevisionID {
		return model.ProviderError{
			Kind:    model.ErrorKindInvalidRequest,
			Source:  "model_resolver",
			Code:    "configured_model_revision_mismatch",
			Message: "The resolved model revision does not match the durable model context.",
		}
	}
	return nil
}

func (e AgentExecutor) modelContextToolRuntime(
	ctx context.Context,
	projectID, agentID storage.ID,
	contextRow executionstore.ModelCallContextRecord,
	now time.Time,
) ([]modelcontext.ToolSpec, error) {
	config, found, err := e.Store.Execution().GetAgentConfig(ctx, projectID, contextRow.AgentConfigID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("model context agent config is missing")
	}
	contract, err := agentconfig.RuntimeContractFromCompiled(
		config.CompiledDefinition,
		config.CompilerVersion,
		config.EffectiveDefinitionHash,
	)
	if err != nil {
		return nil, err
	}
	contextStore := modelcontext.NewStore(
		e.Store.Execution(),
		e.Store.Artifacts(),
		e.Store.Integrations(),
	)
	integrationTargets, err := contextStore.ListIntegrationTargets(ctx, projectID, agentID)
	if err != nil {
		return nil, err
	}
	channelTools, err := contextStore.GetAgentChannelToolEligibility(ctx, projectID, agentID)
	if err != nil {
		return nil, err
	}
	contract, err = modelcontext.WithImplicitIntegrationTools(
		contract,
		integrationTargets,
		channelTools,
	)
	if err != nil {
		return nil, err
	}
	specs, err := modelcontext.RuntimeContractToolSpecs(
		ctx,
		contextStore,
		projectID,
		agentID,
		contract,
		now,
	)
	return specs, err
}

func modelErrorSourceForClient(client model.Client) string {
	if client != nil {
		if apiFormat := model.APIFormatForClient(client); apiFormat != "" {
			return string(apiFormat)
		}
	}
	return "model"
}
