package identitystore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/authz"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/skillops"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) CreateUser(ctx context.Context, input CreateUserInput) (UserRecord, error) {
	row, err := s.q.CreateUser(ctx, dbsqlc.CreateUserParams{DisplayName: input.DisplayName})
	if err != nil {
		return UserRecord{}, fmt.Errorf("create user: %w", err)
	}
	return userRecordFromSQLC(row), nil
}

func (s *Store) CreateUserEmail(
	ctx context.Context,
	input CreateUserEmailInput,
) (UserEmailRecord, error) {
	if isNilID(input.UserID) {
		return UserEmailRecord{}, errors.New("user id is required")
	}
	if input.Email == "" {
		return UserEmailRecord{}, errors.New("email is required")
	}
	if input.NormalizedEmail == "" {
		return UserEmailRecord{}, errors.New("normalized email is required")
	}
	row, err := s.q.CreateUserEmail(ctx, dbsqlc.CreateUserEmailParams{
		UserID:          input.UserID,
		Email:           input.Email,
		NormalizedEmail: input.NormalizedEmail,
		Verified:        input.Verified,
		IsPrimary:       input.IsPrimary,
	})
	if err != nil {
		if storeutil.IsUniqueViolation(err) {
			return UserEmailRecord{}, storeerr.ErrIdempotencyConflict
		}
		return UserEmailRecord{}, fmt.Errorf("create user email: %w", err)
	}
	return userEmailRecordFromSQLC(row), nil
}

func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func isValidProjectRole(role string) bool {
	switch role {
	case authz.ProjectRoleAdmin, authz.ProjectRoleDeveloper, authz.ProjectRoleOperator, authz.ProjectRoleViewer:
		return true
	default:
		return false
	}
}

func guardLastOwnerChange(ctx context.Context, qtx *dbsqlc.Queries, orgID ID, existingRole, newRole string) error {
	if existingRole != authz.OrgRoleOwner || newRole == authz.OrgRoleOwner {
		return nil
	}
	ownerCount, err := qtx.CountOrgOwners(ctx, dbsqlc.CountOrgOwnersParams{OrgID: orgID})
	if err != nil {
		return fmt.Errorf("count org owners: %w", err)
	}
	if ownerCount <= 1 {
		return storeerr.ErrUnauthorized
	}
	return nil
}

func (s *Store) CreateUserAuthIdentity(
	ctx context.Context,
	input CreateUserAuthIdentityInput,
) (UserAuthIdentityRecord, error) {
	if isNilID(input.UserID) {
		return UserAuthIdentityRecord{}, errors.New("user id is required")
	}
	if isNilID(input.AuthConnectorID) {
		return UserAuthIdentityRecord{}, errors.New("auth connector id is required")
	}
	if input.Issuer == "" {
		return UserAuthIdentityRecord{}, errors.New("issuer is required")
	}
	if input.Subject == "" {
		return UserAuthIdentityRecord{}, errors.New("subject is required")
	}
	row, err := s.q.CreateUserAuthIdentity(ctx, dbsqlc.CreateUserAuthIdentityParams{
		UserID:          input.UserID,
		AuthConnectorID: input.AuthConnectorID,
		Issuer:          input.Issuer,
		Subject:         input.Subject,
		EmailAtLink:     input.EmailAtLink,
		EmailVerified:   input.EmailVerified,
	})
	if err != nil {
		if storeutil.IsUniqueViolation(err) {
			return UserAuthIdentityRecord{}, storeerr.ErrIdempotencyConflict
		}
		return UserAuthIdentityRecord{}, fmt.Errorf("create user auth identity: %w", err)
	}
	return userAuthIdentityRecordFromSQLC(row), nil
}

func (s *Store) ResolveAuthIdentityUserAndCreateSession(
	ctx context.Context,
	input ResolveAuthIdentitySessionInput,
) (UserRecord, error) {
	return s.resolveAuthIdentityUser(
		ctx,
		input.ResolveAuthIdentityInput,
		input.SessionToken,
		input.SessionCSRFToken,
		input.SessionTTL,
	)
}

func (s *Store) resolveAuthIdentityUser(
	ctx context.Context,
	input ResolveAuthIdentityInput,
	sessionToken, csrfToken string,
	sessionTTL time.Duration,
) (UserRecord, error) {
	if isNilID(input.AuthConnectorID) {
		return UserRecord{}, errors.New("auth connector id is required")
	}
	if input.Issuer == "" {
		return UserRecord{}, errors.New("issuer is required")
	}
	if input.Subject == "" {
		return UserRecord{}, errors.New("subject is required")
	}
	if identity, err := s.q.GetUserAuthIdentity(
		ctx,
		dbsqlc.GetUserAuthIdentityParams{AuthConnectorID: input.AuthConnectorID, Subject: input.Subject},
	); err == nil {
		if identity.Issuer != input.Issuer {
			return UserRecord{}, storeerr.ErrUnauthorized
		}
		return s.createAuthIdentitySession(ctx, identity.UserID, sessionToken, csrfToken, sessionTTL)
	} else if !errors.Is(
		err,
		pgx.ErrNoRows,
	) {
		return UserRecord{}, fmt.Errorf("load auth identity: %w", err)
	}
	normalizedEmail := NormalizeEmail(input.Email)
	if !input.EmailVerified || normalizedEmail == "" {
		return UserRecord{}, storeerr.ErrUnauthorized
	}
	email := strings.TrimSpace(input.Email)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return UserRecord{}, fmt.Errorf("begin resolve auth identity user: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	emailTrustPolicy, err := qtx.GetAuthConnectorEmailTrustPolicy(
		ctx,
		dbsqlc.GetAuthConnectorEmailTrustPolicyParams{ID: input.AuthConnectorID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserRecord{}, storeerr.ErrUnauthorized
	}
	if err != nil {
		return UserRecord{}, fmt.Errorf("load auth connector email trust policy: %w", err)
	}
	if emailTrustPolicy != AuthConnectorEmailTrustPolicyVerifiedEmail {
		return UserRecord{}, storeerr.ErrUnauthorized
	}
	if err := qtx.LockNormalizedEmailKey(
		ctx,
		dbsqlc.LockNormalizedEmailKeyParams{NormalizedEmail: normalizedEmail},
	); err != nil {
		return UserRecord{}, fmt.Errorf("lock auth identity email key: %w", err)
	}
	if identity, err := qtx.GetUserAuthIdentity(
		ctx,
		dbsqlc.GetUserAuthIdentityParams{AuthConnectorID: input.AuthConnectorID, Subject: input.Subject},
	); err == nil {
		if identity.Issuer != input.Issuer {
			return UserRecord{}, storeerr.ErrUnauthorized
		}
		return s.createAuthIdentitySessionTx(
			ctx,
			tx,
			qtx,
			identity.UserID,
			sessionToken,
			csrfToken,
			sessionTTL,
		)
	} else if !errors.Is(
		err,
		pgx.ErrNoRows,
	) {
		return UserRecord{}, fmt.Errorf("reload auth identity: %w", err)
	}
	if _, err := qtx.LockUserEmailsByNormalizedEmail(
		ctx,
		dbsqlc.LockUserEmailsByNormalizedEmailParams{NormalizedEmail: normalizedEmail},
	); err != nil {
		return UserRecord{}, fmt.Errorf("lock auth identity email rows: %w", err)
	}
	existingEmail, err := qtx.GetVerifiedUserEmailByNormalizedEmail(
		ctx,
		dbsqlc.GetVerifiedUserEmailByNormalizedEmailParams{NormalizedEmail: normalizedEmail},
	)
	if err == nil {
		user, err := qtx.GetUser(ctx, dbsqlc.GetUserParams{ID: existingEmail.UserID})
		if errors.Is(err, pgx.ErrNoRows) {
			return UserRecord{}, storeerr.ErrUnauthorized
		}
		if err != nil {
			return UserRecord{}, fmt.Errorf("load auth identity email owner: %w", err)
		}
		if _, err := qtx.CreateUserAuthIdentity(
			ctx,
			dbsqlc.CreateUserAuthIdentityParams{
				UserID:          existingEmail.UserID,
				AuthConnectorID: input.AuthConnectorID,
				Issuer:          input.Issuer,
				Subject:         input.Subject,
				EmailAtLink:     email,
				EmailVerified:   input.EmailVerified,
			},
		); err != nil {
			if storeutil.IsUniqueViolation(err) {
				return UserRecord{}, storeerr.ErrUnauthorized
			}
			return UserRecord{}, fmt.Errorf("create linked user auth identity: %w", err)
		}
		return s.createAuthIdentitySessionForLoadedUserTx(
			ctx,
			tx,
			qtx,
			user,
			sessionToken,
			csrfToken,
			sessionTTL,
		)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return UserRecord{}, fmt.Errorf("load verified email: %w", err)
	}
	user, err := qtx.CreateUser(ctx, dbsqlc.CreateUserParams{DisplayName: input.DisplayName})
	if err != nil {
		return UserRecord{}, fmt.Errorf("create auth identity user: %w", err)
	}
	if _, err := qtx.CreateUserEmail(
		ctx,
		dbsqlc.CreateUserEmailParams{
			UserID:          user.ID,
			Email:           email,
			NormalizedEmail: normalizedEmail,
			Verified:        true,
			IsPrimary:       true,
		},
	); err != nil {
		if storeutil.IsUniqueViolation(err) {
			return UserRecord{}, storeerr.ErrUnauthorized
		}
		return UserRecord{}, fmt.Errorf("create auth identity user email: %w", err)
	}
	if _, err := qtx.CreateUserAuthIdentity(
		ctx,
		dbsqlc.CreateUserAuthIdentityParams{
			UserID:          user.ID,
			AuthConnectorID: input.AuthConnectorID,
			Issuer:          input.Issuer,
			Subject:         input.Subject,
			EmailAtLink:     email,
			EmailVerified:   input.EmailVerified,
		},
	); err != nil {
		if storeutil.IsUniqueViolation(err) {
			return UserRecord{}, storeerr.ErrUnauthorized
		}
		return UserRecord{}, fmt.Errorf("create user auth identity: %w", err)
	}
	if err := createBrowserSessionTx(ctx, qtx, user.ID, sessionToken, csrfToken, sessionTTL); err != nil {
		return UserRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return UserRecord{}, fmt.Errorf("commit resolve auth identity user: %w", err)
	}
	return userRecordFromSQLC(user), nil
}

func (s *Store) createAuthIdentitySession(
	ctx context.Context,
	userID ID,
	sessionToken, csrfToken string,
	ttl time.Duration,
) (UserRecord, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return UserRecord{}, fmt.Errorf("begin auth identity session: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	return s.createAuthIdentitySessionTx(ctx, tx, qtx, userID, sessionToken, csrfToken, ttl)
}

func (s *Store) createAuthIdentitySessionTx(
	ctx context.Context,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	userID ID,
	sessionToken, csrfToken string,
	ttl time.Duration,
) (UserRecord, error) {
	user, err := qtx.GetUser(ctx, dbsqlc.GetUserParams{ID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return UserRecord{}, storeerr.ErrUnauthorized
	}
	if err != nil {
		return UserRecord{}, fmt.Errorf("load auth identity session user: %w", err)
	}
	return s.createAuthIdentitySessionForLoadedUserTx(ctx, tx, qtx, user, sessionToken, csrfToken, ttl)
}

func (s *Store) createAuthIdentitySessionForLoadedUserTx(
	ctx context.Context,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	user dbsqlc.User,
	sessionToken, csrfToken string,
	ttl time.Duration,
) (UserRecord, error) {
	if err := createBrowserSessionTx(ctx, qtx, user.ID, sessionToken, csrfToken, ttl); err != nil {
		return UserRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return UserRecord{}, fmt.Errorf("commit auth identity session: %w", err)
	}
	return userRecordFromSQLC(user), nil
}

func (s *Store) AddProjectMembership(
	ctx context.Context,
	input AddProjectMembershipInput,
) (ProjectMembershipRecord, error) {
	if isNilID(input.OrgID) {
		return ProjectMembershipRecord{}, errors.New("org id is required")
	}
	if isNilID(input.ProjectID) {
		return ProjectMembershipRecord{}, errors.New("project id is required")
	}
	if isNilID(input.UserID) {
		return ProjectMembershipRecord{}, errors.New("user id is required")
	}
	if !isValidProjectRole(input.Role) {
		return ProjectMembershipRecord{}, storeerr.InvalidRequest(
			errors.New("role must be admin, developer, operator, or viewer"),
		)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ProjectMembershipRecord{}, fmt.Errorf("begin add project membership: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	membership, err := qtx.GetOrgMembershipForUser(
		ctx,
		dbsqlc.GetOrgMembershipForUserParams{OrgID: input.OrgID, UserID: input.UserID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectMembershipRecord{}, storeerr.ErrUnauthorized
	} else if err != nil {
		return ProjectMembershipRecord{}, fmt.Errorf(
			"load org membership for project membership: %w",
			err,
		)
	}
	row, err := qtx.AddProjectMembership(
		ctx,
		dbsqlc.AddProjectMembershipParams{
			OrgID:           input.OrgID,
			ProjectID:       input.ProjectID,
			OrgMembershipID: membership.ID,
			Role:            input.Role,
		},
	)
	if err != nil {
		return ProjectMembershipRecord{}, fmt.Errorf("add project membership: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ProjectMembershipRecord{}, fmt.Errorf("commit add project membership: %w", err)
	}
	return projectMembershipRecordFromSQLC(row), nil
}

func (s *Store) AddOrgMembership(
	ctx context.Context,
	input AddOrgMembershipInput,
) (OrgMembershipRecord, error) {
	if isNilID(input.OrgID) {
		return OrgMembershipRecord{}, errors.New("org id is required")
	}
	if isNilID(input.UserID) {
		return OrgMembershipRecord{}, errors.New("user id is required")
	}
	if input.Role == "" {
		return OrgMembershipRecord{}, errors.New("role is required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OrgMembershipRecord{}, fmt.Errorf("begin add org membership: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	if _, err := qtx.LockUserForUpdate(ctx, dbsqlc.LockUserForUpdateParams{ID: input.UserID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return OrgMembershipRecord{}, storeerr.ErrNotFound
		}
		return OrgMembershipRecord{}, fmt.Errorf("lock organization member: %w", err)
	}
	if _, err := qtx.LockOrg(ctx, dbsqlc.LockOrgParams{ID: input.OrgID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return OrgMembershipRecord{}, storeerr.ErrNotFound
		}
		return OrgMembershipRecord{}, fmt.Errorf("lock org: %w", err)
	}
	existing, err := qtx.LockUserOrgMembership(
		ctx,
		dbsqlc.LockUserOrgMembershipParams{OrgID: input.OrgID, UserID: input.UserID},
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return OrgMembershipRecord{}, fmt.Errorf("lock org membership: %w", err)
	}
	membershipExists := err == nil
	if !membershipExists {
		membershipCount, countErr := qtx.CountOrgMembershipsForUser(
			ctx,
			dbsqlc.CountOrgMembershipsForUserParams{UserID: input.UserID},
		)
		if countErr != nil {
			return OrgMembershipRecord{}, fmt.Errorf("count user org memberships: %w", countErr)
		}
		if membershipCount >= MaxOrgMembershipsPerUser {
			return OrgMembershipRecord{}, storeerr.ErrUnauthorized
		}
	}
	if input.Role == "owner" && (!membershipExists || existing.Role != "owner") {
		ownedCount, countErr := qtx.CountOwnedOrgMembershipsForUser(
			ctx,
			dbsqlc.CountOwnedOrgMembershipsForUserParams{UserID: input.UserID},
		)
		if countErr != nil {
			return OrgMembershipRecord{}, fmt.Errorf("count owned orgs: %w", countErr)
		}
		if ownedCount >= MaxOwnedOrgsPerUser {
			return OrgMembershipRecord{}, storeerr.ErrUnauthorized
		}
	}
	if err == nil {
		if err := guardLastOwnerChange(ctx, qtx, input.OrgID, existing.Role, input.Role); err != nil {
			return OrgMembershipRecord{}, err
		}
	}
	row, err := qtx.AddUserOrgMembership(
		ctx,
		dbsqlc.AddUserOrgMembershipParams{
			OrgID:  input.OrgID,
			UserID: input.UserID,
			Role:   input.Role,
		},
	)
	if err != nil {
		return OrgMembershipRecord{}, fmt.Errorf("add org membership: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return OrgMembershipRecord{}, fmt.Errorf("commit add org membership: %w", err)
	}
	return userOrgMembershipRecord(row.ID, row.OrgID, input.UserID, row.Role, row.CreatedAt), nil
}

func (s *Store) UpdateOrgMemberRole(
	ctx context.Context,
	input UpdateOrgMemberRoleInput,
) (OrgMembershipRecord, error) {
	if isNilID(input.OrgID) {
		return OrgMembershipRecord{}, errors.New("org id is required")
	}
	if isNilID(input.UserID) {
		return OrgMembershipRecord{}, errors.New("user id is required")
	}
	if input.Role != authz.OrgRoleAdmin && input.Role != authz.OrgRoleMember {
		return OrgMembershipRecord{}, storeerr.InvalidRequest(errors.New("role must be admin or member"))
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OrgMembershipRecord{}, fmt.Errorf("begin update org member role: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	if _, err := qtx.LockOrg(ctx, dbsqlc.LockOrgParams{ID: input.OrgID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return OrgMembershipRecord{}, storeerr.ErrNotFound
		}
		return OrgMembershipRecord{}, fmt.Errorf("lock org: %w", err)
	}
	existing, err := qtx.LockUserOrgMembership(
		ctx,
		dbsqlc.LockUserOrgMembershipParams{OrgID: input.OrgID, UserID: input.UserID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return OrgMembershipRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return OrgMembershipRecord{}, fmt.Errorf("lock org membership: %w", err)
	}
	if err := guardLastOwnerChange(ctx, qtx, input.OrgID, existing.Role, input.Role); err != nil {
		return OrgMembershipRecord{}, err
	}
	row, err := qtx.UpdateOrgMembershipRoleForUser(
		ctx,
		dbsqlc.UpdateOrgMembershipRoleForUserParams{
			OrgID:  input.OrgID,
			UserID: input.UserID,
			Role:   input.Role,
		},
	)
	if err != nil {
		return OrgMembershipRecord{}, fmt.Errorf("update org membership role: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return OrgMembershipRecord{}, fmt.Errorf("commit update org member role: %w", err)
	}
	return userOrgMembershipRecord(row.ID, row.OrgID, input.UserID, row.Role, row.CreatedAt), nil
}

func (s *Store) RemoveOrgMember(ctx context.Context, input RemoveOrgMemberInput) error {
	if isNilID(input.OrgID) {
		return errors.New("org id is required")
	}
	if isNilID(input.UserID) {
		return errors.New("user id is required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin remove org member: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	if _, err := qtx.LockOrg(ctx, dbsqlc.LockOrgParams{ID: input.OrgID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrNotFound
		}
		return fmt.Errorf("lock org: %w", err)
	}
	existing, err := qtx.LockUserOrgMembership(
		ctx,
		dbsqlc.LockUserOrgMembershipParams{OrgID: input.OrgID, UserID: input.UserID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock org membership: %w", err)
	}
	if err := guardLastOwnerChange(ctx, qtx, input.OrgID, existing.Role, ""); err != nil {
		return err
	}
	secretsReferenced, err := qtx.OrgMemberOwnedSecretsReferenced(
		ctx,
		dbsqlc.OrgMemberOwnedSecretsReferencedParams{OrgID: input.OrgID, UserID: input.UserID},
	)
	if err != nil {
		return fmt.Errorf("check member-owned secret references: %w", err)
	}
	if secretsReferenced {
		return fmt.Errorf("a member-owned secret is still referenced by another resource: %w", storeerr.ErrConflict)
	}
	if err := qtx.DeleteUserOwnedSecretVersionsForOrgMember(
		ctx,
		dbsqlc.DeleteUserOwnedSecretVersionsForOrgMemberParams{OrgID: input.OrgID, UserID: input.UserID},
	); err != nil {
		return fmt.Errorf("destroy member-owned secret versions: %w", err)
	}
	if err := qtx.DeleteUserOwnedSecretChildrenForOrgMember(
		ctx,
		dbsqlc.DeleteUserOwnedSecretChildrenForOrgMemberParams{OrgID: input.OrgID, UserID: input.UserID},
	); err != nil {
		return fmt.Errorf("delete member-owned secret grants: %w", err)
	}
	if err := qtx.DeleteUserOwnedSecretsForOrgMember(
		ctx,
		dbsqlc.DeleteUserOwnedSecretsForOrgMemberParams{OrgID: input.OrgID, UserID: input.UserID},
	); err != nil {
		return fmt.Errorf("delete user-owned secrets for member: %w", err)
	}
	skillIDs, err := qtx.ListUserOwnedSkillIDsForOrg(
		ctx,
		dbsqlc.ListUserOwnedSkillIDsForOrgParams{OrgID: input.OrgID, UserID: input.UserID},
	)
	if err != nil {
		return fmt.Errorf("list user-owned skills for member: %w", err)
	}
	skillArchives := make([]skillops.ArchiveRef, 0)
	for _, skillID := range skillIDs {
		archives, err := skillops.Delete(ctx, qtx, input.OrgID, skillID)
		if err != nil {
			return fmt.Errorf("delete user-owned skill for member: %w", mapSkillOpsError(err))
		}
		skillArchives = append(skillArchives, archives...)
	}
	if _, err := qtx.RemoveUserOrgMembership(
		ctx,
		dbsqlc.RemoveUserOrgMembershipParams{OrgID: input.OrgID, UserID: input.UserID},
	); err != nil {
		return fmt.Errorf("remove org membership: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit remove org member: %w", err)
	}
	skillops.Purge(ctx, s.blobs, skillArchives)
	return nil
}

func (s *Store) RemoveProjectMembership(ctx context.Context, input RemoveProjectMembershipInput) error {
	if isNilID(input.OrgID) {
		return errors.New("org id is required")
	}
	if isNilID(input.ProjectID) {
		return errors.New("project id is required")
	}
	if isNilID(input.UserID) {
		return errors.New("user id is required")
	}
	membership, err := s.q.GetOrgMembershipForUser(
		ctx,
		dbsqlc.GetOrgMembershipForUserParams{OrgID: input.OrgID, UserID: input.UserID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load org membership for member: %w", err)
	}
	if _, err := s.q.RemoveProjectMembership(
		ctx,
		dbsqlc.RemoveProjectMembershipParams{
			OrgID:           input.OrgID,
			ProjectID:       input.ProjectID,
			OrgMembershipID: membership.ID,
		},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrNotFound
		}
		return fmt.Errorf("remove project membership: %w", err)
	}
	return nil
}

func (s *Store) ListProjectMembershipGrantsForUser(
	ctx context.Context,
	orgID, userID ID,
) ([]ProjectMembershipGrantRecord, error) {
	if isNilID(orgID) {
		return nil, errors.New("org id is required")
	}
	if isNilID(userID) {
		return nil, errors.New("user id is required")
	}
	rows, err := s.q.ListProjectMembershipsForUser(
		ctx,
		dbsqlc.ListProjectMembershipsForUserParams{OrgID: orgID, UserID: userID},
	)
	if err != nil {
		return nil, fmt.Errorf("list project memberships for user: %w", err)
	}
	records := make([]ProjectMembershipGrantRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, ProjectMembershipGrantRecord{
			ProjectID:   row.ProjectID,
			ProjectName: row.ProjectName,
			Role:        row.Role,
			CreatedAt:   row.CreatedAt,
		})
	}
	return records, nil
}

func (s *Store) GetUser(ctx context.Context, userID ID) (UserRecord, error) {
	if isNilID(userID) {
		return UserRecord{}, errors.New("user id is required")
	}
	row, err := s.q.GetUser(ctx, dbsqlc.GetUserParams{ID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return UserRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return UserRecord{}, fmt.Errorf("get user: %w", err)
	}
	return userRecordFromSQLC(row), nil
}

// PrimaryVerifiedUserEmail returns the user's verified primary email, if any.
func (s *Store) PrimaryVerifiedUserEmail(ctx context.Context, userID ID) (string, error) {
	if isNilID(userID) {
		return "", errors.New("user id is required")
	}
	emails, err := s.PrimaryVerifiedUserEmails(ctx, []ID{userID})
	if err != nil {
		return "", err
	}
	return emails[userID], nil
}

// PrimaryVerifiedUserEmails maps each user to their verified primary email.
func (s *Store) PrimaryVerifiedUserEmails(ctx context.Context, userIDs []ID) (map[ID]string, error) {
	if len(userIDs) == 0 {
		return map[ID]string{}, nil
	}
	for _, userID := range userIDs {
		if isNilID(userID) {
			return nil, errors.New("user id is required")
		}
	}
	rows, err := s.q.PrimaryVerifiedEmailsForUsers(ctx, dbsqlc.PrimaryVerifiedEmailsForUsersParams{UserIds: userIDs})
	if err != nil {
		return nil, fmt.Errorf("primary verified emails: %w", err)
	}
	emails := make(map[ID]string, len(rows))
	for _, row := range rows {
		emails[row.UserID] = row.Email
	}
	return emails, nil
}

func (s *Store) ListOrgMembershipsForUser(ctx context.Context, userID ID) ([]UserOrgMembershipRecord, error) {
	if isNilID(userID) {
		return nil, errors.New("user id is required")
	}
	rows, err := s.q.ListOrgMembershipsForUser(ctx, dbsqlc.ListOrgMembershipsForUserParams{UserID: userID})
	if err != nil {
		return nil, fmt.Errorf("list org memberships for user: %w", err)
	}
	records := make([]UserOrgMembershipRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, userOrgMembershipRecordFromSQLC(row))
	}
	return records, nil
}

type ListOrgMembersInput struct {
	OrgID ID
	Limit int
	List  listing.Options
}

type ListOrgMembersResult struct {
	Members []OrgMemberRecord
	HasMore bool
	Next    listing.Cursor
}

// ListOrgMembers returns one filtered and ordered keyset page of an org's members.
func (s *Store) ListOrgMembers(ctx context.Context, input ListOrgMembersInput) (ListOrgMembersResult, error) {
	if isNilID(input.OrgID) {
		return ListOrgMembersResult{}, errors.New("org id is required")
	}
	if input.Limit <= 0 {
		return ListOrgMembersResult{}, errors.New("limit must be positive")
	}
	input.List = listing.Normalize(input.List)
	if !listing.SortAllowed(input.List.SortField, "name", "created_at") {
		return ListOrgMembersResult{}, errors.New("unsupported sort")
	}
	params := dbsqlc.ListOrgMembersParams{
		OrgID:       input.OrgID,
		RowLimit:    int64(input.Limit) + 1,
		SortField:   input.List.SortField,
		SortDesc:    input.List.SortDesc,
		NamePattern: input.List.NamePattern,
	}
	if input.List.After.Set {
		params.CursorSet, params.CursorKey, params.CursorID = true, input.List.After.Key, input.List.After.ID
	}
	rows, err := s.q.ListOrgMembers(ctx, params)
	if err != nil {
		return ListOrgMembersResult{}, fmt.Errorf("list org members: %w", err)
	}
	result := ListOrgMembersResult{}
	if len(rows) > input.Limit {
		result.HasMore = true
		rows = rows[:input.Limit]
	}
	userIDs := make([]ID, 0, len(rows))
	for _, row := range rows {
		userIDs = append(userIDs, row.UserID)
	}
	emails, err := s.PrimaryVerifiedUserEmails(ctx, userIDs)
	if err != nil {
		return ListOrgMembersResult{}, err
	}
	result.Members = make([]OrgMemberRecord, 0, len(rows))
	for _, row := range rows {
		member := orgMemberRecordFromSQLC(row)
		member.Email = emails[member.UserID]
		result.Members = append(result.Members, member)
		result.Next = listing.Cursor{Set: true, IsNull: row.SortIsNull, Key: row.SortKey, ID: row.UserID}
	}
	return result, nil
}

//nolint:lll // Keeping generated query parameters inline makes the account cleanup sequence auditable.
func (s *Store) DeleteUserAccount(ctx context.Context, userID ID) error {
	if isNilID(userID) {
		return errors.New("user is required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin delete user account: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbsqlc.New(tx)
	if _, err := q.LockUserForUpdate(ctx, dbsqlc.LockUserForUpdateParams{ID: userID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrNotFound
		}
		return fmt.Errorf("lock user for account deletion: %w", err)
	}
	isLastOwner, err := q.UserIsLastOwnerOfAnyOrg(ctx, dbsqlc.UserIsLastOwnerOfAnyOrgParams{UserID: userID})
	if err != nil {
		return fmt.Errorf("check user ownership: %w", err)
	}
	if isLastOwner {
		return fmt.Errorf("account is the last owner of an organization: %w", storeerr.ErrConflict)
	}
	skillArchives, err := deleteUserOwnedSkillsTx(ctx, q, userID)
	if err != nil {
		return err
	}
	if err := q.RevokePersonalAccessTokensForUser(ctx, dbsqlc.RevokePersonalAccessTokensForUserParams{UserID: userID}); err != nil {
		return fmt.Errorf("revoke user access tokens: %w", err)
	}
	if err := q.RevokeBrowserSessionsForUser(ctx, dbsqlc.RevokeBrowserSessionsForUserParams{UserID: userID}); err != nil {
		return fmt.Errorf("revoke user sessions: %w", err)
	}
	if err := q.DeleteUserAuthTokensForAccountDeletion(ctx, dbsqlc.DeleteUserAuthTokensForAccountDeletionParams{UserID: userID}); err != nil {
		return fmt.Errorf("delete user auth tokens: %w", err)
	}
	if err := q.DeleteUserEmailsForAccountDeletion(ctx, dbsqlc.DeleteUserEmailsForAccountDeletionParams{UserID: userID}); err != nil {
		return fmt.Errorf("delete user emails: %w", err)
	}
	if err := q.DeleteUserAuthIdentitiesForAccountDeletion(ctx, dbsqlc.DeleteUserAuthIdentitiesForAccountDeletionParams{UserID: userID}); err != nil {
		return fmt.Errorf("delete user auth identities: %w", err)
	}
	if err := q.DeleteUserCredentialsForAccountDeletion(ctx, dbsqlc.DeleteUserCredentialsForAccountDeletionParams{UserID: userID}); err != nil {
		return fmt.Errorf("delete user credentials: %w", err)
	}
	secretsReferenced, err := q.UserOwnedSecretsReferenced(ctx, dbsqlc.UserOwnedSecretsReferencedParams{UserID: userID})
	if err != nil {
		return fmt.Errorf("check personal secret references: %w", err)
	}
	if secretsReferenced {
		return fmt.Errorf("a personal secret is still referenced by another resource: %w", storeerr.ErrConflict)
	}
	if err := q.DeleteUserOwnedSecretVersionsForUser(ctx, dbsqlc.DeleteUserOwnedSecretVersionsForUserParams{UserID: userID}); err != nil {
		return fmt.Errorf("destroy user-owned secret versions: %w", err)
	}
	if err := q.DeleteUserOwnedSecretChildrenForUser(ctx, dbsqlc.DeleteUserOwnedSecretChildrenForUserParams{UserID: userID}); err != nil {
		return fmt.Errorf("delete user-owned secret grants: %w", err)
	}
	if err := q.DeleteUserOwnedSecretsForUser(ctx, dbsqlc.DeleteUserOwnedSecretsForUserParams{UserID: userID}); err != nil {
		return fmt.Errorf("delete user-owned secrets: %w", err)
	}
	if err := q.DeleteUserOrgMemberships(ctx, dbsqlc.DeleteUserOrgMembershipsParams{UserID: userID}); err != nil {
		return fmt.Errorf("delete user memberships: %w", err)
	}
	rows, err := q.DeleteUser(ctx, dbsqlc.DeleteUserParams{ID: userID})
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if rows == 0 {
		return storeerr.ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete user account: %w", err)
	}
	skillops.Purge(ctx, s.blobs, skillArchives)
	return nil
}

func deleteUserOwnedSkillsTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	userID ID,
) ([]skillops.ArchiveRef, error) {
	skills, err := q.ListUserOwnedSkillsForUser(ctx, dbsqlc.ListUserOwnedSkillsForUserParams{UserID: userID})
	if err != nil {
		return nil, fmt.Errorf("list user-owned skills: %w", err)
	}
	archives := make([]skillops.ArchiveRef, 0)
	for _, skill := range skills {
		skillArchives, err := skillops.Delete(ctx, q, skill.OrgID, skill.ID)
		if err != nil {
			return nil, mapSkillOpsError(err)
		}
		archives = append(archives, skillArchives...)
	}
	return archives, nil
}
