//go:build integration

package storage

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/stretchr/testify/require"
)

// Exercise the public creation path using real transactions. The tracer injects
// either an aborted transaction before COMMIT or loss of the commit acknowledgment
// after PostgreSQL has committed, without changing the product transaction API.
func TestCreateArtifactCompensatesOnlyKnownCommitRollback(t *testing.T) {
	t.Parallel()
	for _, committed := range []bool{false, true} {
		name := "known_rollback"
		if committed {
			name = "commit_acknowledgment_lost"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			store, blobs, input := artifactPreparationFixture(t)
			fault := &artifactCommitFault{committed: committed}
			config := store.pool.Config()
			config.MaxConns = 1
			config.ConnConfig.Tracer = fault
			pool, err := pgxpool.NewWithConfig(ctx, config)
			require.NoError(t, err)
			t.Cleanup(pool.Close)
			artifacts := artifactstore.New(pool, blobs)

			record, err := artifacts.CreateArtifact(ctx, input)
			require.Error(t, err)
			require.Equal(t, artifactstore.ArtifactRecord{}, record, "no record may escape a failed creation")
			require.True(t, fault.injected, "fault must exercise COMMIT after the artifact insert")
			require.Len(t, blobs.putKeys, 1)

			var count int
			require.NoError(t, store.pool.QueryRow(ctx,
				`SELECT count(*) FROM artifacts artifact JOIN agents agent ON agent.id=artifact.agent_id
WHERE agent.project_id=$1 AND artifact.agent_id=$2 AND artifact.idempotency_key=$3`,
				input.ProjectID, input.AgentID, input.IdempotencyKey).Scan(&count))
			if !committed {
				require.ErrorIs(t, err, pgx.ErrTxCommitRollback)
				var injectedError *pgconn.PgError
				require.ErrorAs(t, fault.err, &injectedError)
				require.Equal(t, "22012", injectedError.Code)
				require.Zero(t, count)
				require.Equal(t, blobs.putKeys, blobs.deleteKeys, "known rollback compensates the provisional upload")
				require.Empty(t, blobs.content)
				return
			}
			require.NoError(t, fault.err, "PostgreSQL committed before acknowledgment was lost")
			require.ErrorIs(t, err, context.Canceled)
			require.Equal(t, 1, count)
			require.Empty(t, blobs.deleteKeys, "an ambiguous COMMIT must retain the referenced blob")
			var artifactID uuid.UUID
			require.NoError(t, store.pool.QueryRow(ctx,
				`SELECT artifact.id FROM artifacts artifact JOIN agents agent ON agent.id=artifact.agent_id
WHERE agent.project_id=$1 AND artifact.agent_id=$2 AND artifact.idempotency_key=$3`,
				input.ProjectID, input.AgentID, input.IdempotencyKey).Scan(&artifactID))
			content, _, err := store.Artifacts().GetArtifactBlob(ctx, input.ProjectID, input.AgentID, artifactID)
			require.NoError(t, err)
			require.Equal(t, input.Content, content)
		})
	}
}

type artifactCommitFault struct {
	committed bool
	injected  bool
	err       error
}

func (f *artifactCommitFault) TraceQueryStart(
	ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData,
) context.Context {
	if data.SQL != "commit" || f.injected {
		return ctx
	}
	f.injected = true
	if f.committed {
		_, f.err = conn.Exec(ctx, "commit")
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		return canceled
	}
	// This leaves the real transaction aborted, so the outer COMMIT returns
	// PostgreSQL's ROLLBACK command tag and pgx.ErrTxCommitRollback.
	_, f.err = conn.Exec(ctx, "SELECT 1/0")
	return ctx
}

func (*artifactCommitFault) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
