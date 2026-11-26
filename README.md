# Shared Context Protocol (SCP)

**An open protocol for a shared team context between coding agents.**

When a team works with several AI coding agents at once, one or more per person, on different
machines, from different vendors, each agent works alone. What one learns, the others never see.
What one is about to change, the others find out at merge time. Work passes between people as
pasted chat messages.

SCP is a small, open protocol for a server that a team's agents share:

| | |
| --- | --- |
| **Notes** | What one agent learns that the team needs, written once, found by every agent. Notes expire unless promoted to a durable, reviewed record. |
| **Intents** | "I'm about to change X." Another member's agent working on the same area is warned before it writes code. Advisory, never a lock. |
| **Handoffs** | Work passed from one person to another as a structured record their agent can read. |
| **Brief** | Everything the team knows about a piece of work, in one call at session start. |

It is built on the [Model Context Protocol](https://modelcontextprotocol.io/), so any MCP-capable
agent can take part. People stay in charge: agents share information, never act on each other's
requests, and other people's words always reach an agent as data, never as instructions.

## Documents

| Document | |
| --- | --- |
| [Specification](spec/specification.md) | The normative protocol: data model, operations, overlap algorithm, trust model, bindings, errors, conformance. |
| [Reference architecture](docs/architecture.md) | How a server can be built: components, storage, flows, deployment. |
| [Integration guide](docs/integration.md) | Connecting agents, and the project-instructions snippet that makes them use SCP. |

## Status

**0.3.0, published** ([changelog](CHANGELOG.md)). No implementation yet; until 1.0, a minor version may still break compatibility. Feedback on the
open issues in [§20 of the specification](spec/specification.md#20-open-issues) is the most
useful contribution right now.

## How it differs

* **MCP** connects one agent to tools. SCP is an MCP extension whose data is shared by a team.
* **ACP** (Agent Client Protocol) connects an editor to its coding agent. SCP is what the team's
  agents share; an ACP client hands the SCP server to its agent like any MCP server.
* **A2A** lets agents delegate tasks while keeping their memory private. SCP shares context
  among people and their agents, and never delegates actions.
* **Vendor shared workspaces** do this for one vendor's agents in one vendor's cloud. SCP is
  vendor-neutral and self-hostable.

## Authors

Muhammad Salman ([recluzegeek](https://github.com/recluzegeek)).

## License

[Apache License 2.0](LICENSE): the specification, schemas and code. The same license as A2A; its
patent grant covers everyone who implements the protocol.
