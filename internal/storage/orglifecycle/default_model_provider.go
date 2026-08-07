package orglifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
)

func (s *Service) createDefaultModelProviderForOrgTx(
	ctx context.Context,
	tx pgx.Tx,
	orgID, defaultProjectID, createdByUserID ID,
	provisioned *modelstore.ProvisionedDefaultModelProvider,
) error {
	if provisioned == nil {
		return nil
	}
	if isNilID(orgID) || isNilID(defaultProjectID) || isNilID(createdByUserID) {
		return errors.New("org, default project, and creator are required")
	}
	template, err := modelstore.PrepareDefaultModelProviderTemplate(provisioned.Template)
	if err != nil {
		return fmt.Errorf("default model provider %q: %w", provisioned.Template.Name, err)
	}
	credentialValue := strings.TrimSpace(provisioned.CredentialValue)
	if credentialValue == "" {
		return fmt.Errorf("default model provider %q credential is required", template.Name)
	}
	credential, _, err := s.secrets.CreateTx(ctx, tx, secretstore.CreateSecretInput{
		OrgID:          orgID,
		ManagementKind: management.Cluster,
		OwnerKind:      secretstore.SecretOwnerOrg,
		Name:           template.CredentialSecretName,
		Material:       secrets.GenericMaterial{Value: credentialValue},
		Actor: identitystore.PrincipalRecord{
			Type: identitystore.PrincipalTypeUser,
			ID:   createdByUserID,
		},
	})
	if err != nil {
		return fmt.Errorf("create default model provider credential: %w", err)
	}
	if err := s.models.ProvisionDefaultTx(
		ctx,
		tx,
		orgID,
		defaultProjectID,
		createdByUserID,
		credential.ID,
		template,
	); err != nil {
		return fmt.Errorf("provision default model provider: %w", err)
	}
	return nil
}
