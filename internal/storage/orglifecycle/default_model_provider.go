package orglifecycle

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/resourcename"
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

// ErrDefaultModelProviderProvisioningSuperseded means organization state made the job terminal.
var ErrDefaultModelProviderProvisioningSuperseded = errors.New(
	"default model provider provisioning was superseded by organization state",
)

type DefaultModelProviderProvisioningClaim struct {
	OrgID         ID
	CreatorUserID ID
	ClaimToken    ID
	Attempt       int32
}

type CompleteDefaultModelProviderProvisioningInput struct {
	Claim           DefaultModelProviderProvisioningClaim
	Template        modelstore.DefaultModelProviderTemplate
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
	return defaultModelProviderProvisioningClaim(
		row.OrganizationID,
		row.CreatorUserID,
		row.ClaimToken,
		row.AttemptCount,
	)
}

func (s *Service) ClaimDefaultModelProviderProvisioningForOrganization(
	ctx context.Context,
	organizationID ID,
) (DefaultModelProviderProvisioningClaim, bool, error) {
	if isNilID(organizationID) {
		return DefaultModelProviderProvisioningClaim{}, false, errors.New("organization is required")
	}
	row, err := s.q.ClaimDefaultModelProviderProvisioningForOrganization(
		ctx,
		dbsqlc.ClaimDefaultModelProviderProvisioningForOrganizationParams{
			OrganizationID:    organizationID,
			ClaimLeaseSeconds: int64(defaultModelProviderProvisioningClaimLease / time.Second),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultModelProviderProvisioningClaim{}, false, nil
	}
	if err != nil {
		return DefaultModelProviderProvisioningClaim{}, false, fmt.Errorf(
			"claim default model provider provisioning for organization: %w",
			err,
		)
	}
	return defaultModelProviderProvisioningClaim(
		row.OrganizationID,
		row.CreatorUserID,
		row.ClaimToken,
		row.AttemptCount,
	)
}

func defaultModelProviderProvisioningClaim(
	organizationID, creatorUserID ID,
	claimToken *ID,
	attempt int32,
) (DefaultModelProviderProvisioningClaim, bool, error) {
	if claimToken == nil || *claimToken == uuid.Nil || attempt <= 0 {
		return DefaultModelProviderProvisioningClaim{}, false, storeerr.ErrStateTransitionConflict
	}
	return DefaultModelProviderProvisioningClaim{
		OrgID:         organizationID,
		CreatorUserID: creatorUserID,
		ClaimToken:    *claimToken,
		Attempt:       attempt,
	}, true, nil
}

func (s *Service) CompleteDefaultModelProviderProvisioning(
	ctx context.Context,
	input CompleteDefaultModelProviderProvisioningInput,
) error {
	if !validDefaultModelProviderProvisioningClaim(input.Claim) {
		return errors.New("valid default model provider provisioning claim is required")
	}
	prepared, err := modelstore.PrepareDefaultModelProviderTemplate(input.Template)
	if err != nil {
		return fmt.Errorf("default model provider %q: %w", input.Template.Name, err)
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
	if _, err := qtx.LockOrg(ctx, dbsqlc.LockOrgParams{ID: input.Claim.OrgID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrNotFound
		}
		return fmt.Errorf("lock organization for default model provider provisioning: %w", err)
	}
	creatorUserID, err := qtx.LockDefaultModelProviderProvisioning(
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
	if creatorUserID != input.Claim.CreatorUserID {
		return storeerr.ErrStateTransitionConflict
	}
	if _, err := qtx.GetModelProviderConfigByName(
		ctx,
		dbsqlc.GetModelProviderConfigByNameParams{
			OrgID: input.Claim.OrgID,
			Name:  prepared.Name,
		},
	); err == nil {
		return finishSupersededDefaultModelProviderProvisioning(
			ctx,
			tx,
			qtx,
			input.Claim,
			fmt.Sprintf("model provider %q already exists", prepared.Name),
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
		return finishSupersededDefaultModelProviderProvisioning(
			ctx,
			tx,
			qtx,
			input.Claim,
			"default project is missing",
		)
	}
	if err != nil {
		return fmt.Errorf("load default project: %w", err)
	}
	credentialNameID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate default model provider credential name: %w", err)
	}
	prepared.CredentialSecretName = defaultModelProviderCredentialName(
		prepared.CredentialSecretName,
		credentialNameID,
	)
	if err := s.installDefaultModelProviderTx(
		ctx,
		tx,
		input.Claim.OrgID,
		defaultProject.ID,
		input.Claim.CreatorUserID,
		prepared,
		credentialValue,
	); err != nil {
		return err
	}
	if err := completeDefaultModelProviderProvisioningJob(ctx, qtx, input.Claim); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit default model provider provisioning completion: %w", err)
	}
	return nil
}

func defaultModelProviderCredentialName(base string, id uuid.UUID) string {
	suffix := "-" + base64.RawURLEncoding.EncodeToString(id[:])
	prefixRunes := []rune(base)
	maximumPrefix := resourcename.MaxCodePoints - len([]rune(suffix))
	prefix := strings.TrimRight(string(prefixRunes[:min(len(prefixRunes), maximumPrefix)]), " ")
	return prefix + suffix
}

func finishSupersededDefaultModelProviderProvisioning(
	ctx context.Context,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	claim DefaultModelProviderProvisioningClaim,
	reason string,
) error {
	if err := completeDefaultModelProviderProvisioningJob(ctx, qtx, claim); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit superseded default model provider provisioning: %w", err)
	}
	return fmt.Errorf("%w: %s", ErrDefaultModelProviderProvisioningSuperseded, reason)
}

func completeDefaultModelProviderProvisioningJob(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	claim DefaultModelProviderProvisioningClaim,
) error {
	rows, err := qtx.CompleteDefaultModelProviderProvisioning(
		ctx,
		dbsqlc.CompleteDefaultModelProviderProvisioningParams{
			OrganizationID: claim.OrgID,
			ClaimToken:     claim.ClaimToken,
		},
	)
	if err != nil {
		return fmt.Errorf("complete default model provider provisioning job: %w", err)
	}
	if rows != 1 {
		return storeerr.ErrStateTransitionConflict
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

func (s *Service) installDefaultModelProviderTx(
	ctx context.Context,
	tx pgx.Tx,
	orgID, defaultProjectID, createdByUserID ID,
	template modelstore.DefaultModelProviderTemplate,
	credentialValue string,
) error {
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
