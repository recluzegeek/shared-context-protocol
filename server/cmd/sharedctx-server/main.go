// Command sharedctx-server is the reference SCP server.
//
//	sharedctx-server serve
//	sharedctx-server init        -workspace NAME -owner HANDLE -name "Display Name"
//	sharedctx-server add-member  -workspace ID -handle HANDLE -name NAME [-role member]
//	sharedctx-server issue-token -workspace ID -handle HANDLE [-set essentials] [-label laptop]
//
// Configuration comes from the environment: SHAREDCTX_DB (default sharedctx.db),
// SHAREDCTX_TOKEN_KEY (required, at least 32 characters), SHAREDCTX_ADDR (default :8080),
// SHAREDCTX_PUBLIC_URL (default http://localhost:8080), SHAREDCTX_ALLOWED_ORIGINS (comma list).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/recluzegeek/shared-context-protocol/server/internal/adapters/mcpbind"
	"github.com/recluzegeek/shared-context-protocol/server/internal/adapters/sqlite"
	"github.com/recluzegeek/shared-context-protocol/server/internal/app"
	"github.com/recluzegeek/shared-context-protocol/server/internal/domain"
)

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: sharedctx-server serve|init|add-member|issue-token [flags]")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(cmd string, args []string) error {
	key := os.Getenv("SHAREDCTX_TOKEN_KEY")
	if len(key) < 32 {
		return errors.New("SHAREDCTX_TOKEN_KEY must be set to at least 32 characters")
	}
	store, err := sqlite.Open(env("SHAREDCTX_DB", "sharedctx.db"))
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	svc := app.NewService(store, []byte(key))
	ctx := context.Background()
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	switch cmd {
	case "serve":
		return serve(svc)
	case "init":
		name := fs.String("workspace", "", "workspace name")
		owner := fs.String("owner", "", "owner handle")
		display := fs.String("name", "", "owner display name")
		_ = fs.Parse(args)
		w, m, err := svc.CreateWorkspace(ctx, *name, *owner, *display)
		if err != nil {
			return err
		}
		token, err := svc.IssueToken(ctx, w.ID, m.Handle, "bootstrap", app.SetAdmin)
		if err != nil {
			return err
		}
		fmt.Printf("workspace %s\nowner     @%s\ntoken     %s\n(shown once; store it now)\n", w.ID, m.Handle, token)
	case "add-member":
		ws := fs.String("workspace", "", "workspace id")
		handle := fs.String("handle", "", "member handle")
		display := fs.String("name", "", "display name")
		role := fs.String("role", "member", "owner, member or guest")
		_ = fs.Parse(args)
		m, err := svc.AddMember(ctx, *ws, *handle, *display, *role)
		if err != nil {
			return err
		}
		fmt.Printf("member @%s (%s) added\n", m.Handle, m.Role)
	case "issue-token":
		ws := fs.String("workspace", "", "workspace id")
		handle := fs.String("handle", "", "member handle")
		set := fs.String("set", app.SetEssentials, "essentials, full or admin")
		label := fs.String("label", "", "where the token is used, e.g. laptop")
		_ = fs.Parse(args)
		token, err := svc.IssueToken(ctx, *ws, *handle, *label, *set)
		if err != nil {
			return err
		}
		fmt.Printf("%s\n(shown once; store it now)\n", token)
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
	return nil
}

func serve(svc *app.Service) error {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	public := strings.TrimSuffix(env("SHAREDCTX_PUBLIC_URL", "http://localhost:8080"), "/")
	var origins []string
	if o := os.Getenv("SHAREDCTX_ALLOWED_ORIGINS"); o != "" {
		origins = strings.Split(o, ",")
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpbind.New(svc, log).Handler(origins))
	mux.HandleFunc("GET /.well-known/sharedctx", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(discovery(public, svc))
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() { // the sweeper expires notes and intents (spec §7)
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			if err := svc.Sweep(ctx); err != nil && ctx.Err() == nil {
				log.Error("sweep", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	srv := &http.Server{Addr: env("SHAREDCTX_ADDR", ":8080"), Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Info("serving", "addr", srv.Addr, "mcp", public+"/mcp")
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// discovery is the document of spec §12.
func discovery(public string, svc *app.Service) map[string]any {
	var detectors []map[string]any
	for _, d := range domain.Detectors {
		detectors = append(detectors, map[string]any{"name": d.Name, "kind": d.Kind, "pattern": d.Pattern.String()})
	}
	return map[string]any{
		"name":              "sharedctx-server " + mcpbind.ServerVersion,
		"protocol_versions": []string{app.Version},
		"bindings": map[string]any{"mcp": map[string]any{
			"url": public + "/mcp", "extension": mcpbind.ExtensionID,
			"protocol_versions": []string{"2026-07-28", "2025-11-25", "2025-06-18"},
		}},
		"auth":        map[string]any{"methods": []string{"pat"}},
		"conformance": []string{"core", "coordination"},
		"limits":      app.Limits,
		"retention": map[string]any{
			"expired_notes_days": nil, "closed_intents_days": nil, "closed_handoffs_days": nil, "audit_days": nil,
		},
		"secret_detectors":       detectors,
		"case_insensitive_hosts": svc.CaseInsensitiveHosts(),
		"tool_sets":              []string{app.SetEssentials, app.SetFull, app.SetAdmin},
	}
}
