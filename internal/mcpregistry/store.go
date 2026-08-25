package mcpregistry

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	_ "modernc.org/sqlite"
)

var ErrInvalidCursor = errors.New("invalid cursor")

const schema = `
CREATE TABLE IF NOT EXISTS servers (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL UNIQUE,
	title TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	version TEXT NOT NULL DEFAULT '',
	website_url TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL,
	remotes_json TEXT NOT NULL DEFAULT '[]',
	icons_json TEXT NOT NULL DEFAULT '[]',
	remote_urls TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS server_remotes (
	server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
	url TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS server_remotes_url ON server_remotes(url);
CREATE VIRTUAL TABLE IF NOT EXISTS servers_fts USING fts5(
	name, title, description, remote_urls,
	content='servers', content_rowid='id',
	tokenize='unicode61'
);
CREATE TRIGGER IF NOT EXISTS servers_ai AFTER INSERT ON servers BEGIN
	INSERT INTO servers_fts(rowid, name, title, description, remote_urls)
	VALUES (new.id, new.name, new.title, new.description, new.remote_urls);
END;
CREATE TRIGGER IF NOT EXISTS servers_ad AFTER DELETE ON servers BEGIN
	INSERT INTO servers_fts(servers_fts, rowid, name, title, description, remote_urls)
	VALUES ('delete', old.id, old.name, old.title, old.description, old.remote_urls);
END;
CREATE TRIGGER IF NOT EXISTS servers_au AFTER UPDATE ON servers BEGIN
	INSERT INTO servers_fts(servers_fts, rowid, name, title, description, remote_urls)
	VALUES ('delete', old.id, old.name, old.title, old.description, old.remote_urls);
	INSERT INTO servers_fts(rowid, name, title, description, remote_urls)
	VALUES (new.id, new.name, new.title, new.description, new.remote_urls);
END;
`

type Store struct {
	db *sql.DB
}

func OpenStore(ctx context.Context, path string, readOnly bool) (*Store, error) {
	if path == "" {
		return nil, errors.New("registry database path is required")
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("registry database path must be absolute")
	}
	db, err := sql.Open("sqlite", dsn(path, readOnly))
	if err != nil {
		return nil, fmt.Errorf("open registry database: %w", err)
	}
	if readOnly {
		db.SetMaxOpenConns(4)
	} else {
		db.SetMaxOpenConns(1)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open registry database: %w", err)
	}
	if !readOnly {
		if _, err := db.ExecContext(ctx, schema); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("initialize registry schema: %w", err)
		}
	}
	return &Store{db: db}, nil
}

func dsn(path string, readOnly bool) string {
	query := url.Values{}
	query.Add("_pragma", "trusted_schema=OFF")
	if readOnly {
		query.Set("mode", "ro")
		query.Set("immutable", "1")
	} else {
		query.Add("_pragma", "busy_timeout=5000")
		query.Add("_pragma", "journal_mode=WAL")
		query.Add("_pragma", "synchronous=NORMAL")
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: query.Encode()}).String()
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Finalize(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("checkpoint registry database: %w", err)
	}
	var mode string
	if err := s.db.QueryRowContext(ctx, `PRAGMA journal_mode=DELETE`).Scan(&mode); err != nil {
		return fmt.Errorf("finalize registry journal: %w", err)
	}
	if mode != "delete" {
		return fmt.Errorf("finalize registry journal: mode %q", mode)
	}
	return nil
}

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) Count(ctx context.Context) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM servers`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count registry servers: %w", err)
	}
	return count, nil
}

func (s *Store) Replace(ctx context.Context, servers []Server) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin registry replace: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM server_remotes`); err != nil {
		return fmt.Errorf("clear registry remotes: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM servers`); err != nil {
		return fmt.Errorf("clear registry servers: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO servers (
			name, title, description, version, website_url, status, updated_at,
			remotes_json, icons_json, remote_urls
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			title = excluded.title,
			description = excluded.description,
			version = excluded.version,
			website_url = excluded.website_url,
			status = excluded.status,
			updated_at = excluded.updated_at,
			remotes_json = excluded.remotes_json,
			icons_json = excluded.icons_json,
			remote_urls = excluded.remote_urls
	`)
	if err != nil {
		return fmt.Errorf("prepare registry insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()
	remoteStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO server_remotes (server_id, url)
		SELECT id, ? FROM servers WHERE name = ?
	`)
	if err != nil {
		return fmt.Errorf("prepare registry remote insert: %w", err)
	}
	defer func() { _ = remoteStmt.Close() }()
	for _, server := range servers {
		remotes := server.Remotes
		if remotes == nil {
			remotes = []Remote{}
		}
		remotesJSON, err := json.Marshal(remotes)
		if err != nil {
			return fmt.Errorf("encode remotes for %s: %w", server.Name, err)
		}
		icons := server.Icons
		if icons == nil {
			icons = []Icon{}
		}
		iconsJSON, err := json.Marshal(icons)
		if err != nil {
			return fmt.Errorf("encode icons for %s: %w", server.Name, err)
		}
		remoteURLs := make([]string, 0, len(remotes))
		for _, remote := range remotes {
			remoteURLs = append(remoteURLs, normalizeRemoteURL(remote.URL))
		}
		if _, err := stmt.ExecContext(
			ctx,
			server.Name,
			server.Title,
			server.Description,
			server.Version,
			server.WebsiteURL,
			server.Status,
			server.UpdatedAt.UTC().Format(time.RFC3339Nano),
			string(remotesJSON),
			string(iconsJSON),
			strings.Join(remoteURLs, " "),
		); err != nil {
			return fmt.Errorf("insert registry server %s: %w", server.Name, err)
		}
		if _, err := tx.ExecContext(
			ctx,
			`DELETE FROM server_remotes WHERE server_id = (SELECT id FROM servers WHERE name = ?)`,
			server.Name,
		); err != nil {
			return fmt.Errorf("clear registry remotes for %s: %w", server.Name, err)
		}
		for _, remote := range remotes {
			if _, err := remoteStmt.ExecContext(ctx, normalizeRemoteURL(remote.URL), server.Name); err != nil {
				return fmt.Errorf("insert registry remote for %s: %w", server.Name, err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO servers_fts(servers_fts) VALUES ('optimize')`); err != nil {
		return fmt.Errorf("optimize registry index: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit registry replace: %w", err)
	}
	return nil
}

const serverColumns = `s.name, s.title, s.description, s.version, s.website_url, s.status, s.updated_at,
	s.remotes_json, s.icons_json`

func (s *Store) Search(ctx context.Context, params SearchParams) (SearchPage, error) {
	limit := normalizeLimit(params.Limit)
	offset, err := decodeCursor(params.Cursor)
	if err != nil {
		return SearchPage{}, err
	}
	match := ftsQuery(params.Query)
	remoteFilter := ""
	args := []any{}
	if needle := normalizeRemoteURL(params.RemoteURL); needle != "" {
		remoteFilter = ` AND s.id IN (SELECT server_id FROM server_remotes WHERE url = ?)`
		args = append(args, needle)
	}
	var rows *sql.Rows
	if match == "" {
		rows, err = s.db.QueryContext(ctx, `
			SELECT `+serverColumns+`
			FROM servers s
			WHERE 1 = 1`+remoteFilter+`
			ORDER BY s.name
			LIMIT ? OFFSET ?
		`, append(args, limit+1, offset)...)
	} else if urlNeedle := normalizeRemoteURL(params.Query); urlNeedle == "" {
		rows, err = s.db.QueryContext(ctx, `
			SELECT `+serverColumns+`
			FROM servers_fts f
			JOIN servers s ON s.id = f.rowid
			WHERE servers_fts MATCH ?`+remoteFilter+`
			ORDER BY bm25(servers_fts, 1.0, 5.0, 2.0, 1.0), s.name
			LIMIT ? OFFSET ?
		`, append(append([]any{match}, args...), limit+1, offset)...)
	} else {
		ranked := append([]any{match}, args...)
		substring := append([]any{"%" + escapeLike(urlNeedle) + "%", match}, args...)
		rows, err = s.db.QueryContext(ctx, `
			SELECT name, title, description, version, website_url, status, updated_at, remotes_json, icons_json
			FROM (
				SELECT `+serverColumns+`, bm25(servers_fts, 1.0, 5.0, 2.0, 1.0) AS rank
				FROM servers_fts f
				JOIN servers s ON s.id = f.rowid
				WHERE servers_fts MATCH ?`+remoteFilter+`
				UNION ALL
				SELECT `+serverColumns+`, 0 AS rank
				FROM servers s
				WHERE s.remote_urls LIKE ? ESCAPE '\'
					AND s.id NOT IN (SELECT rowid FROM servers_fts WHERE servers_fts MATCH ?)`+remoteFilter+`
			)
			ORDER BY rank, name
			LIMIT ? OFFSET ?
		`, append(append(ranked, substring...), limit+1, offset)...)
	}
	if err != nil {
		return SearchPage{}, fmt.Errorf("search registry servers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	servers := make([]Server, 0, limit)
	for rows.Next() {
		server, err := scanServer(rows)
		if err != nil {
			return SearchPage{}, err
		}
		servers = append(servers, server)
	}
	if err := rows.Err(); err != nil {
		return SearchPage{}, fmt.Errorf("search registry servers: %w", err)
	}
	page := SearchPage{Servers: servers}
	if len(servers) > limit {
		page.Servers = servers[:limit]
		next := encodeCursor(offset + limit)
		page.NextCursor = &next
	}
	return page, nil
}

func scanServer(rows *sql.Rows) (Server, error) {
	var server Server
	var updatedAt, remotesJSON, iconsJSON string
	if err := rows.Scan(
		&server.Name,
		&server.Title,
		&server.Description,
		&server.Version,
		&server.WebsiteURL,
		&server.Status,
		&updatedAt,
		&remotesJSON,
		&iconsJSON,
	); err != nil {
		return Server{}, fmt.Errorf("scan registry server: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return Server{}, fmt.Errorf("parse updated_at for %s: %w", server.Name, err)
	}
	server.UpdatedAt = parsed
	if err := json.Unmarshal([]byte(remotesJSON), &server.Remotes); err != nil {
		return Server{}, fmt.Errorf("decode remotes for %s: %w", server.Name, err)
	}
	if server.Remotes == nil {
		server.Remotes = []Remote{}
	}
	if err := json.Unmarshal([]byte(iconsJSON), &server.Icons); err != nil {
		return Server{}, fmt.Errorf("decode icons for %s: %w", server.Name, err)
	}
	if server.Icons == nil {
		server.Icons = []Icon{}
	}
	return server, nil
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return DefaultSearchLimit
	}
	if limit > MaxSearchLimit {
		return MaxSearchLimit
	}
	return limit
}

var remoteURLScheme = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*://`)

func normalizeRemoteURL(raw string) string {
	trimmed := remoteURLScheme.ReplaceAllString(strings.TrimSpace(raw), "")
	return strings.ToLower(strings.TrimRight(trimmed, "/"))
}

func escapeLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

func encodeCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

func decodeCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, ErrInvalidCursor
	}
	offset, err := strconv.Atoi(string(raw))
	if err != nil || offset < 0 {
		return 0, ErrInvalidCursor
	}
	return offset, nil
}

func ftsQuery(query string) string {
	terms := strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(terms) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		quoted = append(quoted, `"`+term+`"*`)
	}
	return strings.Join(quoted, " ")
}
