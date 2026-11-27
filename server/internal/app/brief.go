package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"time"
)

// BriefParams are brief.get's parameters (spec §5.3.1).
type BriefParams struct {
	Work       *Reference `json:"work,omitempty" jsonschema:"the work the session is on, e.g. github:acme/shop#123"`
	Resources  []Resource `json:"resources,omitempty" jsonschema:"the repositories the session is on (git remote URLs)"`
	NotesLimit int        `json:"notes_limit,omitempty"`
	Since      string     `json:"since,omitempty" jsonschema:"a cursor from an earlier brief, for only what changed"`
	Session    string     `json:"session,omitempty" jsonschema:"your session id, so intents of your other sessions are marked untrusted"`
}

// Brief is everything the workspace knows about a piece of work (spec §4.6).
type Brief struct {
	Work                  *Reference `json:"work,omitempty"`
	Resources             []Resource `json:"resources"`
	Notes                 []any      `json:"notes"`
	NotesNextPageToken    string     `json:"notes_next_page_token,omitempty"`
	Promoted              []any      `json:"promoted"`
	PromotedNextPageToken string     `json:"promoted_next_page_token,omitempty"`
	Intents               []any      `json:"intents"`
	IntentsNextPageToken  string     `json:"intents_next_page_token,omitempty"`
	MyIntents             []Intent   `json:"my_intents"`
	Handoffs              []any      `json:"handoffs"`
	ExpiringNotes         []Note     `json:"expiring_notes"`
	GeneratedAt           time.Time  `json:"generated_at"`
	Truncated             []string   `json:"truncated"`
	Delta                 bool       `json:"delta"`
	Cursor                string     `json:"cursor"`
}

type cursorState struct {
	Seq    int64  `json:"s"`
	Filter string `json:"f"`
}

// BriefGet implements brief.get.
func (s *Service) BriefGet(ctx context.Context, p Principal, in BriefParams) (Brief, error) {
	var out Brief
	err := s.store.Tx(ctx, func(tx Tx) error {
		ws, err := view(tx, p)
		if err != nil {
			return err
		}
		res := in.Resources
		if in.Work == nil && len(res) == 0 {
			res = ws.Settings.DefaultResources
		}
		if in.Work == nil && len(res) == 0 {
			return invalid("resources", "name the work or the resources, or set default_resources")
		}
		if res, err = s.canonicals(res, ws, "resources"); err != nil {
			return err
		}
		var work string
		if in.Work != nil {
			work = in.Work.URI
		}
		filter := filterHash("brief", p.Member.ID, work, uris(res))
		var since int64
		if in.Since != "" {
			b, err := base64.RawURLEncoding.DecodeString(in.Since)
			var c cursorState
			if err != nil || json.Unmarshal(b, &c) != nil {
				return invalid("since", "not a cursor")
			}
			if c.Filter != filter {
				return &Error{
					Code: CodeFailedPrecondition, Message: "cursor was issued for another filter",
					Details: map[string]any{"reason": "cursor_filter_mismatch"},
				}
			}
			since = c.Seq
		}
		top, err := tx.NextSeq(ws.ID) // ponytail: consumes a seq per brief; harmless, seqs only need to grow
		if err != nil {
			return err
		}
		out, err = s.assemble(tx, p, ws, in, work, res, since)
		if err != nil {
			return err
		}
		c, _ := json.Marshal(cursorState{Seq: top, Filter: filter})
		out.Cursor = base64.RawURLEncoding.EncodeToString(c)
		return nil
	})
	return out, err
}

func (s *Service) assemble(tx Tx, p Principal, ws Workspace, in BriefParams, work string, res []Resource, since int64) (Brief, error) {
	now := s.Now()
	b := Brief{
		Work: in.Work, Resources: res, Notes: []any{}, Promoted: []any{}, Intents: []any{}, MyIntents: []Intent{},
		Handoffs: []any{}, ExpiringNotes: []Note{}, GeneratedAt: now, Truncated: []string{}, Delta: since > 0,
	}
	statuses := []string{"active"}
	if b.Delta {
		statuses = nil // a delta carries every change, including objects that left the filter
	}
	notes, err := tx.Notes(ws.ID, statuses, since)
	if err != nil {
		return b, err
	}
	sortNewest(notes)
	limit := pageSize(in.NotesLimit)
	searchFilter := filterHash("note.search", work, uris(res), []string{"active"}, "", []string(nil), "")
	var matched []Note
	for _, n := range notes {
		if noteMatches(n, work, res) && (n.Status == "active" || b.Delta) {
			matched = append(matched, n)
		}
	}
	pg, next := page(matched, 0, limit, searchFilter)
	for _, n := range pg {
		b.Notes = append(b.Notes, wrap(p, n.Provenance, n))
	}
	if next != "" {
		b.NotesNextPageToken = next
		b.Truncated = append(b.Truncated, "notes")
	}
	promoted, err := tx.Notes(ws.ID, []string{"promoted"}, since)
	if err != nil {
		return b, err
	}
	sortNewest(promoted)
	cutoff := now.Add(-time.Duration(ws.Settings.NoteTTLDays) * 24 * time.Hour)
	var recent []Note
	for _, n := range promoted {
		if noteMatches(n, work, res) && n.Provenance.UpdatedAt.After(cutoff) {
			recent = append(recent, n)
		}
	}
	promotedFilter := filterHash("note.search", work, uris(res), []string{"promoted"}, "", []string(nil), "")
	pgp, nextp := page(recent, 0, 20, promotedFilter)
	for _, n := range pgp {
		b.Promoted = append(b.Promoted, wrap(p, n.Provenance, n))
	}
	if nextp != "" {
		b.PromotedNextPageToken = nextp
		b.Truncated = append(b.Truncated, "promoted")
	}
	intents, err := tx.Intents(ws.ID, statuses, since)
	if err != nil {
		return b, err
	}
	sortIntents(intents)
	var listed []Intent // exactly intent.list's result for this filter, in its order
	for _, i := range intents {
		if intentMatches(i, work, res) {
			listed = append(listed, i)
		}
	}
	others := 0
	for pos, i := range listed {
		mine := i.Provenance.Author.ID == p.Member.ID && (in.Session == "" || in.Session == i.Session)
		if mine {
			b.MyIntents = append(b.MyIntents, i)
			continue
		}
		if others == Limits["page_size_max"] {
			// An intent.list page token positioned at the first intent left out (spec §4.6), full
			// briefs only since intent.list has no delta. Keep going for the principal's own.
			if !slices.Contains(b.Truncated, "intents") {
				if !b.Delta {
					b.IntentsNextPageToken = encodePage(pos, filterHash("intent.list", work, uris(res), []string{"active"}, "", ""))
				}
				b.Truncated = append(b.Truncated, "intents")
			}
			continue
		}
		b.Intents = append(b.Intents, wrapIntent(p, i, in.Session))
		others++
	}
	all, err := tx.Notes(ws.ID, []string{"active"}, 0)
	if err != nil {
		return b, err
	}
	warn := now.Add(time.Duration(ws.Settings.NoteWarnDays) * 24 * time.Hour)
	for _, n := range all {
		if n.Provenance.Author.ID == p.Member.ID && n.ExpiresAt.Before(warn) {
			b.ExpiringNotes = append(b.ExpiringNotes, n)
		}
	}
	return b, nil
}
