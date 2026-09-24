package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type integrationToolAccess struct {
	Authority         agentconfig.IntegrationToolAuthority
	Conversation      integrationdefinition.Scope
	OriginalContract  agentconfig.RuntimeContract
	CurrentConfigID   uuid.UUID
	Integration       integrationstore.ProjectIntegrationRecord
	Credential        secrets.Payload
	CredentialVersion uuid.UUID
}

func (e Executor) resolveIntegrationToolAuthority(
	ctx context.Context,
	turn Turn,
	tool executionstore.ToolCallRecord,
) (integrationToolAccess, error) {
	if err := e.ensureIntegrationPostOwnership(ctx, turn); err != nil {
		return integrationToolAccess{}, err
	}
	model, found, err := e.Store.Execution().
		GetModelCallContext(ctx, turn.ProjectID, turn.AgentID, tool.ModelCallContextID)
	if err != nil {
		return integrationToolAccess{}, err
	}
	if !found {
		return integrationToolAccess{}, integrationToolPreparationFailure(
			errors.New("integration tool model context is unavailable"),
		)
	}
	original, err := e.integrationRuntimeContract(ctx, turn.ProjectID, model.AgentConfigID)
	if err != nil {
		return integrationToolAccess{}, err
	}
	agent, err := e.Store.Execution().GetAgentInProject(ctx, turn.ProjectID, turn.AgentID)
	if err != nil {
		return integrationToolAccess{}, err
	}
	if agent.State != executionstore.AgentStateActive {
		return integrationToolAccess{}, integrationToolPreparationFailure(errors.New("agent is inactive"))
	}
	current, err := e.integrationRuntimeContract(ctx, turn.ProjectID, agent.CurrentConfigID)
	if err != nil {
		return integrationToolAccess{}, err
	}
	pinned, ok := original.IntegrationTools[tool.Name]
	if !ok {
		return integrationToolAccess{}, integrationToolPreparationFailure(
			errors.New("integration tool was not configured for this call"),
		)
	}
	integration, err := e.Store.Integrations().GetProjectIntegration(ctx, turn.ProjectID, pinned.IntegrationID)
	if errors.Is(err, storeerr.ErrNotFound) {
		return integrationToolAccess{}, integrationToolPreparationFailure(errors.New("integration is unavailable"))
	}
	if err != nil {
		return integrationToolAccess{}, err
	}
	name, _, valid := toolcatalog.SplitIntegrationToolName(tool.Name)
	if !valid || integration.Name != name || integration.State != integrationstore.ProjectIntegrationStateActive ||
		integration.OrgID != turn.OrgID {
		return integrationToolAccess{}, integrationToolPreparationFailure(errors.New("integration is unavailable"))
	}
	metadata := map[uuid.UUID]agentconfig.IntegrationResolution{
		pinned.IntegrationID: {IntegrationID: pinned.IntegrationID, IntegrationType: integration.IntegrationType},
	}
	authority, err := agentconfig.ResolveIntegrationToolAuthority(original, current, tool.Name, metadata)
	if err != nil {
		return integrationToolAccess{}, integrationToolPreparationFailure(
			fmt.Errorf("%w: %w", ErrToolAuthorizationInvalidated, err),
		)
	}
	entry, err := authority.Definition.Prepare(tool.Name)
	if err != nil {
		return integrationToolAccess{}, err
	}
	if err := jsonschema.Validate(entry.InputSchema, tool.Input); err != nil {
		return integrationToolAccess{}, integrationToolPreparationFailure(err)
	}
	access := integrationToolAccess{
		Authority:        authority,
		OriginalContract: original,
		CurrentConfigID:  agent.CurrentConfigID,
		Integration:      integration,
	}
	return access, nil
}

func (e Executor) integrationToolConversation(
	ctx context.Context, turn Turn, access integrationToolAccess,
) (integrationdefinition.Scope, error) {
	switch access.Authority.Definition.Scope {
	case toolcatalog.IntegrationToolScopeIntegration:
		return integrationdefinition.Scope{}, nil
	case toolcatalog.IntegrationToolScopeConversation:
	default:
		return integrationdefinition.Scope{}, errors.New("integration tool requires an explicit scope")
	}
	address, found, err := e.Store.Integrations().GetAgentIntegrationConversation(
		ctx,
		turn.ProjectID,
		turn.AgentID,
		access.Integration.ID,
	)
	if err != nil {
		return integrationdefinition.Scope{}, err
	}
	if !found {
		return integrationdefinition.Scope{}, integrationToolPreparationFailure(fmt.Errorf(
			"integration %q has no assigned conversation for this agent; this tool requires an assigned conversation", access.Integration.Name,
		))
	}
	conversation, err := integrationdefinition.ParseConversation(access.Integration.Provider, address.Kind, address.Ref)
	if err != nil {
		return integrationdefinition.Scope{}, integrationToolPreparationFailure(err)
	}
	return conversation, nil
}

func (e Executor) resolveIntegrationToolAccess(
	ctx context.Context,
	turn Turn,
	tool executionstore.ToolCallRecord,
) (integrationToolAccess, error) {
	access, err := e.resolveIntegrationToolAuthority(ctx, turn, tool)
	if err != nil {
		return integrationToolAccess{}, err
	}
	return e.prepareIntegrationToolAccess(ctx, turn, tool, access)
}

func (e Executor) prepareIntegrationToolAccess(
	ctx context.Context, turn Turn, tool executionstore.ToolCallRecord, access integrationToolAccess,
) (integrationToolAccess, error) {
	conversation, err := e.integrationToolConversation(ctx, turn, access)
	if err != nil {
		return integrationToolAccess{}, err
	}
	access.Conversation = conversation
	kind, err := integrationstore.ProjectIntegrationCredentialKind(access.Integration.Provider)
	if err != nil {
		return integrationToolAccess{}, err
	}
	credential, err := e.Store.Secrets().
		ReadProjectAvailableSecretPayload(ctx, secretstore.ReadProjectAvailableSecretPayloadInput{
			OrgID: turn.OrgID, ProjectID: turn.ProjectID, SecretID: access.Integration.CredentialSecretID, Kind: kind,
		})
	if err != nil {
		return integrationToolAccess{}, err
	}
	access.Credential, access.CredentialVersion = credential.Payload, credential.CurrentVersionID
	if err := e.recheckIntegrationToolAccess(ctx, turn, tool, access); err != nil {
		return integrationToolAccess{}, err
	}
	return access, nil
}

func (e Executor) recheckIntegrationToolAccess(
	ctx context.Context,
	turn Turn,
	tool executionstore.ToolCallRecord,
	access integrationToolAccess,
) error {
	if err := e.ensureIntegrationPostOwnership(ctx, turn); err != nil {
		return err
	}
	agent, err := e.Store.Execution().GetAgentInProject(ctx, turn.ProjectID, turn.AgentID)
	if err != nil {
		return err
	}
	if agent.State != executionstore.AgentStateActive {
		return errors.New("agent is inactive")
	}
	if agent.CurrentConfigID != access.CurrentConfigID {
		current, err := e.integrationRuntimeContract(ctx, turn.ProjectID, agent.CurrentConfigID)
		if err != nil {
			return err
		}
		ref := access.Authority.Tool.IntegrationID
		metadata := map[uuid.UUID]agentconfig.IntegrationResolution{
			ref: {IntegrationID: ref, IntegrationType: access.Integration.IntegrationType},
		}
		if _, err := agentconfig.ResolveIntegrationToolAuthority(
			access.OriginalContract,
			current,
			tool.Name,
			metadata,
		); err != nil {
			return fmt.Errorf("%w: %w", ErrToolAuthorizationInvalidated, err)
		}
	}
	integration, err := e.Store.Integrations().GetProjectIntegration(ctx, turn.ProjectID, access.Integration.ID)
	if err != nil {
		return err
	}
	if integration.State != integrationstore.ProjectIntegrationStateActive ||
		integration.SetupRevision != access.Integration.SetupRevision {
		return fmt.Errorf("%w: integration setup changed; submit a new call", ErrToolAuthorizationInvalidated)
	}
	secret, err := e.Store.Secrets().GetProjectAvailableSecret(
		ctx,
		turn.OrgID,
		turn.ProjectID,
		integration.CredentialSecretID,
	)
	if err != nil {
		return err
	}
	if secret.Secret.CurrentVersionID != access.CredentialVersion {
		return fmt.Errorf("%w: integration credentials changed; submit a new call", ErrToolAuthorizationInvalidated)
	}
	return nil
}

func (e Executor) integrationRuntimeContract(
	ctx context.Context,
	projectID, configID uuid.UUID,
) (agentconfig.RuntimeContract, error) {
	config, found, err := e.Store.Execution().GetAgentConfig(ctx, projectID, configID)
	if err != nil {
		return agentconfig.RuntimeContract{}, err
	}
	if !found {
		return agentconfig.RuntimeContract{}, errors.New("integration tool config is unavailable")
	}
	return agentconfig.RuntimeContractFromCompiled(
		config.CompiledDefinition,
		config.EffectiveDefinitionHash,
	)
}

func (e Executor) ensureIntegrationPostOwnership(ctx context.Context, turn Turn) error {
	return e.Store.Execution().EnsureRuntimeLockActive(
		ctx,
		turn.ProjectID,
		turn.AgentID,
		turn.RuntimeLockID,
	)
}
