package skillstore

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	SkillOwnerOrg     = "org"
	SkillOwnerProject = "project"
	SkillOwnerUser    = "user"

	SkillAvailabilityDirect = "direct"
	SkillAvailabilityGrant  = "grant"

	MaxActiveSkillsPerOwner int64 = 1_000
)

type Access interface {
	AuthorizeOrg(context.Context, identitystore.AuthorizeOrgInput) (bool, error)
	AuthorizeProject(context.Context, identitystore.AuthorizeProjectInput) (bool, error)
	HasOrgMembership(ctx context.Context, principal identitystore.PrincipalRecord, orgID uuid.UUID) (bool, error)
	GetProject(ctx context.Context, orgID, projectID uuid.UUID) (identitystore.ProjectRecord, error)
}

type Store struct {
	pool   *pgxpool.Pool
	q      *dbsqlc.Queries
	blobs  blobstore.Store
	access Access
}

func New(pool *pgxpool.Pool, blobs blobstore.Store, access Access) *Store {
	return &Store{pool: pool, q: dbsqlc.New(pool), blobs: blobs, access: access}
}

type SkillRecord struct {
	ID             uuid.UUID `json:"id"`
	OrgID          uuid.UUID `json:"org_id"`
	OwnerKind      string    `json:"owner_kind"`
	OwnerProjectID uuid.UUID `json:"owner_project_id,omitempty"`
	OwnerUserID    uuid.UUID `json:"owner_user_id,omitempty"`
	Name           string    `json:"name"`
	RevisionID     uuid.UUID `json:"revision_id"`
	Revision       int32     `json:"revision"`
	Description    string    `json:"description"`
	SkillMd        string    `json:"skill_md"`
	ArchiveDigest  string    `json:"archive_digest"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type SkillGrantRecord struct {
	ID              uuid.UUID `json:"id"`
	OrgID           uuid.UUID `json:"org_id"`
	SkillID         uuid.UUID `json:"skill_id"`
	TargetProjectID uuid.UUID `json:"target_project_id"`
	CreatedAt       time.Time `json:"created_at"`
}

type SkillGrantListRecord struct {
	Grant         SkillGrantRecord
	TargetProject ProjectRecord
}

type ProjectRecord struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ProjectSkillAccessRecord struct {
	Skill        SkillRecord
	ProjectID    uuid.UUID
	Availability string
	GrantID      uuid.UUID
}

type CreateSkillInput struct {
	OrgID          uuid.UUID
	OwnerKind      string
	OwnerProjectID uuid.UUID
	OwnerUserID    uuid.UUID
	Name           string
	Description    string
	SkillMd        string
	ArchiveBytes   []byte
	Actor          identitystore.PrincipalRecord
}

type skillRevisionInsertResult struct {
	record                      SkillRecord
	databaseMayReferenceArchive bool
	retrySkillID                uuid.UUID
}

func invalidSkillRequest(format string, args ...any) error {
	detail := fmt.Sprintf(format, args...)
	return storeerr.InvalidRequest(fmt.Errorf("invalid skill request: %s", detail))
}

func isNilUUID(value uuid.UUID) bool {
	return value == uuid.Nil
}

func sqlcUUIDFromNil(value uuid.UUID) *uuid.UUID {
	if isNilUUID(value) {
		return nil
	}
	return &value
}

func uuidFromSQLCPtr(value *uuid.UUID) uuid.UUID {
	if value == nil {
		return uuid.Nil
	}
	return *value
}
