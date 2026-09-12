package storagefixture

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

type ModelProviderInput struct {
	OrgID, UserID uuid.UUID
	Name          string
}

// EnsureModelProvider reuses a provider or creates one with a usable test credential.
// Requires an existing org and a user authorized to create its secrets.
func EnsureModelProvider(
	t testing.TB,
	ctx context.Context,
	models *modelstore.Store,
	credentials *secretstore.Store,
	input ModelProviderInput,
) modelstore.ModelProviderConfigRecord {
	t.Helper()
	provider, err := models.GetModelProviderConfigByName(ctx, input.OrgID, input.Name)
	if !storeerr.IsNotFound(err) {
		require.NoError(t, err, "load test provider %q", input.Name)
		return provider
	}
	credentialName := input.Name + "-credential"
	credential, err := credentials.GetSecretByOwnerName(
		ctx, input.OrgID, secretstore.SecretOwnerOrg, uuid.Nil, uuid.Nil, credentialName,
	)
	if storeerr.IsNotFound(err) {
		credential, _, err = credentials.CreateSecret(ctx, secretstore.CreateSecretInput{
			OrgID: input.OrgID, OwnerKind: secretstore.SecretOwnerOrg, Name: credentialName,
			Material: secrets.GenericMaterial{Value: "test-key"},
			Actor:    identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: input.UserID},
		})
	}
	require.NoError(t, err, "ensure test provider credential %q", credentialName)
	provider, err = models.CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID: input.OrgID, Name: input.Name,
		APIFormat: modelprotocol.APIFormatOpenAIResponses, APIVariant: modelprotocol.APIVariantDefault,
		BaseURL: "https://api.openai.com/v1", CredentialSecretID: credential.ID,
	})
	require.NoError(t, err, "create test provider %q", input.Name)
	return provider
}
