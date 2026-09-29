# ADR-003: Use the official MCP Go SDK in stateless mode

**Date:** 2026-09-29
**Author:** jasper
**Status:** Accepted

## Context

Until now this binary has two direct dependencies: a Web Push library and a pure-Go
SQLite driver. Every handler is a single request/response exchange — there is no
JSON-RPC, no SSE, no long-lived connection anywhere in the codebase.

The MCP wire protocol is more fiddly than it looks. The specification has an era
split: **2026-07-28 removed the `initialize` handshake, protocol sessions,
`Mcp-Session-Id` and the GET SSE stream** ([changelog](https://modelcontextprotocol.io/specification/2026-07-28/changelog)),
while Claude and ChatGPT still speak the 2025-03-26/06-18/11-25 authorization and
lifecycle behaviour. Getting a tools-only server right means version negotiation,
`Accept` handling, choosing JSON vs `text/event-stream` per response, 202 for
notifications, 405 for GET, honouring `MCP-Protocol-Version`, and validating `Origin`
against DNS rebinding ([transports](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports)).

The alternative to the SDK was hand-rolling `initialize`, `tools/list`, `tools/call`
and `ping`.

## Decision

Depend on **`github.com/modelcontextprotocol/go-sdk`** (v1.8.0, Apache-2.0/MIT) and
serve it with `mcp.NewStreamableHTTPHandler` in **stateless mode**
(`StreamableHTTPOptions.Stateless`), behind the handler's `getServer func(*http.Request)
*mcp.Server` callback so the room, its secret and the calling client's identity come
from the HTTP request in flight rather than from a process-wide server.

## Consequences

- Upstream owns cross-client protocol drift, including the handshake-era split that
  a hand-rolled subset would get wrong for one family of clients or the other.
  `StreamableHTTPOptions.Stateless` and `CrossOriginProtection` cover the session and
  DNS-rebinding requirements in one option each.
- go.mod goes from two direct dependencies to roughly three, with about eight
  transitive. The binary stays a static Go build; nothing about Docker or the
  deployment shape changes.
- Go 1.25.0 is required by the SDK — the module already declares `go 1.25.0`, so
  there is no toolchain cost.
- Stateless is a real constraint, not just a flag: there is no `Mcp-Session-Id`, so a
  client that insists on resuming a session or opening the server-initiated GET stream
  gets 405. That is spec-conformant and sufficient for a tools-only server, but it
  rules out server-pushed `list_changed` and progress notifications without a design
  change.
- Room scoping has to happen in our own layer: validate the room before delegating,
  and build the per-request server from the path. The SDK gives no opinion about it.
