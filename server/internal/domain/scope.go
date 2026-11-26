// Package domain holds SCP's rules that every implementation must reproduce exactly: scope
// normalization and matching, overlap detection, resource canonicalization, search tokens and
// secret detection. Standard library only.
package domain

import (
	"errors"
	"regexp"
	"strings"
)

// ScopeKind is what a scope names (spec §4.8).
type ScopeKind string

// Scope kinds.
const (
	KindPath   ScopeKind = "path"
	KindSymbol ScopeKind = "symbol"
	KindAPI    ScopeKind = "api"
	KindSchema ScopeKind = "schema"
	KindTopic  ScopeKind = "topic"
)

// Scope is a part of a resource an intent changes or relies on.
type Scope struct {
	Kind     ScopeKind `json:"kind"`
	Value    string    `json:"value"`
	Language string    `json:"language,omitempty"`
	To       string    `json:"to,omitempty"` // rename target, only on a rename intent's scopes
}

// Errors from normalization.
var (
	ErrUnknownKind   = errors.New("unknown scope kind")
	ErrEmptyScope    = errors.New("empty scope value")
	ErrWholeResource = errors.New("a path scope may not name the whole resource")
	ErrDotDot        = errors.New(`path scope may not contain ".." segments`)
)

var apiParam = regexp.MustCompile(`\{[^}/]*\}|:[^/]+`)

// NormalizeScope returns s in the normalized form of spec §6.2; To is normalized the same way.
func NormalizeScope(s Scope) (Scope, error) {
	v, err := normalizeValue(s.Kind, s.Value)
	if err != nil {
		return Scope{}, err
	}
	out := Scope{Kind: s.Kind, Value: v, Language: strings.TrimSpace(s.Language)}
	if s.To != "" {
		if out.To, err = normalizeValue(s.Kind, s.To); err != nil {
			return Scope{}, err
		}
	}
	return out, nil
}

func normalizeValue(kind ScopeKind, v string) (string, error) {
	if strings.TrimSpace(v) == "" {
		return "", ErrEmptyScope
	}
	switch kind {
	case KindPath:
		return normalizePath(v)
	case KindSymbol:
		return strings.TrimSpace(v), nil
	case KindAPI:
		return normalizeAPI(v), nil
	case KindSchema:
		return strings.ToLower(strings.TrimSpace(v)), nil
	case KindTopic:
		return strings.ToLower(strings.TrimSpace(v)), nil
	}
	return "", ErrUnknownKind
}

func normalizePath(v string) (string, error) {
	dir := strings.HasSuffix(v, "/")
	var segs []string
	for _, seg := range strings.Split(v, "/") {
		switch seg {
		case "", ".":
			continue
		case "..":
			return "", ErrDotDot
		}
		segs = append(segs, seg)
	}
	if len(segs) == 0 {
		return "", ErrWholeResource
	}
	out := strings.Join(segs, "/")
	if dir {
		out += "/"
	}
	return out, nil
}

// normalizeAPI handles the HTTP form ("METHOD /path") and leaves the RPC form unchanged.
func normalizeAPI(v string) string {
	v = strings.TrimSpace(v)
	method, path, ok := strings.Cut(v, " ")
	if !ok || !strings.HasPrefix(strings.TrimSpace(path), "/") {
		return v
	}
	path = apiParam.ReplaceAllString(strings.TrimSpace(path), "{}")
	if len(path) > 1 {
		path = strings.TrimSuffix(path, "/")
	}
	return strings.ToUpper(method) + " " + path
}

// Broad reports whether a normalized scope is broad (spec §6.2.1).
func (s Scope) Broad() bool {
	switch s.Kind {
	case KindPath:
		return strings.HasSuffix(s.Value, "/") && strings.Count(s.Value, "/") < 2
	case KindSymbol:
		return !strings.ContainsAny(s.Value, ".#/") && !strings.Contains(s.Value, "::")
	case KindAPI:
		return strings.HasPrefix(s.Value, "* ")
	case KindTopic:
		return true
	}
	return false
}

// MatchKind says how two scopes match.
type MatchKind string

// Match kinds.
const (
	Exact       MatchKind = "exact"
	Containment MatchKind = "containment"
)

// scopeMatch is the result of comparing two normalized scopes; container is the containing
// scope for a containment match.
type scopeMatch struct {
	kind      MatchKind
	container Scope
}

// matchScopes compares two normalized scopes, trying each one's rename target as well.
func matchScopes(a, b Scope) (scopeMatch, bool) {
	for _, av := range values(a) {
		for _, bv := range values(b) {
			x, y := a, b
			x.Value, y.Value = av, bv
			if m, ok := matchOne(x, y); ok {
				return m, true
			}
		}
	}
	return scopeMatch{}, false
}

func values(s Scope) []string {
	if s.To != "" {
		return []string{s.Value, s.To}
	}
	return []string{s.Value}
}

func matchOne(a, b Scope) (scopeMatch, bool) {
	if a.Kind != b.Kind {
		return scopeMatch{}, false
	}
	if a.Value == b.Value {
		if a.Kind == KindSymbol && a.Language != "" && b.Language != "" && a.Language != b.Language {
			return scopeMatch{}, false
		}
		return scopeMatch{kind: Exact}, true
	}
	switch a.Kind {
	case KindPath:
		return containedBy(a, b, func(outer, inner string) bool {
			return strings.HasSuffix(outer, "/") && strings.HasPrefix(inner, outer)
		})
	case KindSymbol:
		if a.Language != "" && b.Language != "" && a.Language != b.Language {
			return scopeMatch{}, false
		}
		return containedBy(a, b, func(outer, inner string) bool {
			if !strings.HasPrefix(inner, outer) {
				return false
			}
			rest := inner[len(outer):]
			return strings.HasPrefix(rest, ".") || strings.HasPrefix(rest, "::") ||
				strings.HasPrefix(rest, "#") || strings.HasPrefix(rest, "/")
		})
	case KindAPI:
		return containedBy(a, b, func(outer, inner string) bool {
			om, op, ok1 := strings.Cut(outer, " ")
			_, ip, ok2 := strings.Cut(inner, " ")
			return ok1 && ok2 && om == "*" && op == ip
		})
	case KindSchema:
		return containedBy(a, b, func(outer, inner string) bool {
			return strings.HasPrefix(inner, outer+".")
		})
	}
	return scopeMatch{}, false
}

func containedBy(a, b Scope, contains func(outer, inner string) bool) (scopeMatch, bool) {
	if contains(a.Value, b.Value) {
		return scopeMatch{kind: Containment, container: a}, true
	}
	if contains(b.Value, a.Value) {
		return scopeMatch{kind: Containment, container: b}, true
	}
	return scopeMatch{}, false
}
