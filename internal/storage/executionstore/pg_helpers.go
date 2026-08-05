package executionstore

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

func isRetryableTransactionError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01")
}

func isUniqueViolationOnConstraint(err error, constraintName string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraintName
}
