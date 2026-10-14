package agentconfig

import (
	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type GitCredentialsSource struct {
	Integration string `json:"integration"`
}

type GitCredentialsCompiled struct {
	Integration   string    `json:"integration"`
	IntegrationID uuid.UUID `json:"integration_id"`
}

func compileGitCredentials(source GitCredentialsSource, opts CompileOptions) (GitCredentialsCompiled, error) {
	pointer := jsonPointer("git_credentials", "integration")
	if err := toolcatalog.ValidateIntegrationName(source.Integration); err != nil {
		return GitCredentialsCompiled{}, issueOr(pointer, err)
	}
	integration, err := opts.ResolveIntegrationName(source.Integration)
	if err != nil {
		return GitCredentialsCompiled{}, issueOr(pointer, err)
	}
	definition, _ := integrationdefinition.Lookup(integration.IntegrationKind)
	if definition.Provider != integrationdefinition.ProviderGitHub {
		return GitCredentialsCompiled{}, issuef(pointer, "git credentials require a GitHub integration")
	}
	return GitCredentialsCompiled{
		Integration: source.Integration, IntegrationID: integration.IntegrationID,
	}, nil
}
