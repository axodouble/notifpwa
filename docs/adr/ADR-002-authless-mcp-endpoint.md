# ADR-002: The MCP endpoint is unauthenticated; the room and its secret are the capability

**Date:** 2026-09-29
**Author:** jasper
**Status:** Accepted

## Context

A remote MCP endpoint needs an answer to "who may call this?". Three candidate
answers, and one hard constraint.

The constraint is that **posting to a room is already unauthenticated**
(see the API table in `README.md`: `POST /n/{room}` needs no token). Room
membership proves that a device listens to a room; the per-device room secret is a
*delivery filter*, not an account credential. An authenticated MCP endpoint would
be protecting a capability that a `curl` against `/n/{room}` already grants anyone.

The hard constraint comes from the clients: ChatGPT **refuses** static bearer
tokens — an authenticated server must implement the full OAuth 2.1 authorization
spec ([OpenAI auth](https://developers.openai.com/plugins/build/auth.md)) — while
Claude and Claude Code accept either OAuth or a static header
([Claude auth](https://claude.com/docs/connectors/building/authentication.md),
[Claude Code](https://code.claude.com/docs/en/mcp)). A key-based design would work
for one client and not the other.

The candidates were: the existing admin token as bearer; a new `MCP_TOKEN` shared
secret; full OAuth 2.1; or nothing.

## Decision

**Nothing.** `POST /mcp/{room}` accepts no credential of any kind. The room name in
the path, and optionally its secret supplied as `?secret=` on the URL or as the
`secret` tool argument, *is* the capability. This mirrors `/n/{room}` exactly.

It originally reused `/n/{room}`'s per-IP limiter unchanged. That half of the decision
was reversed by [ADR-005](ADR-005-rate-limit-the-send-not-the-mcp-transport.md): the
notification a call may produce is still limited by that bucket, the transport is not.

## Consequences

- One endpoint behaves like the rest of the public surface. There is no second
  notion of "who is allowed to notify me" to explain, and no secret for the operator
  to rotate, paste into a third-party settings file, or leak.
- ChatGPT's anonymous/read-only mode, Claude's default, and Claude Code all connect
  without configuration. This is the single choice that makes the endpoint work
  uniformly across the clients an operator would actually use.
- The trade is real and accepted: **the URL is the capability.** Anyone who learns
  `https://host/mcp/alerts` can raise a notification on devices in `alerts` that have
  no secret set. That is already true of `https://host/n/alerts`, so the exposure is
  not new — but it is newly *discoverable* by agents, which is why the README tells
  operators to set a room secret and to treat the URL as a secret.
- Claude and ChatGPT call from **shared egress IPs**, so the per-IP send bucket is shared
   with strangers and an operator's own sends can be starved by agents they never ran.
   A bucket of its own was deferred there as an open question; ADR-005 later took the
   limiter off the transport for a different reason, and the shared bucket for the send
   itself is still open.
- Reaching a secret-protected room requires knowing the secret. Secrets are stored
  hashed only, so the server cannot hand the operator a pre-built URL — they compose
  it themselves, from a secret they already know.
