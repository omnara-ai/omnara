package integration

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

var ErrCredentialAccessChanged = fmt.Errorf("integration access changed: %w", storeerr.ErrUnauthorized)

type IntegrationSetupReader interface {
	GetIntegration(context.Context, uuid.UUID, uuid.UUID) (integrationstore.IntegrationRecord, error)
}

type IntegrationCredentialReader interface {
	GetProjectAvailableSecret(
		context.Context, uuid.UUID, uuid.UUID, uuid.UUID,
	) (secretstore.ProjectSecretAccessRecord, error)
}

func checkIntegrationSetup(
	ctx context.Context, integrations IntegrationSetupReader, expected integrationstore.IntegrationRecord,
) error {
	current, err := integrations.GetIntegration(ctx, expected.ProjectID, expected.ID)
	if err != nil {
		return err
	}
	if current.State != integrationstore.IntegrationStateActive || current.ID != expected.ID ||
		current.OrgID != expected.OrgID || current.ProjectID != expected.ProjectID ||
		current.IntegrationKind != expected.IntegrationKind || current.Provider != expected.Provider ||
		current.ProviderTenantID != expected.ProviderTenantID || current.ProviderAccountRef != expected.ProviderAccountRef ||
		current.CredentialSecretID != expected.CredentialSecretID || current.SetupRevision != expected.SetupRevision {
		return fmt.Errorf("%w: integration setup changed", ErrCredentialAccessChanged)
	}
	return nil
}

func CheckCredentialAccess(
	ctx context.Context, integrations IntegrationSetupReader, credentials IntegrationCredentialReader,
	expected integrationstore.IntegrationRecord, version uuid.UUID,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkIntegrationSetup(ctx, integrations, expected); err != nil {
		return err
	}
	kind, err := integrationstore.IntegrationCredentialKind(expected.Provider)
	if err != nil {
		return err
	}
	access, err := credentials.GetProjectAvailableSecret(
		ctx, expected.OrgID, expected.ProjectID, expected.CredentialSecretID,
	)
	if err != nil {
		return err
	}
	if version == uuid.Nil || access.Secret.Kind != kind || access.Secret.CurrentVersionID != version {
		return fmt.Errorf("%w: integration credentials changed", ErrCredentialAccessChanged)
	}
	return nil
}
