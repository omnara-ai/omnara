package dbmigrate

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestRunPostgresRequiresPositiveTimeout(t *testing.T) {
	err := RunPostgres(context.Background(), "not-used", fstest.MapFS{}, 0)
	if err == nil || !strings.Contains(err.Error(), "must be positive") {
		t.Fatalf("migration timeout error = %v", err)
	}
}

func TestAgentConfigNameMigrationRegistrationFailsClosed(t *testing.T) {
	tests := []struct {
		name         string
		migrations   fstest.MapFS
		wantRegister bool
	}{
		{
			name: "migration present",
			migrations: fstest.MapFS{
				agentConfigNameMigrationFile: &fstest.MapFile{},
			},
			wantRegister: true,
		},
		{
			name: "truncated migration set",
			migrations: fstest.MapFS{
				"000025_resource_name_lengths.sql": &fstest.MapFile{},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := shouldRegisterAgentConfigNameMigration(test.migrations)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.wantRegister {
				t.Fatalf("register migration = %t, want %t", got, test.wantRegister)
			}
		})
	}
}

func TestAgentConfigNameMigrationPresenceFailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		present bool
		current int64
		target  int64
		wantErr bool
	}{
		{name: "present", present: true, current: 25, target: 26},
		{name: "truncated set", current: 20, target: 24},
		{name: "already applied", current: 26, target: 27},
		{name: "missing before target", current: 24, target: 25, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateAgentConfigNameMigrationPresence(
				test.present,
				test.current,
				test.target,
			)
			if (err != nil) != test.wantErr {
				t.Fatalf("presence error = %v, want error %t", err, test.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), agentConfigNameMigrationFile) {
				t.Fatalf("presence error = %v, want required migration filename", err)
			}
		})
	}
}

func TestValidatePostgresVersion(t *testing.T) {
	if err := validatePostgresVersion(180000); err != nil {
		t.Fatalf("validate PostgreSQL 18: %v", err)
	}
	if err := validatePostgresVersion(190001); err != nil {
		t.Fatalf("validate newer PostgreSQL: %v", err)
	}
	err := validatePostgresVersion(170006)
	if err == nil || !strings.Contains(err.Error(), "PostgreSQL 18 or newer is required") {
		t.Fatalf("old PostgreSQL error = %v", err)
	}
}

func TestValidatePostgresEncoding(t *testing.T) {
	if err := validatePostgresEncoding("UTF8"); err != nil {
		t.Fatalf("validate PostgreSQL UTF8 encoding: %v", err)
	}
	err := validatePostgresEncoding("SQL_ASCII")
	if err == nil || !strings.Contains(err.Error(), "UTF8 database encoding is required") {
		t.Fatalf("non-UTF8 PostgreSQL encoding error = %v", err)
	}
}

func TestParsePostgresConfigDefaultsAndExplicitPrecedence(t *testing.T) {
	for _, test := range []struct {
		name             string
		databaseURL      string
		applicationName  string
		lockTimeout      string
		statementTimeout string
	}{
		{
			name:             "defaults",
			databaseURL:      "postgres://user:password@database.invalid/omnara?pool_max_conns=5",
			applicationName:  "omnara-migrate",
			lockTimeout:      "30s",
			statementTimeout: "15min",
		},
		{
			name: "keyword DSN overrides",
			databaseURL: "host=database.invalid user=user password=password dbname=omnara " +
				"application_name=operator-migrator lock_timeout=7s statement_timeout=11s " +
				"pool_min_conns=2",
			applicationName:  "operator-migrator",
			lockTimeout:      "7s",
			statementTimeout: "11s",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := parsePostgresConfig(test.databaseURL)
			if err != nil {
				t.Fatalf("parse migration config: %v", err)
			}
			params := cfg.Config.RuntimeParams
			if params["application_name"] != test.applicationName ||
				params["lock_timeout"] != test.lockTimeout ||
				params["statement_timeout"] != test.statementTimeout {
				t.Fatalf("migration runtime params = %+v", params)
			}
			if _, exists := params["pool_max_conns"]; exists {
				t.Fatal("pool_max_conns leaked to PostgreSQL runtime parameters")
			}
			if _, exists := params["pool_min_conns"]; exists {
				t.Fatal("pool_min_conns leaked to PostgreSQL runtime parameters")
			}
		})
	}
}

func TestRunPostgresRejectsExpiredContextBeforeConnecting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := RunPostgres(ctx, "postgres://invalid.invalid/omnara", fstest.MapFS{}, time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("migration error = %v, want context cancellation", err)
	}
}

func TestDeadlineSessionLockerBoundsDetachedUnlock(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	cancelParent()
	detached := context.WithoutCancel(parent)
	if _, hasDeadline := detached.Deadline(); hasDeadline || detached.Err() != nil {
		t.Fatal("test requires a deadline-free detached context")
	}

	delegate := &blockingSessionLocker{}
	locker := deadlineSessionLocker{delegate: delegate, timeout: 20 * time.Millisecond}
	err := locker.SessionUnlock(detached, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unlock error = %v, want deadline exceeded", err)
	}
	if !delegate.unlockHadDeadline {
		t.Fatal("delegate unlock context had no deadline")
	}
}

type blockingSessionLocker struct {
	unlockHadDeadline bool
}

func (*blockingSessionLocker) SessionLock(context.Context, *sql.Conn) error {
	return nil
}

func (locker *blockingSessionLocker) SessionUnlock(ctx context.Context, _ *sql.Conn) error {
	_, locker.unlockHadDeadline = ctx.Deadline()
	<-ctx.Done()
	return ctx.Err()
}
