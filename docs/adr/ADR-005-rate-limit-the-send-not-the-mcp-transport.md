# ADR-005: Rate limiting gates the send, not the MCP transport

**Date:** 2026-10-01
**Author:** jasper
**Status:** Accepted

## Context

`POST /mcp/{room}` rode in `POST /n/{room}`'s per-IP bucket (ADR-002), on the reasoning
that both are unauthenticated public ways to raise a notification. That reasoning holds
for the notification and fails for everything else the MCP endpoint serves, because an
MCP client does far more than call the tool.

The SDK answers `tools/list` with `"ttlMs":0` and advertises `tools.listChanged`, which
tells a client the tool list is not cacheable. OpenCode takes that literally and
re-handshakes and re-lists on every turn of every session. Measured against a deployed
instance, twelve rounds of `initialize` + `tools/list` from one client — 24 POSTs — were
enough to exhaust the bucket (burst 20, refill 5/s) and start answering 429 on requests
that carry no notification at all.

What a client does with that is the expensive part. When the handshake or the listing
fails, OpenCode drops the server: the `notify_operator` tool and the server's
`instructions` block both leave the prompt, and reappear when the bucket refills. Both
sit at the head of the request — tools first, then the system context — so each flicker
invalidates the provider's prefix cache for everything behind it, which is the entire
conversation. A limiter on the transport therefore did not cost the endpoint a call; it
cost the client its cache, on every turn, for as long as any other client on the same
egress IP kept the bucket empty. The shared-egress exposure ADR-002 already accepted
made that likely rather than hypothetical.

Meanwhile the transport is the cheapest thing the server does: stateless JSON (ADR-003),
no database write, no push, no allocation that a caller controls.

## Decision

**Only sends are rate limited.** One bucket, keyed by client IP, consulted at the two
places a notification actually leaves the process:

- `POST /n/{room}`, through the `rateLimitSend` middleware as before;
- the `notify_operator` tool handler inside the MCP endpoint, which consults the same
  bucket before broadcasting and answers with a tool error naming the limit and asking
  for a pause, rather than with an HTTP status.

The MCP handshake, `tools/list`, and `initialized` are not rate limited at all, and
neither are the browser's room endpoints (`GET/POST/DELETE /api/rooms`,
`GET /api/rooms/log`) — reads and keyed membership writes that are not sends.
`POST /api/subscribe` keeps its own limiter: it is not a send, but it is an
unauthenticated database write, and a flood of registrations is its own problem.

## Consequences

- The prompt an agent sends stops changing between turns, which is what keeps a
  provider-side prefix cache warm. Reliability of the notification path improves with it:
  an agent that is limited reads a sentence, not a transport failure, and retries once
  instead of concluding the server is gone.
- The cost ceiling is unchanged where it matters. Push to real devices remains capped per
  IP at the same rate as `POST /n/{room}`, so one agent cannot spend more of the
  operator's push quota than one `curl` could.
- Cheap requests are now genuinely unthrottled, so an attacker can make the server do
  work — TLS termination, JSON parse, an SDK server built per request — without ever
  sending anything. That work is constant-size and reaches no database and no vendor, so
  the exposure is bandwidth and CPU rather than money. If it ever needs a ceiling, it
  belongs at the proxy, which is where connection-level limits live anyway, not in an
  application bucket that agents can trip by behaving normally.
- Shared egress IPs still share one send bucket, so a noisy agent on a hosted client can
  still starve an operator's own sends. This narrows the starvation to sends only; it
  does not solve it. A per-room or per-token bucket would, and stays open.
- The IP is a bucket key and nothing else: it is never in a notification, a log row, or a
  tool result, so ADR-004's rule that no caller-identifying data reaches a notification
  stands. It is also spoofable through `X-Forwarded-For`, as it always was — these
  buckets are fairness, not a security control.
- `ttlMs: 0` and `listChanged` are left as the SDK sets them. With the transport
  unthrottled they no longer cost anything, and overriding them means fighting the SDK's
  defaults for a benefit that is now only latency.
