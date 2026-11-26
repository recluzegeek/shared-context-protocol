package domain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The rules in this package are checked against the language-neutral vectors in
// ../../../conformance/vectors, which every SCP implementation is meant to pass.

func load(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "conformance", "vectors", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

type vecSide struct {
	Resource string  `json:"resource"`
	Scopes   []Scope `json:"scopes"`
}

type vecIntent struct {
	Member  string    `json:"member"`
	Session string    `json:"session"`
	Action  Action    `json:"action"`
	vecSide           // resource and scopes of the change side
	Uses    []vecSide `json:"uses"`
}

var resources = map[string]string{"R": "git:github.com/acme/api", "C": "git:github.com/acme/contracts"}

func (v vecIntent) view(t *testing.T, id string) IntentView {
	t.Helper()
	side := func(s vecSide) Side {
		out := Side{Resource: resources[s.Resource]}
		for _, sc := range s.Scopes {
			n, err := NormalizeScope(sc)
			if err != nil {
				t.Fatalf("normalize %+v: %v", sc, err)
			}
			out.Scopes = append(out.Scopes, n)
		}
		return out
	}
	iv := IntentView{ID: id, Member: v.Member, Session: v.Session, Action: v.Action, Change: side(v.vecSide)}
	for _, u := range v.Uses {
		iv.Uses = append(iv.Uses, side(u))
	}
	return iv
}

func TestOverlapVectors(t *testing.T) {
	var f struct {
		Cases []struct {
			Name    string    `json:"name"`
			Subject vecIntent `json:"subject"`
			Other   vecIntent `json:"other"`
			Want    *struct {
				Severity  Severity   `json:"severity"`
				Relations []Relation `json:"relations"`
			} `json:"want"`
		} `json:"cases"`
	}
	load(t, "overlap.json", &f)
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got := Overlaps(c.Subject.view(t, "subject"), []IntentView{c.Other.view(t, "other")})
			if c.Want == nil {
				if len(got) != 0 {
					t.Fatalf("want no overlap, got %+v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("want one overlap, got %d", len(got))
			}
			var rels []Relation
			for _, m := range got[0].Matches {
				if !slices.Contains(rels, m.Relation) {
					rels = append(rels, m.Relation)
				}
			}
			slices.Sort(rels)
			if got[0].Severity != c.Want.Severity || !slices.Equal(rels, c.Want.Relations) {
				t.Fatalf("got %s %v, want %s %v", got[0].Severity, rels, c.Want.Severity, c.Want.Relations)
			}
		})
	}
}

func TestCanonicalVectors(t *testing.T) {
	var f struct {
		Cases []struct {
			Input string  `json:"input"`
			Want  *string `json:"want"`
		} `json:"cases"`
	}
	load(t, "canonical.json", &f)
	for _, c := range f.Cases {
		got, err := CanonicalResource(c.Input, DefaultCaseInsensitiveHosts)
		switch {
		case c.Want == nil && err == nil:
			t.Errorf("%q: want rejection, got %q", c.Input, got)
		case c.Want != nil && (err != nil || got != *c.Want):
			t.Errorf("%q: got %q, %v; want %q", c.Input, got, err, *c.Want)
		}
	}
}

func TestScopeVectors(t *testing.T) {
	var f struct {
		Cases []struct {
			Scope Scope   `json:"scope"`
			Want  *string `json:"want"`
			Broad bool    `json:"broad"`
		} `json:"cases"`
	}
	load(t, "scopes.json", &f)
	for _, c := range f.Cases {
		got, err := NormalizeScope(c.Scope)
		switch {
		case c.Want == nil && err == nil:
			t.Errorf("%+v: want rejection, got %+v", c.Scope, got)
		case c.Want != nil && (err != nil || got.Value != *c.Want || got.Broad() != c.Broad):
			t.Errorf("%+v: got %q broad=%v, %v; want %q broad=%v", c.Scope, got.Value, got.Broad(), err, *c.Want, c.Broad)
		}
	}
}

func TestSearchVectors(t *testing.T) {
	var f struct {
		Tokenize []struct {
			Text   string   `json:"text"`
			Tokens []string `json:"tokens"`
		} `json:"tokenize"`
		Match []struct {
			Query string `json:"query"`
			Text  string `json:"text"`
			Match bool   `json:"match"`
		} `json:"match"`
	}
	load(t, "search.json", &f)
	for _, c := range f.Tokenize {
		if got := Tokenize(c.Text); !slices.Equal(got, c.Tokens) && len(got)+len(c.Tokens) > 0 {
			t.Errorf("Tokenize(%q) = %q, want %q", c.Text, got, c.Tokens)
		}
	}
	for _, c := range f.Match {
		if got := MatchesQuery(Tokenize(c.Query), Tokenize(c.Text)); got != c.Match {
			t.Errorf("%q in %q = %v, want %v", c.Query, c.Text, got, c.Match)
		}
	}
}

func TestSecretVectors(t *testing.T) {
	var f struct {
		Cases []struct {
			Text string  `json:"text"`
			Want *string `json:"want"`
		} `json:"cases"`
	}
	load(t, "secrets.json", &f)
	for _, c := range f.Cases {
		reject, _ := Scan(map[string]string{"body": c.Text}, ScanPolicy{})
		switch {
		case c.Want == nil && len(reject) > 0:
			t.Errorf("%q: want accepted, got %+v", c.Text, reject)
		case c.Want != nil && !slices.ContainsFunc(reject, func(f Finding) bool { return f.Detector == *c.Want }):
			t.Errorf("%q: want %s, got %+v", c.Text, *c.Want, reject)
		}
	}
}

// Owners may relax heuristic detectors, never format ones (spec §9.1).
func TestScanPolicy(t *testing.T) {
	warnAll := ScanPolicy{Modes: map[string]string{"assigned_secret": "warn", "jwt": "warn"}}
	reject, warn := Scan(map[string]string{"body": "password = Sup3rS3cretValue"}, warnAll)
	if len(reject) != 0 || len(warn) != 1 {
		t.Errorf("heuristic in warn mode: reject %v warn %v", reject, warn)
	}
	reject, _ = Scan(map[string]string{"body": "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijklmn"}, warnAll)
	if len(reject) != 1 {
		t.Errorf("format detector must reject even when set to warn: %v", reject)
	}
}
