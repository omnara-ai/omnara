package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type appToolAccess struct {
	Authority         agentconfig.AppToolAuthority
	Arguments         toolcatalog.AppToolArguments
	OriginalContract  agentconfig.RuntimeContract
	CurrentConfigID   uuid.UUID
	App               integrationstore.ProjectAppRecord
	FollowListenerKey string
	Credential        secrets.Payload
	CredentialVersion uuid.UUID
}

// Resolve the immutable proposed tool first; a reused app name cannot retarget
// it. The current config and live app remain revocable execution authority.
func (e Executor) resolveAppToolScope(
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
		return appToolAccess{}, errors.New("app tool model context is unavailable")
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
		return appToolAccess{}, errors.New("agent is inactive")
	}
	current, err := e.appRuntimeContract(ctx, turn.ProjectID, agent.CurrentConfigID)
	if err != nil {
		return appToolAccess{}, err
	}
	pinned, ok := original.AppTools[tool.Name]
	if !ok {
		return appToolAccess{}, errors.New("app tool was not configured for this call")
	}
	id, err := publicid.Decode(publicid.KindProjectApp, pinned.AppID)
	if err != nil {
		return appToolAccess{}, err
	}
	app, err := e.Store.Integrations().GetProjectApp(ctx, turn.ProjectID, id)
	if err != nil {
		return appToolAccess{}, err
	}
	name, _, valid := toolcatalog.SplitAppToolName(tool.Name)
	if !valid || app.Name != name || app.State != integrationstore.ProjectAppStateActive || app.OrgID != turn.OrgID {
		return appToolAccess{}, errors.New("app is unavailable")
	}
	metadata := map[string]agentconfig.AppResolution{pinned.AppID: {AppID: pinned.AppID, Definition: app.DefinitionID}}
	authority, err := agentconfig.ResolveAppToolAuthority(original, current, tool.Name, metadata)
	if err != nil {
		return appToolAccess{}, fmt.Errorf("%w: %w", ErrToolAuthorizationInvalidated, err)
	}
	args, err := authority.Definition.ResolveArgs(authority.Tool.Config, tool.Input)
	if err != nil {
		return appToolAccess{}, err
	}
	access := appToolAccess{
		Authority:        authority,
		Arguments:        args,
		OriginalContract: original,
		CurrentConfigID:  agent.CurrentConfigID,
		App:              app,
	}
	if args.FollowReplies {
		key := name + "__" + authority.Definition.FollowListener
		before, was := original.Listeners[key]
		after, is := current.Listeners[key]
		if authority.Definition.FollowListener == "" || !was || !is || before.AppID != pinned.AppID ||
			after.AppID != pinned.AppID {
			return appToolAccess{}, errors.New(
				"following replies requires the app's listener in the original and current config",
			)
		}
		access.FollowListenerKey = key
	}
	return access, nil
}

func (e Executor) resolveAppToolAccess(
	ctx context.Context,
	turn Turn,
	tool executionstore.ToolCallRecord,
) (appToolAccess, error) {
	access, err := e.resolveAppToolScope(ctx, turn, tool)
	if err != nil {
		return appToolAccess{}, err
	}
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
		metadata := map[string]agentconfig.AppResolution{ref: {AppID: ref, Definition: access.App.DefinitionID}}
		if _, err := agentconfig.ResolveAppToolAuthority(access.OriginalContract, current, tool.Name, metadata); err != nil {
			return fmt.Errorf("%w: %w", ErrToolAuthorizationInvalidated, err)
		}
		if access.FollowListenerKey != "" && current.Listeners[access.FollowListenerKey].AppID != ref {
			return fmt.Errorf("%w: listener removed", ErrToolAuthorizationInvalidated)
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
		config.CompilerVersion,
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
