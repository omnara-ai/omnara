package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
)

type appToolAccess struct {
	Authority         agentconfig.AppToolAuthority
	OriginalContract  agentconfig.RuntimeContract
	CurrentConfigID   uuid.UUID
	Connection        integrationstore.IntegrationConnectionRecord
	Credential        secrets.Payload
	CredentialVersion uuid.UUID
}

// Resolve against the model request's immutable config first. A reused resource
// key or an added destination must never change a previously proposed call.
// Current config, live connection and secret grants remain revocable authority.
func (e Executor) resolveAppToolScope(
	ctx context.Context,
	turn Turn,
	tool executionstore.ToolCallRecord,
	resourceKey string,
) (appToolAccess, error) {
	if err := e.ensureIntegrationPostOwnership(ctx, turn); err != nil {
		return appToolAccess{}, err
	}
	modelContext, found, err := e.Store.Execution().
		GetModelCallContext(ctx, turn.ProjectID, turn.AgentID, tool.ModelCallContextID)
	if err != nil {
		return appToolAccess{}, err
	}
	if !found {
		return appToolAccess{}, errors.New("app tool model context is unavailable")
	}
	original, err := e.appRuntimeContract(ctx, turn.ProjectID, modelContext.AgentConfigID)
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
	authority, err := agentconfig.ResolveAppToolAuthority(original, current, tool.Name, resourceKey)
	if err != nil {
		return appToolAccess{}, fmt.Errorf("%w: %w", ErrToolAuthorizationInvalidated, err)
	}
	connectionID, err := publicid.Decode(publicid.KindIntegrationConnection, authority.Original.ConnectionID)
	if err != nil {
		return appToolAccess{}, err
	}
	connection, err := e.Store.Integrations().GetIntegrationConnection(ctx, turn.ProjectID, connectionID)
	if err != nil {
		return appToolAccess{}, err
	}
	definition, _ := appdefinition.Lookup(authority.Original.Definition)
	if connection.State != integrationstore.IntegrationConnectionStateActive ||
		connection.Provider != definition.Provider ||
		connection.OrgID != turn.OrgID {
		return appToolAccess{}, errors.New("app connection is unavailable")
	}
	return appToolAccess{
		Authority:        authority,
		OriginalContract: original,
		CurrentConfigID:  agent.CurrentConfigID,
		Connection:       connection,
	}, nil
}

func (e Executor) resolveAppToolAccess(
	ctx context.Context,
	turn Turn,
	tool executionstore.ToolCallRecord,
	resourceKey string,
) (appToolAccess, error) {
	access, err := e.resolveAppToolScope(ctx, turn, tool, resourceKey)
	if err != nil {
		return appToolAccess{}, err
	}
	connection := access.Connection
	kind, err := integrationstore.IntegrationConnectionCredentialKind(connection.Provider)
	if err != nil {
		return appToolAccess{}, err
	}
	credential, err := e.Store.Secrets().
		ReadProjectAvailableSecretPayload(ctx, secretstore.ReadProjectAvailableSecretPayloadInput{
			OrgID: turn.OrgID, ProjectID: turn.ProjectID, SecretID: connection.CredentialSecretID, Kind: kind,
		})
	if err != nil {
		return appToolAccess{}, err
	}
	// Secret resolution can involve external key unwrapping. Recheck identity
	// and revocation afterwards instead of retaining a transaction during I/O.
	latest, err := e.Store.Integrations().GetIntegrationConnection(ctx, turn.ProjectID, connection.ID)
	if err != nil {
		return appToolAccess{}, err
	}
	if latest.State != integrationstore.IntegrationConnectionStateActive ||
		!latest.UpdatedAt.Equal(connection.UpdatedAt) {
		return appToolAccess{}, errors.New("app connection changed while resolving credentials")
	}
	access.Credential, access.CredentialVersion = credential.Payload, credential.CurrentVersionID
	return access, nil
}

func (e Executor) recheckAppToolAccess(
	ctx context.Context,
	turn Turn,
	tool executionstore.ToolCallRecord,
	access appToolAccess,
	scope appdefinition.Scope,
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
	// Configs are immutable. Reuse the resolved contract while its ID remains
	// current, but check live connection and credential availability every time.
	authority := access.Authority
	if agent.CurrentConfigID != access.CurrentConfigID {
		current, err := e.appRuntimeContract(ctx, turn.ProjectID, agent.CurrentConfigID)
		if err != nil {
			return err
		}
		authority, err = agentconfig.ResolveAppToolAuthority(
			access.OriginalContract,
			current,
			tool.Name,
			access.Authority.ResourceKey,
		)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrToolAuthorizationInvalidated, err)
		}
	}
	connection, err := e.Store.Integrations().GetIntegrationConnection(ctx, turn.ProjectID, access.Connection.ID)
	if err != nil {
		return err
	}
	if !authority.AllowsScope(scope) || connection.State != integrationstore.IntegrationConnectionStateActive ||
		!connection.UpdatedAt.Equal(access.Connection.UpdatedAt) {
		return fmt.Errorf("%w: app scope or connection changed; submit a new call", ErrToolAuthorizationInvalidated)
	}
	secret, err := e.Store.Secrets().
		GetProjectAvailableSecret(ctx, turn.OrgID, turn.ProjectID, connection.CredentialSecretID)
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
