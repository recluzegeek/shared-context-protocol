package mcpbind

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/recluzegeek/shared-context-protocol/server/internal/adapters/sqlite"
	"github.com/recluzegeek/shared-context-protocol/server/internal/app"
)

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

func connect(t *testing.T, url, token string) agent {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil)
	cs, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: url, HTTPClient: &http.Client{Transport: bearer{token, http.DefaultTransport}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return agent{t, cs}
}

// call runs a tool and returns its structured content and whether it was an error.
func (a agent) call(name string, args any) (map[string]any, bool) {
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
	return names
}

func setup(t *testing.T) (url string, alice, bob, guest string) {
	t.Helper()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "scp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := app.NewService(store, []byte("0123456789abcdef0123456789abcdef"))
	ctx := context.Background()
	w, _, err := svc.CreateWorkspace(ctx, "Acme", "alice", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddMember(ctx, w.ID, "bob", "Bob", "member"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddMember(ctx, w.ID, "gina", "Gina", "guest"); err != nil {
		t.Fatal(err)
	}
	alice, _ = svc.IssueToken(ctx, w.ID, "alice", "laptop", app.SetEssentials)
	bob, _ = svc.IssueToken(ctx, w.ID, "bob", "desktop", app.SetFull)
	guest, _ = svc.IssueToken(ctx, w.ID, "gina", "", app.SetEssentials)
	srv := httptest.NewServer(New(svc, slog.New(slog.DiscardHandler)).Handler(nil))
	t.Cleanup(srv.Close)
	return srv.URL, alice, bob, guest
}

const repo = "https://github.com/Acme/Shop.git"

// Two members' agents collide through the server: the whole point of SCP.
func TestTwoAgentsCollide(t *testing.T) {
	url, aliceTok, bobTok, _ := setup(t)
	alice, bob := connect(t, url, aliceTok), connect(t, url, bobTok)

	sess, _ := alice.call("sharedctx_session_start", map[string]any{})
	session, _ := sess["session"].(string)
	if !strings.HasPrefix(session, "s_") {
		t.Fatalf("session = %v", sess)
	}
	mine, isErr := alice.call("sharedctx_intent_announce", map[string]any{
		"resource": map[string]any{"uri": repo}, "action": "change", "summary": "Validate ids",
		"scopes": []any{map[string]any{"kind": "path", "value": "internal/http/guard.go"}}, "session": session, "lease_minutes": 60,
	})
	if isErr || len(mine["overlaps"].([]any)) != 0 {
		t.Fatalf("alice announce = %v", mine)
	}

	theirs, isErr := bob.call("sharedctx_intent_announce", map[string]any{
		"resource": map[string]any{"uri": "git@github.com:acme/shop.git"}, "action": "remove", "summary": "Move guard",
		"scopes": []any{map[string]any{"kind": "path", "value": "internal/http/"}},
	})
	overlaps, _ := theirs["overlaps"].([]any)
	if isErr || len(overlaps) != 1 {
		t.Fatalf("bob announce = %v", theirs)
	}
	o := overlaps[0].(map[string]any)
	wrapped := o["intent"].(map[string]any)
	if o["severity"] != "high" || wrapped["untrusted"] != true || wrapped["author"].(map[string]any)["handle"] != "alice" {
		t.Fatalf("overlap = %v", o)
	}
	if strings.Contains(o["reason"].(string), "guard.go") {
		t.Fatalf("reason leaks a member-written scope value: %q", o["reason"])
	}
	if w := theirs["warnings"].([]any); len(w) == 0 {
		t.Fatalf("bob's announce without a session should warn no_session: %v", theirs)
	}

	brief, isErr := alice.call("sharedctx_brief_get", map[string]any{"resources": []any{map[string]any{"uri": repo}}, "session": session})
	if isErr || len(brief["intents"].([]any)) != 1 || len(brief["my_intents"].([]any)) != 1 || brief["cursor"] == "" {
		t.Fatalf("alice brief = %v", brief)
	}
	delta, _ := alice.call("sharedctx_brief_get", map[string]any{"resources": []any{map[string]any{"uri": repo}}, "session": session, "since": brief["cursor"]})
	if delta["delta"] != true || len(delta["intents"].([]any)) != 0 {
		t.Fatalf("empty delta = %v", delta)
	}
	moved, isErr := alice.call("sharedctx_brief_get", map[string]any{"resources": []any{map[string]any{"uri": "https://github.com/acme/other"}}, "since": brief["cursor"]})
	if !isErr || moved["error"].(map[string]any)["details"].(map[string]any)["reason"] != "cursor_filter_mismatch" {
		t.Fatalf("cursor reused on another filter = %v", moved)
	}

	closed, _ := alice.call("sharedctx_intent_close_session", map[string]any{"session": session, "outcome": "completed"})
	if len(closed["intents"].([]any)) != 1 {
		t.Fatalf("close_session = %v", closed)
	}
}

// One person's two sessions warn each other (spec §6.1).
func TestSameMemberTwoSessions(t *testing.T) {
	url, aliceTok, _, _ := setup(t)
	laptop, desktop := connect(t, url, aliceTok), connect(t, url, aliceTok)
	announce := func(a agent, session string) map[string]any {
		out, _ := a.call("sharedctx_intent_announce", map[string]any{
			"resource": map[string]any{"uri": repo}, "action": "change", "summary": "x", "session": session,
			"scopes": []any{map[string]any{"kind": "symbol", "value": "shop/cart.Total"}},
		})
		return out
	}
	announce(laptop, "s_laptop")
	if got := announce(desktop, "s_desktop"); len(got["overlaps"].([]any)) != 1 {
		t.Fatalf("second session of the same member = %v", got)
	}
}

func TestNotesSecretsAndSearch(t *testing.T) {
	url, aliceTok, bobTok, _ := setup(t)
	alice, bob := connect(t, url, aliceTok), connect(t, url, bobTok)

	refused, isErr := alice.call("sharedctx_note_write", map[string]any{"kind": "gotcha", "body": "deploy key AKIAABCDEFGHIJKLMNOP"})
	e, _ := refused["error"].(map[string]any)
	if !isErr || e["code"] != "SECRET_DETECTED" || strings.Contains(e["message"].(string), "AKIA") {
		t.Fatalf("secret note = %v", refused)
	}
	note := map[string]any{
		"kind": "contract", "body": "Quote ids from pricing are q_ + random text, not UUIDs.",
		"resources": []any{map[string]any{"uri": repo}}, "client_request_id": "r1",
	}
	first, _ := alice.call("sharedctx_note_write", note)
	again, _ := alice.call("sharedctx_note_write", note)
	if first["note"].(map[string]any)["id"] != again["note"].(map[string]any)["id"] {
		t.Fatalf("a retry created a second note: %v vs %v", first, again)
	}
	found, _ := bob.call("sharedctx_note_search", map[string]any{"query": "uuid"})
	notes := found["notes"].([]any)
	if len(notes) != 1 || notes[0].(map[string]any)["untrusted"] != true {
		t.Fatalf("bob's search = %v", found)
	}
}

// Tool lists follow the credential's set; guests can't write (spec §11.1.2.1, §10.3).
func TestToolSetsAndRoles(t *testing.T) {
	url, aliceTok, bobTok, guestTok := setup(t)
	if n := len(connect(t, url, aliceTok).tools()); n != 8 {
		t.Errorf("essentials lists %d tools, want 8 (handoff.inbox waits for the Handoff profile)", n)
	}
	if n := len(connect(t, url, bobTok).tools()); n <= 8 {
		t.Errorf("full lists %d tools", n)
	}
	out, isErr := connect(t, url, guestTok).call("sharedctx_note_write", map[string]any{"kind": "gotcha", "body": "x"})
	if !isErr || out["error"].(map[string]any)["code"] != "PERMISSION_DENIED" {
		t.Errorf("guest write = %v", out)
	}
	resp, err := http.Post(url, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: %d, want 401", resp.StatusCode)
	}
}
