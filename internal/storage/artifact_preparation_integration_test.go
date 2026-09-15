//go:build integration

package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func artifactPreparationFixture(t *testing.T) (*Store, *recordingBlobStore, artifactstore.CreateArtifactInput) {
	t.Helper()
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	blobs := newRecordingBlobStore()
	store := newIntegrationStore(pool, WithBlobStore(blobs))
	return store, blobs, artifactstore.CreateArtifactInput{
		ProjectID: testProjectID, AgentID: mustCreateAgent(t, ctx, store),
		ContentType: "image/png", Filename: "attachment.png", Content: []byte("original image"),
		IdempotencyKey: "receipt:attachment:0", MaxBytes: 1024,
	}
}

func TestPreparedArtifactComposesWithUncommittedAgent(t *testing.T) {
	t.Parallel()
	for _, outcome := range []artifactstore.ArtifactTransactionOutcome{
		artifactstore.ArtifactTransactionCommitted,
		artifactstore.ArtifactTransactionRolledBack,
		artifactstore.ArtifactTransactionUnknown,
	} {
		name := map[artifactstore.ArtifactTransactionOutcome]string{
			artifactstore.ArtifactTransactionCommitted:  "commit",
			artifactstore.ArtifactTransactionRolledBack: "rollback",
			artifactstore.ArtifactTransactionUnknown:    "commit acknowledgement lost",
		}[outcome]
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			pool := openIntegrationDB(t, ctx)
			seedMigratedDB(t, ctx, pool)
			blobs := newRecordingBlobStore()
			store := newIntegrationStore(pool, WithBlobStore(blobs))
			configID := mustCreateAgentConfig(t, ctx, store, testProjectID)
			agentID, err := uuid.NewV7()
			require.NoError(t, err)
			input := artifactstore.CreateArtifactInput{
				ProjectID: testProjectID, AgentID: agentID, ContentType: "image/png",
				Filename: "first.png", Content: []byte("first input media"), IdempotencyKey: "first-media",
			}
			blobs.afterPut = func() {
				var count int
				require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM agents WHERE id = $1`, agentID).Scan(&count))
				require.Zero(t, count, "upload must precede creation of the agent")
			}
			prepared, err := store.Artifacts().PrepareArtifact(ctx, input)
			require.NoError(t, err)
			require.Len(t, blobs.putKeys, 1)
			tx := integrationdb.BeginTx(t, ctx, pool)
			// Exercise the existing insertion query with a canonical model/config
			// fixture. Full launch admission belongs to the execution-store slice.
			_, err = dbsqlc.New(tx).InsertAgent(ctx, dbsqlc.InsertAgentParams{
				ID: &agentID, OrgID: testOrgID, ProjectID: testProjectID, CurrentConfigID: configID,
			})
			require.NoError(t, err)
			record, err := store.Artifacts().PersistPreparedArtifact(ctx, tx, prepared)
			require.NoError(t, err)
			require.True(t, record.Created)
			require.Equal(t, agentID, record.AgentID)
			require.Equal(t, artifactObjectKey(agentID, record.ID), blobs.putKeys[0])
			var inside, outside int
			require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM artifacts WHERE id = $1`, record.ID).Scan(&inside))
			require.Equal(t, 1, inside)
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM artifacts WHERE id = $1`, record.ID).Scan(&outside))
			require.Zero(t, outside, "persist must not commit the caller's transaction")
			require.Len(t, blobs.putKeys, 1, "persist must not upload")
			require.Empty(t, blobs.deleteKeys, "persist must not compensate")
			if outcome == artifactstore.ArtifactTransactionRolledBack {
				require.NoError(t, tx.Rollback(ctx))
			} else {
				require.NoError(t, tx.Commit(ctx))
			}
			require.NoError(t, store.Artifacts().FinishPreparedArtifacts(ctx, outcome, prepared))
			require.NoError(t, store.Artifacts().FinishPreparedArtifacts(ctx, outcome, prepared))
			var agents, artifacts int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM agents WHERE id = $1`, agentID).Scan(&agents))
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM artifacts WHERE id = $1`, record.ID).Scan(&artifacts))
			if outcome == artifactstore.ArtifactTransactionRolledBack {
				require.Zero(t, agents)
				require.Zero(t, artifacts)
				require.Equal(t, blobs.putKeys, blobs.deleteKeys)
				require.Empty(t, blobs.content)
			} else {
				require.Equal(t, 1, agents)
				require.Equal(t, 1, artifacts)
				require.Empty(t, blobs.deleteKeys)
				content, persisted, err := store.Artifacts().GetArtifactBlob(ctx, testProjectID, agentID, record.ID)
				require.NoError(t, err)
				require.Equal(t, input.Content, content)
				require.Equal(t, blobstore.ContentDigest(input.Content), persisted.Digest)
			}
		})
	}
}

func TestPreparedArtifactConcurrentReplayReturnsCanonicalID(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	store, blobs, input := artifactPreparationFixture(t)
	first, err := store.Artifacts().PrepareArtifact(ctx, input)
	require.NoError(t, err)
	second, err := store.Artifacts().PrepareArtifact(ctx, input)
	require.NoError(t, err)
	firstTx := integrationdb.BeginTx(t, ctx, store.pool)
	canonical, err := store.Artifacts().PersistPreparedArtifact(ctx, firstTx, first)
	require.NoError(t, err)
	require.True(t, canonical.Created)
	secondTx := integrationdb.BeginTx(t, ctx, store.pool)
	type result struct {
		record artifactstore.ArtifactRecord
		err    error
	}
	done := make(chan result, 1)
	go func() {
		record, err := store.Artifacts().PersistPreparedArtifact(ctx, secondTx, second)
		done <- result{record: record, err: err}
	}()
	integrationdb.WaitForNamedLockWaiters(t, ctx, store.pool, "LockAgentInProject", 1)
	select {
	case got := <-done:
		t.Fatalf("replay returned before first writer settled: %+v", got)
	default:
	}
	require.NoError(t, firstTx.Commit(ctx))
	var replay result
	select {
	case replay = <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.NoError(t, replay.err)
	require.False(t, replay.record.Created)
	require.Equal(t, canonical.ID, replay.record.ID)
	require.Equal(t, canonical.Digest, replay.record.Digest)
	require.Empty(t, blobs.deleteKeys, "replay cleanup belongs to the caller after commit")
	require.NoError(t, secondTx.Commit(ctx))
	require.NoError(t,
		store.Artifacts().FinishPreparedArtifacts(ctx, artifactstore.ArtifactTransactionCommitted, first),
	)
	require.NoError(t,
		store.Artifacts().FinishPreparedArtifacts(ctx, artifactstore.ArtifactTransactionCommitted, second),
	)
	require.Equal(t, []string{blobs.putKeys[1]}, blobs.deleteKeys)
	require.Len(t, blobs.content, 1)
	content, _, err := store.Artifacts().GetArtifactBlob(ctx, input.ProjectID, input.AgentID, replay.record.ID)
	require.NoError(t, err)
	require.Equal(t, input.Content, content)
}

func TestPreparedArtifactRejectsConflictingReplay(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"digest", "content type", "filename", "size"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			store, blobs, input := artifactPreparationFixture(t)
			canonical, err := store.Artifacts().CreateArtifact(ctx, input)
			require.NoError(t, err)
			switch field {
			case "digest":
				input.Content = []byte("different image")
			case "content type":
				input.ContentType = "image/jpeg"
			case "filename":
				input.Filename = "different.png"
			case "size":
				// An inconsistent retained size must not be accepted just because
				// the caller reuses the digest and idempotency key.
				_, err = store.pool.Exec(ctx, `UPDATE artifacts SET size_bytes = size_bytes + 1 WHERE id = $1`, canonical.ID)
				require.NoError(t, err)
			}
			prepared, err := store.Artifacts().PrepareArtifact(ctx, input)
			require.NoError(t, err)
			tx := integrationdb.BeginTx(t, ctx, store.pool)
			_, err = store.Artifacts().PersistPreparedArtifact(ctx, tx, prepared)
			require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
			require.Empty(t, blobs.deleteKeys)
			require.NoError(t, tx.Rollback(ctx))
			require.NoError(t,
				store.Artifacts().FinishPreparedArtifacts(ctx, artifactstore.ArtifactTransactionRolledBack, prepared),
			)
			require.Equal(t, []string{blobs.putKeys[1]}, blobs.deleteKeys)
			require.Len(t, blobs.content, 1)
			retained, err := store.Artifacts().GetArtifact(ctx, input.ProjectID, input.AgentID, canonical.ID)
			require.NoError(t, err)
			require.Equal(t, canonical.Digest, retained.Digest)
			require.Equal(t, canonical.Filename, retained.Filename)
			require.Equal(t, canonical.ContentType, retained.ContentType)
		})
	}
}

func TestPreparedArtifactRequiresLiveScopedAgentAtPersistence(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"missing agent", "wrong project", "archived agent replay"} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			store, blobs, input := artifactPreparationFixture(t)
			wantErr := storeerr.ErrNotFound
			if scope == "missing agent" {
				input.AgentID = testID("uncreated-prepared-agent")
			} else if scope == "wrong project" {
				input.ProjectID = testID("different-artifact-project")
			}
			var canonical artifactstore.ArtifactRecord
			if scope == "archived agent replay" {
				var err error
				canonical, err = store.Artifacts().CreateArtifact(ctx, input)
				require.NoError(t, err)
				wantErr = storeerr.ErrStateTransitionConflict
			}
			prepared, err := store.Artifacts().PrepareArtifact(ctx, input)
			require.NoError(t, err, "preparation must not grant or require DB authority")
			if scope == "archived agent replay" {
				user := mustCreateProjectOperatorUser(t, ctx, store, "prepare-archive@example.com", "Prepare Archive")
				_, _, err := store.Execution().ArchiveAgent(ctx, input.ProjectID, input.AgentID, userPrincipal(user.ID))
				require.NoError(t, err)
			}
			tx := integrationdb.BeginTx(t, ctx, store.pool)
			_, err = store.Artifacts().PersistPreparedArtifact(ctx, tx, prepared)
			require.ErrorIs(t, err, wantErr)
			require.NoError(t, tx.Rollback(ctx))
			require.NoError(t,
				store.Artifacts().FinishPreparedArtifacts(ctx, artifactstore.ArtifactTransactionRolledBack, prepared),
			)
			require.Equal(t, []string{blobs.putKeys[len(blobs.putKeys)-1]}, blobs.deleteKeys)
			if scope == "archived agent replay" {
				replayed, err := store.Artifacts().CreateArtifact(ctx, input)
				require.NoError(t, err, "standalone historical replay remains supported")
				require.Equal(t, canonical.ID, replayed.ID)
				require.False(t, replayed.Created)
			}
		})
	}
}

func TestPreparedArtifactOuterRollbackOwnsWholeBatch(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store, blobs, input := artifactPreparationFixture(t)
	canonical, err := store.Artifacts().CreateArtifact(ctx, input)
	require.NoError(t, err)
	replay, err := store.Artifacts().PrepareArtifact(ctx, input)
	require.NoError(t, err)
	input.IdempotencyKey = "receipt:attachment:1"
	created, err := store.Artifacts().PrepareArtifact(ctx, input)
	require.NoError(t, err)
	input.IdempotencyKey = "receipt:attachment:2"
	unused, err := store.Artifacts().PrepareArtifact(ctx, input)
	require.NoError(t, err)
	tx := integrationdb.BeginTx(t, ctx, store.pool)
	replayed, err := store.Artifacts().PersistPreparedArtifact(ctx, tx, replay)
	require.NoError(t, err)
	require.Equal(t, canonical.ID, replayed.ID)
	require.False(t, replayed.Created)
	inserted, err := store.Artifacts().PersistPreparedArtifact(ctx, tx, created)
	require.NoError(t, err)
	require.True(t, inserted.Created)
	require.Empty(t, blobs.deleteKeys)
	require.NoError(t, tx.Rollback(ctx))
	// Compensation must neither depend on the request context nor run under
	// the agent lock held during persistence.
	blobs.beforeDelete = func() {
		lockCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		check := integrationdb.BeginTx(t, lockCtx, store.pool)
		_, err := dbsqlc.New(check).LockAgentInProject(lockCtx, dbsqlc.LockAgentInProjectParams{
			ProjectID: input.ProjectID, ID: input.AgentID,
		})
		require.NoError(t, err)
		require.NoError(t, check.Rollback(lockCtx))
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	batch := []*artifactstore.PreparedArtifact{replay, created, unused}
	require.NoError(t,
		store.Artifacts().FinishPreparedArtifacts(canceled, artifactstore.ArtifactTransactionRolledBack, batch...),
	)
	require.NoError(t,
		store.Artifacts().FinishPreparedArtifacts(canceled, artifactstore.ArtifactTransactionRolledBack, batch...),
	)
	require.Equal(t, blobs.putKeys[1:], blobs.deleteKeys)
	for _, err := range blobs.deleteContextErrors {
		require.NoError(t, err)
	}
	require.Len(t, blobs.content, 1)
	_, err = store.Artifacts().GetArtifact(ctx, input.ProjectID, input.AgentID, inserted.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	content, _, err := store.Artifacts().GetArtifactBlob(ctx, input.ProjectID, input.AgentID, canonical.ID)
	require.NoError(t, err)
	require.Equal(t, input.Content, content)
}

func TestPreparedArtifactUnknownOutcomeRetainsEntireBatch(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store, blobs, input := artifactPreparationFixture(t)
	canonical, err := store.Artifacts().CreateArtifact(ctx, input)
	require.NoError(t, err)
	replay, err := store.Artifacts().PrepareArtifact(ctx, input)
	require.NoError(t, err)
	input.IdempotencyKey = "unknown-new"
	created, err := store.Artifacts().PrepareArtifact(ctx, input)
	require.NoError(t, err)
	unused, err := store.Artifacts().PrepareArtifact(ctx, input)
	require.NoError(t, err)
	tx := integrationdb.BeginTx(t, ctx, store.pool)
	replayed, err := store.Artifacts().PersistPreparedArtifact(ctx, tx, replay)
	require.NoError(t, err)
	require.Equal(t, canonical.ID, replayed.ID)
	inserted, err := store.Artifacts().PersistPreparedArtifact(ctx, tx, created)
	require.NoError(t, err)
	// Model a server-side commit whose acknowledgement the caller never sees.
	require.NoError(t, tx.Commit(ctx))
	batch := []*artifactstore.PreparedArtifact{replay, created, unused}
	require.NoError(t,
		store.Artifacts().FinishPreparedArtifacts(ctx, artifactstore.ArtifactTransactionUnknown, batch...),
	)
	require.NoError(t,
		store.Artifacts().FinishPreparedArtifacts(ctx, artifactstore.ArtifactTransactionUnknown, batch...),
	)
	require.ErrorIs(t,
		store.Artifacts().FinishPreparedArtifacts(ctx, artifactstore.ArtifactTransactionRolledBack, batch...),
		storeerr.ErrInvalidRequest,
	)
	require.Empty(t, blobs.deleteKeys)
	require.Len(t, blobs.content, 4, "even replay and unused uploads must survive an ambiguous outcome")
	_, err = store.Artifacts().GetArtifact(ctx, input.ProjectID, input.AgentID, inserted.ID)
	require.NoError(t, err)
}

func TestPreparedArtifactCommittedBatchCleansOnlyUnusedUploads(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store, blobs, input := artifactPreparationFixture(t)
	created, err := store.Artifacts().PrepareArtifact(ctx, input)
	require.NoError(t, err)
	duplicate, err := store.Artifacts().PrepareArtifact(ctx, input)
	require.NoError(t, err)
	unused, err := store.Artifacts().PrepareArtifact(ctx, input)
	require.NoError(t, err)
	tx := integrationdb.BeginTx(t, ctx, store.pool)
	canonical, err := store.Artifacts().PersistPreparedArtifact(ctx, tx, created)
	require.NoError(t, err)
	require.True(t, canonical.Created)
	replay, err := store.Artifacts().PersistPreparedArtifact(ctx, tx, duplicate)
	require.NoError(t, err)
	require.False(t, replay.Created)
	require.Equal(t, canonical.ID, replay.ID, "in-transaction replay must also return the canonical ID")
	require.NoError(t, tx.Commit(ctx))
	batch := []*artifactstore.PreparedArtifact{created, duplicate, unused}
	require.NoError(t,
		store.Artifacts().FinishPreparedArtifacts(ctx, artifactstore.ArtifactTransactionCommitted, batch...),
	)
	require.NoError(t,
		store.Artifacts().FinishPreparedArtifacts(ctx, artifactstore.ArtifactTransactionCommitted, batch...),
	)
	require.Equal(t, blobs.putKeys[1:], blobs.deleteKeys)
	require.Len(t, blobs.content, 1)
	content, _, err := store.Artifacts().GetArtifactBlob(ctx, input.ProjectID, input.AgentID, canonical.ID)
	require.NoError(t, err)
	require.Equal(t, input.Content, content)
}

func TestPreparedArtifactCleanupRetryAndOwnership(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store, blobs, input := artifactPreparationFixture(t)
	prepared, err := store.Artifacts().PrepareArtifact(ctx, input)
	require.NoError(t, err)
	otherStore := artifactstore.New(store.pool, blobs)
	tx := integrationdb.BeginTx(t, ctx, store.pool)
	_, err = otherStore.PersistPreparedArtifact(ctx, tx, prepared)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	record, err := store.Artifacts().PersistPreparedArtifact(ctx, tx, prepared)
	require.NoError(t, err)
	_, err = store.Artifacts().PersistPreparedArtifact(ctx, tx, prepared)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	require.NoError(t, tx.Rollback(ctx))
	require.ErrorIs(t,
		otherStore.FinishPreparedArtifacts(ctx, artifactstore.ArtifactTransactionRolledBack, prepared),
		storeerr.ErrInvalidRequest,
	)
	require.ErrorIs(t,
		store.Artifacts().FinishPreparedArtifacts(ctx, artifactstore.ArtifactTransactionOutcome(99), prepared),
		storeerr.ErrInvalidRequest,
	)
	require.Empty(t, blobs.deleteKeys)
	blobs.deleteErr = errors.New("temporary blob deletion failure")
	err = store.Artifacts().FinishPreparedArtifacts(ctx, artifactstore.ArtifactTransactionRolledBack, prepared)
	require.ErrorIs(t, err, blobs.deleteErr)
	require.Len(t, blobs.content, 1)
	blobs.deleteErr = nil
	require.NoError(t,
		store.Artifacts().FinishPreparedArtifacts(ctx, artifactstore.ArtifactTransactionRolledBack, prepared),
	)
	require.NoError(t,
		store.Artifacts().FinishPreparedArtifacts(ctx, artifactstore.ArtifactTransactionRolledBack, prepared),
	)
	require.Len(t, blobs.deleteKeys, 2, "only a failed cleanup is retried")
	require.Empty(t, blobs.content)
	nextTx := integrationdb.BeginTx(t, ctx, store.pool)
	_, err = store.Artifacts().PersistPreparedArtifact(ctx, nextTx, prepared)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	require.NoError(t, nextTx.Rollback(ctx))
	_, err = store.Artifacts().GetArtifact(ctx, input.ProjectID, input.AgentID, record.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
}

func artifactPreparationRuntime(
	t *testing.T,
	store *Store,
) (integrationstore.IntegrationInstallRecord, integrationstore.IntegrationRuntimeLeaseProof) {
	t.Helper()
	ctx := t.Context()
	user := mustCreateProjectRoleUser(t, ctx, store, "artifact-runtime@example.com", "Artifact Runtime", "admin")
	app, err := store.Integrations().CreateIntegrationApp(ctx, integrationstore.CreateIntegrationAppInput{
		OrgID: testOrgID, Provider: "discord", ConnectorKey: "chat_sdk_v1",
		ProviderAppRef: "artifact-runtime", DisplayName: "Artifact Runtime",
		State: integrationstore.IntegrationAppStateActive,
	})
	require.NoError(t, err)
	install, err := store.Integrations().UpsertIntegrationInstall(ctx, integrationstore.UpsertIntegrationInstallInput{
		OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: app.ID, InstalledBy: userPrincipal(user.ID),
		Provider: app.Provider, IntegrationKind: integrationstore.IntegrationKindManaged, ConnectionMode: "gateway",
		State: integrationstore.IntegrationInstallStateActive, ProviderTenantID: "artifact-tenant", ProviderAccountRef: "bot",
	})
	require.NoError(t, err)
	unit, err := store.Integrations().UpsertIntegrationRuntimeUnit(ctx, integrationstore.UpsertIntegrationRuntimeUnitInput{
		OrgID: testOrgID, IntegrationAppID: app.ID, ProjectID: testProjectID, IntegrationInstallID: install.ID,
		UnitKey: "artifact-runtime", RuntimeKind: "provider_socket",
		DesiredState: integrationstore.IntegrationRuntimeDesiredStateRunning, SpecRevision: 1,
	})
	require.NoError(t, err)
	claims, err := store.Integrations().ClaimIntegrationRuntimeUnits(
		ctx,
		integrationstore.ClaimIntegrationRuntimeUnitsInput{
			LeaseOwner: "artifact-preparation-test", LeaseDuration: time.Minute, Limit: 1,
			Capability: channelconnector.Capability{ConnectorKey: "chat_sdk_v1", Provider: app.Provider},
		},
	)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, unit.ID, claims[0].ID)
	return install, integrationstore.IntegrationRuntimeLeaseProof{
		IntegrationAppID: app.ID, UnitID: unit.ID,
		LeaseToken: claims[0].LeaseToken, LeaseGeneration: claims[0].LeaseGeneration,
	}
}

func TestPreparedArtifactRuntimeProofRevalidatedInsideTransaction(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{
		"valid copied proof", "wrong app", "wrong installation", "wrong project", "wrong token", "wrong generation",
		"expired after upload", "installation disabled after upload",
	} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			store, blobs, input := artifactPreparationFixture(t)
			install, proof := artifactPreparationRuntime(t, store)
			installID := install.ID
			switch scenario {
			case "wrong app":
				proof.IntegrationAppID = testID("wrong-artifact-runtime-app")
			case "wrong installation":
				other, err := store.Integrations().UpsertIntegrationInstall(ctx, integrationstore.UpsertIntegrationInstallInput{
					OrgID: testOrgID, ProjectID: testProjectID, IntegrationAppID: install.IntegrationAppID,
					InstalledBy: install.InstalledBy, Provider: install.Provider, IntegrationKind: integrationstore.IntegrationKindManaged,
					ConnectionMode: "gateway", State: integrationstore.IntegrationInstallStateActive,
					ProviderTenantID: "other-artifact-tenant", ProviderAccountRef: "bot",
				})
				require.NoError(t, err)
				installID = other.ID
			case "wrong project":
				input.ProjectID = testID("wrong-artifact-runtime-project")
			case "wrong token":
				proof.LeaseToken = testID("wrong-artifact-runtime-token")
			case "wrong generation":
				proof.LeaseGeneration++
			}
			prepared, err := store.Artifacts().PrepareArtifactWithIntegrationRuntimeLease(ctx, input, installID, &proof)
			require.NoError(t, err)
			require.Len(t, blobs.putKeys, 1)
			switch scenario {
			case "valid copied proof":
				proof.LeaseGeneration++
				proof.LeaseToken = testID("caller-mutated-proof")
			case "expired after upload":
				_, err := store.pool.Exec(ctx, `UPDATE integration_runtime_units
SET leased_at = statement_timestamp() - interval '3 minutes',
    renewed_at = statement_timestamp() - interval '2 minutes',
    lease_expires_at = statement_timestamp() - interval '1 minute'
WHERE id = $1`, proof.UnitID)
				require.NoError(t, err)
			case "installation disabled after upload":
				_, err := store.pool.Exec(ctx, `UPDATE integration_installs SET state = 'disabled' WHERE id = $1`, install.ID)
				require.NoError(t, err)
			}
			tx := integrationdb.BeginTx(t, ctx, store.pool)
			record, err := store.Artifacts().PersistPreparedArtifact(ctx, tx, prepared)
			if scenario == "valid copied proof" {
				require.NoError(t, err)
				require.NoError(t, tx.Commit(ctx))
				require.NoError(t,
					store.Artifacts().FinishPreparedArtifacts(ctx, artifactstore.ArtifactTransactionCommitted, prepared),
				)
				_, err := store.Artifacts().GetArtifact(ctx, input.ProjectID, input.AgentID, record.ID)
				require.NoError(t, err)
				require.Empty(t, blobs.deleteKeys)
			} else {
				if scenario == "wrong project" {
					require.ErrorIs(t, err, storeerr.ErrNotFound)
				} else {
					require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
				}
				require.Empty(t, blobs.deleteKeys)
				require.NoError(t, tx.Rollback(ctx))
				require.NoError(t,
					store.Artifacts().FinishPreparedArtifacts(ctx, artifactstore.ArtifactTransactionRolledBack, prepared),
				)
				require.Equal(t, blobs.putKeys, blobs.deleteKeys)
				var count int
				require.NoError(t, store.pool.QueryRow(ctx, `SELECT count(*) FROM artifacts`).Scan(&count))
				require.Zero(t, count)
			}
		})
	}
}

func TestPreparedArtifactHoldsRuntimeFenceUntilCallerCommit(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	store, blobs, input := artifactPreparationFixture(t)
	install, proof := artifactPreparationRuntime(t, store)
	prepared, err := store.Artifacts().PrepareArtifactWithIntegrationRuntimeLease(ctx, input, install.ID, &proof)
	require.NoError(t, err)
	tx := integrationdb.BeginTx(t, ctx, store.pool)
	record, err := store.Artifacts().PersistPreparedArtifact(ctx, tx, prepared)
	require.NoError(t, err)
	revoked := integrationdb.RunAsyncError(func() error {
		_, err := store.pool.Exec(ctx, `-- name: ArtifactPreparationRevokeRuntime :exec
UPDATE integration_runtime_units SET lease_generation = lease_generation + 1 WHERE id = $1`, proof.UnitID)
		return err
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, store.pool, "ArtifactPreparationRevokeRuntime", 1)
	select {
	case err := <-revoked:
		t.Fatalf("runtime fence released before caller commit: %v", err)
	default:
	}
	require.NoError(t, tx.Commit(ctx))
	select {
	case err := <-revoked:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.NoError(t,
		store.Artifacts().FinishPreparedArtifacts(ctx, artifactstore.ArtifactTransactionCommitted, prepared),
	)
	require.Empty(t, blobs.deleteKeys)
	_, err = store.Artifacts().GetArtifact(ctx, input.ProjectID, input.AgentID, record.ID)
	require.NoError(t, err)
}

// Keep pgx's closed-transaction sentinel in this test's contract: persistence
// cannot reopen or silently own a transaction supplied by the caller.
func TestPreparedArtifactRejectsClosedTransaction(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store, blobs, input := artifactPreparationFixture(t)
	prepared, err := store.Artifacts().PrepareArtifact(ctx, input)
	require.NoError(t, err)
	tx := integrationdb.BeginTx(t, ctx, store.pool)
	require.NoError(t, tx.Rollback(ctx))
	_, err = store.Artifacts().PersistPreparedArtifact(ctx, tx, prepared)
	require.ErrorIs(t, err, pgx.ErrTxClosed)
	require.Empty(t, blobs.deleteKeys)
	require.NoError(t,
		store.Artifacts().FinishPreparedArtifacts(ctx, artifactstore.ArtifactTransactionRolledBack, prepared),
	)
	require.Empty(t, blobs.content)
}
