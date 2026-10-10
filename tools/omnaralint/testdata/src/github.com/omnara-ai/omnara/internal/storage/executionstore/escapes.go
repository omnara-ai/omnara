package executionstore

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type executor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	SendBatch(context.Context, *pgx.Batch) pgx.BatchResults
	CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error)
}

func escapes(ctx context.Context, db executor, sql string) {
	db.Exec( // want "execution writes belong to agentexecution"
		ctx,
		`INSERT INTO agents(id) VALUES ($1)`,
		1,
	)
	db.QueryRow( // want "execution writes belong to agentexecution"
		ctx,
		`WITH changed AS (DELETE FROM agent_inputs WHERE id=$1 RETURNING id) SELECT id FROM changed`,
		1,
	)
	db.Exec( // want "dynamic SQL cannot bypass"
		ctx,
		sql,
	)
	exec := db.Exec
	exec( // want "execution writes belong to agentexecution"
		ctx,
		`UPDATE /* comment */ "public"."tool_calls" SET state='ready' WHERE id=$1`,
		1,
	)
	db.SendBatch( // want "execution-capable bulk SQL belongs to agentexecution"
		ctx,
		&pgx.Batch{},
	)
	batch := db.SendBatch
	batch( // want "execution-capable bulk SQL belongs to agentexecution"
		ctx,
		&pgx.Batch{},
	)
	db.CopyFrom( // want "execution-capable bulk SQL belongs to agentexecution"
		ctx,
		pgx.Identifier{"agents"},
		[]string{"id"},
		nil,
	)
	copyRows := db.CopyFrom
	copyRows( // want "execution-capable bulk SQL belongs to agentexecution"
		ctx,
		pgx.Identifier{"agents"},
		[]string{"id"},
		nil,
	)
	db.Exec(ctx, `UPDATE machines SET display_name=$1 WHERE id=$2`, "machine", 1)
	db.QueryRow(ctx, `SELECT id FROM agents WHERE id=$1 FOR UPDATE`, 1)
}
