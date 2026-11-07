package secretops

import (
	"bytes"
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type Facts struct {
	ID             uuid.UUID
	OrgID          uuid.UUID
	ManagementKind management.Kind
	OwnerKind      string
	OwnerProjectID uuid.UUID
	OwnerUserID    uuid.UUID
	Kind           secrets.Kind
}

func LockReference(
	ctx context.Context,
	tx dbsqlc.DBTX,
	orgID, secretID uuid.UUID,
) (Facts, error) {
	q := dbsqlc.New(tx)
	if _, err := q.LockSecretForReference(
		ctx,
		dbsqlc.LockSecretForReferenceParams{OrgID: orgID, ID: secretID},
	); err != nil {
		return Facts{}, fmt.Errorf("lock secret for reference: %w", err)
	}
	return GetFacts(ctx, q, orgID, secretID)
}

func GetFacts(ctx context.Context, q *dbsqlc.Queries, orgID, secretID uuid.UUID) (Facts, error) {
	row, err := q.GetSecret(ctx, dbsqlc.GetSecretParams{OrgID: orgID, ID: secretID})
	if err != nil {
		return Facts{}, fmt.Errorf("get secret facts: %w", err)
	}
	return FactsFromGet(row), nil
}

func FactsFromGet(row dbsqlc.GetSecretRow) Facts {
	return Facts{
		ID:             row.ID,
		OrgID:          row.OrgID,
		ManagementKind: management.Kind(row.ManagementKind),
		OwnerKind:      row.OwnerKind,
		OwnerProjectID: storeutil.IDFromPtr(row.OwnerProjectID),
		OwnerUserID:    storeutil.IDFromPtr(row.OwnerUserID),
		Kind:           secrets.Kind(row.Kind),
	}
}

func FactsFromProjectAvailable(row dbsqlc.GetProjectAvailableSecretRow) Facts {
	return Facts{
		ID:             row.ID,
		OrgID:          row.OrgID,
		ManagementKind: management.Kind(row.ManagementKind),
		OwnerKind:      row.OwnerKind,
		OwnerProjectID: storeutil.IDFromPtr(row.OwnerProjectID),
		OwnerUserID:    storeutil.IDFromPtr(row.OwnerUserID),
		Kind:           secrets.Kind(row.Kind),
	}
}

// RequireUnreferenced checks the durable consumers of locked secret IDs. A scope
// teardown excludes only projects whose lifecycle gates it holds exclusively.
func RequireUnreferenced(ctx context.Context, q *dbsqlc.Queries, ids, excludedProjectIDs []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	referenced, err := q.ListReferencedSecretIDs(ctx, dbsqlc.ListReferencedSecretIDsParams{
		SecretIds: ids, ExcludedProjectIds: excludedProjectIDs,
	})
	if err != nil {
		return fmt.Errorf("check secret references: %w", err)
	}
	if len(referenced) > 0 {
		return fmt.Errorf("a secret is still referenced by a resource with access: %w", storeerr.ErrConflict)
	}
	return nil
}

// LockReferences acquires all secret rows in the same order used by owner
// deletion. The following reads revalidate liveness after any lock wait.
func LockReferences(
	ctx context.Context, tx dbsqlc.DBTX, orgID uuid.UUID, ids []uuid.UUID,
) (map[uuid.UUID]Facts, error) {
	ordered := slices.Clone(ids)
	slices.SortFunc(ordered, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	facts := make(map[uuid.UUID]Facts, len(ordered))
	for _, id := range slices.Compact(ordered) {
		fact, err := LockReference(ctx, tx, orgID, id)
		if err != nil {
			return nil, &ReferenceError{SecretID: id, Err: err}
		}
		facts[id] = fact
	}
	return facts, nil
}

// ReferenceError identifies the failed reference so the consumer can report its
// own configuration field without exposing secret identities or values.
type ReferenceError struct {
	SecretID uuid.UUID
	Err      error
}

func (err *ReferenceError) Error() string { return err.Err.Error() }

func (err *ReferenceError) Unwrap() error { return err.Err }

// DestroyUnreferencedVersionsForDeletedOrg preserves credentials still needed by
// pool teardown. Call within the teardown transaction after acquiring all
// lifecycle and consumer locks; no pool, machine, or agent locks may be acquired
// afterward. The organization is deleted, so new consumer admission is closed
// and teardown only releases refs.
// Serialize this final cleanup through commit so concurrent pools sharing one
// secret cannot each retain ciphertext after seeing the other's uncommitted ref.
// This advisory gate does not acquire late secret-row locks.
func DestroyUnreferencedVersionsForDeletedOrg(ctx context.Context, q *dbsqlc.Queries, orgID uuid.UUID) error {
	ids, err := q.ListSecretIDsForDeletedOrgCleanup(ctx, dbsqlc.ListSecretIDsForDeletedOrgCleanupParams{OrgID: orgID})
	if err != nil {
		return fmt.Errorf("list deleted organization secrets: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	if err := q.LockResourceCreation(ctx, dbsqlc.LockResourceCreationParams{
		ResourceKind: "deleted_org_secret_cleanup", Scope: orgID.String(),
	}); err != nil {
		return fmt.Errorf("lock deleted organization secret cleanup: %w", err)
	}
	// Candidates can only disappear while waiting. Take the reference snapshot
	// after the gate so the preceding cleanup's committed releases are visible.
	referenced, err := q.ListReferencedSecretIDs(ctx, dbsqlc.ListReferencedSecretIDsParams{SecretIds: ids})
	if err != nil {
		return fmt.Errorf("check deleted organization secret references: %w", err)
	}
	retained := make(map[uuid.UUID]bool, len(referenced))
	for _, id := range referenced {
		retained[id] = true
	}
	unused := slices.DeleteFunc(ids, func(id uuid.UUID) bool { return retained[id] })
	if len(unused) == 0 {
		return nil
	}
	if err := q.DeleteSecretVersionsForSecrets(ctx, dbsqlc.DeleteSecretVersionsForSecretsParams{
		SecretIds: unused,
	}); err != nil {
		return fmt.Errorf("destroy unreferenced secret versions: %w", err)
	}
	return nil
}
