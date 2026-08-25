package identitystore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/bearertoken"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/internal/tokenutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type CreatedPersonalAccessToken struct {
	Record PersonalAccessTokenRecord
	Token  string
}

func (s *Store) CreatePersonalAccessTokenWithPlaintext(
	ctx context.Context,
	input CreatePersonalAccessTokenInput,
) (CreatedPersonalAccessToken, error) {
	prepared, err := preparePersonalAccessTokenInput(input)
	if err != nil {
		return CreatedPersonalAccessToken{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CreatedPersonalAccessToken{}, fmt.Errorf("begin create personal access token: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := createPersonalAccessTokenTx(ctx, s.q.WithTx(tx), prepared)
	if err != nil {
		return CreatedPersonalAccessToken{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CreatedPersonalAccessToken{}, fmt.Errorf("commit create personal access token: %w", err)
	}
	return CreatedPersonalAccessToken{
		Record: personalAccessTokenRecordFromSQLC(row),
		Token:  prepared.token,
	}, nil
}

type preparedPersonalAccessToken struct {
	input   CreatePersonalAccessTokenInput
	tokenID string
	token   string
}

func preparePersonalAccessTokenInput(input CreatePersonalAccessTokenInput) (preparedPersonalAccessToken, error) {
	if input.Name == "" {
		return preparedPersonalAccessToken{}, errors.New("personal access token name is required")
	}
	normalizedName, err := resourcename.CanonicalizeRequired("personal access token name", input.Name)
	if err != nil {
		return preparedPersonalAccessToken{}, storeerr.InvalidRequest(err)
	}
	input.Name = normalizedName
	if isNilID(input.UserID) {
		return preparedPersonalAccessToken{}, errors.New("personal access token user id is required")
	}
	if input.ActorPrincipal.Type != "" && input.ActorPrincipal.ID != input.UserID {
		return preparedPersonalAccessToken{}, storeerr.ErrUnauthorized
	}
	tokenID, err := randomTokenPart(10)
	if err != nil {
		return preparedPersonalAccessToken{}, fmt.Errorf("generate personal access token id: %w", err)
	}
	token, err := bearertoken.Generate(bearertoken.KindPersonalAccess)
	if err != nil {
		return preparedPersonalAccessToken{}, err
	}
	return preparedPersonalAccessToken{input: input, tokenID: tokenID, token: token}, nil
}

func createPersonalAccessTokenTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	prepared preparedPersonalAccessToken,
) (dbsqlc.PersonalAccessToken, error) {
	input := prepared.input
	if _, err := qtx.LockUserForUpdate(ctx, dbsqlc.LockUserForUpdateParams{ID: input.UserID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return dbsqlc.PersonalAccessToken{}, storeerr.ErrUnauthorized
		}
		return dbsqlc.PersonalAccessToken{}, fmt.Errorf("lock user for personal access token: %w", err)
	}
	if err := ensureUserPrincipalStillActive(ctx, qtx, input.ActorPrincipal); err != nil {
		return dbsqlc.PersonalAccessToken{}, err
	}
	row, err := qtx.CreatePersonalAccessToken(
		ctx,
		dbsqlc.CreatePersonalAccessTokenParams{
			UserID:    input.UserID,
			Name:      input.Name,
			TokenID:   prepared.tokenID,
			TokenHash: HashBearerToken(prepared.token),
		},
	)
	if err != nil {
		if storeutil.IsUniqueViolation(err) {
			return dbsqlc.PersonalAccessToken{}, storeerr.ErrIdempotencyConflict
		}
		return dbsqlc.PersonalAccessToken{}, fmt.Errorf("create personal access token: %w", err)
	}
	tokenCount, err := qtx.CountActivePersonalAccessTokensForUser(
		ctx,
		dbsqlc.CountActivePersonalAccessTokensForUserParams{UserID: input.UserID},
	)
	if err != nil {
		return dbsqlc.PersonalAccessToken{}, fmt.Errorf("count active personal access tokens: %w", err)
	}
	if tokenCount > MaxActivePersonalAccessTokensPerUser {
		return dbsqlc.PersonalAccessToken{}, resourceLimitExceeded(
			"active personal access tokens",
			MaxActivePersonalAccessTokensPerUser,
		)
	}
	return row, nil
}

func (s *Store) AuthenticatePersonalAccessToken(
	ctx context.Context,
	token string,
) (PrincipalRecord, error) {
	if err := bearertoken.Validate(token, bearertoken.KindPersonalAccess); err != nil {
		return PrincipalRecord{}, storeerr.ErrUnauthorized
	}
	row, err := s.q.AuthenticatePersonalAccessToken(
		ctx,
		dbsqlc.AuthenticatePersonalAccessTokenParams{
			TokenHash:            HashBearerToken(token),
			TouchIntervalSeconds: int64(bearerTokenTouchInterval / time.Second),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PrincipalRecord{}, storeerr.ErrUnauthorized
	}
	if err != nil {
		return PrincipalRecord{}, fmt.Errorf("authenticate personal access token: %w", err)
	}
	return NewPersonalAccessTokenPrincipal(row.UserID, row.PersonalAccessTokenID), nil
}

type ListPersonalAccessTokensInput struct {
	UserID ID
	Limit  int
	After  listing.KeysetCursor
}

type ListPersonalAccessTokensResult struct {
	Tokens  []PersonalAccessTokenRecord
	HasMore bool
}

// ListPersonalAccessTokensForUser returns one keyset page of a user's personal access tokens, newest first.
func (s *Store) ListPersonalAccessTokensForUser(
	ctx context.Context,
	input ListPersonalAccessTokensInput,
) (ListPersonalAccessTokensResult, error) {
	if isNilID(input.UserID) {
		return ListPersonalAccessTokensResult{}, errors.New("user id is required")
	}
	if input.Limit <= 0 {
		return ListPersonalAccessTokensResult{}, errors.New("limit must be positive")
	}
	params := dbsqlc.ListPersonalAccessTokensForUserParams{
		UserID:   input.UserID,
		RowLimit: int64(input.Limit) + 1,
	}
	if input.After.Set {
		createdAt := input.After.CreatedAt
		id := input.After.ID
		params.CursorCreatedAt = &createdAt
		params.CursorID = &id
	}
	rows, err := s.q.ListPersonalAccessTokensForUser(ctx, params)
	if err != nil {
		return ListPersonalAccessTokensResult{}, fmt.Errorf("list personal access tokens: %w", err)
	}
	result := ListPersonalAccessTokensResult{}
	if len(rows) > input.Limit {
		result.HasMore = true
		rows = rows[:input.Limit]
	}
	result.Tokens = make([]PersonalAccessTokenRecord, 0, len(rows))
	for _, row := range rows {
		result.Tokens = append(result.Tokens, personalAccessTokenRecordFromSQLC(row))
	}
	return result, nil
}

// RevokePersonalAccessToken revokes the caller's token. It is idempotent: a
// token that is already revoked keeps its original revoked_at. A token that
// does not exist or belongs to another user yields storeerr.ErrNotFound, so token
// existence is not leaked across users.
func (s *Store) RevokePersonalAccessToken(
	ctx context.Context,
	userID, tokenID ID,
) (PersonalAccessTokenRecord, error) {
	if isNilID(userID) {
		return PersonalAccessTokenRecord{}, errors.New("user id is required")
	}
	if isNilID(tokenID) {
		return PersonalAccessTokenRecord{}, errors.New("personal access token id is required")
	}
	row, err := s.q.RevokePersonalAccessToken(
		ctx,
		dbsqlc.RevokePersonalAccessTokenParams{ID: tokenID, UserID: userID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PersonalAccessTokenRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return PersonalAccessTokenRecord{}, fmt.Errorf("revoke personal access token: %w", err)
	}
	return personalAccessTokenRecordFromSQLC(row), nil
}

func HashBearerToken(token string) string {
	return tokenutil.Hash(token)
}

func randomTokenPart(size int) (string, error) {
	return tokenutil.RandomHex(size)
}
