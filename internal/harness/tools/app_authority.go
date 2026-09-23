package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type appToolAccess struct {
	Authority         agentconfig.AppToolAuthority
	Conversation      appdefinition.Scope
	OriginalContract  agentconfig.RuntimeContract
	CurrentConfigID   uuid.UUID
	App               integrationstore.ProjectAppRecord
	Credential        secrets.Payload
	CredentialVersion uuid.UUID
}

func (e Executor) resolveAppToolAuthority(
	ctx context.Context,
	turn Turn,
	tool executionstore.ToolCallRecord,
) (appToolAccess, error) {
	if err := e.ensureIntegrationPostOwnership(ctx, turn); err != nil {
		return appToolAccess{}, err
	}
	model, found, err := e.Store.Execution().
		GetModelCallContext(ctx, turn.ProjectID, turn.AgentID, tool.ModelCallContextID)
	if err != nil {
		return appToolAccess{}, err
	}
	if !found {
		return appToolAccess{}, appToolPreparationFailure(errors.New("app tool model context is unavailable"))
	}
	original, err := e.appRuntimeContract(ctx, turn.ProjectID, model.AgentConfigID)
	if err != nil {
		return appToolAccess{}, err
	}
	agent, err := e.Store.Execution().GetAgentInProject(ctx, turn.ProjectID, turn.AgentID)
	if err != nil {
		return appToolAccess{}, err
	}
	if agent.State != executionstore.AgentStateActive {
		return appToolAccess{}, appToolPreparationFailure(errors.New("agent is inactive"))
	}
	current, err := e.appRuntimeContract(ctx, turn.ProjectID, agent.CurrentConfigID)
	if err != nil {
		return appToolAccess{}, err
	}
	pinned, ok := original.AppTools[tool.Name]
	if !ok {
		return appToolAccess{}, appToolPreparationFailure(errors.New("app tool was not configured for this call"))
	}
	app, err := e.Store.Integrations().GetProjectApp(ctx, turn.ProjectID, pinned.AppID)
	if errors.Is(err, storeerr.ErrNotFound) {
		return appToolAccess{}, appToolPreparationFailure(errors.New("app is unavailable"))
	}
	if err != nil {
		return appToolAccess{}, err
	}
	name, _, valid := toolcatalog.SplitAppToolName(tool.Name)
	if !valid || app.Name != name || app.State != integrationstore.ProjectAppStateActive || app.OrgID != turn.OrgID {
		return appToolAccess{}, appToolPreparationFailure(errors.New("app is unavailable"))
	}
	metadata := map[uuid.UUID]agentconfig.AppResolution{pinned.AppID: {AppID: pinned.AppID, AppType: app.AppType}}
	authority, err := agentconfig.ResolveAppToolAuthority(original, current, tool.Name, metadata)
	if err != nil {
		return appToolAccess{}, appToolPreparationFailure(fmt.Errorf("%w: %w", ErrToolAuthorizationInvalidated, err))
	}
	entry, err := authority.Definition.Prepare(tool.Name)
	if err != nil {
		return appToolAccess{}, err
	}
	if err := jsonschema.Validate(entry.InputSchema, tool.Input); err != nil {
		return appToolAccess{}, appToolPreparationFailure(err)
	}
	access := appToolAccess{
		Authority:        authority,
		OriginalContract: original,
		CurrentConfigID:  agent.CurrentConfigID,
		App:              app,
	}
	return access, nil
}

func (e Executor) appToolConversation(
	ctx context.Context, turn Turn, access appToolAccess,
) (appdefinition.Scope, error) {
	switch access.Authority.Definition.Scope {
	case toolcatalog.AppToolScopeApp:
		return appdefinition.Scope{}, nil
	case toolcatalog.AppToolScopeConversation:
	default:
		return appdefinition.Scope{}, errors.New("app tool requires an explicit scope")
	}
	address, found, err := e.Store.Integrations().GetAgentAppConversation(ctx, turn.ProjectID, turn.AgentID, access.App.ID)
	if err != nil {
		return appdefinition.Scope{}, err
	}
	if !found {
		return appdefinition.Scope{}, appToolPreparationFailure(fmt.Errorf(
			"app %q has no assigned conversation for this agent; this tool requires an assigned conversation", access.App.Name,
		))
	}
	conversation, err := appdefinition.ParseConversation(access.App.Provider, address.Kind, address.Ref)
	if err != nil {
		return appdefinition.Scope{}, appToolPreparationFailure(err)
	}
	return conversation, nil
}

func (e Executor) resolveAppToolAccess(
	ctx context.Context,
	turn Turn,
	tool executionstore.ToolCallRecord,
) (appToolAccess, error) {
	access, err := e.resolveAppToolAuthority(ctx, turn, tool)
	if err != nil {
		return appToolAccess{}, err
	}
	return e.prepareAppToolAccess(ctx, turn, tool, access)
}

func (e Executor) prepareAppToolAccess(
	ctx context.Context, turn Turn, tool executionstore.ToolCallRecord, access appToolAccess,
) (appToolAccess, error) {
	conversation, err := e.appToolConversation(ctx, turn, access)
	if err != nil {
		return appToolAccess{}, err
	}
	access.Conversation = conversation
	kind, err := integrationstore.ProjectAppCredentialKind(access.App.Provider)
	if err != nil {
		return appToolAccess{}, err
	}
	credential, err := e.Store.Secrets().
		ReadProjectAvailableSecretPayload(ctx, secretstore.ReadProjectAvailableSecretPayloadInput{
			OrgID: turn.OrgID, ProjectID: turn.ProjectID, SecretID: access.App.CredentialSecretID, Kind: kind,
		})
	if err != nil {
		return appToolAccess{}, err
	}
	access.Credential, access.CredentialVersion = credential.Payload, credential.CurrentVersionID
	if err := e.recheckAppToolAccess(ctx, turn, tool, access); err != nil {
		return appToolAccess{}, err
	}
	return access, nil
}

func (e Executor) recheckAppToolAccess(
	ctx context.Context,
	turn Turn,
	tool executionstore.ToolCallRecord,
	access appToolAccess,
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
		current, err := e.appRuntimeContract(ctx, turn.ProjectID, agent.CurrentConfigID)
		if err != nil {
			return err
		}
		ref := access.Authority.Tool.AppID
		metadata := map[uuid.UUID]agentconfig.AppResolution{ref: {AppID: ref, AppType: access.App.AppType}}
		if _, err := agentconfig.ResolveAppToolAuthority(access.OriginalContract, current, tool.Name, metadata); err != nil {
			return fmt.Errorf("%w: %w", ErrToolAuthorizationInvalidated, err)
		}
	}
	app, err := e.Store.Integrations().GetProjectApp(ctx, turn.ProjectID, access.App.ID)
	if err != nil {
		return err
	}
	if app.State != integrationstore.ProjectAppStateActive || app.SetupRevision != access.App.SetupRevision {
		return fmt.Errorf("%w: app setup changed; submit a new call", ErrToolAuthorizationInvalidated)
	}
	secret, err := e.Store.Secrets().GetProjectAvailableSecret(ctx, turn.OrgID, turn.ProjectID, app.CredentialSecretID)
	if err != nil {
		return err
	}
	if secret.Secret.CurrentVersionID != access.CredentialVersion {
		return fmt.Errorf("%w: app credentials changed; submit a new call", ErrToolAuthorizationInvalidated)
	}
	return nil
}

func (e Executor) appRuntimeContract(
	ctx context.Context,
	projectID, configID uuid.UUID,
) (agentconfig.RuntimeContract, error) {
	config, found, err := e.Store.Execution().GetAgentConfig(ctx, projectID, configID)
	if err != nil {
		return agentconfig.RuntimeContract{}, err
	}
	if !found {
		return agentconfig.RuntimeContract{}, errors.New("app tool config is unavailable")
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
