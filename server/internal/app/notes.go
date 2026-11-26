package app

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/recluzegeek/shared-context-protocol/server/internal/domain"
)

var noteKinds = []string{"decision", "gotcha", "contract", "howto", "context"}

// NoteWriteParams are note.write's parameters (spec §5.4.1).
type NoteWriteParams struct {
	Kind            string      `json:"kind" jsonschema:"decision, gotcha, contract, howto or context"`
	Body            string      `json:"body" jsonschema:"the fact, plain text or CommonMark; no secrets, code or transcripts"`
	Resources       []Resource  `json:"resources,omitempty" jsonschema:"repositories it applies to; empty for workspace-wide"`
	Refs            []Reference `json:"refs,omitempty"`
	ClientRequestID string      `json:"client_request_id,omitempty" jsonschema:"unique per attempt, makes retries safe"`
	Agent           *AgentInfo  `json:"agent,omitempty"`
}

// NoteResult is a note write's result.
type NoteResult struct {
	Note     Note      `json:"note"`
	Warnings []Warning `json:"warnings,omitempty"`
}

func (s *Service) checkRefs(refs []Reference) error {
	if len(refs) > Limits["references"] {
		return &Error{Code: CodeContentTooLarge, Message: "too many references", Details: map[string]any{"field": "refs", "limit": Limits["references"]}}
	}
	for _, r := range refs {
		if !slices.Contains([]string{"work", "change", "doc", "env", "url"}, r.Type) || r.URI == "" {
			return invalid("refs", "type must be work, change, doc, env or url, with a uri")
		}
	}
	return nil
}

// NoteWrite implements note.write.
func (s *Service) NoteWrite(ctx context.Context, p Principal, in NoteWriteParams) (NoteResult, error) {
	var out NoteResult
	err := s.store.Tx(ctx, func(tx Tx) error {
		ws, err := view(tx, p)
		if err != nil {
			return err
		}
		if err := canWrite(p); err != nil {
			return err
		}
		out, err = once(tx, p, in.ClientRequestID, in, s.Now(), func() (NoteResult, error) {
			return s.noteWrite(tx, p, ws, in)
		})
		return err
	})
	return out, err
}

func (s *Service) noteWrite(tx Tx, p Principal, ws Workspace, in NoteWriteParams) (NoteResult, error) {
	if !slices.Contains(noteKinds, in.Kind) {
		return NoteResult{}, invalid("kind", strings.Join(noteKinds, ", "))
	}
	if strings.TrimSpace(in.Body) == "" {
		return NoteResult{}, invalid("body", "required")
	}
	if err := limit("body", in.Body, Limits["note_body"]); err != nil {
		return NoteResult{}, err
	}
	if err := s.checkRefs(in.Refs); err != nil {
		return NoteResult{}, err
	}
	warnings, err := checkContent(in, ws)
	if err != nil {
		return NoteResult{}, err
	}
	res, err := s.canonicals(in.Resources, ws, "resources")
	if err != nil {
		return NoteResult{}, err
	}
	seq, err := tx.NextSeq(ws.ID)
	if err != nil {
		return NoteResult{}, err
	}
	now := s.Now()
	ref := p.Member.Ref()
	n := Note{
		ID: "n_" + s.NewID(), WorkspaceID: ws.ID, Kind: in.Kind, Body: in.Body, Resources: res,
		Refs: nonNil(in.Refs), Status: "active", Revision: 1, Seq: seq,
		ExpiresAt:  now.Add(time.Duration(ws.Settings.NoteTTLDays) * 24 * time.Hour),
		Provenance: Provenance{Author: ref, Agent: in.Agent, CreatedAt: now, UpdatedAt: now, UpdatedBy: ref},
	}
	if err := tx.SaveNote(n); err != nil {
		return NoteResult{}, err
	}
	return NoteResult{Note: n, Warnings: warnings}, tx.Audit(AuditEntry{ws.ID, p.Member.ID, "note.write", n.ID, "", now})
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// NoteSearchParams are note.search's parameters (spec §5.4.3).
type NoteSearchParams struct {
	Query     string     `json:"query,omitempty" jsonschema:"words; each matches a word of the note by prefix"`
	Kind      []string   `json:"kind,omitempty"`
	Resources []Resource `json:"resources,omitempty"`
	Work      *Reference `json:"work,omitempty"`
	Author    string     `json:"author,omitempty"`
	Status    []string   `json:"status,omitempty" jsonschema:"default [active]"`
	Order     string     `json:"order,omitempty" jsonschema:"newest (default) or relevance"`
	PageSize  int        `json:"page_size,omitempty"`
	PageToken string     `json:"page_token,omitempty"`
}

// NoteList is a page of notes, each wrapped unless the principal wrote it.
type NoteList struct {
	Notes         []any  `json:"notes"`
	NextPageToken string `json:"next_page_token,omitempty"`
}

// NoteSearch implements note.search.
func (s *Service) NoteSearch(ctx context.Context, p Principal, in NoteSearchParams) (NoteList, error) {
	var out NoteList
	err := s.store.Tx(ctx, func(tx Tx) error {
		ws, err := view(tx, p)
		if err != nil {
			return err
		}
		notes, filter, err := s.searchNotes(tx, ws, in)
		if err != nil {
			return err
		}
		offset, err := decodePage(in.PageToken, filter)
		if err != nil {
			return err
		}
		pg, next := page(notes, offset, pageSize(in.PageSize), filter)
		out = NoteList{Notes: make([]any, 0, len(pg)), NextPageToken: next}
		for _, n := range pg {
			out.Notes = append(out.Notes, wrap(p, n.Provenance, n))
		}
		return nil
	})
	return out, err
}

// searchNotes returns every note note.search matches, newest first, and the query's filter key.
// ponytail: filters in memory over the workspace's notes; move them into SQL past ~100k notes.
func (s *Service) searchNotes(tx Tx, ws Workspace, in NoteSearchParams) ([]Note, string, error) {
	status := in.Status
	if len(status) == 0 {
		status = []string{"active"}
	}
	res, err := s.canonicals(in.Resources, ws, "resources")
	if err != nil {
		return nil, "", err
	}
	var work string
	if in.Work != nil {
		work = in.Work.URI
	}
	all, err := tx.Notes(ws.ID, status, 0)
	if err != nil {
		return nil, "", err
	}
	q := domain.Tokenize(in.Query)
	var out []Note
	for _, n := range all {
		if len(in.Kind) > 0 && !slices.Contains(in.Kind, n.Kind) ||
			in.Author != "" && n.Provenance.Author.ID != in.Author ||
			!noteMatches(n, work, res) {
			continue
		}
		if len(q) > 0 {
			text := n.Body
			for _, r := range n.Refs {
				text += " " + r.Title
			}
			if !domain.MatchesQuery(q, domain.Tokenize(text)) {
				continue
			}
		}
		out = append(out, n)
	}
	sortNewest(out)
	return out, filterHash("note.search", work, uris(res), status, in.Query, in.Kind, in.Author), nil
}

// noteMatches is the brief's note filter (spec §5.3.1): refs contain the work, or resources
// intersect, or the note is workspace-wide and the request names a resource.
func noteMatches(n Note, work string, res []Resource) bool {
	if work == "" && len(res) == 0 {
		return true
	}
	if work != "" && slices.ContainsFunc(n.Refs, func(r Reference) bool { return r.Type == "work" && r.URI == work }) {
		return true
	}
	if len(res) > 0 && len(n.Resources) == 0 {
		return true
	}
	for _, r := range n.Resources {
		if slices.ContainsFunc(res, func(x Resource) bool { return x.URI == r.URI }) {
			return true
		}
	}
	return false
}

func uris(rs []Resource) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.URI)
	}
	slices.Sort(out)
	return out
}

func sortNewest(ns []Note) {
	slices.SortFunc(ns, func(a, b Note) int {
		if c := b.Provenance.UpdatedAt.Compare(a.Provenance.UpdatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
}

// NoteGet implements note.get.
func (s *Service) NoteGet(ctx context.Context, p Principal, id string) (any, error) {
	var out any
	err := s.store.Tx(ctx, func(tx Tx) error {
		ws, err := view(tx, p)
		if err != nil {
			return err
		}
		n, err := tx.Note(ws.ID, id)
		if err != nil {
			return errNotFound
		}
		out = wrap(p, n.Provenance, n)
		return nil
	})
	return out, err
}

// NoteChangeParams cover note.update, note.renew, note.promote and note.retire.
type NoteChangeParams struct {
	ID               string      `json:"id"`
	ExpectedRevision int         `json:"expected_revision"`
	Kind             *string     `json:"kind,omitempty"`
	Body             *string     `json:"body,omitempty"`
	Resources        []Resource  `json:"resources,omitempty"`
	Refs             []Reference `json:"refs,omitempty"`
	PromotedTo       *Reference  `json:"promoted_to,omitempty"`
	Reason           string      `json:"reason,omitempty"`
}

// NoteChange applies one of note.update/renew/promote/retire (spec §5.4.4-§5.4.7).
func (s *Service) NoteChange(ctx context.Context, p Principal, op string, in NoteChangeParams) (NoteResult, error) {
	var out NoteResult
	err := s.store.Tx(ctx, func(tx Tx) error {
		ws, err := view(tx, p)
		if err != nil {
			return err
		}
		n, err := tx.Note(ws.ID, in.ID)
		if err != nil {
			return errNotFound
		}
		if p.Member.Role != "owner" && n.Provenance.Author.ID != p.Member.ID {
			return errorf(CodePermissionDenied, "only the author or an owner can change a note")
		}
		if n.Revision != in.ExpectedRevision {
			return &Error{Code: CodeConflict, Message: "revision changed", Details: map[string]any{"current_revision": n.Revision}}
		}
		if n.Status != "active" {
			return &Error{Code: CodeFailedPrecondition, Message: "note is not active", Details: map[string]any{"status": n.Status}}
		}
		now := s.Now()
		switch op {
		case "note.update":
			if out.Warnings, err = s.applyNoteUpdate(&n, ws, in); err != nil {
				return err
			}
		case "note.renew":
			if n.Renewals >= 2 {
				return errorf(CodeFailedPrecondition, "a note can be renewed twice; promote it instead")
			}
			n.Renewals++
			n.ExpiresAt = now.Add(time.Duration(ws.Settings.NoteTTLDays) * 24 * time.Hour)
		case "note.promote":
			if in.PromotedTo == nil || (in.PromotedTo.Type != "doc" && in.PromotedTo.Type != "change") || in.PromotedTo.URI == "" {
				return invalid("promoted_to", "a doc or change reference")
			}
			verified := false // ponytail: no tracker integrations yet, so promotions are never verified
			n.Status, n.PromotedTo, n.PromotionVerified = "promoted", in.PromotedTo, &verified
		case "note.retire":
			n.Status = "retired"
		}
		seq, err := tx.NextSeq(ws.ID)
		if err != nil {
			return err
		}
		n.Revision++
		n.Seq = seq
		n.Provenance.UpdatedAt, n.Provenance.UpdatedBy = now, p.Member.Ref()
		if err := tx.SaveNote(n); err != nil {
			return err
		}
		out.Note = n
		return tx.Audit(AuditEntry{ws.ID, p.Member.ID, op, n.ID, "", now})
	})
	return out, err
}

func (s *Service) applyNoteUpdate(n *Note, ws Workspace, in NoteChangeParams) ([]Warning, error) {
	if in.Kind != nil {
		if !slices.Contains(noteKinds, *in.Kind) {
			return nil, invalid("kind", strings.Join(noteKinds, ", "))
		}
		n.Kind = *in.Kind
	}
	if in.Body != nil {
		if strings.TrimSpace(*in.Body) == "" {
			return nil, invalid("body", "required")
		}
		if err := limit("body", *in.Body, Limits["note_body"]); err != nil {
			return nil, err
		}
		n.Body = *in.Body
	}
	if in.Resources != nil {
		res, err := s.canonicals(in.Resources, ws, "resources")
		if err != nil {
			return nil, err
		}
		n.Resources = res
	}
	if in.Refs != nil {
		if err := s.checkRefs(in.Refs); err != nil {
			return nil, err
		}
		n.Refs = in.Refs
	}
	return checkContent(in, ws)
}
