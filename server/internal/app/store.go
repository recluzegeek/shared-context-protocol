package app

import (
	"context"
	"errors"
	"time"
)

// ErrNoRow is what stores return for a missing row.
var ErrNoRow = errors.New("no row")

// Token is a personal access token (spec §10.2), stored only as an HMAC.
type Token struct {
	ID        string
	MemberID  string
	HMAC      []byte
	Label     string
	ToolSet   string
	CreatedAt time.Time
	RevokedAt *time.Time
}

// AuditEntry is one audit record (spec §10.5); it never holds object bodies.
type AuditEntry struct {
	WorkspaceID, MemberID, Operation, ObjectID, Detail string
	At                                                 time.Time
}

// Store persists SCP objects. Every method of Tx runs inside one transaction.
type Store interface {
	Tx(ctx context.Context, fn func(Tx) error) error
}

// Tx is the transactional view of a Store.
type Tx interface {
	// NextSeq returns the workspace's next change sequence number; every write stamps one.
	NextSeq(workspaceID string) (int64, error)

	Workspace(id string) (Workspace, error)
	SaveWorkspace(w Workspace) error

	Member(id string) (Member, error)
	MemberByHandle(workspaceID, handle string) (Member, error)
	Members(workspaceID string) ([]Member, error)
	SaveMember(m Member) error

	TokenByHMAC(h []byte) (Token, error)
	SaveToken(t Token) error

	Note(workspaceID, id string) (Note, error)
	// Notes returns the workspace's notes with seq > afterSeq, in any of statuses (all when empty).
	Notes(workspaceID string, statuses []string, afterSeq int64) ([]Note, error)
	SaveNote(n Note) error

	Intent(workspaceID, id string) (Intent, error)
	Intents(workspaceID string, statuses []string, afterSeq int64) ([]Intent, error)
	SaveIntent(i Intent) error

	// DueNotes and DueIntents are active objects whose expires_at has passed, across workspaces.
	DueNotes(now time.Time) ([]Note, error)
	DueIntents(now time.Time) ([]Intent, error)

	// Request looks up a retry of client_request_id; ok is false for a first attempt.
	Request(memberID, clientRequestID string) (paramsHash []byte, result []byte, ok bool, err error)
	SaveRequest(memberID, clientRequestID string, paramsHash, result []byte, at time.Time) error

	Audit(e AuditEntry) error
}
