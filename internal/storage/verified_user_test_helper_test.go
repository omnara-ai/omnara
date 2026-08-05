//go:build integration

package storage

import (
	"context"
	"errors"
	"strings"

	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type CreateVerifiedUserInput struct {
	DisplayName string
	Email       string
}

func (s *Store) CreateVerifiedUser(
	ctx context.Context,
	input CreateVerifiedUserInput,
) (identitystore.UserRecord, error) {
	if s == nil || s.pool == nil {
		return identitystore.UserRecord{}, errors.New("store is required")
	}
	email := strings.TrimSpace(input.Email)
	normalizedEmail := identitystore.NormalizeEmail(email)
	if normalizedEmail == "" {
		return identitystore.UserRecord{}, errors.New("email is required")
	}
	var user identitystore.UserRecord
	err := s.pool.QueryRow(ctx, `
WITH email_lock AS MATERIALIZED (
  SELECT pg_advisory_xact_lock(hashtext($1::text))
), created_user AS (
  INSERT INTO users(display_name, created_at, updated_at)
  SELECT $2, transaction_timestamp(), transaction_timestamp()
  FROM email_lock
  RETURNING id, display_name, created_at, updated_at
), created_email AS (
  INSERT INTO user_emails(
    user_id, email, normalized_email, verified_at, is_primary, created_at, updated_at
  )
  SELECT id, $3, $1, transaction_timestamp(), true,
         transaction_timestamp(), transaction_timestamp()
  FROM created_user
  RETURNING user_id
)
SELECT users.id, users.display_name, users.created_at, users.updated_at
FROM created_user users
JOIN created_email email ON email.user_id = users.id
`, normalizedEmail, input.DisplayName, email).Scan(
		&user.ID,
		&user.DisplayName,
		&user.CreatedAt,
		&user.UpdatedAt,
	)
	if storeutil.IsUniqueViolation(err) {
		return identitystore.UserRecord{}, storeerr.ErrIdempotencyConflict
	}
	return user, err
}
