# SCP reference architecture

Non-normative. One way to build a server for the [specification](../spec/specification.md), and
how a team connects to it. Section links like §6 point into the specification. Diagrams are
Mermaid.

## 1. System context

```mermaid
flowchart LR
    subgraph m1["Alice's machine"]
        A["Coding agent<br/>(any MCP client)"]
    end
    subgraph m2["Bob's machine"]
        B["Coding agent"]
    end
    subgraph m3["Carol's machine"]
        C["Coding agent"]
    end
    UI["Web UI<br/>(optional)"]

    S["SCP server<br/>MCP + HTTP+JSON"]
    TR["Issue trackers<br/>(GitHub, Jira, Linear, Plane…)"]
    IDP["Identity provider<br/>(OAuth 2.1 / OIDC)"]

    A -- "MCP, member token" --> S
    B -- "MCP, member token" --> S
    C -- "MCP, member token" --> S
    UI -- "HTTP+JSON" --> S
    S -- "read work items<br/>(configured hosts only)" --> TR
    A -. "sign in" .-> IDP
    S -. "validate tokens" .-> IDP
```

* Agents never talk to each other. They read and write one shared store, through one server.
* Code stays in version control and work stays in the tracker. The server holds short text about
  both, and references to them (§4.9).

## 2. Containers

```mermaid
flowchart TB
    subgraph client["Each member's machine"]
        AG["Coding agent"]
        HK["Session-start hook<br/>brief.get for the current branch"]
        RULE["Project instructions file<br/>'read the brief, announce intents, write notes'"]
        PRE["Client-side secret pre-scan<br/>(recommended, §9.1)"]
    end

    subgraph server["SCP server: one process"]
        direction TB
        EDGE["Bindings<br/>MCP Streamable HTTP, pinned version (spec §1.5):<br/>stateless, server/discover<br/>· HTTP+JSON · SSE"]
        AUTHN["Authentication<br/>token -> member, workspace"]
        GUARD["Content safety<br/>limits, control chars, secret detectors"]
        CORE["Domain<br/>notes · intents · overlap · handoffs · brief"]
        ENV["Untrusted envelope<br/>wrap other members' text (§8.2)"]
        SWEEP["Sweeper<br/>expire notes, intents, handoffs; warnings"]
        EVT["Event fan-out"]
    end

    DB[("Relational database<br/>workspaces, members, tokens,<br/>notes, intents, handoffs, audit")]
    TRK["Tracker adapters"]

    AG --> EDGE
    HK --> EDGE
    EDGE --> AUTHN --> GUARD --> CORE
    CORE --> ENV --> EDGE
    CORE <--> DB
    CORE --> TRK
    SWEEP --> DB
    SWEEP --> EVT
    CORE --> EVT --> EDGE
```

| Concern | Reference choice | Why |
| --- | --- | --- |
| Language | Go, single static binary | Easy to self-host; mature MCP SDK |
| Structure | Hexagonal: `domain/` depends on nothing; bindings, storage, trackers are adapters | The overlap rule and lifecycles are pure functions, testable without I/O |
| Storage | PostgreSQL; SQLite for single-team installs | Full-text search built in; one dependency |
| Search | Postgres `tsvector` with `websearch_to_tsquery` | Meets §5.4.3 without a vector store |
| Background work | One sweeper loop, one minute tick, `SELECT … FOR UPDATE SKIP LOCKED` | Lifecycle deadlines in §7 without a queue |
| Events | In-process fan-out; Postgres `LISTEN/NOTIFY` across replicas | No broker to run |
| Auth | Personal access tokens (HMAC-SHA-256 at rest) and OAuth 2.1 via an external IdP | §10.2 |

## 3. Storage model

```mermaid
erDiagram
    WORKSPACE ||--o{ MEMBER : has
    MEMBER ||--o{ TOKEN : holds
    WORKSPACE ||--o{ NOTE : contains
    WORKSPACE ||--o{ INTENT : contains
    WORKSPACE ||--o{ HANDOFF : contains
    INTENT ||--|{ INTENT_SCOPE : declares
    MEMBER ||--o{ NOTE : authors
    MEMBER ||--o{ INTENT : announces
    MEMBER ||--o{ HANDOFF : sends
    WORKSPACE ||--o{ AUDIT : records

    WORKSPACE {
        text id PK
        text name
        jsonb settings
        int revision
    }
    MEMBER {
        text id PK
        text workspace_id FK
        text handle "unique per workspace"
        text display_name
        text role "owner|member|guest"
        bool active
    }
    TOKEN {
        text id PK
        text member_id FK
        bytea hmac "never the token"
        text label "per device"
        timestamptz revoked_at
    }
    NOTE {
        text id PK
        text workspace_id FK
        text kind
        text body
        text[] resource_uris
        jsonb refs
        text[] work_uris "denormalized from refs for brief"
        tsvector search
        text status
        jsonb promoted_to
        int revision
        timestamptz expires_at
    }
    INTENT {
        text id PK
        text workspace_id FK
        text resource_uri "canonical"
        text action
        text summary
        text work_uri
        text session "client-chosen, nullable"
        int lease_minutes "nullable"
        text status
        int revision
        timestamptz heartbeat_at
        timestamptz expires_at
    }
    REQUEST_LOG {
        text member_id FK
        text client_request_id "retry key, kept 24h"
        bytea params_hash
        jsonb result
        timestamptz at
    }
    INTENT_SCOPE {
        text intent_id FK
        text side "change | use"
        text resource_uri "the intent's, or the dependency's"
        text kind
        text value_norm "normalized per §6.2"
        text to_norm "rename target, nullable"
        text language
        bool broad "per §6.2.1"
    }
    HANDOFF {
        text id PK
        text workspace_id FK
        text from_id FK
        text to_id FK
        text work_uri
        text done
        text needed
        jsonb refs
        text status
        int revision
        timestamptz expires_at
    }
    AUDIT {
        bigint id PK
        text workspace_id
        text member_id
        text operation
        text object_id
        jsonb agent
        timestamptz at
    }
```

Indexes that matter:

| Query | Index |
| --- | --- |
| Active intents on a resource (overlap, brief) | `intent (workspace_id, resource_uri) WHERE status = 'active'` |
| Change and use sides on a resource | `intent_scope (resource_uri, side, kind)` |
| Scope candidates | `intent_scope (kind, value_norm text_pattern_ops)` and `(kind, to_norm)` for prefix lookups |
| Brief notes | GIN on `note.resource_uris`, GIN on `note.work_uris`, both `WHERE status = 'active'` |
| Search | GIN on `note.search` |
| Sweeper | `note (expires_at) WHERE status = 'active'`, same for intents and handoffs |

## 4. Overlap computation

The rule is §6. With a resource's active intents usually in the tens, the reference server loads
them and matches in memory; the scope index narrows candidates when a resource has many.

```mermaid
flowchart TD
    S["subject intent<br/>(announce, update, heartbeat, check)"] --> N["normalize scopes, mark broad<br/>(§6.2, §6.2.1)"]
    N --> Q["load other members' active intents<br/>whose change or use side shares a resource<br/>with the subject's change or use side"]
    Q --> P["pairs by relation (§6.1):<br/>change↔change, change→use (breaks),<br/>use→change (broken_by); never use↔use"]
    P --> L{"same kind?"}
    L -- no --> X["no match"]
    L -- yes --> M{"match by kind rule<br/>(value, and to for renames)"}
    M -- "equal" --> E["exact"]
    M -- "contains" --> C["containment"]
    M -- no --> X
    E --> SV["severity table (§6.3)"]
    C --> CAP{"containing scope broad?"}
    CAP -- yes --> LOW["cap at low"] --> SV
    CAP -- no --> SV
    SV --> R["overlap: highest severity,<br/>matched pairs with relation,<br/>fixed-template reason"]
```

The domain function has the shape
`overlaps(subject Intent, others []Intent) []Overlap`: pure, deterministic, and table-tested
against every row of §6.2 and §6.3, every relation of §6.1, and the broad cap.

## 5. Flows

### 5.1 Session start

```mermaid
sequenceDiagram
    autonumber
    participant M as Member
    participant A as Agent (client)
    participant S as Server
    participant T as Tracker

    M->>A: open session on branch feat/1061-cohost-reply
    A->>S: brief.get(work=github:acme/shop#1061, resources=[git:github.com/acme/shop-api])
    S->>S: authenticate, load notes / intents / handoffs
    S->>T: get issue 1061 (configured host, cached 15 min)
    T-->>S: title, state
    S-->>A: Brief: others' items wrapped as untrusted (§8.2)
    A-->>M: "Alice's note: quote ids aren't UUIDs.<br/>Bob is removing internal/http/guard.go (high overlap)."
```

How the client finds `work` and `resources`: the hook reads the Git remote (canonicalized per
§4.7.1) and looks for a work reference in the branch name or the latest commit message, using a
pattern the team configures (e.g. `[A-Z]+-\d+`).

### 5.2 Before editing

```mermaid
sequenceDiagram
    autonumber
    participant A as Alice's agent
    participant S as Server
    participant B as Bob's agent

    B->>S: intent.announce(resource, path internal/http/guard.go, remove)
    S-->>B: intent i_17, no overlaps
    A->>S: intent.announce(resource, path internal/http/, change)
    S->>S: overlap: containment, remove + change = high (§6.3)
    S-->>A: intent i_19 + overlap with Bob's i_17 (high)
    S--)B: event intent.overlap (Events profile)
    A-->>A: show to Alice before editing (§6.5)
    Note over A,B: Alice and Bob decide. Neither agent instructs the other.
```

### 5.3 While working

```mermaid
sequenceDiagram
    participant A as Agent
    participant S as Server
    loop every intent_ttl_hours / 4
        A->>S: intent.heartbeat([i_19])
        S-->>A: intents + current overlaps
    end
    A->>S: note.write(contract, "Quote ids are q_ + random text, not UUIDs")
    S->>S: secret scan (§9.1), limits, store
    S-->>A: note n_81, expires in 30 days
    A->>S: intent.close(i_19, completed, refs=[PR 156])
```

### 5.4 Handoff

```mermaid
sequenceDiagram
    participant A as Alice's agent
    participant S as Server
    participant C as Carol's agent
    A->>S: handoff.post(to=carol, done, needed, refs)
    S--)C: event handoff.received
    C->>S: brief.get / handoff.inbox
    S-->>C: handoff (untrusted, from Alice)
    C-->>C: show to Carol. Act only when Carol asks (§8.3)
    C->>S: handoff.respond(acknowledge)
    S--)A: event handoff.updated
```

### 5.5 Memory lifecycle

```mermaid
stateDiagram-v2
    [*] --> active: note.write
    active --> active: note.renew
    active --> promoted: note.promote (ADR, guide, PR)
    active --> retired: note.retire
    active --> expired: expires_at passes (sweeper)
    promoted --> [*]
    retired --> [*]
    expired --> [*]: deleted after retention
```

## 6. Trust boundaries

```mermaid
flowchart LR
    subgraph own["Trusted: the member's own session"]
        INS["System and project instructions,<br/>permissions, tool config"]
        AG["Agent reasoning"]
    end
    subgraph shared["Untrusted: shared content"]
        OT["Other members' notes,<br/>intents, handoffs"]
        EX["Tracker text"]
    end
    OT -- "envelope: untrusted, author" --> AG
    EX -- "envelope: untrusted, system" --> AG
    INS --> AG
    AG -- "actions need the member's consent<br/>when prompted by shared content" --> ACT["files, commands, other tools"]
```

The server enforces what it can (author from credentials, wrapping, secret refusal, isolation);
the client enforces the rest (§8.3). Neither alone is enough.

## 7. Deployment

```mermaid
flowchart LR
    subgraph team["Team network or VPN"]
        LB["TLS reverse proxy"]
        S1["sharedctx-server replica"]
        S2["sharedctx-server replica"]
        PG[("PostgreSQL")]
    end
    DEV["Members' agents"] -- "HTTPS" --> LB
    LB --> S1 & S2
    S1 & S2 --> PG
    S1 & S2 -. "outbound HTTPS only" .-> TR["Trackers"]
```

| Install | Shape |
| --- | --- |
| One small team | One binary with SQLite on a single host behind TLS |
| Organization | Two or more replicas, PostgreSQL, OIDC from the company IdP, behind VPN or an authenticating proxy |

Configuration (environment): database URL, public base URL, token HMAC key, OAuth issuer and
audience, tracker credentials per host, retention periods, rate limits.

## 8. Code layout

```text
shared-context-protocol/
  cmd/sharedctx-server/     main: config, wiring, migrations, server
  cmd/sharedctx-bridge/     stdio MCP bridge to the remote server (spec §11.1.1.2)
  internal/domain/          entities, lifecycles, overlap rule, brief assembly (stdlib only)
  internal/app/             operations (§5): authorization, revisions, audit
  internal/adapters/
    mcp/                    tools, resources, prompts, untrusted rendering (§11.1)
    httpapi/                HTTP+JSON binding and SSE (§11.2, §13)
    store/postgres/         queries, migrations
    store/sqlite/
    secrets/                detectors (Appendix B)
    trackers/{github,jira,linear,plane}/
    auth/                   tokens, OAuth resource server
  schemas/                  JSON Schemas (spec Appendix A)
  conformance/              black-box test suite against any server
  spec/  docs/
```

## 9. Testing

| Layer | What |
| --- | --- |
| Domain | Table tests for every §6.2 matching rule, rename `to` matching, every §6.1 relation, every §6.3 severity cell and the broad cap; lifecycle transitions in §7, including illegal ones; the renewal cap |
| Membership | Identity-provider group changes (§10.6): join, role change, removal denied on the next request, open intents closed |
| Content safety | Each Appendix B detector, plus placeholders that must pass |
| Bindings | The same operation through MCP and HTTP returns the same result and errors |
| Isolation | Every operation with another workspace's ids returns `NOT_FOUND` |
| Conformance | A suite runnable against any server URL, reported per profile (§16) |
