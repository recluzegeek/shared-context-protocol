# sharedctx-server

A reference implementation of the [Shared Context Protocol](../spec/specification.md) in Go: one
implementation, not the implementation. Where it and the [conformance vectors](../conformance)
disagree, the vectors are right.

**Version 0.1.0**, implementing spec 0.4. One static binary, SQLite storage, MCP over stateless
Streamable HTTP.

## What it implements

| Spec | Status |
| --- | --- |
| Core: notes (write, get, search, update, renew, promote, retire), brief with deltas and page tokens, workspace and member reads, discovery | Yes |
| Coordination: intents with sessions and leases, overlap detection (§6), `intent.close_session`, `lease_expiring` | Yes |
| `session.start`, retries with `client_request_id`, secret detection with warn modes and allow-lists, untrusted wrapping by last writer | Yes |
| Tool sets per credential (`essentials`, `full`, `admin`) | Yes |
| MCP binding: pinned MCP version plus older clients, extension `io.github.recluzegeek/sharedctx`, structured errors, short text summaries | Yes |
| stdio bridge for agents that only run stdio MCP servers | Yes (`sharedctx-bridge`) |
| Personal access tokens | Yes |
| Handoff profile, Events profile, HTTP+JSON binding | Not yet |
| OAuth 2.1, identity-provider groups, `member.invite/update/remove`, `workspace.update` over MCP | Not yet (members and tokens are managed with the CLI) |
| Tracker integrations (work details, verified promotion) | Not yet: promotions are recorded with `promotion_verified: false` |
| Retention and deletion of expired data | Not yet: data is kept |
| Requested SCP version in client capabilities | Not checked yet: every request gets 0.4 |

## Run it

```sh
docker build -t sharedctx-server .
export SHAREDCTX_TOKEN_KEY=$(head -c 32 /dev/urandom | base64)   # keep it: tokens depend on it

# Create the workspace and its owner (prints the owner's token once).
docker run --rm -v scp-data:/data -e SHAREDCTX_TOKEN_KEY sharedctx-server \
  init -workspace Acme -owner alice -name "Alice"

# Add people and give each of them a token.
docker run --rm -v scp-data:/data -e SHAREDCTX_TOKEN_KEY sharedctx-server \
  add-member -workspace <id> -handle bob -name "Bob"
docker run --rm -v scp-data:/data -e SHAREDCTX_TOKEN_KEY sharedctx-server \
  issue-token -workspace <id> -handle bob -set essentials -label laptop

# Serve. Put it behind TLS before anyone outside your machine uses it.
docker run -d -p 8080:8080 -v scp-data:/data -e SHAREDCTX_TOKEN_KEY \
  -e SHAREDCTX_PUBLIC_URL=https://scp.example.com sharedctx-server
```

Without Docker: `go build ./cmd/...` and run `sharedctx-server` with the same commands.

| Variable | Default | |
| --- | --- | --- |
| `SHAREDCTX_TOKEN_KEY` | (required) | HMAC key for tokens, at least 32 characters |
| `SHAREDCTX_DB` | `sharedctx.db` (`/data/sharedctx.db` in Docker) | SQLite file |
| `SHAREDCTX_ADDR` | `:8080` | Listen address |
| `SHAREDCTX_PUBLIC_URL` | `http://localhost:8080` | Base URL published in discovery |
| `SHAREDCTX_ALLOWED_ORIGINS` | none | Comma-separated origins allowed to call `/mcp` from a browser |

Endpoints: `/mcp` (MCP), `/.well-known/sharedctx` (discovery), `/healthz`.

## Connect an agent

Remote MCP clients: add `https://<host>/mcp` with the header `Authorization: Bearer <token>`.

Stdio-only hosts: run the bridge as the MCP server.

```sh
SHAREDCTX_URL=https://<host>/mcp SHAREDCTX_TOKEN=<token> sharedctx-bridge
```

Then paste the snippet from [docs/integration.md](../docs/integration.md) into your project's
instructions.

## Develop

```sh
go test ./...        # domain rules against ../conformance/vectors, plus end-to-end MCP tests
go vet ./...
```

`internal/domain` is standard-library only and holds every rule the spec makes deterministic;
`internal/app` the operations; `internal/adapters` SQLite and MCP. Tags are `server/vX.Y.Z`.
