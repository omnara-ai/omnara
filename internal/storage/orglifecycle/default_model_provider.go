package orglifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type CompleteDefaultModelProviderProvisioningInput struct {
	OrgID           ID
	CreatedByUserID ID
	Provider        modelstore.ProvisionedDefaultModelProvider
}

// CompleteDefaultModelProviderProvisioning installs a hosted provider after organization creation.
func (s *Service) CompleteDefaultModelProviderProvisioning(
	ctx context.Context,
	input CompleteDefaultModelProviderProvisioningInput,
) (bool, error) {
	if isNilID(input.OrgID) || isNilID(input.CreatedByUserID) {
		return false, errors.New("org and creator are required")
	}
	prepared, err := modelstore.PrepareDefaultModelProviderTemplate(input.Provider.Template)
	if err != nil {
		return false, fmt.Errorf("default model provider %q: %w", input.Provider.Template.Name, err)
	}
	input.Provider.Template = prepared
	input.Provider.CredentialValue = strings.TrimSpace(input.Provider.CredentialValue)
	if input.Provider.CredentialValue == "" {
		return false, fmt.Errorf("default model provider %q credential is required", prepared.Name)
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin default model provider completion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	if _, err := qtx.LockOrg(ctx, dbsqlc.LockOrgParams{ID: input.OrgID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, storeerr.ErrNotFound
		}
		return false, fmt.Errorf("lock organization for default model provider completion: %w", err)
	}
	existing, err := qtx.GetModelProviderConfigByName(
		ctx,
		dbsqlc.GetModelProviderConfigByNameParams{OrgID: input.OrgID, Name: prepared.Name},
	)
	if err == nil {
		if management.Kind(existing.ManagementKind) != management.Cluster {
			return false, fmt.Errorf(
				"default model provider %q is tenant-managed: %w",
				prepared.Name,
				storeerr.ErrStateTransitionConflict,
			)
		}
		return false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("load default model provider %q: %w", prepared.Name, err)
	}
	credentialNameID, err := uuid.NewV7()
	if err != nil {
		return false, fmt.Errorf("generate default model provider credential name: %w", err)
	}
	input.Provider.Template.CredentialSecretName = prepared.CredentialSecretName + "-" + credentialNameID.String()
	defaultProject, err := qtx.GetProjectByIdempotencyKey(
		ctx,
		dbsqlc.GetProjectByIdempotencyKeyParams{
			OrgID:          input.OrgID,
			IdempotencyKey: identitystore.DefaultProjectKey,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("default project is missing: %w", storeerr.ErrStateTransitionConflict)
	}
	if err != nil {
		return false, fmt.Errorf("load default project: %w", err)
	}
	if err := s.createDefaultModelProviderForOrgTx(
		ctx,
		tx,
		input.OrgID,
		defaultProject.ID,
		input.CreatedByUserID,
		&input.Provider,
	); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit default model provider completion: %w", err)
	}
	return true, nil
}

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
		Actor:          identitystore.NewUserPrincipal(createdByUserID),
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
