// Package sqlite is the SQLite adapter for app.Store: one file, pure Go, no cgo.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // driver "sqlite"

	"github.com/recluzegeek/shared-context-protocol/server/internal/app"
)

const schema = `
CREATE TABLE IF NOT EXISTS workspaces (id TEXT PRIMARY KEY, seq INTEGER NOT NULL DEFAULT 0, doc TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS members (
  id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, handle TEXT NOT NULL, doc TEXT NOT NULL,
  UNIQUE (workspace_id, handle));
CREATE TABLE IF NOT EXISTS tokens (
  id TEXT PRIMARY KEY, member_id TEXT NOT NULL, hmac BLOB NOT NULL UNIQUE, label TEXT NOT NULL,
  tool_set TEXT NOT NULL, created_at INTEGER NOT NULL, revoked_at INTEGER);
CREATE TABLE IF NOT EXISTS notes (
  id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, status TEXT NOT NULL, seq INTEGER NOT NULL,
  expires_at INTEGER NOT NULL, doc TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS notes_ws ON notes (workspace_id, status, seq);
CREATE INDEX IF NOT EXISTS notes_due ON notes (status, expires_at);
CREATE TABLE IF NOT EXISTS intents (
  id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, status TEXT NOT NULL, seq INTEGER NOT NULL,
  expires_at INTEGER NOT NULL, doc TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS intents_ws ON intents (workspace_id, status, seq);
CREATE INDEX IF NOT EXISTS intents_due ON intents (status, expires_at);
CREATE TABLE IF NOT EXISTS requests (
  member_id TEXT NOT NULL, client_request_id TEXT NOT NULL, params_hash BLOB NOT NULL,
  result BLOB NOT NULL, at INTEGER NOT NULL, PRIMARY KEY (member_id, client_request_id));
CREATE TABLE IF NOT EXISTS audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT, workspace_id TEXT NOT NULL, member_id TEXT NOT NULL,
  operation TEXT NOT NULL, object_id TEXT NOT NULL, detail TEXT NOT NULL, at INTEGER NOT NULL);
`

// Store is an app.Store on one SQLite database.
type Store struct{ db *sql.DB }

// Open opens (creating if needed) the database at path and applies the schema.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)")
	if err != nil {
		return nil, err
	}
	// ponytail: one connection serializes every transaction; plenty for a team, Postgres past that.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("sqlite: schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Tx implements app.Store.
func (s *Store) Tx(ctx context.Context, fn func(app.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(&txn{tx}); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

type txn struct{ tx *sql.Tx }

func noRow(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return app.ErrNoRow
	}
	return err
}

func (t *txn) NextSeq(ws string) (int64, error) {
	var seq int64
	err := t.tx.QueryRow(`UPDATE workspaces SET seq = seq + 1 WHERE id = ? RETURNING seq`, ws).Scan(&seq)
	return seq, noRow(err)
}

func getDoc[T any](t *txn, query string, args ...any) (T, error) {
	var out T
	var doc string
	if err := t.tx.QueryRow(query, args...).Scan(&doc); err != nil {
		return out, noRow(err)
	}
	return out, json.Unmarshal([]byte(doc), &out)
}

func (t *txn) Workspace(id string) (app.Workspace, error) {
	return getDoc[app.Workspace](t, `SELECT doc FROM workspaces WHERE id = ?`, id)
}

func (t *txn) SaveWorkspace(w app.Workspace) error {
	doc, _ := json.Marshal(w)
	_, err := t.tx.Exec(`INSERT INTO workspaces (id, doc) VALUES (?, ?) ON CONFLICT (id) DO UPDATE SET doc = excluded.doc`, w.ID, string(doc))
	return err
}

func (t *txn) member(query string, args ...any) (app.Member, error) {
	var ws, doc string
	if err := t.tx.QueryRow(query, args...).Scan(&ws, &doc); err != nil {
		return app.Member{}, noRow(err)
	}
	var m app.Member
	err := json.Unmarshal([]byte(doc), &m)
	m.WorkspaceID = ws
	return m, err
}

func (t *txn) Member(id string) (app.Member, error) {
	return t.member(`SELECT workspace_id, doc FROM members WHERE id = ?`, id)
}

func (t *txn) MemberByHandle(ws, handle string) (app.Member, error) {
	return t.member(`SELECT workspace_id, doc FROM members WHERE workspace_id = ? AND handle = ?`, ws, handle)
}

func (t *txn) Members(ws string) ([]app.Member, error) {
	rows, err := t.tx.Query(`SELECT doc FROM members WHERE workspace_id = ? ORDER BY handle`, ws)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []app.Member
	for rows.Next() {
		var doc string
		var m app.Member
		if err := rows.Scan(&doc); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(doc), &m); err != nil {
			return nil, err
		}
		m.WorkspaceID = ws
		out = append(out, m)
	}
	return out, rows.Err()
}

func (t *txn) SaveMember(m app.Member) error {
	doc, _ := json.Marshal(m)
	_, err := t.tx.Exec(`INSERT INTO members (id, workspace_id, handle, doc) VALUES (?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET handle = excluded.handle, doc = excluded.doc`, m.ID, m.WorkspaceID, m.Handle, string(doc))
	return err
}

func (t *txn) TokenByHMAC(h []byte) (app.Token, error) {
	var tok app.Token
	var created int64
	var revoked sql.NullInt64
	err := t.tx.QueryRow(`SELECT id, member_id, hmac, label, tool_set, created_at, revoked_at FROM tokens WHERE hmac = ?`, h).
		Scan(&tok.ID, &tok.MemberID, &tok.HMAC, &tok.Label, &tok.ToolSet, &created, &revoked)
	if err != nil {
		return tok, noRow(err)
	}
	tok.CreatedAt = time.Unix(0, created).UTC()
	if revoked.Valid {
		r := time.Unix(0, revoked.Int64).UTC()
		tok.RevokedAt = &r
	}
	return tok, nil
}

func (t *txn) SaveToken(tok app.Token) error {
	var revoked any
	if tok.RevokedAt != nil {
		revoked = tok.RevokedAt.UnixNano()
	}
	_, err := t.tx.Exec(`INSERT INTO tokens (id, member_id, hmac, label, tool_set, created_at, revoked_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET revoked_at = excluded.revoked_at`,
		tok.ID, tok.MemberID, tok.HMAC, tok.Label, tok.ToolSet, tok.CreatedAt.UnixNano(), revoked)
	return err
}

// objects reads rows of (workspace_id, seq, doc) into notes or intents.
func objects[T any](t *txn, set func(*T, string, int64), query string, args ...any) ([]T, error) {
	rows, err := t.tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []T
	for rows.Next() {
		var ws, doc string
		var seq int64
		var v T
		if err := rows.Scan(&ws, &seq, &doc); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(doc), &v); err != nil {
			return nil, err
		}
		set(&v, ws, seq)
		out = append(out, v)
	}
	return out, rows.Err()
}

func setNote(n *app.Note, ws string, seq int64)     { n.WorkspaceID, n.Seq = ws, seq }
func setIntent(i *app.Intent, ws string, seq int64) { i.WorkspaceID, i.Seq = ws, seq }

// listQuery selects a workspace's rows with seq > after and, when statuses is non-empty, one of them.
func listQuery(table string, ws string, statuses []string, after int64) (string, []any) {
	q := `SELECT workspace_id, seq, doc FROM ` + table + ` WHERE workspace_id = ? AND seq > ?`
	args := []any{ws, after}
	if len(statuses) > 0 {
		q += ` AND status IN (?` + strings.Repeat(`, ?`, len(statuses)-1) + `)`
		for _, s := range statuses {
			args = append(args, s)
		}
	}
	return q + ` ORDER BY seq`, args
}

func (t *txn) Note(ws, id string) (app.Note, error) {
	out, err := objects(t, setNote, `SELECT workspace_id, seq, doc FROM notes WHERE workspace_id = ? AND id = ?`, ws, id)
	if err != nil || len(out) == 0 {
		return app.Note{}, errors.Join(err, app.ErrNoRow)
	}
	return out[0], nil
}

func (t *txn) Notes(ws string, statuses []string, after int64) ([]app.Note, error) {
	q, args := listQuery("notes", ws, statuses, after)
	return objects(t, setNote, q, args...)
}

func (t *txn) SaveNote(n app.Note) error {
	doc, _ := json.Marshal(n)
	_, err := t.tx.Exec(`INSERT INTO notes (id, workspace_id, status, seq, expires_at, doc) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET status = excluded.status, seq = excluded.seq, expires_at = excluded.expires_at, doc = excluded.doc`,
		n.ID, n.WorkspaceID, n.Status, n.Seq, n.ExpiresAt.UnixNano(), string(doc))
	return err
}

func (t *txn) Intent(ws, id string) (app.Intent, error) {
	out, err := objects(t, setIntent, `SELECT workspace_id, seq, doc FROM intents WHERE workspace_id = ? AND id = ?`, ws, id)
	if err != nil || len(out) == 0 {
		return app.Intent{}, errors.Join(err, app.ErrNoRow)
	}
	return out[0], nil
}

func (t *txn) Intents(ws string, statuses []string, after int64) ([]app.Intent, error) {
	q, args := listQuery("intents", ws, statuses, after)
	return objects(t, setIntent, q, args...)
}

func (t *txn) SaveIntent(i app.Intent) error {
	doc, _ := json.Marshal(i)
	_, err := t.tx.Exec(`INSERT INTO intents (id, workspace_id, status, seq, expires_at, doc) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET status = excluded.status, seq = excluded.seq, expires_at = excluded.expires_at, doc = excluded.doc`,
		i.ID, i.WorkspaceID, i.Status, i.Seq, i.ExpiresAt.UnixNano(), string(doc))
	return err
}

func (t *txn) DueNotes(now time.Time) ([]app.Note, error) {
	return objects(t, setNote, `SELECT workspace_id, seq, doc FROM notes WHERE status = 'active' AND expires_at < ?`, now.UnixNano())
}

func (t *txn) DueIntents(now time.Time) ([]app.Intent, error) {
	return objects(t, setIntent, `SELECT workspace_id, seq, doc FROM intents WHERE status = 'active' AND expires_at < ?`, now.UnixNano())
}

func (t *txn) Request(member, crid string) ([]byte, []byte, bool, error) {
	var hash, result []byte
	err := t.tx.QueryRow(`SELECT params_hash, result FROM requests WHERE member_id = ? AND client_request_id = ?`, member, crid).Scan(&hash, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, false, nil
	}
	return hash, result, err == nil, err
}

func (t *txn) SaveRequest(member, crid string, hash, result []byte, at time.Time) error {
	_, err := t.tx.Exec(`INSERT INTO requests (member_id, client_request_id, params_hash, result, at) VALUES (?, ?, ?, ?, ?)`,
		member, crid, hash, result, at.UnixNano())
	return err
}

func (t *txn) Audit(e app.AuditEntry) error {
	_, err := t.tx.Exec(`INSERT INTO audit (workspace_id, member_id, operation, object_id, detail, at) VALUES (?, ?, ?, ?, ?, ?)`,
		e.WorkspaceID, e.MemberID, e.Operation, e.ObjectID, e.Detail, e.At.UnixNano())
	return err
}
