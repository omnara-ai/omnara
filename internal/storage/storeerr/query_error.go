package storeerr

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/errutil"
)

type QueryError struct {
	Query      string
	SQLState   string
	Constraint string
	cause      error
}

func WrapQuery(query string, err error) error {
	if err == nil || slices.Contains([]error{
		pgx.ErrNoRows, pgx.ErrTxClosed, pgx.ErrTxCommitRollback, context.Canceled, context.DeadlineExceeded,
	}, err) {
		return err
	}
	wrapped := &QueryError{Query: query, cause: err}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		wrapped.SQLState = pgErr.Code
		wrapped.Constraint = pgErr.ConstraintName
	}
	return wrapped
}

func (err *QueryError) Error() string {
	if err.SQLState == "" {
		switch {
		case errutil.OnlyMatches(err.cause, context.Canceled):
			return fmt.Sprintf("db query %s: canceled", err.Query)
		case errutil.OnlyMatches(err.cause, context.DeadlineExceeded):
			return fmt.Sprintf("db query %s: deadline exceeded", err.Query)
		}
		var timeout net.Error
		if errors.As(err.cause, &timeout) && timeout.Timeout() {
			return fmt.Sprintf("db query %s: timeout", err.Query)
		}
		return fmt.Sprintf("db query %s: %T", err.Query, err.cause)
	}
	text := fmt.Sprintf("db query %s: SQLSTATE %s", err.Query, err.SQLState)
	if err.Constraint != "" {
		text += " (constraint " + err.Constraint + ")"
	}
	return text
}

func (err *QueryError) Unwrap() error {
	return err.cause
}
