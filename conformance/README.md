# SCP conformance vectors

Language-neutral test data for the parts of the specification every implementation must
reproduce exactly. They decide what "correct" means; the reference server in `../server` is just
the first implementation that passes them.

| File | Spec | Checks |
| --- | --- | --- |
| `vectors/overlap.json` | §6 | Every scope kind's matching rule, every severity cell, the three relations, the broad and topic caps, rename targets, same-member sessions |
| `vectors/scopes.json` | §6.2, §6.2.1 | Scope normalization and which scopes are broad |
| `vectors/canonical.json` | §4.7.1 | Git remote canonicalization: ports, case-insensitive hosts, credentials stripped |
| `vectors/search.json` | §5.4.3 | The tokenizer and the minimum prefix match |
| `vectors/secrets.json` | §9.1, Appendix B | The minimum secret detectors, including documentation that must pass |

Each file describes its own format in a `description` field. To use them, load each file in your
implementation's tests and run every case; the Go runner is
`server/internal/domain/conformance_test.go`.

`tools.json` lists every MCP tool with its profile, set and whether it is read-only, so a
credential's exact tool list can be computed from the server's declared profiles (§11.1.2.1).

## Wire suite

`wire/` is a Go module that runs operations against a **live server over MCP** and checks what
the spec requires, without knowing the implementation:

| Check | Spec |
| --- | --- |
| Tool lists match profiles, sets and roles, computed from `tools.json` and discovery | §11.1.2.1 |
| An overlap reaches the other member, wrapped, with a reason free of member text | §6, §8.2 |
| `intent.check` without a session shows the principal's own intents | §5.5.7 |
| Wrapping follows the last writer, not the author | §8.2 |
| Brief deltas, `expiring_notes` in every brief, cursor bound to its filter | §5.3.1 |
| Another workspace can't read or find anything | §10.4 |
| Secrets refused and never echoed | §9.1 |
| `client_request_id` retries and conflicts | §3.3.1 |

Run it against any server with five credentials it issued (owner with the admin set, member with
full, a second member with essentials, a guest, a member of another workspace):

```sh
cd wire
SCP_BASE_URL=https://scp.example.com SCP_MCP_URL=https://scp.example.com/mcp \
SCP_OWNER=… SCP_MEMBER=… SCP_COLLEAGUE=… SCP_GUEST=… SCP_OUTSIDER=… go test ./...
```

Or against the reference server, which the suite bootstraps itself:

```sh
(cd ../server && go build -o /tmp/scp ./cmd/sharedctx-server)
SCP_SERVER_BIN=/tmp/scp go test ./...
```

Not covered yet ([spec §20](../spec/specification.md#20-open-issues)): handoffs, events, the
HTTP+JSON binding, administration, and client tests for the `trust` and `overlap-presentation`
profiles.
