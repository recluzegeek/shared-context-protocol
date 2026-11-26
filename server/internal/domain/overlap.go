package domain

import (
	"cmp"
	"slices"
)

// Action is what an intent does to its change side (spec §4.4); ActionUse is the implicit
// action of a use side.
type Action string

// Actions.
const (
	ActionAdd     Action = "add"
	ActionChange  Action = "change"
	ActionRemove  Action = "remove"
	ActionReplace Action = "replace"
	ActionRename  Action = "rename"
	ActionUse     Action = "use"
)

// Severity of an overlap (spec §6.3).
type Severity string

// Severities, ordered by rank.
const (
	SeverityLow    Severity = "low"
	SeverityNormal Severity = "normal"
	SeverityHigh   Severity = "high"
)

// Rank orders severities: low < normal < high.
func (s Severity) Rank() int {
	switch s {
	case SeverityHigh:
		return 3
	case SeverityNormal:
		return 2
	case SeverityLow:
		return 1
	}
	return 0
}

// Relation says which sides of two intents matched (spec §6.1).
type Relation string

// Relations.
const (
	RelChange   Relation = "change"
	RelBreaks   Relation = "breaks"
	RelBrokenBy Relation = "broken_by"
)

// Side is a resource and the scopes of it an intent changes or uses. Scopes are normalized and
// Resource is canonical.
type Side struct {
	Resource string
	Scopes   []Scope
}

// IntentView is what overlap detection needs to know about an active intent.
type IntentView struct {
	ID      string
	Member  string
	Session string
	Action  Action
	Change  Side
	Uses    []Side
}

// ScopePair is one matching pair of scopes.
type ScopePair struct {
	Relation Relation
	Resource string
	Mine     Scope
	Theirs   Scope
	Match    MatchKind
	Broad    bool // a broad or topic cap applied to this pair
}

// Overlap of the subject intent with another active intent.
type Overlap struct {
	Other    IntentView
	Severity Severity
	Matches  []ScopePair
}

// Overlaps returns every overlap of subject with others, highest severity first, then by the
// other intent's id. It is deterministic and depends on nothing but its arguments.
func Overlaps(subject IntentView, others []IntentView) []Overlap {
	var out []Overlap
	for _, other := range others {
		if other.ID == subject.ID || sameWork(subject, other) {
			continue
		}
		if o, ok := overlapWith(subject, other); ok {
			out = append(out, o)
		}
	}
	slices.SortFunc(out, func(a, b Overlap) int {
		if c := cmp.Compare(b.Severity.Rank(), a.Severity.Rank()); c != 0 {
			return c
		}
		return cmp.Compare(a.Other.ID, b.Other.ID)
	})
	return out
}

// sameWork: a member's intents in one session (or with no session on either) never overlap.
func sameWork(a, b IntentView) bool {
	return a.Member == b.Member && a.Session == b.Session
}

func overlapWith(subject, other IntentView) (Overlap, bool) {
	o := Overlap{Other: other}
	best := 0
	add := func(rel Relation, mine, theirs Side, myAction, theirAction Action) {
		if mine.Resource != theirs.Resource {
			return
		}
		for _, ms := range mine.Scopes {
			for _, ts := range theirs.Scopes {
				m, ok := matchScopes(ms, ts)
				if !ok {
					continue
				}
				sev, ok := severity(myAction, theirAction, m.kind)
				if !ok {
					continue
				}
				capped := false
				if (m.kind == Containment && m.container.Broad()) || (ms.Kind == KindTopic && ts.Kind == KindTopic) {
					if sev.Rank() > SeverityLow.Rank() {
						sev = SeverityLow
					}
					capped = true
				}
				o.Matches = append(o.Matches, ScopePair{
					Relation: rel, Resource: mine.Resource, Mine: ms, Theirs: ts, Match: m.kind, Broad: capped,
				})
				if sev.Rank() > best {
					best, o.Severity = sev.Rank(), sev
				}
			}
		}
	}
	add(RelChange, subject.Change, other.Change, subject.Action, other.Action)
	for _, u := range other.Uses {
		add(RelBreaks, subject.Change, u, subject.Action, ActionUse)
	}
	for _, u := range subject.Uses {
		add(RelBrokenBy, u, other.Change, ActionUse, other.Action)
	}
	return o, len(o.Matches) > 0
}

// severity is the table of spec §6.3; ok is false for "no overlap".
func severity(a, b Action, m MatchKind) (Severity, bool) {
	exact := m == Exact
	pick := func(onExact, onContain Severity) (Severity, bool) {
		if exact {
			return onExact, true
		}
		return onContain, true
	}
	destructive := func(x Action) bool { return x == ActionRemove || x == ActionReplace || x == ActionRename }
	has := func(x Action) bool { return a == x || b == x }
	switch {
	case destructive(a) || destructive(b):
		return SeverityHigh, true
	case a == ActionChange && b == ActionChange:
		return pick(SeverityHigh, SeverityNormal)
	case has(ActionChange) && has(ActionUse):
		return pick(SeverityNormal, SeverityLow)
	case has(ActionChange) && has(ActionAdd):
		return SeverityNormal, true
	case a == ActionAdd && b == ActionAdd:
		return pick(SeverityNormal, SeverityLow)
	}
	return "", false // add + use, use + use
}
