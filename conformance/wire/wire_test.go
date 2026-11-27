package wire

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestConformance runs the suite against a server named in the environment:
//
//	SCP_BASE_URL, SCP_MCP_URL and the tokens SCP_OWNER, SCP_MEMBER, SCP_COLLEAGUE, SCP_GUEST,
//	SCP_OUTSIDER: any running server.
//	SCP_SERVER_BIN: a sharedctx-server binary; the test bootstraps a fresh one with its CLI.
func TestConformance(t *testing.T) {
	tg := Target{ToolsFile: filepath.Join("..", "tools.json")}
	switch {
	case os.Getenv("SCP_MCP_URL") != "":
		tg.BaseURL, tg.MCPURL = os.Getenv("SCP_BASE_URL"), os.Getenv("SCP_MCP_URL")
		tg.Owner, tg.Member, tg.Colleague = os.Getenv("SCP_OWNER"), os.Getenv("SCP_MEMBER"), os.Getenv("SCP_COLLEAGUE")
		tg.Guest, tg.Outsider = os.Getenv("SCP_GUEST"), os.Getenv("SCP_OUTSIDER")
	case os.Getenv("SCP_SERVER_BIN") != "":
		tg = reference(t, os.Getenv("SCP_SERVER_BIN"), tg)
	default:
		t.Skip("set SCP_MCP_URL and tokens, or SCP_SERVER_BIN, to run the wire suite")
	}
	Run(t, tg)
}

// reference bootstraps the reference server through its CLI: the only implementation-specific
// part of this package.
func reference(t *testing.T, bin string, tg Target) Target {
	t.Helper()
	dir := t.TempDir()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	env := append(os.Environ(), "SHAREDCTX_DB="+filepath.Join(dir, "scp.db"),
		"SHAREDCTX_TOKEN_KEY=conformance-0123456789abcdef0123456789", "SHAREDCTX_ADDR="+addr,
		"SHAREDCTX_PUBLIC_URL=http://"+addr)
	cli := func(args ...string) map[string]string {
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		fields := map[string]string{}
		sc := bufio.NewScanner(strings.NewReader(string(out)))
		for sc.Scan() {
			if k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), " "); ok {
				fields[k] = strings.TrimSpace(v)
			} else if strings.HasPrefix(sc.Text(), "sharedctx_pat_") {
				fields["token"] = sc.Text()
			}
		}
		return fields
	}
	a := cli("init", "-workspace", "Conformance", "-owner", "olivia", "-name", "Olivia")
	ws := a["workspace"]
	tg.Owner = a["token"]
	cli("add-member", "-workspace", ws, "-handle", "mia", "-name", "Mia")
	cli("add-member", "-workspace", ws, "-handle", "cole", "-name", "Cole")
	cli("add-member", "-workspace", ws, "-handle", "gus", "-name", "Gus", "-role", "guest")
	tg.Member = cli("issue-token", "-workspace", ws, "-handle", "mia", "-set", "full")["token"]
	tg.Colleague = cli("issue-token", "-workspace", ws, "-handle", "cole", "-set", "essentials")["token"]
	tg.Guest = cli("issue-token", "-workspace", ws, "-handle", "gus", "-set", "essentials")["token"]
	tg.Outsider = cli("init", "-workspace", "Elsewhere", "-owner", "otto", "-name", "Otto")["token"]

	srv := exec.Command(bin, "serve")
	srv.Env = env
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Process.Kill(); _ = srv.Wait() })
	tg.BaseURL, tg.MCPURL = "http://"+addr, "http://"+addr+"/mcp"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		if resp, err := http.Get(tg.BaseURL + "/healthz"); err == nil {
			_ = resp.Body.Close()
			return tg
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal(fmt.Errorf("server did not start at %s", addr))
	return tg
}
