package identitystore

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil"
)

func TestAuthenticateBrowserSessionRetry(t *testing.T) {
	retryable := &browserSessionRetryError{cause: errors.New("write timeout"), safe: true}
	partialWrite := &browserSessionRetryError{cause: errors.New("partial write timeout")}
	writeTimeout := &browserSessionRetryError{cause: &net.OpError{Op: "write", Err: os.ErrDeadlineExceeded}, safe: true}
	readTimeout := &browserSessionRetryError{cause: &net.OpError{Op: "read", Err: os.ErrDeadlineExceeded}, safe: true}
	partialTimeout := &browserSessionRetryError{cause: &net.OpError{Op: "write", Err: os.ErrDeadlineExceeded}}
	queryError := &pgconn.PgError{Code: "42601", Message: "syntax error"}
	cfg, err := pgconn.ParseConfig("postgres://user:pass@127.0.0.1:1,127.0.0.2:1/db?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	dialCalls := 0
	acquireFailure := errors.New("connection refused")
	cfg.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		dialCalls++
		if dialCalls == 1 {
			return nil, &net.OpError{Op: "dial", Err: os.ErrDeadlineExceeded}
		}
		return nil, &net.OpError{Op: "dial", Err: acquireFailure}
	}
	_, connectErr := pgconn.ConnectConfig(context.Background(), cfg)
	var connectTimeout net.Error
	if dialCalls != 2 || !errors.Is(connectErr, acquireFailure) ||
		!errors.As(connectErr, &connectTimeout) || !connectTimeout.Timeout() {
		t.Fatalf("expected mixed timeout and acquisition failure, got %v (%d dials)", connectErr, dialCalls)
	}
	userID, sessionID := uuid.New(), uuid.New()
	wantPrincipal := NewBrowserSessionPrincipal(userID, sessionID)
	for _, tt := range []struct {
		name       string
		errs       []error
		cancel     bool
		emptyToken bool
		expired    bool
		wantErr    error
	}{
		{name: "success", errs: []error{nil}},
		{name: "retry succeeds", errs: []error{retryable, nil}},
		{name: "wrapped retryable error", errs: []error{fmt.Errorf("wrapped: %w", retryable), nil}},
		{name: "retry exhausted", errs: []error{retryable, retryable}, wantErr: retryable},
		{name: "retry returns different error", errs: []error{retryable, queryError}, wantErr: queryError},
		{name: "retry returns unauthorized", errs: []error{retryable, pgx.ErrNoRows}, wantErr: storeerr.ErrUnauthorized},
		{name: "unauthorized", errs: []error{pgx.ErrNoRows}, wantErr: storeerr.ErrUnauthorized},
		{name: "query error", errs: []error{queryError}, wantErr: queryError},
		{name: "partial write", errs: []error{partialWrite}, wantErr: partialWrite},
		{name: "canceled during first attempt", errs: []error{retryable}, cancel: true, wantErr: retryable},
		{name: "deadline exceeded", errs: []error{context.DeadlineExceeded}, wantErr: context.DeadlineExceeded},
		{name: "canceled write timeout", errs: []error{writeTimeout}, cancel: true, wantErr: context.Canceled},
		{name: "canceled write after retry", errs: []error{retryable, writeTimeout}, cancel: true, wantErr: context.Canceled},
		{name: "active write timeout recovers", errs: []error{writeTimeout, nil}},
		{name: "canceled partial write", errs: []error{partialTimeout}, cancel: true, wantErr: context.Canceled},
		{name: "canceled read timeout", errs: []error{readTimeout}, cancel: true, wantErr: context.Canceled},
		{name: "canceled postgres failure", errs: []error{queryError}, cancel: true, wantErr: queryError},
		{name: "canceled mixed acquisition failure", errs: []error{connectErr}, cancel: true, wantErr: connectErr},
		{
			name: "canceled non-timeout write", errs: []error{&net.OpError{Op: "write", Err: os.ErrClosed}},
			cancel: true, wantErr: os.ErrClosed,
		},
		{
			name: "canceled non-timeout dial", errs: []error{&net.OpError{Op: "dial", Err: os.ErrPermission}},
			cancel: true, wantErr: os.ErrPermission,
		},
		{name: "canceled success", errs: []error{nil}, cancel: true},
		{name: "expired write timeout", errs: []error{writeTimeout}, expired: true, wantErr: writeTimeout},
		{name: "empty token", emptyToken: true, wantErr: storeerr.ErrUnauthorized},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.expired {
				expiredCtx, expireCancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer expireCancel()
				ctx = expiredCtx
			}
			calls := 0
			token := "session-token"
			if tt.emptyToken {
				token = ""
			}
			wantArgs := []any{HashBearerToken(token), int64(7 * 24 * 60 * 60), int64(5 * 60)}
			db := browserSessionQueryDB{queryRow: func(gotCtx context.Context, _ string, args ...any) pgx.Row {
				if gotCtx != ctx {
					t.Fatal("query did not preserve request context")
				}
				if !reflect.DeepEqual(args, wantArgs) {
					t.Fatalf("query args = %v, want %v", args, wantArgs)
				}
				if calls >= len(tt.errs) {
					t.Fatalf("unexpected query attempt %d", calls+1)
				}
				err := tt.errs[calls]
				calls++
				return browserSessionQueryRow(func(dest ...any) error {
					if tt.cancel && calls == len(tt.errs) {
						cancel()
					}
					if err != nil {
						return err
					}
					*testutil.RequireType[*uuid.UUID](t, dest[0]) = userID
					*testutil.RequireType[*uuid.UUID](t, dest[1]) = sessionID
					*testutil.RequireType[*string](t, dest[2]) = "csrf-hash"
					*testutil.RequireType[*time.Time](t, dest[3]) = time.Now()
					return nil
				})
			}}
			s := &Store{q: dbsqlc.New(db)}
			principal, csrfHash, err := s.AuthenticateBrowserSession(ctx, token)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if errors.Is(tt.wantErr, context.Canceled) && !strings.Contains(err.Error(), tt.errs[len(tt.errs)-1].Error()) {
				t.Fatalf("cancellation lost socket error detail: %v", err)
			}
			if calls != len(tt.errs) {
				t.Fatalf("query attempts = %d, want %d", calls, len(tt.errs))
			}
			if err == nil {
				if !reflect.DeepEqual(principal, wantPrincipal) || csrfHash != "csrf-hash" {
					t.Fatalf("authentication = %+v, %q; want %+v, csrf-hash", principal, csrfHash, wantPrincipal)
				}
			} else if !reflect.DeepEqual(principal, PrincipalRecord{}) || csrfHash != "" {
				t.Fatalf("failed authentication returned credentials: %+v, %q", principal, csrfHash)
			}
		})
	}
}

type browserSessionRetryError struct {
	cause error
	safe  bool
}

func (e *browserSessionRetryError) Error() string { return e.cause.Error() }

func (e *browserSessionRetryError) Unwrap() error { return e.cause }

func (e *browserSessionRetryError) SafeToRetry() bool { return e.safe }

type browserSessionQueryDB struct {
	dbsqlc.DBTX
	queryRow func(context.Context, string, ...any) pgx.Row
}

func (db browserSessionQueryDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return db.queryRow(ctx, sql, args...)
}

type browserSessionQueryRow func(...any) error

func (row browserSessionQueryRow) Scan(dest ...any) error { return row(dest...) }
