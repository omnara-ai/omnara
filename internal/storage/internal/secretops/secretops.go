// Package secretops coordinates secret references and encrypted-data retention
// across storage domains.
//
// Operations require a caller-owned READ COMMITTED transaction. Pass its database
// handle (including an execution Unit's DB), never a pool: locks must survive
// through the caller's mutations and later statements must see committed changes
// made while waiting for those locks.
package secretops

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/management"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type Facts struct {
	ManagementKind management.Kind
	OwnerKind      string
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
	row, err := q.GetSecret(ctx, dbsqlc.GetSecretParams{OrgID: orgID, ID: secretID})
	if err != nil {
		return Facts{}, fmt.Errorf("get secret facts: %w", err)
	}
	return FactsFromGet(row), nil
}

func FactsFromGet(row dbsqlc.GetSecretRow) Facts {
	return Facts{
		ManagementKind: management.Kind(row.ManagementKind),
		OwnerKind:      row.OwnerKind,
		Kind:           secrets.Kind(row.Kind),
	}
}

func FactsFromProjectAvailable(row dbsqlc.GetProjectAvailableSecretRow) Facts {
	return Facts{
		ManagementKind: management.Kind(row.ManagementKind),
		OwnerKind:      row.OwnerKind,
		Kind:           secrets.Kind(row.Kind),
	}
}

// RequireUnreferenced checks durable consumers after the transaction has locked
// the candidate secrets against reference admission and grant changes.
func RequireUnreferenced(ctx context.Context, tx dbsqlc.DBTX, ids []uuid.UUID) error {
	return requireUnreferenced(ctx, tx, ids, nil)
}

// LockProjectSecretsForDeletion is called by the execution Unit after it verifies
// the project's exclusive lifecycle gate. The project's own consumers retire in
// that transaction; consumers in other projects must still prevent deletion.
func LockProjectSecretsForDeletion(ctx context.Context, tx dbsqlc.DBTX, orgID, projectID uuid.UUID) error {
	ids, err := dbsqlc.New(tx).LockProjectOwnedSecretsForDeletion(ctx, dbsqlc.LockProjectOwnedSecretsForDeletionParams{
		OrgID: orgID, ProjectID: projectID,
	})
	if err != nil {
		return fmt.Errorf("lock project secrets for deletion: %w", err)
	}
	return requireUnreferenced(ctx, tx, ids, []uuid.UUID{projectID})
}

func requireUnreferenced(ctx context.Context, tx dbsqlc.DBTX, ids, excludedProjectIDs []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	referenced, err := dbsqlc.New(tx).ListReferencedSecretIDs(ctx, dbsqlc.ListReferencedSecretIDsParams{
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

// DestroyUnreferencedVersionsForDeletedOrg runs at teardown commit, after all
// consumer mutations. Deleted organizations cannot admit new consumers; teardown
// only releases references. Serializing cleanup through commit prevents two pools
// from each retaining ciphertext because the other's release is uncommitted.
// The advisory gate avoids acquiring secret-row locks after consumer locks.
func DestroyUnreferencedVersionsForDeletedOrg(ctx context.Context, tx dbsqlc.DBTX, orgID uuid.UUID) error {
	q := dbsqlc.New(tx)
	ids, err := q.ListSecretIDsForDeletedOrgCleanup(ctx, dbsqlc.ListSecretIDsForDeletedOrgCleanupParams{OrgID: orgID})
	if err != nil {
		return fmt.Errorf("list deleted organization secrets: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	if err := q.LockDeletedOrganizationSecretCleanup(ctx,
		dbsqlc.LockDeletedOrganizationSecretCleanupParams{OrgID: orgID}); err != nil {
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
