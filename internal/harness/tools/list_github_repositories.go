package tools

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	integrationruntime "github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type githubRepositoryPayload struct {
	FullName string `json:"full_name"`
}

type githubRepositoryListResult struct {
	CanPush      bool                      `json:"can_push"`
	Repositories []githubRepositoryPayload `json:"repositories"`
	listing.OffsetPage
}

func runListGitHubRepositories(ctx context.Context, call asyncToolContext) (asyncPhaseResult, error) {
	var input struct {
		Offset int `json:"offset"`
		Limit  int `json:"limit"`
	}
	if err := decodeSingleStrictJSON(call.Call.Input, &input, "list_github_repositories request"); err != nil {
		return nil, err
	}
	if input.Limit == 0 {
		input.Limit = toolcatalog.ListGitHubRepositoriesDefaultLimit
	}
	contract, err := call.Executor.runtimeContractForTurn(ctx, call.Turn)
	if err != nil {
		return nil, err
	}
	credentials := contract.GitCredentials
	if credentials == nil {
		return integrationToolFailure(errors.New("no Git credentials are configured for this agent"))
	}
	integration, err := call.Executor.Store.Integrations().
		GetIntegration(ctx, call.Turn.ProjectID, credentials.IntegrationID)
	if err != nil && !errors.Is(err, storeerr.ErrNotFound) {
		return nil, err
	}
	if err != nil || integration.State != integrationstore.IntegrationStateActive ||
		integration.OrgID != call.Turn.OrgID {
		return integrationToolFailure(errors.New("integration is unavailable"))
	}
	settings, err := integrationdefinition.ReadGitHubSettings(integration.Settings)
	if err != nil {
		return nil, err
	}
	kind, err := integrationstore.IntegrationCredentialKind(integration.Provider)
	if err != nil {
		return nil, err
	}
	credential, err := call.Executor.Store.Secrets().
		ReadProjectAvailableSecretPayload(ctx, secretstore.ReadProjectAvailableSecretPayloadInput{
			OrgID: call.Turn.OrgID, ProjectID: call.Turn.ProjectID, SecretID: integration.CredentialSecretID, Kind: kind,
		})
	if err != nil {
		return nil, err
	}
	config, err := call.Executor.githubClientConfig(integration, credential.Payload)
	if err != nil {
		return integrationToolFailure(err)
	}
	config.BeforeRequest = func(ctx context.Context) error {
		return call.Executor.recheckGitCredentialAccess(ctx, call.Turn, credentials, integration, credential.CurrentVersionID)
	}
	client, err := github.NewClient(config)
	if err != nil {
		return integrationToolFailure(err)
	}
	canPush := settings.CodeWrite && !credentials.ReadOnly
	page := input.Offset/github.InstallationRepositoriesPageSize + 1
	window, next, err := client.InstallationRepositories(ctx, canPush, credentials.RepositoryIDs, page)
	if err != nil {
		return integrationToolFailure(err)
	}
	nextStart := 0
	if next != 0 {
		nextStart = (next - 1) * github.InstallationRepositoriesPageSize
	}
	windowStart := (page - 1) * github.InstallationRepositoriesPageSize
	result := githubRepositoryPage(window, windowStart, nextStart, input.Offset, input.Limit)
	result.CanPush = canPush
	content, err := structuredToolResultContent(result)
	if err != nil {
		return nil, err
	}
	return completeAsynchronously(content), nil
}

func githubRepositoryPage(
	window []github.Repository, windowStart, nextStart, offset, limit int,
) githubRepositoryListResult {
	start := min(offset-windowStart, len(window))
	end := min(start+limit, len(window))
	result := githubRepositoryListResult{
		Repositories: make([]githubRepositoryPayload, 0, end-start),
		OffsetPage:   listing.ContinueAt(nextStart, nextStart > 0),
	}
	if end < len(window) {
		result.OffsetPage = listing.ContinueAt(offset+end-start, true)
	}
	for _, repository := range window[start:end] {
		result.Repositories = append(result.Repositories, githubRepositoryPayload{FullName: repository.FullName})
	}
	return result
}

func (e Executor) recheckGitCredentialAccess(
	ctx context.Context,
	turn Turn,
	credentials *agentconfig.GitCredentialsCompiled,
	integration integrationstore.IntegrationRecord,
	credentialVersion uuid.UUID,
) error {
	agent, err := e.Store.Execution().GetAgentInProject(ctx, turn.ProjectID, turn.AgentID)
	if err != nil {
		return err
	}
	if agent.State != executionstore.AgentStateActive {
		return errors.New("agent is inactive")
	}
	current, err := e.integrationRuntimeContract(ctx, turn.ProjectID, agent.CurrentConfigID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current.GitCredentials, credentials) {
		return fmt.Errorf("%w: Git credentials changed; submit a new call", ErrToolAuthorizationInvalidated)
	}
	err = integrationruntime.CheckCredentialAccess(ctx, e.Store.Integrations(), e.Store.Secrets(),
		integration, credentialVersion)
	if errors.Is(err, integrationruntime.ErrCredentialAccessChanged) {
		return fmt.Errorf("%w: %w; submit a new call", ErrToolAuthorizationInvalidated, err)
	}
	return err
}
