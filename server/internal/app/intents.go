package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/recluzegeek/shared-context-protocol/server/internal/domain"
)

var actions = []domain.Action{domain.ActionAdd, domain.ActionChange, domain.ActionRemove, domain.ActionReplace, domain.ActionRename}

// IntentParams are intent.announce's and intent.check's parameters (spec §5.5.1, §5.5.7).
type IntentParams struct {
	Resource        Resource       `json:"resource" jsonschema:"the repository being changed, e.g. its git remote URL"`
	Scopes          []domain.Scope `json:"scopes" jsonschema:"the narrowest scopes covering the change: symbol, api or schema before path; files before directories"`
	Action          domain.Action  `json:"action" jsonschema:"add, change, remove, replace or rename"`
	Summary         string         `json:"summary,omitempty" jsonschema:"one line"`
	Uses            []Dependency   `json:"uses,omitempty" jsonschema:"contracts the change relies on without changing"`
	Work            *Reference     `json:"work,omitempty"`
	Session         string         `json:"session,omitempty" jsonschema:"your session id from session.start"`
	LeaseMinutes    int            `json:"lease_minutes,omitempty" jsonschema:"keep the intent active this long without heartbeats"`
	ClientRequestID string         `json:"client_request_id,omitempty"`
	Agent           *AgentInfo     `json:"agent,omitempty"`
}

// OverlapOut is an overlap as returned (spec §6.4).
type OverlapOut struct {
	Intent   any        `json:"intent"`
	Severity string     `json:"severity"`
	Matches  []MatchOut `json:"matches"`
	Reason   string     `json:"reason"`
}

// MatchOut is one matching scope pair; theirs is always wrapped.
type MatchOut struct {
	Relation string       `json:"relation"`
	Resource Resource     `json:"resource"`
	Mine     domain.Scope `json:"mine"`
	Theirs   Untrusted    `json:"theirs"`
	Match    string       `json:"match"`
	Broad    bool         `json:"broad"`
}

// IntentResult is announce's, update's and check's result.
type IntentResult struct {
	Intent   *Intent      `json:"intent,omitempty"`
	Overlaps []OverlapOut `json:"overlaps"`
	Warnings []Warning    `json:"warnings,omitempty"`
}

// prepare validates and normalizes intent parameters and returns its warnings.
func (s *Service) prepare(ws Workspace, in *IntentParams, needSummary bool) ([]Warning, error) {
	if !slices.Contains(actions, in.Action) {
		return nil, invalid("action", "add, change, remove, replace or rename")
	}
	if needSummary && strings.TrimSpace(in.Summary) == "" {
		return nil, invalid("summary", "required")
	}
	if err := limit("summary", in.Summary, Limits["intent_summary"]); err != nil {
		return nil, err
	}
	if len(in.Scopes) == 0 {
		return nil, invalid("scopes", "at least one")
	}
	if len(in.Scopes) > Limits["scopes"] || len(in.Uses) > Limits["uses"] {
		return nil, &Error{Code: CodeContentTooLarge, Message: "too many scopes or uses"}
	}
	warnings, err := checkContent(in, ws)
	if err != nil {
		return nil, err
	}
	res, err := s.canonical(in.Resource, ws, "resource")
	if err != nil {
		return nil, err
	}
	in.Resource = res
	topics := 0
	for i, sc := range in.Scopes {
		if sc.To != "" && in.Action != domain.ActionRename {
			return nil, invalid(fmt.Sprintf("scopes[%d].to", i), "only on a rename")
		}
		n, err := domain.NormalizeScope(sc)
		if err != nil {
			return nil, invalid(fmt.Sprintf("scopes[%d]", i), err.Error())
		}
		in.Scopes[i] = n
		if n.Broad() {
			warnings = append(warnings, Warning{Code: "broad_scope", Scope: &in.Scopes[i]})
		}
		if n.Kind == domain.KindTopic {
			topics++
		}
	}
	if topics == len(in.Scopes) {
		warnings = append(warnings, Warning{Code: "topic_only"})
	}
	for i := range in.Uses {
		u := &in.Uses[i]
		if u.Resource, err = s.canonical(u.Resource, ws, fmt.Sprintf("uses[%d].resource", i)); err != nil {
			return nil, err
		}
		if len(u.Scopes) == 0 || len(u.Scopes) > Limits["scopes"] {
			return nil, invalid(fmt.Sprintf("uses[%d].scopes", i), "1 to the scope limit")
		}
		for j, sc := range u.Scopes {
			if sc.To != "" {
				return nil, invalid(fmt.Sprintf("uses[%d].scopes[%d].to", i, j), "not allowed on a dependency")
			}
			if u.Scopes[j], err = domain.NormalizeScope(sc); err != nil {
				return nil, invalid(fmt.Sprintf("uses[%d].scopes[%d]", i, j), err.Error())
			}
		}
	}
	if in.Session == "" {
		warnings = append(warnings, Warning{Code: "no_session"})
	}
	if max := ws.Settings.IntentTTLHours * 60; in.LeaseMinutes > max {
		warnings = append(warnings, Warning{Code: "lease_capped", Detail: fmt.Sprint(max)})
		in.LeaseMinutes = max
	}
	if in.LeaseMinutes < 0 {
		return nil, invalid("lease_minutes", "positive")
	}
	return warnings, nil
}

// expiry: a lease, when set, decides expiry on its own; otherwise heartbeat + TTL (spec §4.4.1).
func expiry(at time.Time, leaseMinutes int, ws Workspace) time.Time {
	if leaseMinutes > 0 {
		return at.Add(time.Duration(leaseMinutes) * time.Minute)
	}
	return at.Add(time.Duration(ws.Settings.IntentTTLHours) * time.Hour)
}

// overlapsFor computes subject's overlaps with the workspace's other active intents.
func (s *Service) overlapsFor(tx Tx, p Principal, ws Workspace, subject Intent) ([]OverlapOut, error) {
	active, err := tx.Intents(ws.ID, []string{"active"}, 0)
	if err != nil {
		return nil, err
	}
	byID := map[string]Intent{}
	views := make([]domain.IntentView, 0, len(active))
	for _, i := range active {
		byID[i.ID] = i
		views = append(views, i.View())
	}
	var out []OverlapOut
	for _, o := range domain.Overlaps(subject.View(), views) {
		other := byID[o.Other.ID]
		author := other.Provenance.UpdatedBy
		oo := OverlapOut{
			Intent:   Untrusted{Untrusted: true, Source: "member", Author: &author, Value: other},
			Severity: string(o.Severity),
		}
		for _, m := range o.Matches {
			oo.Matches = append(oo.Matches, MatchOut{
				Relation: string(m.Relation), Resource: Resource{URI: m.Resource}, Mine: m.Mine,
				Theirs: Untrusted{Untrusted: true, Source: "member", Author: &author, Value: m.Theirs},
				Match:  string(m.Match), Broad: m.Broad,
			})
		}
		oo.Reason = reason(other, o)
		out = append(out, oo)
	}
	return nonNil(out), nil
}

// reason is built only from server-controlled parts: handle, action, kinds (spec §6.4, §8.4).
func reason(other Intent, o domain.Overlap) string {
	if len(o.Matches) == 0 {
		return other.Provenance.Author.Handle + " has an overlapping intent"
	}
	m := o.Matches[0]
	for _, x := range o.Matches {
		if x.Match == domain.Exact {
			m = x
			break
		}
	}
	verb := map[domain.Action]string{"add": "add to", "change": "change", "remove": "remove", "replace": "replace", "rename": "rename"}
	theirs := verb[other.Action]
	mine := "will change"
	switch m.Relation {
	case domain.RelBreaks:
		theirs, mine = "rely on", "will change"
	case domain.RelBrokenBy:
		mine = "rely on"
	}
	rel := "the same " + string(m.Mine.Kind) + " as"
	if m.Match == domain.Containment {
		rel = "a " + string(m.Theirs.Kind) + " overlapping"
	}
	return fmt.Sprintf("%s will %s %s a %s you %s", other.Provenance.Author.Handle, theirs, rel, m.Mine.Kind, mine)
}

// IntentAnnounce implements intent.announce.
func (s *Service) IntentAnnounce(ctx context.Context, p Principal, in IntentParams) (IntentResult, error) {
	var out IntentResult
	err := s.store.Tx(ctx, func(tx Tx) error {
		ws, err := view(tx, p)
		if err != nil {
			return err
		}
		if err := canWrite(p); err != nil {
			return err
		}
		out, err = once(tx, p, in.ClientRequestID, in, s.Now(), func() (IntentResult, error) {
			warnings, err := s.prepare(ws, &in, true)
			if err != nil {
				return IntentResult{}, err
			}
			seq, err := tx.NextSeq(ws.ID)
			if err != nil {
				return IntentResult{}, err
			}
			now := s.Now()
			ref := p.Member.Ref()
			i := Intent{
				ID: "i_" + s.NewID(), WorkspaceID: ws.ID, Resource: in.Resource, Scopes: in.Scopes,
				Action: in.Action, Uses: nonNil(in.Uses), Summary: in.Summary, Work: in.Work, Session: in.Session,
				Status: "active", HeartbeatAt: now, LeaseMinutes: in.LeaseMinutes,
				ExpiresAt: expiry(now, in.LeaseMinutes, ws), Revision: 1, Seq: seq,
				Provenance: Provenance{Author: ref, Agent: in.Agent, CreatedAt: now, UpdatedAt: now, UpdatedBy: ref},
			}
			if err := tx.SaveIntent(i); err != nil {
				return IntentResult{}, err
			}
			overlaps, err := s.overlapsFor(tx, p, ws, i)
			if err != nil {
				return IntentResult{}, err
			}
			return IntentResult{Intent: &i, Overlaps: overlaps, Warnings: warnings},
				tx.Audit(AuditEntry{ws.ID, p.Member.ID, "intent.announce", i.ID, "", now})
		})
		return err
	})
	return out, err
}

// IntentCheck implements intent.check: the overlaps an announce would get, creating nothing.
func (s *Service) IntentCheck(ctx context.Context, p Principal, in IntentParams) (IntentResult, error) {
	var out IntentResult
	err := s.store.Tx(ctx, func(tx Tx) error {
		ws, err := view(tx, p)
		if err != nil {
			return err
		}
		warnings, err := s.prepare(ws, &in, false)
		if err != nil {
			return err
		}
		// Without a session the probe is a new session, so it shows the principal's own intents
		// instead of hiding them as the same work (spec §5.5.7).
		session := in.Session
		if session == "" {
			session = "\x00check"
		}
		probe := Intent{
			ID: "", Resource: in.Resource, Scopes: in.Scopes, Action: in.Action, Uses: in.Uses,
			Session: session, Provenance: Provenance{Author: p.Member.Ref()},
		}
		overlaps, err := s.overlapsFor(tx, p, ws, probe)
		if err != nil {
			return err
		}
		out = IntentResult{Overlaps: overlaps, Warnings: warnings}
		return tx.Audit(AuditEntry{ws.ID, p.Member.ID, "intent.check", "", scopeAudit(in.Resource, in.Scopes), s.Now()})
	})
	return out, err
}

// scopeAudit records the scopes queried (spec §17, probing in-flight work).
func scopeAudit(r Resource, scopes []domain.Scope) string {
	parts := []string{r.URI}
	for _, sc := range scopes {
		parts = append(parts, string(sc.Kind)+":"+sc.Value)
	}
	return strings.Join(parts, " ")
}

// IntentUpdateParams are intent.update's parameters (spec §5.5.3).
type IntentUpdateParams struct {
	ID               string         `json:"id"`
	ExpectedRevision int            `json:"expected_revision"`
	Scopes           []domain.Scope `json:"scopes,omitempty"`
	Action           domain.Action  `json:"action,omitempty"`
	Summary          *string        `json:"summary,omitempty"`
	Uses             []Dependency   `json:"uses,omitempty"`
	Work             *Reference     `json:"work,omitempty"`
	LeaseMinutes     int            `json:"lease_minutes,omitempty" jsonschema:"a new lease from now"`
}

// IntentUpdate implements intent.update.
func (s *Service) IntentUpdate(ctx context.Context, p Principal, in IntentUpdateParams) (IntentResult, error) {
	var out IntentResult
	err := s.store.Tx(ctx, func(tx Tx) error {
		ws, i, err := s.ownActive(tx, p, in.ID)
		if err != nil {
			return err
		}
		if i.Revision != in.ExpectedRevision {
			return &Error{Code: CodeConflict, Message: "revision changed", Details: map[string]any{"current_revision": i.Revision}}
		}
		params := IntentParams{
			Resource: i.Resource, Scopes: i.Scopes, Action: i.Action, Summary: i.Summary,
			Uses: i.Uses, Work: i.Work, Session: i.Session, LeaseMinutes: i.LeaseMinutes,
		}
		if in.Scopes != nil {
			params.Scopes = in.Scopes
		}
		if in.Action != "" {
			params.Action = in.Action
		}
		if in.Summary != nil {
			params.Summary = *in.Summary
		}
		if in.Uses != nil {
			params.Uses = in.Uses
		}
		if in.Work != nil {
			params.Work = in.Work
		}
		if in.LeaseMinutes > 0 {
			params.LeaseMinutes = in.LeaseMinutes
		}
		warnings, err := s.prepare(ws, &params, true)
		if err != nil {
			return err
		}
		now := s.Now()
		i.Scopes, i.Action, i.Summary, i.Uses, i.Work = params.Scopes, params.Action, params.Summary, nonNil(params.Uses), params.Work
		if in.LeaseMinutes > 0 {
			i.LeaseMinutes, i.HeartbeatAt = params.LeaseMinutes, now
			i.ExpiresAt = expiry(now, i.LeaseMinutes, ws)
		}
		if i.Seq, err = tx.NextSeq(ws.ID); err != nil {
			return err
		}
		i.Revision++
		i.Provenance.UpdatedAt = now
		if err := tx.SaveIntent(i); err != nil {
			return err
		}
		overlaps, err := s.overlapsFor(tx, p, ws, i)
		out = IntentResult{Intent: &i, Overlaps: overlaps, Warnings: warnings}
		return err
	})
	return out, err
}

func (s *Service) ownActive(tx Tx, p Principal, id string) (Workspace, Intent, error) {
	ws, err := view(tx, p)
	if err != nil {
		return ws, Intent{}, err
	}
	i, err := tx.Intent(ws.ID, id)
	if err != nil {
		return ws, i, errNotFound
	}
	if i.Provenance.Author.ID != p.Member.ID {
		return ws, i, errorf(CodePermissionDenied, "only the author can change an intent")
	}
	if i.Status != "active" {
		return ws, i, &Error{Code: CodeFailedPrecondition, Message: "intent is not active", Details: map[string]any{"status": i.Status}}
	}
	return ws, i, nil
}

// HeartbeatResult reports each intent's current state and overlaps.
type HeartbeatResult struct {
	Intents []IntentResult    `json:"intents"`
	Errors  map[string]*Error `json:"errors,omitempty"`
}

// IntentHeartbeat implements intent.heartbeat for ids or a whole session. It moves timestamps
// only, never the revision (spec §3.3).
func (s *Service) IntentHeartbeat(ctx context.Context, p Principal, ids []string, session string) (HeartbeatResult, error) {
	out := HeartbeatResult{Intents: []IntentResult{}}
	err := s.store.Tx(ctx, func(tx Tx) error {
		ws, err := view(tx, p)
		if err != nil {
			return err
		}
		if session != "" {
			mine, err := s.sessionIntents(tx, p, ws, session)
			if err != nil {
				return err
			}
			for _, i := range mine {
				ids = append(ids, i.ID)
			}
		}
		if len(ids) > 50 {
			return invalid("ids", "at most 50")
		}
		now := s.Now()
		for _, id := range ids {
			_, i, err := s.ownActive(tx, p, id)
			if err != nil {
				if out.Errors == nil {
					out.Errors = map[string]*Error{}
				}
				out.Errors[id] = asError(err)
				continue
			}
			i.HeartbeatAt = now
			i.ExpiresAt = expiry(now, i.LeaseMinutes, ws)
			if err := tx.SaveIntent(i); err != nil {
				return err
			}
			overlaps, err := s.overlapsFor(tx, p, ws, i)
			if err != nil {
				return err
			}
			out.Intents = append(out.Intents, IntentResult{Intent: &i, Overlaps: overlaps})
		}
		return nil
	})
	return out, err
}

func (s *Service) sessionIntents(tx Tx, p Principal, ws Workspace, session string) ([]Intent, error) {
	all, err := tx.Intents(ws.ID, []string{"active"}, 0)
	var out []Intent
	for _, i := range all {
		if i.Provenance.Author.ID == p.Member.ID && i.Session == session {
			out = append(out, i)
		}
	}
	return out, err
}

// IntentClose implements intent.close and intent.close_session.
func (s *Service) IntentClose(ctx context.Context, p Principal, id, session, outcome string) ([]Intent, error) {
	if outcome != "completed" && outcome != "abandoned" {
		return nil, invalid("outcome", "completed or abandoned")
	}
	out := []Intent{}
	err := s.store.Tx(ctx, func(tx Tx) error {
		ws, err := view(tx, p)
		if err != nil {
			return err
		}
		var targets []Intent
		if session != "" {
			if targets, err = s.sessionIntents(tx, p, ws, session); err != nil {
				return err
			}
		} else {
			i, err := tx.Intent(ws.ID, id)
			if err != nil {
				return errNotFound
			}
			if p.Member.Role != "owner" && i.Provenance.Author.ID != p.Member.ID {
				return errorf(CodePermissionDenied, "only the author or an owner can close an intent")
			}
			if i.Status != "active" {
				return &Error{Code: CodeFailedPrecondition, Message: "intent is not active", Details: map[string]any{"status": i.Status}}
			}
			targets = []Intent{i}
		}
		now := s.Now()
		for _, i := range targets {
			seq, err := tx.NextSeq(ws.ID)
			if err != nil {
				return err
			}
			i.Status, i.ClosedAt, i.Seq = outcome, &now, seq
			i.Revision++
			i.Provenance.UpdatedAt, i.Provenance.UpdatedBy = now, p.Member.Ref()
			if err := tx.SaveIntent(i); err != nil {
				return err
			}
			out = append(out, i)
		}
		return tx.Audit(AuditEntry{ws.ID, p.Member.ID, "intent.close", id, session, now})
	})
	return out, err
}

// IntentListParams are intent.list's parameters (spec §5.5.6).
type IntentListParams struct {
	Resources []Resource `json:"resources,omitempty" jsonschema:"matches the change side or any uses side"`
	Work      *Reference `json:"work,omitempty"`
	Author    string     `json:"author,omitempty"`
	Session   string     `json:"session,omitempty"`
	Status    []string   `json:"status,omitempty" jsonschema:"default [active]"`
	PageSize  int        `json:"page_size,omitempty"`
	PageToken string     `json:"page_token,omitempty"`
}

// IntentList is a page of intents; other members' are wrapped.
type IntentList struct {
	Intents       []any  `json:"intents"`
	NextPageToken string `json:"next_page_token,omitempty"`
}

// IntentListOp implements intent.list.
func (s *Service) IntentListOp(ctx context.Context, p Principal, in IntentListParams) (IntentList, error) {
	var out IntentList
	err := s.store.Tx(ctx, func(tx Tx) error {
		ws, err := view(tx, p)
		if err != nil {
			return err
		}
		matched, filter, err := s.listIntents(tx, ws, in)
		if err != nil {
			return err
		}
		offset, err := decodePage(in.PageToken, filter)
		if err != nil {
			return err
		}
		pg, next := page(matched, offset, pageSize(in.PageSize), filter)
		out = IntentList{Intents: make([]any, 0, len(pg)), NextPageToken: next}
		for _, i := range pg {
			out.Intents = append(out.Intents, wrapIntent(p, i, ""))
		}
		return tx.Audit(AuditEntry{ws.ID, p.Member.ID, "intent.list", "", strings.Join(uris(in.Resources), " "), s.Now()})
	})
	return out, err
}

func (s *Service) listIntents(tx Tx, ws Workspace, in IntentListParams) ([]Intent, string, error) {
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
	all, err := tx.Intents(ws.ID, status, 0)
	if err != nil {
		return nil, "", err
	}
	var out []Intent
	for _, i := range all {
		if in.Author != "" && i.Provenance.Author.ID != in.Author || in.Session != "" && i.Session != in.Session ||
			!intentMatches(i, work, res) {
			continue
		}
		out = append(out, i)
	}
	sortIntents(out)
	return out, filterHash("intent.list", work, uris(res), status, in.Author, in.Session), nil
}

// sortIntents is intent.list's order, newest first then id; the brief uses it too, so a brief's
// continuation offset points into the same list.
func sortIntents(is []Intent) {
	slices.SortFunc(is, func(a, b Intent) int {
		if c := b.Provenance.CreatedAt.Compare(a.Provenance.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
}

// intentMatches is the brief's intent filter: change or uses resource, or the same work.
func intentMatches(i Intent, work string, res []Resource) bool {
	if work == "" && len(res) == 0 {
		return true
	}
	if work != "" && i.Work != nil && i.Work.URI == work {
		return true
	}
	in := func(uri string) bool { return slices.ContainsFunc(res, func(r Resource) bool { return r.URI == uri }) }
	if in(i.Resource.URI) {
		return true
	}
	return slices.ContainsFunc(i.Uses, func(d Dependency) bool { return in(d.Resource.URI) })
}

// wrapIntent leaves an intent unwrapped only for its author in the same session (spec §8.2).
func wrapIntent(p Principal, i Intent, session string) any {
	if i.Provenance.Author.ID == p.Member.ID && i.Provenance.UpdatedBy.ID == p.Member.ID && (session == "" || session == i.Session) {
		return i
	}
	by := i.Provenance.UpdatedBy
	return Untrusted{Untrusted: true, Source: "member", Author: &by, Value: i}
}

// LeaseWarnings are lease_expiring warnings for the principal's intents (spec §5.5.4).
func (s *Service) LeaseWarnings(ctx context.Context, p Principal) []Warning {
	var out []Warning
	_ = s.store.Tx(ctx, func(tx Tx) error {
		all, err := tx.Intents(p.Member.WorkspaceID, []string{"active"}, 0)
		now := s.Now()
		for _, i := range all {
			if i.Provenance.Author.ID != p.Member.ID {
				continue
			}
			window := 15 * time.Minute // a fifth of the lease, at most 15 minutes (spec §5.5.4)
			if i.LeaseMinutes > 0 {
				window = min(window, time.Duration(i.LeaseMinutes)*time.Minute/5)
			}
			if i.ExpiresAt.Sub(now) <= window {
				out = append(out, Warning{Code: "lease_expiring", Detail: i.ID + " expires at " + i.ExpiresAt.Format(time.RFC3339)})
			}
		}
		return err
	})
	return out
}

// Sweep expires notes and intents past their expiry, bumping revision and seq (spec §7).
func (s *Service) Sweep(ctx context.Context) error {
	now := s.Now()
	return s.store.Tx(ctx, func(tx Tx) error {
		notes, err := tx.DueNotes(now)
		if err != nil {
			return err
		}
		for _, n := range notes {
			if n.Seq, err = tx.NextSeq(n.WorkspaceID); err != nil {
				return err
			}
			n.Status = "expired"
			n.Revision++
			if err := tx.SaveNote(n); err != nil {
				return err
			}
		}
		intents, err := tx.DueIntents(now)
		if err != nil {
			return err
		}
		for _, i := range intents {
			if i.Seq, err = tx.NextSeq(i.WorkspaceID); err != nil {
				return err
			}
			i.Status, i.ClosedAt = "expired", &now
			i.Revision++
			if err := tx.SaveIntent(i); err != nil {
				return err
			}
		}
		return nil
	})
}
