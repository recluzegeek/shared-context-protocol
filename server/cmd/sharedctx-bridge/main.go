// Command sharedctx-bridge is a stdio MCP server that forwards every tool to a remote SCP server
// (spec §11.1.1.2), for agent hosts that only start stdio MCP servers. It reads SHAREDCTX_URL
// (the MCP endpoint) and SHAREDCTX_TOKEN from its environment, and never caches, rewrites or
// logs bodies.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "sharedctx-bridge:", err)
		os.Exit(1)
	}
}

func run() error {
	url, token := os.Getenv("SHAREDCTX_URL"), os.Getenv("SHAREDCTX_TOKEN")
	if url == "" || token == "" {
		return fmt.Errorf("SHAREDCTX_URL and SHAREDCTX_TOKEN must be set")
	}
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "sharedctx-bridge", Version: "0.1.0"}, nil)
	remote, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: url, HTTPClient: &http.Client{Transport: bearer{token, http.DefaultTransport}},
	}, nil)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = remote.Close() }()

	local := mcp.NewServer(&mcp.Implementation{Name: "sharedctx-bridge", Version: "0.1.0"}, nil)
	for t, err := range remote.Tools(ctx, nil) {
		if err != nil {
			return fmt.Errorf("list tools: %w", err)
		}
		local.AddTool(t, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return remote.CallTool(ctx, &mcp.CallToolParams{Name: req.Params.Name, Arguments: req.Params.Arguments})
		})
	}
	return local.Run(ctx, &mcp.StdioTransport{})
}
