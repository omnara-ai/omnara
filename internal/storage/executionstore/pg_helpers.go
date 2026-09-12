package executionstore

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

func isUniqueViolationOnConstraint(err error, constraintName string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraintName
}
