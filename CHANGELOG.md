# Changelog

Versions follow `MAJOR.MINOR.PATCH`. Before 1.0.0, a minor version may break compatibility.

## 0.4.0

From a reviewer's read of 0.3. Breaking for anyone implementing 0.3:

- **Topic scopes are broad** (§6.2.1, §6.3): every `topic` scope is broad, topic-to-topic matches
  are capped at `low`, and an intent with only topic scopes gets a `topic_only` warning, so
  `topic` can't route around the alert-fatigue defense.
- **Search matching is token-based** (§5.4.3): a defined tokenizer (split at every non-letter,
  non-digit) and prefix matching per token, so `quote` finds `quote_id` and `pricing.v1.Quote`
  on every server. Replaces "contains all terms", which no full-text engine implements exactly.
- **Brief cursors are bound to their filter** (§5.3.1): a `since` used with a different `work` or
  resource set fails with `cursor_filter_mismatch` instead of returning a delta against a moved
  filter.
- **Identity-provider membership stays off the request path** (§10.6): group claims come from the
  validated token; the server never calls the identity provider per request.

Added:

- **Second residual risk** (§17, §8.3): SCP's safety properties hold only for conforming clients,
  and the protocol cannot enforce client conformance. Clients may declare the client profiles they
  implement (self-asserted); owners can see what members' clients declared.
- **Low `uses` coverage stated** (§6.7): no `breaks` warning means no *declared* dependency;
  clients should fill `uses` from imports, generated-client manifests and lockfiles.
- **Probing in-flight work** (§17): `intent.check`/`intent.list` are audited with the scopes
  queried, and should be rate-limited.
- Topic scopes only alongside specific scopes (§6.6).

## 0.3.0

First published version (no longer marked draft; pre-1.0 rules still apply).

- **Layering stated** (§1.4): SCP is a protocol of its own with MCP and HTTP+JSON bindings; its MCP
  binding is a third-party MCP extension under a vendor prefix the authors control (§11.1.1.1).
- **Bringing a team in** (§5.1.0): who creates a workspace, how people join and leave, and how
  agents act for their person.

From an agent's review of 0.2. Breaking for anyone implementing 0.2:

- **Sessions and leases** (§4.4.1): intents carry a client-chosen `session` and an optional
  `lease_minutes`, so agents that can't run a timer never need to heartbeat;
  `intent.close_session` closes everything a session announced; heartbeat accepts a `session`.
- **Same-member overlaps** (§6.1): one member's intents from different sessions now overlap, so
  one person's two agents on two machines warn each other.
- **Revisions everywhere** (§3.3): `Workspace`, `Member` and `Intent` gain `revision`; every
  change, server-made lifecycle changes included, increments it, so it alone orders events (§13).
- **Untrusted envelope closes its gaps** (§8): wrapping is decided by the last writer, not the
  author; an overlap's matched `theirs` scopes, a tracker's `state` and `url`, and a handoff's
  `response` in the outbox are wrapped; server-generated strings (overlap `reason`, errors,
  warnings) never contain member-written text; attribution survives summarization and
  compaction (§8.3).
- **Git canonicalization** (§4.7.1): non-default ports are kept; paths on case-insensitive hosts
  (`github.com`, `gitlab.com`, `bitbucket.org`, plus owner-added) are lowercased.
- **Brief**: `truncated` names the lists that were cut; `since`/`cursor` deltas; a token for
  `promoted`; `notes_limit` bounded by the server's page size; a no-argument brief uses
  `WorkspaceSettings.default_resources` (§5.3.1).
- **MCP results** (§11.1.3): the text block is a short summary, no longer a second copy of the
  result; `structuredContent` is authoritative.

Added:

- **Tool sets** (§11.1.2.1): `essentials` (eight tools, the default), `full` and `admin`, granted
  per credential, so agents load only the tools they need.
- **Retries** (§3.3.1): `client_request_id` on every create makes retries safe.
- **SCP version over MCP** (§15.1): the client asks for one in its per-request capabilities.
- **Aliases** (§4.7.2) and contract-naming guidance (§6.7) for shared contracts reached under
  different URIs.
- **Secret detection** (§9.1): detectors listed in discovery; heuristic detectors can be set to
  `warn` (`possible_secret` warning); owner allow-lists for documentation placeholders; format
  detectors always reject.
- **Warnings** have a defined code list (§5.5.4); `events.subscribe` is defined per binding
  (§5.7).
- **Integration guide** (`docs/integration.md`) with a project-instructions snippet.
- Appendix A no longer promises a `schemas/` directory; per-operation schemas are an open issue,
  and size limits live only in discovery.

## 0.2.0-draft

Breaking for anyone implementing 0.1:

- **Overlap rules** (§6): intents gain `uses` (dependencies, possibly in other resources), and
  overlaps are compared in three relations: `change`, `breaks`, `broken_by`. New severity rows
  for `use`. Renames carry `to` and match on both names.
- **Broad scopes** (§6.2.1): whole-resource `path` scopes are refused; broad scopes return a
  `broad_scope` warning, and containment through a broad scope is capped at `low`.
- **Overlap object**: each match carries `relation`, `resource` and `broad`.
- **Notes**: renewal capped at two (`renewals` field); promotion only to `doc` or `change`
  references, verified where possible (`promotion_verified`).
- **Brief**: `notes_limit`, `notes_next_page_token` and `promoted`; brief notes are defined as the
  first page of `note.search`.
- **Search**: `order` parameter; `newest` is the default and identical on every server.
- **Events**: no ordering guarantee; no overlap events for closed intents; clients confirm before
  showing.

Added:

- Member administration: `member.invite`, `member.update`, `member.remove` (§5.2).
- Membership from an identity provider's groups, evaluated on every request (§10.6).
- Scope guidance (§6.6) and cross-resource coordination (§6.7).
- Clients must not present an empty overlap list as "no conflict" (§6.5); §17 states the residual
  risk.
- Example §19.5: a contract rename warning a dependent service.

Aligned with the Model Context Protocol and Agent Client Protocol versions now pinned in §1.5:

- **Normative references** (§1.5): every external specification pinned once, with an exact
  version; the body refers to them by name, and the spec carries no dates of its own.
- **MCP binding** (§11.1.1): targets the pinned MCP version (no handshake, per-request version and
  capabilities, `server/discover`, stateless); older MCP versions served as "dual-era".
- **SCP is an MCP extension** (§11.1.1.1), `io.github.recluzegeek/sharedctx`, advertised in
  `server/discover`; the SCP version moves from the removed `initialize` result to the extension
  entry and every result's `_meta` (§15.1). Discovery lists it with the MCP versions served (§12).
- **Events over MCP** (§13.2): the MCP Events extension where both sides support it; otherwise
  resource notifications on `subscriptions/listen` (replacing `resources/subscribe`).
- **Authorization** (§10.2): OAuth exactly as the pinned MCP version (Protected Resource Metadata,
  PKCE S256, resource indicators, `iss` check, audience validation, Client ID Metadata Documents);
  no token passthrough to trackers.
- **Local agents** (§11.1.1.2): a stdio bridge for hosts that only start stdio MCP servers.
- **Hosts that build prompts** (§11.1.4), e.g. ACP clients, never put SCP content in system
  prompts; they pass it as embedded resources.
- **Tools** (§11.1.2): `openWorldHint`, tool lists vary only by authorization; errors follow MCP's
  tool-error guidance (§11.1.3).
- **Subagents** act for the same member (§10.1); W3C Trace Context recorded with audit (§10.5).
- Extension identifiers use MCP's `{vendor-prefix}/{name}` format (§15.3).

## 0.1.0-draft

First draft: data model, operations, deterministic overlap detection, trust model, content
safety, MCP and HTTP+JSON bindings, discovery, events, errors, conformance.
