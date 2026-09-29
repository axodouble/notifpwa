# Agent notifications via MCP — design

**Date:** 2026-09-29
**Status:** Approved
**Records:** [ADR-001](../../adr/ADR-001-remote-mcp-over-browser-webmcp.md),
[ADR-002](../../adr/ADR-002-authless-mcp-endpoint.md),
[ADR-003](../../adr/ADR-003-official-go-mcp-sdk.md),
[FDR-001](../../fdr/FDR-001-agent-notifications.md)

## Problem

An AI agent doing long work should be able to buzz the operator's phone when it
finishes, and again when it is blocked waiting for input. The operator should configure
it by handing the agent a URL.

The original ask was "WebMCP support". It is the wrong instrument for this goal: the
browser API is in Chrome/Edge origin trial only, has no WebKit implementation on the
app's flagship platform (iOS), needs an origin-bound token a self-hoster cannot ship,
and only exists while a human has the page open. Details and sources: ADR-001.

## Chosen approach

Add a **remote MCP server** to the existing binary at `POST /mcp/{room}`. It publishes
exactly one tool. It is a second front door onto the existing room broadcast path, not
a second delivery system: the tool calls `Server.broadcastRoom(room, secret, payload)`
(`internal/server/rooms.go:233`), so delivery, secret filtering, the room post log, and
expired-endpoint pruning are shared with `POST /n/{room}` byte for byte.

Rejected: browser WebMCP page tools (ADR-001); an authenticated endpoint using the
existing admin token, a new `MCP_TOKEN`, or OAuth 2.1 (ADR-002); a hand-rolled
JSON-RPC subset (ADR-003); per-device addressing, which needs an admin-capable device
lookup.

## Architecture

One new file, `internal/server/mcp.go`, plus one line in `Server.Handler()`:

```go
mux.HandleFunc("POST /mcp/{room}", s.rateLimitPost(s.handleMCP))
```

- **Room scoping.** The SDK's Streamable HTTP handler is built once, in `New()`, and
  held on `Server`. `handleMCP` validates `r.PathValue("room")` with the existing
  `validRoomName()` — invalid → `400` — then hands the request to it. The room and the
  URL's `?secret=` reach the tool through that handler's
  `getServer func(*http.Request) *mcp.Server` callback (ADR-003), which returns the
  room-bound server for the request in flight.
- **Stateless.** `StreamableHTTPOptions{Stateless: true}`: no `Mcp-Session-Id`, one
  request/response at a time. `CrossOriginProtection` stays on, satisfying the spec's
  `Origin` validation against DNS rebinding. GET on the path returns 405, which the
  transport permits for a server that offers no server-initiated stream.
- **Server identity.** `serverInfo` name `notifpwa`, version from the existing
  `appVersion()`. `instructions` states the purpose of the endpoint, with the operative
  sentence inside the first 512 characters.
- **Rate limiting.** No new policy: the existing `rateLimitPost` bucket, unchanged,
  shared with the public room endpoints (ADR-002).

## Tool contract

`notify_operator` — one tool, all arguments except `body` optional.

| Argument | Required | Default | Notes |
|---|---|---|---|
| `body` | yes | — | what to tell the operator |
| `title` | no | caller address, prefixed with the client name when the request carries one | agent-overridable |
| `url` | no | `/` | resolved by the service worker against the app origin (`internal/server/web/sw.js:70`) |
| `urgency` | no | `high` | `very-high` \| `high` \| `normal` \| `low` |
| `secret` | no | the URL's `?secret=` | explicit argument wins |

Tool annotations: `readOnlyHint:false`, `destructiveHint:false`,
`idempotentHint:false`, `openWorldHint:true`. `title` (display name) and every
parameter carry a description, since agents select arguments from them.

Defaults rationale — why the title names the caller, why the link is `/` rather than a
URL assembled from `Host`/`X-Forwarded-Host`, and why urgency is high by default — is
FDR-001 §3–5.

## Result and failure handling

The tool returns text content summarising `sent`/`failed`/`pruned`/`recipients`.

The one case that must not pass for success: `broadcastRoom` deliberately returns
`recipients:0` and leaves no trace when nothing matches (`rooms.go:239`), so a typo'd
room name or a wrong secret would otherwise read as *"notified the operator"*. `recipients
== 0` therefore sets `isError: true` with text naming the two likely causes. Store or
delivery errors are likewise reported as tool errors with a short message rather than a
protocol error, so the agent can act on them.

HTTP-level failures keep the app's existing shape: `400` invalid room name, `429` rate
limited. Both are outside the MCP envelope, which is acceptable for a server whose only
method surface is one tool.

## Security posture

The endpoint is unauthenticated by decision, not by omission — posting to a room is
already unauthenticated, and the room secret is a delivery filter, not a credential.
The consequences, including "the URL is the capability" and the shared-egress-IP rate
limit exposure, are recorded in ADR-002 and must be reflected in the README.

Two specific refusals: the notification link is never derived from a request header, and
no admin capability (device list, room list, tokens) is reachable through this endpoint.

## Out of scope

Browser WebMCP page tools; any credential or OAuth flow; admin-visible MCP tooling;
per-device targeting; a UI that displays an agent URL (secrets are stored hashed, so a
guarded URL cannot be reconstructed — FDR-001 §8); a dedicated rate-limit bucket; any
change to `POST /n/{room}`.

## Verified behaviour (go-sdk v1.8.0, probed before writing this)

Checked against a throwaway program rather than assumed, because two of the results
contradict the obvious guess:

- `initialize` → 200 `application/json` echoing the client's `protocolVersion`;
  `notifications/initialized` → `202` empty; `tools/list` and `tools/call` both answer
  plain JSON with `JSONResponse: true`.
- A struct with `json:"x,omitempty"` yields `required: ["body"]` and
  `additionalProperties: false` — so optionality comes from `omitempty`, and an agent
  that sends an unexpected argument is rejected before the handler runs.
- Returning `&mcp.CallToolResult{IsError: true, Content: ...}` reaches the client as
  `isError: true` with the text intact. Missing `body` is auto-reported as a tool error,
  so the handler needs no manual argument validation.
- Stateless mode answers `GET` with `405` without any extra wiring.
- **A 2025-06-18-era `tools/call` carries no client identity** (Claude, ChatGPT). Only
  the 2026-07-28 protocol attaches `io.modelcontextprotocol/clientInfo` per request. This
  is why the default title leads with the address and treats the client name as a bonus;
  see FDR-001 §3. That newer revision is stricter about transport metadata too: it
  requires `Mcp-Method` and `Mcp-Name` headers and a `_meta` object carrying
  `protocolVersion`, `clientCapabilities` and `clientInfo`, each omission answered with
  its own 400. Real clients of that revision send all of it; only hand-built test requests
  have to spell it out.
- `StreamableHTTPOptions.CrossOriginProtection` is deprecated against a `net/http` type
  that only exists in Go 1.26, while this module declares `go 1.25.0`. Rather than raise
  the build floor for self-hosters, the `Origin` check the spec requires is done in
  `mcp.go` in a few lines: an `Origin` header that does not match the request host is
  refused with `403`. The SDK's automatic localhost rebinding guard stays on.

## Verification

New `internal/server/mcp_test.go`, following the existing pattern (`newTestApp`,
`httptest.NewRequest` → `s.Handler().ServeHTTP`, `sendOne` stubbed — see
`internal/server/rooms_test.go:176` for the stub idiom and
`internal/server/push_test.go:13` for the harness). Requests are raw JSON-RPC so the
protocol shape is asserted, not hidden behind a client:

1. `initialize` → 200; echoes the client's `protocolVersion`; declares the `tools`
   capability; carries `serverInfo` and non-empty `instructions` mentioning the operator.
2. `notifications/initialized` → `202` with no body.
3. `tools/list` → exactly one tool named `notify_operator`; `inputSchema` requires only
   `body`.
4. `tools/call` with `sendOne` stubbed → delivered, room log row written, result text
   contains the counts, `isError` false.
5. `tools/call` against a room with no matching subscriber → `isError` true.
6. Secret precedence: `?secret=` on the URL reaches a secret-guarded device; an explicit
   `secret` argument overrides the URL.
7. Invalid room name → `400`; `GET /mcp/{room}` → `405`.
8. `MCP-Protocol-Version` on post-initialize requests is accepted, and a request without
   it is not rejected.

`go test ./...` is the gate. There is no JS or Docker surface in this change.

Manual check, documented but not automated: `claude mcp add --transport http notifpwa
http://localhost:8080/mcp/<room>`, then ask the agent to notify the operator, and confirm
the notification's title, link and urgency.

## Documentation and records

- README: a row for `POST /mcp/{room}` in the API table (auth: none); an **Agents
  (MCP)** section giving the URL form, the secret-in-URL form, the Claude Code connect
  command, the note that hosted clients need a publicly reachable HTTPS host, and an
  explicit warning that the URL is a capability so a room secret is worth setting.
- `docs/adr/` and `docs/fdr/` are seeded by this change (they did not exist): three ADRs
  and FDR-001 above, each with its `INDEX.md`.
- The transient research note on the browser API is not kept: it documents the direction
  this design rejected.

## Delivery

One commit on a `feat/mcp-notify` branch, `--no-gpg-sign`, not pushed. Contents:
`internal/server/mcp.go`, `internal/server/mcp_test.go`, the route line in
`handlers.go`, `go.mod`/`go.sum`, README, and `docs/`.
