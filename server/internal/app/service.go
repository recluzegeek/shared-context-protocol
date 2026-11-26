package app

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/recluzegeek/shared-context-protocol/server/internal/domain"
)

// Limits are the server's limits (spec §3.4), published in discovery.
var Limits = map[string]int{
	"note_body": 4000, "intent_summary": 500, "handoff_text": 4000, "references": 20,
	"scopes": 25, "uses": 10, "page_size_default": 50, "page_size_max": 100,
}

// Tool sets (spec §11.1.2.1).
const (
	SetEssentials = "essentials"
	SetFull       = "full"
	SetAdmin      = "admin"
)

// Service runs SCP operations for authenticated principals.
type Service struct {
	store    Store
	tokenKey []byte
	ciHosts  []string
	Now      func() time.Time
	NewID    func() string
}

// NewService builds a Service. tokenKey is the HMAC key for personal access tokens.
func NewService(store Store, tokenKey []byte) *Service {
	return &Service{
		store: store, tokenKey: tokenKey, ciHosts: domain.DefaultCaseInsensitiveHosts,
		Now: func() time.Time { return time.Now().UTC() }, NewID: randomID,
	}
}

// CaseInsensitiveHosts is published in discovery.
func (s *Service) CaseInsensitiveHosts() []string { return s.ciHosts }

func randomID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- authentication and bootstrap (spec §5.1.0, §10.2) ----

const tokenPrefix = "sharedctx_pat_"

func (s *Service) tokenHMAC(token string) []byte {
	m := hmac.New(sha256.New, s.tokenKey)
	m.Write([]byte(token))
	return m.Sum(nil)
}

// Authenticate resolves a bearer token to its principal.
func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	var p Principal
	err := s.store.Tx(ctx, func(tx Tx) error {
		t, err := tx.TokenByHMAC(s.tokenHMAC(token))
		if err != nil || t.RevokedAt != nil {
			return &Error{Code: CodeUnauthenticated, Message: "invalid or revoked token"}
		}
		m, err := tx.Member(t.MemberID)
		if err != nil || !m.Active {
			return &Error{Code: CodeUnauthenticated, Message: "member removed"}
		}
		p = Principal{Member: m, ToolSet: allowedSet(t.ToolSet, m.Role)}
		return nil
	})
	return p, err
}

// allowedSet narrows a token's set to what the role allows (spec §11.1.2.1).
func allowedSet(set, role string) string {
	if set == SetAdmin && role != "owner" {
		return SetFull
	}
	return set
}

var handleRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,38}$`)

// CreateWorkspace is the operator's out-of-band bootstrap: a workspace and its first owner.
func (s *Service) CreateWorkspace(ctx context.Context, name, ownerHandle, ownerName string) (Workspace, Member, error) {
	now := s.Now()
	w := Workspace{ID: "w_" + s.NewID(), Name: name, Settings: DefaultSettings(), Revision: 1, CreatedAt: now}
	m := Member{ID: "m_" + s.NewID(), WorkspaceID: w.ID, Handle: ownerHandle, DisplayName: ownerName, Role: "owner", Active: true, Revision: 1, JoinedAt: now}
	if !handleRE.MatchString(ownerHandle) {
		return w, m, invalid("handle", "must match "+handleRE.String())
	}
	err := s.store.Tx(ctx, func(tx Tx) error {
		if err := tx.SaveWorkspace(w); err != nil {
			return err
		}
		return tx.SaveMember(m)
	})
	return w, m, err
}

// AddMember adds a member to a workspace (out of band until member.invite is implemented).
func (s *Service) AddMember(ctx context.Context, workspaceID, handle, name, role string) (Member, error) {
	if !handleRE.MatchString(handle) {
		return Member{}, invalid("handle", "must match "+handleRE.String())
	}
	if !slices.Contains([]string{"owner", "member", "guest"}, role) {
		return Member{}, invalid("role", "owner, member or guest")
	}
	m := Member{ID: "m_" + s.NewID(), WorkspaceID: workspaceID, Handle: handle, DisplayName: name, Role: role, Active: true, Revision: 1, JoinedAt: s.Now()}
	err := s.store.Tx(ctx, func(tx Tx) error {
		if _, err := tx.Workspace(workspaceID); err != nil {
			return errNotFound
		}
		if _, err := tx.MemberByHandle(workspaceID, handle); err == nil {
			return errorf(CodeConflict, "handle already taken")
		}
		return tx.SaveMember(m)
	})
	return m, err
}

// IssueToken creates a personal access token for a member; the plain token is returned once.
func (s *Service) IssueToken(ctx context.Context, workspaceID, handle, label, set string) (string, error) {
	if !slices.Contains([]string{SetEssentials, SetFull, SetAdmin}, set) {
		return "", invalid("tool_set", "essentials, full or admin")
	}
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	token := tokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	err := s.store.Tx(ctx, func(tx Tx) error {
		m, err := tx.MemberByHandle(workspaceID, handle)
		if err != nil {
			return errNotFound
		}
		return tx.SaveToken(Token{ID: "t_" + s.NewID(), MemberID: m.ID, HMAC: s.tokenHMAC(token), Label: label, ToolSet: set, CreatedAt: s.Now()})
	})
	return token, err
}

// ---- shared helpers ----

// view loads the principal's workspace and checks the principal is still active.
func view(tx Tx, p Principal) (Workspace, error) {
	m, err := tx.Member(p.Member.ID)
	if err != nil || !m.Active {
		return Workspace{}, &Error{Code: CodeUnauthenticated, Message: "member removed"}
	}
	return tx.Workspace(m.WorkspaceID)
}

func canWrite(p Principal) error {
	if p.Member.Role == "guest" {
		return errorf(CodePermissionDenied, "guests are read-only")
	}
	return nil
}

// canonical canonicalizes a resource and applies the workspace's aliases (spec §4.7).
func (s *Service) canonical(r Resource, ws Workspace, field string) (Resource, error) {
	uri, err := domain.CanonicalResource(r.URI, s.ciHosts)
	if err != nil {
		return r, invalid(field, err.Error())
	}
	if to, ok := ws.Settings.ResourceAliases[uri]; ok {
		uri = to
	}
	return Resource{URI: uri, Label: r.Label}, nil
}

func (s *Service) canonicals(rs []Resource, ws Workspace, field string) ([]Resource, error) {
	out := make([]Resource, 0, len(rs))
	for i, r := range rs {
		c, err := s.canonical(r, ws, fmt.Sprintf("%s[%d]", field, i))
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// checkContent validates every string in params (spec §9.2) and scans it for secrets (§9.1).
func checkContent(params any, ws Workspace) ([]Warning, error) {
	b, _ := json.Marshal(params)
	var tree any
	_ = json.Unmarshal(b, &tree)
	fields := map[string]string{}
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case string:
			fields[path] = x
		case map[string]any:
			for k, c := range x {
				walk(strings.TrimPrefix(path+"."+k, "."), c)
			}
		case []any:
			for i, c := range x {
				walk(fmt.Sprintf("%s[%d]", path, i), c)
			}
		}
	}
	walk("", tree)
	for field, text := range fields {
		if strings.ContainsFunc(text, func(r rune) bool {
			return unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r'
		}) {
			return nil, invalid(field, "control characters are not allowed")
		}
	}
	policy := domain.ScanPolicy{Modes: ws.Settings.SecretDetectors.Modes}
	for _, p := range ws.Settings.SecretDetectors.AllowList {
		if re, err := regexp.Compile(p); err == nil {
			policy.AllowList = append(policy.AllowList, re)
		}
	}
	reject, warn := domain.Scan(fields, policy)
	sortFindings(reject)
	sortFindings(warn)
	if len(reject) > 0 {
		f := reject[0]
		return nil, &Error{
			Code: CodeSecretDetected, Message: f.Field + " contains a credential and was not stored",
			Details: map[string]any{"field": f.Field, "detector": f.Detector},
		}
	}
	var out []Warning
	for _, f := range warn {
		out = append(out, Warning{Code: "possible_secret", Detail: f.Field + ": " + f.Detector})
	}
	return out, nil
}

func sortFindings(fs []domain.Finding) {
	sort.Slice(fs, func(i, j int) bool {
		if fs[i].Field != fs[j].Field {
			return fs[i].Field < fs[j].Field
		}
		return fs[i].Detector < fs[j].Detector
	})
}

func limit(field, text string, max int) error {
	if len([]rune(text)) > max {
		return &Error{Code: CodeContentTooLarge, Message: field + " is over the limit", Details: map[string]any{"field": field, "limit": max}}
	}
	return nil
}

// wrap returns v untrusted unless the principal wrote it and made its last change (spec §8.2).
func wrap(p Principal, prov Provenance, v any) any {
	if prov.Author.ID == p.Member.ID && prov.UpdatedBy.ID == p.Member.ID {
		return v
	}
	by := prov.UpdatedBy
	return Untrusted{Untrusted: true, Source: "member", Author: &by, Value: v}
}

// ---- paging (spec §5.9) ----

type pageState struct {
	Offset int    `json:"o"`
	Filter string `json:"f"`
}

func filterHash(parts ...any) string {
	b, _ := json.Marshal(parts)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

func encodePage(offset int, filter string) string {
	b, _ := json.Marshal(pageState{offset, filter})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodePage(token, filter string) (int, error) {
	if token == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	var st pageState
	if err != nil || json.Unmarshal(b, &st) != nil || st.Filter != filter || st.Offset < 0 {
		return 0, invalid("page_token", "not a token for this query")
	}
	return st.Offset, nil
}

func pageSize(n int) int {
	if n <= 0 {
		return Limits["page_size_default"]
	}
	return min(n, Limits["page_size_max"])
}

// page cuts items at offset/size and returns the next token, empty on the last page.
func page[T any](items []T, offset, size int, filter string) ([]T, string) {
	if offset >= len(items) {
		return []T{}, ""
	}
	end := min(offset+size, len(items))
	next := ""
	if end < len(items) {
		next = encodePage(end, filter)
	}
	return items[offset:end], next
}

// ---- idempotent creates (spec §3.3.1) ----

// once runs create at most once per client_request_id: a retry with the same parameters gets the
// stored result, with different parameters CONFLICT.
func once[T any](tx Tx, p Principal, crid string, params any, now time.Time, create func() (T, error)) (T, error) {
	var zero T
	if crid == "" {
		return create()
	}
	if len(crid) > 64 {
		return zero, invalid("client_request_id", "at most 64 characters")
	}
	b, _ := json.Marshal(params)
	sum := sha256.Sum256(b)
	hash, stored, ok, err := tx.Request(p.Member.ID, crid)
	if err != nil {
		return zero, err
	}
	if ok {
		if !hmac.Equal(hash, sum[:]) {
			return zero, errorf(CodeConflict, "client_request_id was used with different parameters")
		}
		var out T
		return out, json.Unmarshal(stored, &out)
	}
	out, err := create()
	if err != nil {
		return zero, err
	}
	res, _ := json.Marshal(out)
	return out, tx.SaveRequest(p.Member.ID, crid, sum[:], res, now)
}

// ---- workspace and members (spec §5.1, §5.2) ----

// WorkspaceGet implements workspace.get.
func (s *Service) WorkspaceGet(ctx context.Context, p Principal) (Workspace, error) {
	var w Workspace
	err := s.store.Tx(ctx, func(tx Tx) error {
		var err error
		w, err = view(tx, p)
		return err
	})
	return w, err
}

// MemberList implements member.list.
func (s *Service) MemberList(ctx context.Context, p Principal, active bool) ([]Member, error) {
	var out []Member
	err := s.store.Tx(ctx, func(tx Tx) error {
		if _, err := view(tx, p); err != nil {
			return err
		}
		all, err := tx.Members(p.Member.WorkspaceID)
		for _, m := range all {
			if m.Active == active {
				out = append(out, m)
			}
		}
		return err
	})
	return out, err
}

// SessionStart implements session.start (spec §5.2.6).
func (s *Service) SessionStart(ctx context.Context, p Principal) (map[string]any, error) {
	if err := canWrite(p); err != nil {
		return nil, err
	}
	return map[string]any{"session": "s_" + s.NewID(), "started_at": s.Now()}, nil
}

func asError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Code: CodeInternal, Message: "internal error", Retryable: true}
}

// AsError converts any error to an SCP error for the bindings.
func AsError(err error) *Error { return asError(err) }
