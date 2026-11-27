// Package wire is SCP's wire-level conformance suite: it runs operations against a live server
// over MCP and checks what the spec says must happen. It knows nothing about any implementation.
package wire

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Target is the server under test and five credentials it issued: Owner (owner, admin set),
// Member (member, full set), Colleague (another member, essentials set), Guest (guest,
// essentials set) and Outsider (a member of a different workspace).
type Target struct {
	BaseURL   string // the server's origin; discovery is BaseURL/.well-known/sharedctx
	MCPURL    string
	ToolsFile string // path to conformance/tools.json
	Owner     string
	Member    string
	Colleague string
	Guest     string
	Outsider  string
}

// Run runs every check against tg.
func Run(t *testing.T, tg Target) {
	resource := "git:example.com/scp-conformance/" + randomHex()
	c := &suite{t: t, tg: tg, resource: resource}
	t.Run("tool lists follow profiles, sets and roles (§11.1.2.1)", c.toolSets)
	t.Run("an overlap reaches the other member, wrapped (§6, §8.2)", c.overlap)
	t.Run("intent.check without a session shows the principal's own intents (§5.5.7)", c.checkOwn)
	t.Run("wrapping is decided by the last writer (§8.2)", c.lastWriter)
	t.Run("brief deltas and cursor binding (§5.3.1)", c.briefDelta)
	t.Run("workspaces are isolated (§10.4)", c.isolation)
	t.Run("secrets are refused and never echoed (§9.1)", c.secrets)
	t.Run("client_request_id makes creates safe to retry (§3.3.1)", c.retries)
}

type suite struct {
	t        *testing.T
	tg       Target
	resource string
}

func randomHex() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- MCP plumbing ----

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

type agent struct {
	t  *testing.T
	cs *mcp.ClientSession
}

func (c *suite) as(token string) agent {
	c.t.Helper()
	cl := mcp.NewClient(&mcp.Implementation{Name: "scp-conformance", Version: "1"}, nil)
	cs, err := cl.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: c.tg.MCPURL, HTTPClient: &http.Client{Transport: bearer{token, http.DefaultTransport}},
	}, nil)
	if err != nil {
		c.t.Fatalf("connect: %v", err)
	}
	c.t.Cleanup(func() { _ = cs.Close() })
	return agent{c.t, cs}
}

// call returns the structured result (or {"error": ...}) and whether the tool reported an error.
func (a agent) call(name string, args map[string]any) (map[string]any, bool) {
	a.t.Helper()
	res, err := a.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		a.t.Fatalf("%s: %v", name, err)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out, res.IsError
}

func (a agent) tools() []string {
	a.t.Helper()
	var names []string
	for tl, err := range a.cs.Tools(context.Background(), nil) {
		if err != nil {
			a.t.Fatal(err)
		}
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	return names
}

func errCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func untrusted(v any) bool {
	m, _ := v.(map[string]any)
	return m["untrusted"] == true
}

func list(out map[string]any, key string) []any {
	l, _ := out[key].([]any)
	return l
}

// ---- checks ----

func (c *suite) toolSets(t *testing.T) {
	resp, err := http.Get(strings.TrimSuffix(c.tg.BaseURL, "/") + "/.well-known/sharedctx")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var disc struct {
		Conformance []string `json:"conformance"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&disc); err != nil {
		t.Fatalf("discovery: %v", err)
	}
	raw, err := os.ReadFile(c.tg.ToolsFile)
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Tools []struct {
			Name     string `json:"name"`
			Profile  string `json:"profile"`
			Set      string `json:"set"`
			ReadOnly bool   `json:"read_only"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatal(err)
	}
	rank := map[string]int{"essentials": 1, "full": 2, "admin": 3}
	expected := func(set string, guest bool) []string {
		var out []string
		for _, tl := range table.Tools {
			if slices.Contains(disc.Conformance, tl.Profile) && rank[tl.Set] <= rank[set] && (!guest || tl.ReadOnly) {
				out = append(out, tl.Name)
			}
		}
		slices.Sort(out)
		return out
	}
	for _, cred := range []struct {
		name, token, set string
		guest            bool
	}{
		{"owner (admin)", c.tg.Owner, "admin", false},
		{"member (full)", c.tg.Member, "full", false},
		{"colleague (essentials)", c.tg.Colleague, "essentials", false},
		{"guest (essentials)", c.tg.Guest, "essentials", true},
	} {
		got, want := c.as(cred.token).tools(), expected(cred.set, cred.guest)
		if !slices.Equal(got, want) {
			t.Errorf("%s: tools\n got  %v\n want %v", cred.name, got, want)
		}
	}
}

func (c *suite) announce(a agent, action, path, session string) map[string]any {
	c.t.Helper()
	args := map[string]any{
		"resource": map[string]any{"uri": c.resource}, "action": action, "summary": "conformance",
		"scopes": []any{map[string]any{"kind": "path", "value": path}}, "lease_minutes": 15,
	}
	if session != "" {
		args["session"] = session
	}
	out, isErr := a.call("sharedctx_intent_announce", args)
	if isErr {
		c.t.Fatalf("announce: %v", out)
	}
	return out
}

func (c *suite) overlap(t *testing.T) {
	member, colleague := c.as(c.tg.Member), c.as(c.tg.Colleague)
	c.announce(member, "change", "conf/overlap/secret-name.go", "s_member")
	out := c.announce(colleague, "remove", "conf/overlap/", "s_colleague")
	o := list(out, "overlaps")
	if len(o) != 1 {
		t.Fatalf("overlaps = %v", out)
	}
	ov := o[0].(map[string]any)
	if ov["severity"] != "high" || !untrusted(ov["intent"]) {
		t.Errorf("overlap = %v", ov)
	}
	for _, m := range list(ov, "matches") {
		if !untrusted(m.(map[string]any)["theirs"]) {
			t.Errorf("matched scope of the other member is not wrapped: %v", m)
		}
	}
	if strings.Contains(ov["reason"].(string), "secret-name") {
		t.Errorf("reason contains member-written text: %q", ov["reason"])
	}
	member.call("sharedctx_intent_close_session", map[string]any{"session": "s_member", "outcome": "abandoned"})
	colleague.call("sharedctx_intent_close_session", map[string]any{"session": "s_colleague", "outcome": "abandoned"})
}

func (c *suite) checkOwn(t *testing.T) {
	member := c.as(c.tg.Member)
	created := c.announce(member, "change", "conf/check/own.go", "")
	out, isErr := member.call("sharedctx_intent_check", map[string]any{
		"resource": map[string]any{"uri": c.resource}, "action": "change",
		"scopes": []any{map[string]any{"kind": "path", "value": "conf/check/own.go"}},
	})
	if isErr || len(list(out, "overlaps")) != 1 {
		t.Errorf("check without a session = %v", out)
	}
	id := created["intent"].(map[string]any)["id"]
	member.call("sharedctx_intent_close", map[string]any{"id": id, "outcome": "abandoned"})
}

func (c *suite) writeNote(a agent, body string) map[string]any {
	c.t.Helper()
	out, isErr := a.call("sharedctx_note_write", map[string]any{
		"kind": "context", "body": body, "resources": []any{map[string]any{"uri": c.resource}},
	})
	if isErr {
		c.t.Fatalf("note.write: %v", out)
	}
	return out["note"].(map[string]any)
}

func (c *suite) lastWriter(t *testing.T) {
	member, owner := c.as(c.tg.Member), c.as(c.tg.Owner)
	n := c.writeNote(member, "conformance: written by the member")
	got, _ := member.call("sharedctx_note_get", map[string]any{"id": n["id"]})
	if untrusted(got["note"]) {
		t.Errorf("the member's own unedited note came back wrapped: %v", got)
	}
	if _, isErr := owner.call("sharedctx_note_update", map[string]any{
		"id": n["id"], "expected_revision": n["revision"], "body": "conformance: edited by the owner",
	}); isErr {
		t.Fatal("owner could not edit the note")
	}
	got, _ = member.call("sharedctx_note_get", map[string]any{"id": n["id"]})
	if !untrusted(got["note"]) {
		t.Errorf("a note last edited by the owner must be wrapped for its author: %v", got)
	}
}

func (c *suite) briefDelta(t *testing.T) {
	member, colleague := c.as(c.tg.Member), c.as(c.tg.Colleague)
	res := []any{map[string]any{"uri": c.resource}}
	first, isErr := member.call("sharedctx_brief_get", map[string]any{"resources": res})
	if isErr || first["cursor"] == "" {
		t.Fatalf("brief = %v", first)
	}
	quiet, _ := member.call("sharedctx_brief_get", map[string]any{"resources": res, "since": first["cursor"]})
	if quiet["delta"] != true || len(list(quiet, "notes")) != 0 {
		t.Errorf("delta with no change = %v", quiet)
	}
	n := c.writeNote(colleague, "conformance: a change for the delta")
	delta, _ := member.call("sharedctx_brief_get", map[string]any{"resources": res, "since": first["cursor"]})
	notes := list(delta, "notes")
	if len(notes) != 1 || notes[0].(map[string]any)["value"].(map[string]any)["id"] != n["id"] {
		t.Errorf("delta after a colleague's note = %v", delta)
	}
	if _, ok := delta["expiring_notes"].([]any); !ok {
		t.Errorf("expiring_notes is part of every brief: %v", delta)
	}
	moved, isErr := member.call("sharedctx_brief_get", map[string]any{
		"resources": []any{map[string]any{"uri": c.resource + "-elsewhere"}}, "since": first["cursor"],
	})
	details, _ := moved["error"].(map[string]any)["details"].(map[string]any)
	if !isErr || errCode(moved) != "FAILED_PRECONDITION" || details["reason"] != "cursor_filter_mismatch" {
		t.Errorf("cursor reused with another filter = %v", moved)
	}
}

func (c *suite) isolation(t *testing.T) {
	member, outsider := c.as(c.tg.Member), c.as(c.tg.Outsider)
	n := c.writeNote(member, "conformance: isolation probe")
	got, isErr := outsider.call("sharedctx_note_get", map[string]any{"id": n["id"]})
	if !isErr || errCode(got) != "NOT_FOUND" {
		t.Errorf("another workspace read the note: %v", got)
	}
	found, _ := outsider.call("sharedctx_note_search", map[string]any{"query": "isolation probe"})
	if len(list(found, "notes")) != 0 {
		t.Errorf("another workspace found the note: %v", found)
	}
}

func (c *suite) secrets(t *testing.T) {
	const key = "AKIAABCDEFGHIJKLMNOP"
	out, isErr := c.as(c.tg.Member).call("sharedctx_note_write", map[string]any{"kind": "gotcha", "body": "key " + key})
	raw, _ := json.Marshal(out)
	if !isErr || errCode(out) != "SECRET_DETECTED" || strings.Contains(string(raw), key) {
		t.Errorf("secret note = %s", raw)
	}
}

func (c *suite) retries(t *testing.T) {
	member := c.as(c.tg.Member)
	id := "conf-" + randomHex()
	args := map[string]any{"kind": "context", "body": "conformance: retried", "client_request_id": id}
	first, _ := member.call("sharedctx_note_write", args)
	again, _ := member.call("sharedctx_note_write", args)
	if first["note"].(map[string]any)["id"] != again["note"].(map[string]any)["id"] {
		t.Errorf("a retry created a second note")
	}
	args["body"] = "conformance: different"
	if out, isErr := member.call("sharedctx_note_write", args); !isErr || errCode(out) != "CONFLICT" {
		t.Errorf("same client_request_id, different parameters = %v", out)
	}
}
