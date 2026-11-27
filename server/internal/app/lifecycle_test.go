package app_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/recluzegeek/shared-context-protocol/server/internal/adapters/sqlite"
	"github.com/recluzegeek/shared-context-protocol/server/internal/app"
	"github.com/recluzegeek/shared-context-protocol/server/internal/domain"
)

// Leases decide expiry on their own; the sweeper expires intents and bumps their revision, and
// a lease_expiring warning appears before that (spec §4.4.1, §3.3, §5.5.4).
func TestLeaseLifecycle(t *testing.T) {
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "scp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	svc := app.NewService(store, []byte("0123456789abcdef0123456789abcdef"))
	now := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return now }
	ctx := context.Background()
	w, _, err := svc.CreateWorkspace(ctx, "Acme", "alice", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := svc.IssueToken(ctx, w.ID, "alice", "", app.SetEssentials)
	p, err := svc.Authenticate(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}

	res, err := svc.IntentAnnounce(ctx, p, app.IntentParams{
		Resource: app.Resource{URI: "git:github.com/acme/shop"}, Action: domain.ActionChange, Summary: "x",
		Scopes: []domain.Scope{{Kind: domain.KindPath, Value: "a.go"}}, Session: "s1", LeaseMinutes: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(30 * time.Minute); !res.Intent.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at = %v, want %v (the lease, not the 24h TTL)", res.Intent.ExpiresAt, want)
	}

	now = now.Add(20 * time.Minute)
	if w := svc.LeaseWarnings(ctx, p); len(w) != 0 {
		t.Fatalf("10 minutes left of a 30-minute lease is outside its 6-minute window: %v", w)
	}
	now = now.Add(5 * time.Minute)
	if w := svc.LeaseWarnings(ctx, p); len(w) != 1 || w[0].Code != "lease_expiring" {
		t.Fatalf("5 minutes before expiry: %v", w)
	}

	now = now.Add(10 * time.Minute)
	if err := svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := svc.IntentListOp(ctx, p, app.IntentListParams{Status: []string{"expired"}})
	if err != nil || len(list.Intents) != 1 {
		t.Fatalf("expired intents = %v, %v", list, err)
	}
	if got := list.Intents[0].(app.Intent); got.Revision != 2 {
		t.Fatalf("expiry must bump the revision: %d", got.Revision)
	}
}
