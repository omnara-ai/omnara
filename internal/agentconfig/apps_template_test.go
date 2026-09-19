package agentconfig

import (
	"errors"
	"testing"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/stretchr/testify/require"
)

func TestAppTemplateValidatesMCPSecretReferences(t *testing.T) {
	for _, test := range []struct {
		authType string
		kind     secrets.Kind
	}{
		{MCPAuthTypeBearer, secrets.KindGeneric},
		{MCPAuthTypeOAuth, secrets.KindOAuthTokenSet},
		{MCPAuthTypeSigV4, secrets.KindAWSCredentials},
	} {
		t.Run(test.authType, func(t *testing.T) {
			secretID := testMachineSourcePublicID(t, publicid.KindSecret, "app-template-secret")
			auth := &AgentConfigMCPAuthSource{Type: test.authType, SecretID: secretID}
			if test.authType == MCPAuthTypeSigV4 {
				auth.Service, auth.Region = "execute-api", "us-east-1"
			}
			source := AgentConfigAppResourceSource{
				Definition: appdefinition.Slack,
				MCP: map[string]AgentConfigMCPSource{
					"crm": {URL: "https://example.com/mcp", Auth: auth},
				},
			}
			// Existing callers may perform structural validation without resolving
			// project references. Saving setup supplies the authorization callback.
			require.NoError(t, ValidateAppResourceTemplate(source))
			calls := 0
			var callbackError error
			opts := CompileOptions{ValidateSecretID: func(id string, kind secrets.Kind) error {
				calls++
				require.Equal(t, secretID, id)
				require.Equal(t, test.kind, kind)
				return callbackError
			}}
			require.NoError(t, ValidateAppResourceTemplate(source, opts))
			require.Equal(t, 1, calls)
			callbackError = errors.New("secret is unavailable to the project")
			err := ValidateAppResourceTemplate(source, opts)
			require.ErrorIs(t, err, callbackError)
			require.Equal(t, 2, calls)
			var issue issueError
			require.ErrorAs(t, err, &issue)
			require.Equal(t, "/mcp/crm/auth/secret_id", issue.issue.Path)
		})
	}
}

func TestAppTemplateRejectsAmbiguousCompileOptions(t *testing.T) {
	err := ValidateAppResourceTemplate(
		AgentConfigAppResourceSource{Definition: appdefinition.Slack},
		CompileOptions{},
		CompileOptions{},
	)
	require.ErrorContains(t, err, "at most one compile options")
}
