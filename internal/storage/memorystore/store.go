package memorystore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/skills"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/memoryops"
	"github.com/omnara-ai/omnara/internal/storage/internal/resourceguard"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type Access interface {
	AuthorizeProject(context.Context, identitystore.AuthorizeProjectInput) (bool, error)
}

type Store struct {
	pool   *pgxpool.Pool
	q      *dbsqlc.Queries
	files  *Filesystem
	access Access
}

func New(pool *pgxpool.Pool, files *Filesystem, access Access) *Store {
	return &Store{pool: pool, q: dbsqlc.New(pool), files: files, access: access}
}

type Scope struct {
	OrgID     uuid.UUID
	ProjectID uuid.UUID
	AgentID   uuid.UUID
	Principal identitystore.PrincipalRecord
}

type Record struct {
	ID          uuid.UUID
	ProjectID   uuid.UUID
	Name        string
	Description string
	ReadOnly    bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func record(r dbsqlc.MemoryStore) Record {
	return Record{
		ID: r.ID, ProjectID: r.ProjectID, Name: r.Name, Description: r.Description,
		ReadOnly: r.ReadOnly, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func mapped(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return storeerr.ErrNotFound
	}
	if storeutil.IsUniqueViolation(err) {
		return storeerr.ErrConflict
	}
	return err
}

func (s *Store) authorize(ctx context.Context, scope Scope, manage bool) error {
	if scope.AgentID != uuid.Nil {
		return storeerr.ErrUnauthorized
	}
	action := identitystore.ProjectActionRead
	if manage {
		action = identitystore.ProjectActionManage
	}
	allowed, err := s.access.AuthorizeProject(
		ctx,
		identitystore.AuthorizeProjectInput{
			Principal: scope.Principal,
			OrgID:     scope.OrgID,
			ProjectID: scope.ProjectID,
			Action:    action,
		})
	if err != nil {
		return fmt.Errorf("authorize memory store: %w", err)
	}
	if !allowed {
		return storeerr.ErrNotFound
	}
	return nil
}

func (s *Store) Create(ctx context.Context, scope Scope, name, description string, readOnly bool) (Record, error) {
	if err := s.authorize(ctx, scope, true); err != nil {
		return Record{}, fmt.Errorf("create memory store: %w", err)
	}
	if err := skills.ValidateName(name); err != nil {
		return Record{}, storeerr.InvalidRequest(err)
	}
	if err := dbsafe.Text(description); err != nil {
		return Record{}, storeerr.InvalidRequest(err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Record{}, fmt.Errorf("generate memory store id: %w", err)
	}
	ref, err := memoryops.NewStoreRef(scope.OrgID, scope.ProjectID, id, name)
	if err != nil {
		return Record{}, err
	}
	lock, err := s.files.Lock(ctx, ref)
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = lock.Close() }()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Record{}, fmt.Errorf("create memory store: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lifecyclelock.EnterActiveProject(ctx, tx, scope.OrgID, scope.ProjectID); err != nil {
		return Record{}, err
	}
	q := s.q.WithTx(tx)
	if err = resourceguard.Lock(ctx, q, "memory_stores", scope.ProjectID.String()); err != nil {
		return Record{}, fmt.Errorf("create memory store: %w", err)
	}
	limits, err := resourceguard.ResolveLimits(ctx, q, scope.OrgID)
	if err != nil {
		return Record{}, fmt.Errorf("create memory store: %w", err)
	}
	count, err := q.CountMemoryStores(ctx, dbsqlc.CountMemoryStoresParams{ProjectID: scope.ProjectID})
	if err != nil {
		return Record{}, fmt.Errorf("create memory store: %w", err)
	}
	if count >= limits.MaxActiveMemoryStoresPerProject {
		return Record{}, fmt.Errorf("memory store limit reached: %w", storeerr.ErrConflict)
	}
	r, err := q.CreateMemoryStore(ctx, dbsqlc.CreateMemoryStoreParams{
		ID:          id,
		ProjectID:   scope.ProjectID,
		Name:        name,
		Description: description,
		ReadOnly:    readOnly,
	})
	if err != nil {
		return Record{}, fmt.Errorf("create memory store: %w", mapped(err))
	}
	if err := s.files.RemoveStore(ref); err != nil {
		return Record{}, fmt.Errorf("prepare memory store: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return Record{}, fmt.Errorf("create memory store: %w", err)
	}
	return record(r), nil
}

func (s *Store) Get(ctx context.Context, scope Scope, id uuid.UUID) (Record, error) {
	if err := s.authorize(ctx, scope, false); err != nil {
		return Record{}, err
	}
	r, err := s.q.GetMemoryStore(ctx, dbsqlc.GetMemoryStoreParams{ProjectID: scope.ProjectID, ID: id})
	if err != nil {
		return Record{}, fmt.Errorf("get memory store: %w", mapped(err))
	}
	return record(r), nil
}

func (s *Store) Resolve(ctx context.Context, projectID uuid.UUID, name string) (Record, error) {
	r, err := s.q.GetMemoryStoreByName(ctx, dbsqlc.GetMemoryStoreByNameParams{ProjectID: projectID, Name: name})
	if err != nil {
		return Record{}, fmt.Errorf("get memory store: %w", mapped(err))
	}
	return record(r), nil
}

type ListResult struct {
	Records []Record
	HasMore bool
	Next    listing.Cursor
}

func (s *Store) List(ctx context.Context, scope Scope, options listing.Options, limit int) (ListResult, error) {
	if err := s.authorize(ctx, scope, false); err != nil {
		return ListResult{}, fmt.Errorf("list memory stores: %w", err)
	}
	if options.After.Key != "" {
		if err := skills.ValidateName(options.After.Key); err != nil {
			return ListResult{}, storeerr.InvalidRequest(err)
		}
	}
	if limit < 1 || limit > 100 {
		return ListResult{}, storeerr.InvalidRequest(errors.New("invalid limit"))
	}
	rows, err := s.q.ListMemoryStores(ctx, dbsqlc.ListMemoryStoresParams{
		ProjectID:   scope.ProjectID,
		AfterName:   options.After.Key,
		NamePattern: options.NamePattern,
		RowLimit:    int32(limit + 1),
	})
	if err != nil {
		return ListResult{}, fmt.Errorf("list memory stores: %w", err)
	}
	out := ListResult{Records: make([]Record, 0, min(len(rows), limit))}
	if len(rows) > limit {
		out.HasMore, rows = true, rows[:limit]
	}
	for _, r := range rows {
		out.Records = append(out.Records, record(r))
		out.Next = listing.Cursor{Set: true, Key: r.Name, ID: r.ID}
	}
	return out, nil
}

func (s *Store) Update(
	ctx context.Context,
	scope Scope,
	id uuid.UUID,
	description *string,
	readOnly *bool,
) (Record, error) {
	if err := s.authorize(ctx, scope, true); err != nil {
		return Record{}, fmt.Errorf("update memory store: %w", err)
	}
	row, err := s.q.GetMemoryStore(ctx, dbsqlc.GetMemoryStoreParams{ProjectID: scope.ProjectID, ID: id})
	if err != nil {
		return Record{}, mapped(err)
	}
	ref, err := memoryops.NewStoreRef(scope.OrgID, scope.ProjectID, id, row.Name)
	if err != nil {
		return Record{}, err
	}
	lock, err := s.files.Lock(ctx, ref)
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = lock.Close() }()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Record{}, fmt.Errorf("update memory store: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lifecyclelock.EnterActiveProject(ctx, tx, scope.OrgID, scope.ProjectID); err != nil {
		return Record{}, err
	}
	q := s.q.WithTx(tx)
	r, err := q.LockMemoryStore(ctx, dbsqlc.LockMemoryStoreParams{ProjectID: scope.ProjectID, ID: id})
	if err != nil {
		return Record{}, fmt.Errorf("update memory store: %w", mapped(err))
	}
	if description != nil {
		if err := dbsafe.Text(*description); err != nil {
			return Record{}, storeerr.InvalidRequest(err)
		}
		r.Description = *description
	}
	if readOnly != nil {
		r.ReadOnly = *readOnly
	}
	r, err = q.UpdateMemoryStore(ctx, dbsqlc.UpdateMemoryStoreParams{
		ProjectID:   scope.ProjectID,
		ID:          id,
		Description: r.Description,
		ReadOnly:    r.ReadOnly,
	})
	if err != nil {
		return Record{}, fmt.Errorf("update memory store: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return Record{}, fmt.Errorf("update memory store: %w", err)
	}
	return record(r), nil
}

func (s *Store) authorizeAttachment(
	ctx context.Context,
	q *dbsqlc.Queries,
	scope Scope,
	storeID uuid.UUID,
	write bool,
) error {
	if scope.AgentID == uuid.Nil {
		return s.authorize(ctx, scope, write)
	}
	raw, err := q.GetAgentMemoryConfig(ctx, dbsqlc.GetAgentMemoryConfigParams{
		ProjectID: scope.ProjectID,
		AgentID:   scope.AgentID,
	})
	if err != nil {
		return fmt.Errorf("authorize memory file: %w", mapped(err))
	}
	var stores []agentconfig.MemoryStoreCompiled
	if err = json.Unmarshal(raw, &stores); err != nil {
		return fmt.Errorf("authorize memory file: %w", err)
	}
	for _, a := range stores {
		id, e := publicid.Decode(publicid.KindMemoryStore, a.PublicID)
		if e == nil && id == storeID && (!write || a.Access == "read_write") {
			return nil
		}
	}
	return storeerr.ErrNotFound
}

func (s *Store) Delete(ctx context.Context, scope Scope, id uuid.UUID) error {
	if err := s.authorize(ctx, scope, true); err != nil {
		return fmt.Errorf("delete memory store: %w", err)
	}
	row, err := s.q.GetMemoryStore(ctx, dbsqlc.GetMemoryStoreParams{ProjectID: scope.ProjectID, ID: id})
	if err != nil {
		return mapped(err)
	}
	ref, err := memoryops.NewStoreRef(scope.OrgID, scope.ProjectID, id, row.Name)
	if err != nil {
		return err
	}
	lock, err := s.files.Lock(ctx, ref)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("delete memory store: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lifecyclelock.EnterActiveProject(ctx, tx, scope.OrgID, scope.ProjectID); err != nil {
		return err
	}
	q := s.q.WithTx(tx)
	_, err = q.LockMemoryStore(ctx, dbsqlc.LockMemoryStoreParams{ProjectID: scope.ProjectID, ID: id})
	if err != nil {
		return fmt.Errorf("delete memory store: %w", mapped(err))
	}
	publicID, err := publicid.Encode(publicid.KindMemoryStore, id)
	if err != nil {
		return fmt.Errorf("delete memory store: %w", err)
	}
	used, err := q.MemoryStoreHasActiveReferences(ctx, dbsqlc.MemoryStoreHasActiveReferencesParams{
		ProjectID: scope.ProjectID,
		PublicID:  publicID,
	})
	if err != nil {
		return fmt.Errorf("delete memory store: %w", err)
	}
	if used {
		return fmt.Errorf("store is used by an active agent: %w", storeerr.ErrConflict)
	}
	if err = q.DeleteMemoryStore(ctx, dbsqlc.DeleteMemoryStoreParams{ProjectID: scope.ProjectID, ID: id}); err != nil {
		return fmt.Errorf("delete memory store: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("delete memory store: %w", err)
	}
	_ = s.files.RemoveStore(ref)
	return nil
}
