package orglifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

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

const (
	defaultModelProviderProvisioningClaimLease = 2 * time.Minute
	maximumDefaultModelProviderRetryDelay      = 24 * time.Hour
)

type DefaultModelProviderProvisioningClaim struct {
	OrgID         ID
	CreatorUserID ID
	ClaimToken    ID
	Attempt       int32
	Template      modelstore.DefaultModelProviderTemplate
}

type CompleteDefaultModelProviderProvisioningInput struct {
	Claim           DefaultModelProviderProvisioningClaim
	CredentialValue string
}

type RetryDefaultModelProviderProvisioningInput struct {
	Claim DefaultModelProviderProvisioningClaim
	Delay time.Duration
}

func (s *Service) ClaimDefaultModelProviderProvisioning(
	ctx context.Context,
) (DefaultModelProviderProvisioningClaim, bool, error) {
	row, err := s.q.ClaimDefaultModelProviderProvisioning(
		ctx,
		dbsqlc.ClaimDefaultModelProviderProvisioningParams{
			ClaimLeaseSeconds: int64(defaultModelProviderProvisioningClaimLease / time.Second),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultModelProviderProvisioningClaim{}, false, nil
	}
	if err != nil {
		return DefaultModelProviderProvisioningClaim{}, false, fmt.Errorf(
			"claim default model provider provisioning: %w",
			err,
		)
	}
	if row.ClaimToken == nil || *row.ClaimToken == uuid.Nil || row.AttemptCount <= 0 {
		return DefaultModelProviderProvisioningClaim{}, false, storeerr.ErrStateTransitionConflict
	}
	template, err := decodeDefaultModelProviderTemplate(row.ProviderTemplate)
	if err != nil {
		return DefaultModelProviderProvisioningClaim{}, false, err
	}
	return DefaultModelProviderProvisioningClaim{
		OrgID:         row.OrganizationID,
		CreatorUserID: row.CreatorUserID,
		ClaimToken:    *row.ClaimToken,
		Attempt:       row.AttemptCount,
		Template:      template,
	}, true, nil
}

func (s *Service) CompleteDefaultModelProviderProvisioning(
	ctx context.Context,
	input CompleteDefaultModelProviderProvisioningInput,
) error {
	if !validDefaultModelProviderProvisioningClaim(input.Claim) {
		return errors.New("valid default model provider provisioning claim is required")
	}
	credentialValue := strings.TrimSpace(input.CredentialValue)
	if credentialValue == "" {
		return errors.New("default model provider credential is required")
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin default model provider provisioning completion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	job, err := qtx.LockDefaultModelProviderProvisioning(
		ctx,
		dbsqlc.LockDefaultModelProviderProvisioningParams{
			OrganizationID: input.Claim.OrgID,
			ClaimToken:     input.Claim.ClaimToken,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrStateTransitionConflict
	}
	if err != nil {
		return fmt.Errorf("lock default model provider provisioning: %w", err)
	}
	if job.CreatorUserID != input.Claim.CreatorUserID {
		return storeerr.ErrStateTransitionConflict
	}
	prepared, err := decodeDefaultModelProviderTemplate(job.ProviderTemplate)
	if err != nil {
		return err
	}
	if _, err := qtx.LockOrg(ctx, dbsqlc.LockOrgParams{ID: input.Claim.OrgID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrNotFound
		}
		return fmt.Errorf("lock organization for default model provider provisioning: %w", err)
	}
	if _, err := qtx.GetModelProviderConfigByName(
		ctx,
		dbsqlc.GetModelProviderConfigByNameParams{
			OrgID: input.Claim.OrgID,
			Name:  prepared.Name,
		},
	); err == nil {
		return fmt.Errorf(
			"default model provider %q already exists: %w",
			prepared.Name,
			storeerr.ErrStateTransitionConflict,
		)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("load default model provider %q: %w", prepared.Name, err)
	}
	defaultProject, err := qtx.GetProjectByIdempotencyKey(
		ctx,
		dbsqlc.GetProjectByIdempotencyKeyParams{
			OrgID:          input.Claim.OrgID,
			IdempotencyKey: identitystore.DefaultProjectKey,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("default project is missing: %w", storeerr.ErrStateTransitionConflict)
	}
	if err != nil {
		return fmt.Errorf("load default project: %w", err)
	}
	credentialNameID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate default model provider credential name: %w", err)
	}
	prepared.CredentialSecretName += "-" + credentialNameID.String()
	if err := s.installDefaultModelProviderTx(
		ctx,
		tx,
		input.Claim.OrgID,
		defaultProject.ID,
		input.Claim.CreatorUserID,
		modelstore.ProvisionedDefaultModelProvider{
			Template:        prepared,
			CredentialValue: credentialValue,
		},
	); err != nil {
		return err
	}
	rows, err := qtx.CompleteDefaultModelProviderProvisioning(
		ctx,
		dbsqlc.CompleteDefaultModelProviderProvisioningParams{
			OrganizationID: input.Claim.OrgID,
			ClaimToken:     input.Claim.ClaimToken,
		},
	)
	if err != nil {
		return fmt.Errorf("complete default model provider provisioning job: %w", err)
	}
	if rows != 1 {
		return storeerr.ErrStateTransitionConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit default model provider provisioning completion: %w", err)
	}
	return nil
}

func (s *Service) RetryDefaultModelProviderProvisioning(
	ctx context.Context,
	input RetryDefaultModelProviderProvisioningInput,
) error {
	if !validDefaultModelProviderProvisioningClaim(input.Claim) ||
		input.Delay < time.Second || input.Delay > maximumDefaultModelProviderRetryDelay {
		return errors.New("valid default model provider provisioning claim and retry delay are required")
	}
	rows, err := s.q.RetryDefaultModelProviderProvisioning(
		ctx,
		dbsqlc.RetryDefaultModelProviderProvisioningParams{
			RetryDelaySeconds: int64(input.Delay / time.Second),
			OrganizationID:    input.Claim.OrgID,
			ClaimToken:        input.Claim.ClaimToken,
		},
	)
	if err != nil {
		return fmt.Errorf("retry default model provider provisioning: %w", err)
	}
	if rows != 1 {
		return storeerr.ErrStateTransitionConflict
	}
	return nil
}

func validDefaultModelProviderProvisioningClaim(claim DefaultModelProviderProvisioningClaim) bool {
	return !isNilID(claim.OrgID) && !isNilID(claim.CreatorUserID) &&
		!isNilID(claim.ClaimToken) && claim.Attempt > 0
}

func decodeDefaultModelProviderTemplate(raw json.RawMessage) (modelstore.DefaultModelProviderTemplate, error) {
	var template modelstore.DefaultModelProviderTemplate
	if err := json.Unmarshal(raw, &template); err != nil {
		return modelstore.DefaultModelProviderTemplate{}, fmt.Errorf(
			"decode default model provider template: %w",
			err,
		)
	}
	prepared, err := modelstore.PrepareDefaultModelProviderTemplate(template)
	if err != nil {
		return modelstore.DefaultModelProviderTemplate{}, fmt.Errorf(
			"default model provider %q: %w",
			template.Name,
			err,
		)
	}
	return prepared, nil
}

func (s *Service) installDefaultModelProviderTx(
	ctx context.Context,
	tx pgx.Tx,
	orgID, defaultProjectID, createdByUserID ID,
	provider modelstore.ProvisionedDefaultModelProvider,
) error {
	credential, _, err := s.secrets.CreateTx(ctx, tx, secretstore.CreateSecretInput{
		OrgID:          orgID,
		ManagementKind: management.Cluster,
		OwnerKind:      secretstore.SecretOwnerOrg,
		Name:           provider.Template.CredentialSecretName,
		Material:       secrets.GenericMaterial{Value: provider.CredentialValue},
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
		provider.Template,
	); err != nil {
		return fmt.Errorf("provision default model provider: %w", err)
	}
	return nil
}
