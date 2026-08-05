package identitystore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/authn"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	UserAuthTokenPurposeEmailVerification = "email_verification"
	UserAuthTokenPurposePasswordReset     = "password_reset"

	defaultEmailVerificationTTL = 24 * time.Hour
	defaultPasswordResetTTL     = time.Hour
	authCleanupBatchSize        = 500
	abandonedSignupRetention    = 7 * 24 * time.Hour
)

type AuthStateCleanupResult struct {
	DeletedInactiveTokens  int64
	DeletedBrowserSessions int64
	DeletedAbandonedUsers  int64
	DeletedDeviceFlows     int64
}

func (s *Store) StartPasswordSignup(
	ctx context.Context,
	input PasswordSignupStartInput,
) (PasswordSignupStartRecord, error) {
	if input.Email == "" {
		return PasswordSignupStartRecord{}, errors.New("email is required")
	}
	normalizedEmail := NormalizeEmail(input.Email)
	if _, err := s.q.GetVerifiedUserEmailByNormalizedEmail(
		ctx,
		dbsqlc.GetVerifiedUserEmailByNormalizedEmailParams{NormalizedEmail: normalizedEmail},
	); err == nil {
		return PasswordSignupStartRecord{EmailAlreadyVerified: true}, nil
	} else if !errors.Is(
		err,
		pgx.ErrNoRows,
	) {
		return PasswordSignupStartRecord{}, fmt.Errorf("load verified email: %w", err)
	}
	token, err := randomTokenPart(32)
	if err != nil {
		return PasswordSignupStartRecord{}, fmt.Errorf("generate email verification token: %w", err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return PasswordSignupStartRecord{}, fmt.Errorf("begin password signup: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	user, err := qtx.CreateUser(ctx, dbsqlc.CreateUserParams{DisplayName: ""})
	if err != nil {
		return PasswordSignupStartRecord{}, fmt.Errorf("create signup user: %w", err)
	}
	email, err := qtx.CreateUserEmail(
		ctx,
		dbsqlc.CreateUserEmailParams{
			UserID:          user.ID,
			Email:           input.Email,
			NormalizedEmail: normalizedEmail,
			Verified:        false,
			IsPrimary:       true,
		},
	)
	if err != nil {
		return PasswordSignupStartRecord{}, fmt.Errorf("create signup email: %w", err)
	}
	if _, err := qtx.CreateUserAuthToken(ctx, dbsqlc.CreateUserAuthTokenParams{
		UserID:      user.ID,
		UserEmailID: &email.ID,
		Purpose:     UserAuthTokenPurposeEmailVerification,
		TokenHash:   HashBearerToken(token),
		TtlSeconds:  int64(defaultEmailVerificationTTL / time.Second),
	}); err != nil {
		return PasswordSignupStartRecord{}, fmt.Errorf("create email verification token: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return PasswordSignupStartRecord{}, fmt.Errorf("commit password signup: %w", err)
	}
	return PasswordSignupStartRecord{
		User:  userRecordFromSQLC(user),
		Email: userEmailRecordFromSQLC(email),
		Token: token,
	}, nil
}

func (s *Store) CleanupInactiveAuthState(ctx context.Context) (AuthStateCleanupResult, error) {
	deletedTokens, err := s.q.DeleteInactiveUserAuthTokens(
		ctx,
		dbsqlc.DeleteInactiveUserAuthTokensParams{LimitCount: authCleanupBatchSize},
	)
	if err != nil {
		return AuthStateCleanupResult{}, fmt.Errorf("delete inactive auth tokens: %w", err)
	}
	deletedDeviceFlows, err := s.q.DeleteExpiredAuthDeviceFlows(
		ctx,
		dbsqlc.DeleteExpiredAuthDeviceFlowsParams{LimitCount: authCleanupBatchSize},
	)
	if err != nil {
		return AuthStateCleanupResult{}, fmt.Errorf("delete expired auth device flows: %w", err)
	}
	deletedBrowserSessions, err := s.q.DeleteInactiveBrowserSessions(
		ctx,
		dbsqlc.DeleteInactiveBrowserSessionsParams{LimitCount: authCleanupBatchSize},
	)
	if err != nil {
		return AuthStateCleanupResult{}, fmt.Errorf("delete inactive browser sessions: %w", err)
	}
	deletedUsers, err := s.q.DeleteAbandonedUnverifiedSignupUsers(ctx, dbsqlc.DeleteAbandonedUnverifiedSignupUsersParams{
		MinimumAgeSeconds: int64(abandonedSignupRetention / time.Second),
		LimitCount:        authCleanupBatchSize,
	})
	if err != nil {
		return AuthStateCleanupResult{}, fmt.Errorf("delete abandoned signup users: %w", err)
	}
	return AuthStateCleanupResult{
		DeletedInactiveTokens:  deletedTokens,
		DeletedBrowserSessions: deletedBrowserSessions,
		DeletedAbandonedUsers:  deletedUsers,
		DeletedDeviceFlows:     deletedDeviceFlows,
	}, nil
}

func (s *Store) ActiveAuthTokenNormalizedEmail(
	ctx context.Context,
	token, purpose string,
) (string, error) {
	if token == "" || purpose == "" {
		return "", storeerr.ErrUnauthorized
	}
	row, err := s.q.GetActiveUserAuthTokenByHash(
		ctx,
		dbsqlc.GetActiveUserAuthTokenByHashParams{TokenHash: HashBearerToken(token), Purpose: purpose},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", storeerr.ErrUnauthorized
	}
	if err != nil {
		return "", fmt.Errorf("load active auth token: %w", err)
	}
	if row.NormalizedEmail != nil {
		return *row.NormalizedEmail, nil
	}
	emails, err := s.q.ListVerifiedUserEmailsByUser(ctx, dbsqlc.ListVerifiedUserEmailsByUserParams{UserID: row.UserID})
	if err != nil {
		return "", fmt.Errorf("load active token user email: %w", err)
	}
	if len(emails) == 0 {
		return "", nil
	}
	return emails[0].NormalizedEmail, nil
}

func (s *Store) CompletePasswordSignup(
	ctx context.Context,
	input CompletePasswordSignupInput,
) (CompletePasswordSignupRecord, error) {
	if input.Token == "" || input.PasswordHash == "" {
		return CompletePasswordSignupRecord{}, storeerr.ErrUnauthorized
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CompletePasswordSignupRecord{}, fmt.Errorf("begin complete password signup: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	token, err := qtx.GetActiveUserAuthTokenByHashForUpdate(ctx, dbsqlc.GetActiveUserAuthTokenByHashForUpdateParams{
		TokenHash: HashBearerToken(input.Token),
		Purpose:   UserAuthTokenPurposeEmailVerification,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return CompletePasswordSignupRecord{}, storeerr.ErrUnauthorized
	}
	if err != nil {
		return CompletePasswordSignupRecord{}, fmt.Errorf("load email verification token: %w", err)
	}
	if token.UserEmailID == nil || token.Email == nil || token.NormalizedEmail == nil {
		return CompletePasswordSignupRecord{}, fmt.Errorf("email verification token missing email")
	}
	if err := qtx.LockNormalizedEmailKey(
		ctx,
		dbsqlc.LockNormalizedEmailKeyParams{NormalizedEmail: *token.NormalizedEmail},
	); err != nil {
		return CompletePasswordSignupRecord{}, fmt.Errorf("lock signup email key: %w", err)
	}
	if _, err := qtx.LockUserEmailsByNormalizedEmail(
		ctx,
		dbsqlc.LockUserEmailsByNormalizedEmailParams{NormalizedEmail: *token.NormalizedEmail},
	); err != nil {
		return CompletePasswordSignupRecord{}, fmt.Errorf("lock matching emails: %w", err)
	}
	existing, err := qtx.GetVerifiedUserEmailByNormalizedEmail(
		ctx,
		dbsqlc.GetVerifiedUserEmailByNormalizedEmailParams{NormalizedEmail: *token.NormalizedEmail},
	)
	if err == nil && existing.ID != *token.UserEmailID {
		consumed, err := qtx.ConsumeUserAuthToken(
			ctx,
			dbsqlc.ConsumeUserAuthTokenParams{ID: token.ID},
		)
		if err != nil {
			return CompletePasswordSignupRecord{}, fmt.Errorf("consume losing email verification token: %w", err)
		}
		if consumed != 1 {
			return CompletePasswordSignupRecord{}, storeerr.ErrUnauthorized
		}
		if err := tx.Commit(ctx); err != nil {
			return CompletePasswordSignupRecord{}, fmt.Errorf("commit losing password signup: %w", err)
		}
		return CompletePasswordSignupRecord{}, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return CompletePasswordSignupRecord{}, fmt.Errorf("load verified email: %w", err)
	}
	if _, err := qtx.VerifyUserEmail(
		ctx,
		dbsqlc.VerifyUserEmailParams{ID: *token.UserEmailID, UserID: token.UserID},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CompletePasswordSignupRecord{}, storeerr.ErrUnauthorized
		}
		return CompletePasswordSignupRecord{}, fmt.Errorf("verify signup email: %w", err)
	}
	if _, err := qtx.CreateUserCredential(
		ctx,
		dbsqlc.CreateUserCredentialParams{UserID: token.UserID, PasswordHash: input.PasswordHash},
	); err != nil {
		return CompletePasswordSignupRecord{}, fmt.Errorf("create user credential: %w", err)
	}
	displayName := strings.TrimSpace(input.DisplayName)
	if displayName == "" {
		displayName = defaultDisplayName(*token.Email)
	}
	user, err := qtx.UpdateUserDisplayName(
		ctx,
		dbsqlc.UpdateUserDisplayNameParams{ID: token.UserID, DisplayName: displayName},
	)
	if err != nil {
		return CompletePasswordSignupRecord{}, fmt.Errorf("update signup user display name: %w", err)
	}
	if err := qtx.ConsumeUnconsumedUserAuthTokensForUserPurposeExcept(
		ctx,
		dbsqlc.ConsumeUnconsumedUserAuthTokensForUserPurposeExceptParams{
			UserID:     token.UserID,
			Purpose:    UserAuthTokenPurposeEmailVerification,
			ExcludedID: token.ID,
		},
	); err != nil {
		return CompletePasswordSignupRecord{}, fmt.Errorf("consume signup verification tokens: %w", err)
	}
	if input.SessionToken != "" || input.SessionCSRFToken != "" || input.SessionTTL > 0 {
		if err := createBrowserSessionTx(
			ctx,
			qtx,
			token.UserID,
			input.SessionToken,
			input.SessionCSRFToken,
			input.SessionTTL,
		); err != nil {
			return CompletePasswordSignupRecord{}, err
		}
	}
	consumed, err := qtx.ConsumeUserAuthToken(ctx, dbsqlc.ConsumeUserAuthTokenParams{ID: token.ID})
	if err != nil {
		return CompletePasswordSignupRecord{}, fmt.Errorf("consume email verification token: %w", err)
	}
	if consumed != 1 {
		return CompletePasswordSignupRecord{}, storeerr.ErrUnauthorized
	}
	if err := tx.Commit(ctx); err != nil {
		return CompletePasswordSignupRecord{}, fmt.Errorf("commit complete password signup: %w", err)
	}
	return CompletePasswordSignupRecord{User: userRecordFromSQLC(user), Verified: true}, nil
}

func (s *Store) AuthenticatePasswordAndCreateSession(
	ctx context.Context,
	input PasswordLoginSessionInput,
) (UserRecord, error) {
	normalizedEmail := NormalizeEmail(input.Email)
	if normalizedEmail == "" || input.Password == "" {
		authn.EqualizePasswordVerifyTiming(input.Password)
		return UserRecord{}, storeerr.ErrUnauthorized
	}
	login, err := s.q.GetPasswordLoginByVerifiedEmail(
		ctx,
		dbsqlc.GetPasswordLoginByVerifiedEmailParams{NormalizedEmail: normalizedEmail},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		authn.EqualizePasswordVerifyTiming(input.Password)
		return UserRecord{}, storeerr.ErrUnauthorized
	}
	if err != nil {
		return UserRecord{}, fmt.Errorf("load password credential: %w", err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return UserRecord{}, fmt.Errorf("begin password login: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	if _, err := qtx.LockUserForUpdate(ctx, dbsqlc.LockUserForUpdateParams{ID: login.UserID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			authn.EqualizePasswordVerifyTiming(input.Password)
			return UserRecord{}, storeerr.ErrUnauthorized
		}
		return UserRecord{}, fmt.Errorf("lock password login user: %w", err)
	}
	credential, err := qtx.GetPasswordCredentialByUserForUpdate(
		ctx,
		dbsqlc.GetPasswordCredentialByUserForUpdateParams{UserID: login.UserID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		authn.EqualizePasswordVerifyTiming(input.Password)
		return UserRecord{}, storeerr.ErrUnauthorized
	}
	if err != nil {
		return UserRecord{}, fmt.Errorf("load locked password credential: %w", err)
	}
	ok, err := authn.VerifyPassword(input.Password, credential.PasswordHash)
	if err != nil {
		return UserRecord{}, fmt.Errorf("verify password hash: %w", err)
	}
	if !ok {
		return UserRecord{}, storeerr.ErrUnauthorized
	}
	needsRehash, err := authn.PasswordHashNeedsRehash(credential.PasswordHash)
	if err != nil {
		return UserRecord{}, fmt.Errorf("inspect password hash: %w", err)
	}
	if err := createBrowserSessionTx(
		ctx,
		qtx,
		credential.UserID,
		input.SessionToken,
		input.SessionCSRFToken,
		input.SessionTTL,
	); err != nil {
		return UserRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return UserRecord{}, fmt.Errorf("commit password login: %w", err)
	}
	if needsRehash {
		s.rehashPasswordAfterLogin(ctx, credential.UserID, credential.PasswordHash, input.Password)
	}
	return UserRecord{
		ID:          credential.UserID,
		DisplayName: credential.DisplayName,
		CreatedAt:   credential.UserCreatedAt,
		UpdatedAt:   credential.UserUpdatedAt,
	}, nil
}

func (s *Store) rehashPasswordAfterLogin(
	ctx context.Context,
	userID ID,
	previousPasswordHash, password string,
) {
	passwordHash, err := authn.HashPassword(password)
	if err != nil {
		return
	}
	_ = s.q.RehashUserCredentialPasswordHash(
		ctx,
		dbsqlc.RehashUserCredentialPasswordHashParams{
			UserID:               userID,
			PreviousPasswordHash: previousPasswordHash,
			PasswordHash:         passwordHash,
		},
	)
}

func (s *Store) StartPasswordReset(
	ctx context.Context,
	input PasswordResetStartInput,
) (PasswordResetStartRecord, error) {
	normalizedEmail := NormalizeEmail(input.Email)
	if normalizedEmail == "" {
		return PasswordResetStartRecord{}, nil
	}
	row, err := s.q.GetPasswordLoginByVerifiedEmail(
		ctx,
		dbsqlc.GetPasswordLoginByVerifiedEmailParams{NormalizedEmail: normalizedEmail},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PasswordResetStartRecord{Email: input.Email}, nil
	}
	if err != nil {
		return PasswordResetStartRecord{}, fmt.Errorf("load reset credential: %w", err)
	}
	token, err := randomTokenPart(32)
	if err != nil {
		return PasswordResetStartRecord{}, fmt.Errorf("generate password reset token: %w", err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return PasswordResetStartRecord{}, fmt.Errorf("begin password reset request: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	if _, err := qtx.LockUserForUpdate(ctx, dbsqlc.LockUserForUpdateParams{ID: row.UserID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PasswordResetStartRecord{Email: input.Email}, nil
		}
		return PasswordResetStartRecord{}, fmt.Errorf("lock password reset user: %w", err)
	}
	if err := qtx.ConsumeUnconsumedUserAuthTokensForUserPurpose(
		ctx,
		dbsqlc.ConsumeUnconsumedUserAuthTokensForUserPurposeParams{
			UserID:  row.UserID,
			Purpose: UserAuthTokenPurposePasswordReset,
		},
	); err != nil {
		return PasswordResetStartRecord{}, fmt.Errorf("consume existing password reset tokens: %w", err)
	}
	if _, err := qtx.CreateUserAuthToken(ctx, dbsqlc.CreateUserAuthTokenParams{
		UserID:     row.UserID,
		Purpose:    UserAuthTokenPurposePasswordReset,
		TokenHash:  HashBearerToken(token),
		TtlSeconds: int64(defaultPasswordResetTTL / time.Second),
	}); err != nil {
		return PasswordResetStartRecord{}, fmt.Errorf("create password reset token: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return PasswordResetStartRecord{}, fmt.Errorf("commit password reset request: %w", err)
	}
	return PasswordResetStartRecord{Email: row.Email, Token: token, Found: true}, nil
}

func (s *Store) CompletePasswordReset(ctx context.Context, input CompletePasswordResetInput) (UserRecord, error) {
	if input.Token == "" || input.PasswordHash == "" {
		return UserRecord{}, storeerr.ErrUnauthorized
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return UserRecord{}, fmt.Errorf("begin complete password reset: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	observedToken, err := qtx.GetActiveUserAuthTokenByHash(ctx, dbsqlc.GetActiveUserAuthTokenByHashParams{
		TokenHash: HashBearerToken(input.Token),
		Purpose:   UserAuthTokenPurposePasswordReset,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return UserRecord{}, storeerr.ErrUnauthorized
	}
	if err != nil {
		return UserRecord{}, fmt.Errorf("load password reset token: %w", err)
	}
	if _, err := qtx.LockUserForUpdate(ctx, dbsqlc.LockUserForUpdateParams{ID: observedToken.UserID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return UserRecord{}, storeerr.ErrUnauthorized
		}
		return UserRecord{}, fmt.Errorf("lock password reset user: %w", err)
	}
	token, err := qtx.GetActiveUserAuthTokenByHashForUpdate(ctx, dbsqlc.GetActiveUserAuthTokenByHashForUpdateParams{
		TokenHash: HashBearerToken(input.Token),
		Purpose:   UserAuthTokenPurposePasswordReset,
	})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && token.UserID != observedToken.UserID) {
		return UserRecord{}, storeerr.ErrUnauthorized
	}
	if err != nil {
		return UserRecord{}, fmt.Errorf("lock password reset token: %w", err)
	}
	user, err := qtx.GetUser(ctx, dbsqlc.GetUserParams{ID: token.UserID})
	if err != nil {
		return UserRecord{}, fmt.Errorf("load password reset user: %w", err)
	}
	if _, err := qtx.UpdateUserCredentialPasswordHash(
		ctx,
		dbsqlc.UpdateUserCredentialPasswordHashParams{UserID: token.UserID, PasswordHash: input.PasswordHash},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return UserRecord{}, storeerr.ErrUnauthorized
		}
		return UserRecord{}, fmt.Errorf("update password reset credential: %w", err)
	}
	if err := qtx.ConsumeUnconsumedUserAuthTokensForUserPurposeExcept(
		ctx,
		dbsqlc.ConsumeUnconsumedUserAuthTokensForUserPurposeExceptParams{
			UserID:     token.UserID,
			Purpose:    UserAuthTokenPurposePasswordReset,
			ExcludedID: token.ID,
		},
	); err != nil {
		return UserRecord{}, fmt.Errorf("consume password reset tokens: %w", err)
	}
	if err := qtx.RevokeBrowserSessionsForUser(
		ctx,
		dbsqlc.RevokeBrowserSessionsForUserParams{UserID: token.UserID},
	); err != nil {
		return UserRecord{}, fmt.Errorf("revoke password reset sessions: %w", err)
	}
	if err := createBrowserSessionTx(
		ctx,
		qtx,
		token.UserID,
		input.SessionToken,
		input.SessionCSRFToken,
		input.SessionTTL,
	); err != nil {
		return UserRecord{}, err
	}
	consumed, err := qtx.ConsumeUserAuthToken(ctx, dbsqlc.ConsumeUserAuthTokenParams{ID: token.ID})
	if err != nil {
		return UserRecord{}, fmt.Errorf("consume password reset token: %w", err)
	}
	if consumed != 1 {
		return UserRecord{}, storeerr.ErrUnauthorized
	}
	if err := tx.Commit(ctx); err != nil {
		return UserRecord{}, fmt.Errorf("commit complete password reset: %w", err)
	}
	return userRecordFromSQLC(user), nil
}

func (s *Store) PrimaryVerifiedEmailForUser(ctx context.Context, userID ID) (UserEmailRecord, bool, error) {
	if isNilID(userID) {
		return UserEmailRecord{}, false, storeerr.ErrUnauthorized
	}
	rows, err := s.q.ListVerifiedUserEmailsByUser(ctx, dbsqlc.ListVerifiedUserEmailsByUserParams{UserID: userID})
	if err != nil {
		return UserEmailRecord{}, false, fmt.Errorf("list verified user emails: %w", err)
	}
	if len(rows) == 0 {
		return UserEmailRecord{}, false, nil
	}
	return userEmailRecordFromSQLC(rows[0]), true, nil
}

func (s *Store) ChangePassword(ctx context.Context, input ChangePasswordInput) (UserRecord, error) {
	if input.PasswordHash == "" {
		return UserRecord{}, errors.New("password hash is required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return UserRecord{}, fmt.Errorf("begin change password: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	user, err := verifyUserPasswordForUpdate(ctx, qtx, input.UserID, input.CurrentPassword)
	if err != nil {
		return UserRecord{}, err
	}
	if _, err := qtx.UpdateUserCredentialPasswordHash(
		ctx,
		dbsqlc.UpdateUserCredentialPasswordHashParams{UserID: input.UserID, PasswordHash: input.PasswordHash},
	); err != nil {
		return UserRecord{}, fmt.Errorf("update changed password: %w", err)
	}
	if err := qtx.RevokeBrowserSessionsForUser(
		ctx,
		dbsqlc.RevokeBrowserSessionsForUserParams{UserID: input.UserID},
	); err != nil {
		return UserRecord{}, fmt.Errorf("revoke changed password sessions: %w", err)
	}
	if err := createBrowserSessionTx(
		ctx,
		qtx,
		input.UserID,
		input.SessionToken,
		input.SessionCSRFToken,
		input.SessionTTL,
	); err != nil {
		return UserRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return UserRecord{}, fmt.Errorf("commit change password: %w", err)
	}
	return user, nil
}

func (s *Store) ValidateCompromiseRevocationTx(
	ctx context.Context,
	tx pgx.Tx,
	userID ID,
	currentPassword string,
) error {
	qtx := s.q.WithTx(tx)
	if _, err := qtx.LockUserForUpdate(ctx, dbsqlc.LockUserForUpdateParams{ID: userID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storeerr.ErrUnauthorized
		}
		return fmt.Errorf("lock user for compromise revocation: %w", err)
	}
	row, err := qtx.GetPasswordCredentialByUserForUpdate(
		ctx,
		dbsqlc.GetPasswordCredentialByUserForUpdateParams{UserID: userID},
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("load locked user credential: %w", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if currentPassword == "" {
		authn.EqualizePasswordVerifyTiming(currentPassword)
		return storeerr.ErrUnauthorized
	}
	_, err = verifyPasswordCredentialRow(row, currentPassword)
	return err
}

func (s *Store) RevokeUserAuthTokensTx(ctx context.Context, tx pgx.Tx, userID ID) error {
	qtx := s.q.WithTx(tx)
	if err := qtx.RevokeBrowserSessionsForUser(
		ctx,
		dbsqlc.RevokeBrowserSessionsForUserParams{UserID: userID},
	); err != nil {
		return fmt.Errorf("revoke compromised sessions: %w", err)
	}
	if err := qtx.RevokePersonalAccessTokensForUser(
		ctx,
		dbsqlc.RevokePersonalAccessTokensForUserParams{UserID: userID},
	); err != nil {
		return fmt.Errorf("revoke compromised personal access tokens: %w", err)
	}
	return nil
}

func verifyUserPasswordForUpdate(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	userID ID,
	password string,
) (UserRecord, error) {
	if _, err := qtx.LockUserForUpdate(ctx, dbsqlc.LockUserForUpdateParams{ID: userID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			authn.EqualizePasswordVerifyTiming(password)
			return UserRecord{}, storeerr.ErrUnauthorized
		}
		return UserRecord{}, fmt.Errorf("lock user for password verification: %w", err)
	}
	return verifyUserPasswordCredentialForUpdate(ctx, qtx, userID, password)
}

func verifyUserPasswordCredentialForUpdate(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	userID ID,
	password string,
) (UserRecord, error) {
	if isNilID(userID) || password == "" {
		authn.EqualizePasswordVerifyTiming(password)
		return UserRecord{}, storeerr.ErrUnauthorized
	}
	row, err := qtx.GetPasswordCredentialByUserForUpdate(
		ctx,
		dbsqlc.GetPasswordCredentialByUserForUpdateParams{UserID: userID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		authn.EqualizePasswordVerifyTiming(password)
		return UserRecord{}, storeerr.ErrUnauthorized
	}
	if err != nil {
		return UserRecord{}, fmt.Errorf("load locked user credential: %w", err)
	}
	return verifyPasswordCredentialRow(row, password)
}

func verifyPasswordCredentialRow(
	row dbsqlc.GetPasswordCredentialByUserForUpdateRow,
	password string,
) (UserRecord, error) {
	ok, err := authn.VerifyPassword(password, row.PasswordHash)
	if err != nil {
		return UserRecord{}, fmt.Errorf("verify user password hash: %w", err)
	}
	if !ok {
		return UserRecord{}, storeerr.ErrUnauthorized
	}
	return UserRecord{
		ID:          row.UserID,
		DisplayName: row.DisplayName,
		CreatedAt:   row.UserCreatedAt,
		UpdatedAt:   row.UserUpdatedAt,
	}, nil
}

func createBrowserSessionTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	userID ID,
	sessionToken, csrfToken string,
	ttl time.Duration,
) error {
	if sessionToken == "" || csrfToken == "" {
		return errors.New("session token and csrf token are required together")
	}
	if ttl < time.Millisecond {
		return errors.New("session ttl must be at least one millisecond")
	}
	if _, err := qtx.CreateBrowserSession(ctx, dbsqlc.CreateBrowserSessionParams{
		UserID:          userID,
		TokenHash:       HashBearerToken(sessionToken),
		CsrfTokenHash:   HashBearerToken(csrfToken),
		TtlMilliseconds: ttl.Milliseconds(),
	}); err != nil {
		if storeutil.IsUniqueViolation(err) {
			return storeerr.ErrIdempotencyConflict
		}
		return fmt.Errorf("create browser session: %w", err)
	}
	return nil
}

func defaultDisplayName(email string) string {
	local, _, ok := strings.Cut(email, "@")
	if ok && strings.TrimSpace(local) != "" {
		return strings.TrimSpace(local)
	}
	return strings.TrimSpace(email)
}
