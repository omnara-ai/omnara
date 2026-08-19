// Package statedb owns small, structured per-machine facts that must survive
// daemon and supervisor restarts. PostgreSQL remains authoritative for product
// state; credentials, launch inputs, and large artifacts stay elsewhere.
package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb/internal/dbsqlc"
	sqlite3 "modernc.org/sqlite"
	sqlite3lib "modernc.org/sqlite/lib"
)

type ProcessPhase = daemonprotocol.ProcessPhase

const (
	ProcessPreparing = daemonprotocol.ProcessPhasePreparing
	ProcessPrepared  = daemonprotocol.ProcessPhasePrepared
	ProcessAccepted  = daemonprotocol.ProcessPhaseAccepted
	ProcessTerminal  = daemonprotocol.ProcessPhaseTerminal
)

type ReportState string

const (
	ReportPending      ReportState = "pending"
	ReportAcknowledged ReportState = "acknowledged"
	ReportRejected     ReportState = "rejected"
)

type ReportKind string

const (
	ReportProcessStarted  ReportKind = "process_started"
	ReportProcessTerminal ReportKind = "process_terminal"
	ReportActionTerminal  ReportKind = "action_terminal"
)

var (
	ErrBusy                       = errors.New("state database is busy")
	ErrFull                       = errors.New("state database is full")
	ErrProcessExists              = errors.New("local state already exists for process")
	ErrSupervisorIdentityMismatch = errors.New("supervisor identity mismatch")
	ErrStateConflict              = errors.New("local state conflict")
	ErrActionBlocked              = errors.New("process action is blocked by an earlier sequence")
	ErrClosureBlocked             = errors.New("local process closure is blocked")
)

type Process struct {
	ProcessID             string
	SupervisorInstanceID  string
	SupervisorToken       string
	Phase                 ProcessPhase
	ResolvedActionSeq     int64
	ExecCommitted         bool
	ContainmentKind       string
	ContainmentID         string
	ContainmentEmpty      bool
	ActionAdmissionClosed bool
	LocalClosed           bool
	ServerReleased        bool
}

type Action struct {
	ID              string
	ProcessID       string
	Kind            daemonprotocol.ProcessActionKind
	Seq             int64
	EffectCommitted bool
}

type Report struct {
	ID        string
	ProcessID string
	ActionID  string
	Kind      ReportKind
	Body      []byte
	State     ReportState
	ErrorCode string
	Error     string
}

type Store struct {
	db *sql.DB
	q  *dbsqlc.Queries
}

type Supervisor struct {
	store                *Store
	processID            string
	supervisorInstanceID string
}

func Open(
	ctx context.Context,
	path, installationID, machineID string,
) (*Store, error) {
	return open(ctx, path, installationID, machineID, true)
}

func OpenSupervisor(
	ctx context.Context,
	path, installationID, machineID, processID, supervisorInstanceID, supervisorToken string,
) (*Supervisor, error) {
	if processID == "" || supervisorInstanceID == "" || supervisorToken == "" {
		return nil, errors.New("process, supervisor instance ID, and supervisor token are required")
	}
	store, err := open(ctx, path, installationID, machineID, false)
	if err != nil {
		return nil, err
	}
	process, found, err := store.Process(ctx, processID)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	if !found || process.SupervisorInstanceID != supervisorInstanceID || process.SupervisorToken != supervisorToken {
		_ = store.Close()
		return nil, fmt.Errorf("%w: %s", ErrSupervisorIdentityMismatch, processID)
	}
	return &Supervisor{
		store:                store,
		processID:            processID,
		supervisorInstanceID: supervisorInstanceID,
	}, nil
}

func open(
	ctx context.Context,
	path, installationID, machineID string,
	mainDaemon bool,
) (*Store, error) {
	if path == "" || installationID == "" || machineID == "" {
		return nil, errors.New("state database path, installation, and machine are required")
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("state database path must be absolute")
	}
	existed := true
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		existed = false
		if !mainDaemon {
			return nil, os.ErrNotExist
		}
		if err := localstore.EnsurePrivateDir(filepath.Dir(path)); err != nil {
			return nil, fmt.Errorf("prepare state database directory: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("inspect state database: %w", err)
	} else if err := localstore.ValidatePrivateFile(path); err != nil {
		return nil, fmt.Errorf("validate state database: %w", err)
	}

	db, err := sql.Open("sqlite", stateDSN(path, !mainDaemon))
	if err != nil {
		return nil, fmt.Errorf("open state database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	failed := true
	defer func() {
		if failed {
			_ = db.Close()
		}
	}()
	if err := verifyPragmas(ctx, db); err != nil {
		return nil, err
	}
	if !existed {
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("secure state database: %w", err)
		}
		if err := localstore.ValidatePrivateFile(path); err != nil {
			return nil, fmt.Errorf("validate new state database: %w", err)
		}
	}

	if mainDaemon {
		if err := verifyExistingStateDatabase(
			ctx,
			db,
			installationID,
			machineID,
		); err != nil {
			return nil, err
		}
		if err := applyEmbeddedMigrations(ctx, db); err != nil {
			return nil, err
		}
		if err := bindOrVerifyIdentity(
			ctx,
			db,
			installationID,
			machineID,
		); err != nil {
			return nil, err
		}
		if err := localstore.SyncDir(filepath.Dir(path)); err != nil {
			return nil, fmt.Errorf("sync state database directory: %w", err)
		}
	} else if err := verifyIdentity(
		ctx,
		db,
		installationID,
		machineID,
	); err != nil {
		return nil, err
	}
	store := &Store{
		db: db,
		q:  dbsqlc.New(db),
	}
	if mainDaemon {
		if err := store.Audit(ctx); err != nil {
			return nil, err
		}
	}
	failed = false
	return store, nil
}

func verifyExistingStateDatabase(
	ctx context.Context,
	db *sql.DB,
	installationID, machineID string,
) error {
	var tableCount int
	var identityTable, gooseTable bool
	if err := db.QueryRowContext(
		ctx,
		`SELECT (
		     SELECT count(*)
		     FROM sqlite_schema
		     WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		   ),
		   EXISTS (
		     SELECT 1
		     FROM sqlite_schema
		     WHERE type = 'table' AND name = 'machine_identity'
		   ),
		   EXISTS (
		     SELECT 1
		     FROM sqlite_schema
		     WHERE type = 'table' AND name = 'goose_db_version'
		   )`,
	).Scan(&tableCount, &identityTable, &gooseTable); err != nil {
		return dbError("inspect state database schema", err)
	}
	if !identityTable {
		if tableCount == 0 || (tableCount == 1 && gooseTable) {
			return nil
		}
		return errors.New("state database has an unrecognized non-empty schema")
	}
	if !gooseTable {
		return errors.New("state database has an unrecognized non-empty schema")
	}

	storedInstallation, storedMachine, found, err := readIdentity(
		ctx,
		dbsqlc.New(db),
	)
	if err != nil {
		return dbError("read state database identity", err)
	}
	if !found {
		return nil
	}
	return verifyIdentityValues(
		storedInstallation,
		storedMachine,
		installationID,
		machineID,
	)
}

func bindOrVerifyIdentity(
	ctx context.Context,
	db *sql.DB,
	installationID, machineID string,
) error {
	tx, err := beginWrite(ctx, db)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	qtx := dbsqlc.New(tx)
	storedInstallation, storedMachine, found, err := readIdentity(ctx, qtx)
	if err != nil {
		return dbError("read state database identity", err)
	}
	if !found {
		if err := qtx.BindMachineIdentity(
			ctx,
			dbsqlc.BindMachineIdentityParams{
				InstallationID: installationID,
				MachineID:      machineID,
			},
		); err != nil {
			return dbError("bind state database identity", err)
		}
	} else if err := verifyIdentityValues(
		storedInstallation,
		storedMachine,
		installationID,
		machineID,
	); err != nil {
		return err
	}
	if err := commitWrite(tx); err != nil {
		return err
	}
	return nil
}

func verifyIdentity(
	ctx context.Context,
	db *sql.DB,
	installationID, machineID string,
) error {
	storedInstallation, storedMachine, found, err := readIdentity(
		ctx,
		dbsqlc.New(db),
	)
	if err != nil {
		return dbError("read state database identity", err)
	}
	if !found {
		return errors.New("state database has no identity binding")
	}
	return verifyIdentityValues(
		storedInstallation,
		storedMachine,
		installationID,
		machineID,
	)
}

func readIdentity(
	ctx context.Context,
	q *dbsqlc.Queries,
) (
	installationID, machineID string,
	found bool,
	err error,
) {
	row, err := q.GetMachineIdentity(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", false, nil
		}
		return "", "", false, err
	}
	return row.InstallationID, row.MachineID, true, nil
}

func verifyIdentityValues(
	storedInstallation, storedMachine,
	installationID, machineID string,
) error {
	if storedInstallation == installationID && storedMachine == machineID {
		return nil
	}
	return fmt.Errorf(
		"state database identity mismatch: have %q/%q, want %q/%q",
		storedInstallation,
		storedMachine,
		installationID,
		machineID,
	)
}

func verifyPragmas(ctx context.Context, db *sql.DB) error {
	expected := map[string]int{
		"busy_timeout":   5000,
		"foreign_keys":   1,
		"synchronous":    2,
		"trusted_schema": 0,
	}
	if runtime.GOOS == "darwin" {
		expected["fullfsync"] = 1
		expected["checkpoint_fullfsync"] = 1
	}
	for pragma, want := range expected {
		var got int
		if err := db.QueryRowContext(
			ctx,
			`PRAGMA `+pragma,
		).Scan(&got); err != nil {
			return dbError("read state database pragma "+pragma, err)
		}
		if got != want {
			return fmt.Errorf(
				"state database pragma %s=%d, want %d",
				pragma,
				got,
				want,
			)
		}
	}
	var journal string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journal); err != nil {
		return dbError("read state database journal mode", err)
	}
	if !strings.EqualFold(journal, "wal") {
		return fmt.Errorf("state database journal mode %q, want WAL", journal)
	}
	return nil
}

func stateDSN(path string, existingOnly bool) string {
	slashPath := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && len(slashPath) >= 2 && slashPath[1] == ':' {
		slashPath = "/" + slashPath
	}
	dsn := url.URL{Scheme: "file", Path: slashPath}
	query := url.Values{}
	if existingOnly {
		query.Set("mode", "rw")
	}
	query.Set("_txlock", "immediate")
	for _, pragma := range []string{
		"busy_timeout=5000",
		"foreign_keys=ON",
		"trusted_schema=OFF",
		"journal_mode=WAL",
		"synchronous=FULL",
	} {
		query.Add("_pragma", pragma)
	}
	if runtime.GOOS == "darwin" {
		query.Add("_pragma", "fullfsync=ON")
		query.Add("_pragma", "checkpoint_fullfsync=ON")
	}
	dsn.RawQuery = query.Encode()
	return dsn.String()
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Supervisor) Close() error {
	if s == nil {
		return nil
	}
	return s.store.Close()
}

func newReportID() string {
	return "rpt_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func beginWrite(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, dbError("begin state database write", err)
	}
	return tx, nil
}

func commitWrite(tx *sql.Tx) error {
	if err := tx.Commit(); err != nil {
		return dbError("commit state database write", err)
	}
	return nil
}

func dbError(operation string, err error) error {
	if err == nil {
		return nil
	}
	var sqliteErr *sqlite3.Error
	if errors.As(err, &sqliteErr) {
		code := sqliteErr.Code() & 0xff
		if code == sqlite3lib.SQLITE_FULL {
			return fmt.Errorf("%s: %w: %w", operation, ErrFull, err)
		}
		if code == sqlite3lib.SQLITE_BUSY || code == sqlite3lib.SQLITE_LOCKED {
			return fmt.Errorf("%s: %w: %w", operation, ErrBusy, err)
		}
	}
	return fmt.Errorf("%s: %w", operation, err)
}
