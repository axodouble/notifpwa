# ADR-004: Agent notification content is public by default — no IP or personal data is embedded or accepted

**Date:** 2026-09-29
**Author:** jasper
**Status:** Accepted
**Supersedes:** the client-address default title recorded in FDR-001 §3 (that decision is rewritten in place; the Feature Decision Record is rewritten, not this ADR)

## Context

A room notification leaves the instance through a push service that is not ours:
Web Push hands the ciphertext and the subscription endpoint to a third-party push
provider (Mozilla, Google, or whatever self-hosted relay an operator runs) before it
reaches the device. Web Push encrypts the payload to the subscription key, but that
is a thin guarantee, not a boundary we control:

- The subscription endpoint URLs, the `/mcp/{room}` path, and any `?secret=` ride in
  request lines that cross proxies and provider logs we do not own (see ADR-002,
  which deliberately puts the room secret in the URL).
- The VAPID `Subscriber` and per-provider metadata are ours to lose but visible
  upstream.
- Encryption depends on the deployment. A relay, a proxy, or a client on an older
  revision can expose content we assumed was sealed.

The surface we actually control is what an agent puts in the `title` and `body`, and
what the server itself adds to a notification. Today the server adds one thing: the
default title is the caller's IP address, optionally prefixed with the client name
(FDR-001 §3). So the endpoint is, by construction, putting a client IP into a payload
that traverses third parties — the exact class of data we should never send here.

Agents compound this. A coding agent told to "notify me when you're done" will happily
paste a hostname, an internal URL, a stack trace with file paths and usernames, a
connection string, or a token into a message it believes goes only to its operator.
The operator did not consent to that leaving the machine, and the agent cannot know
which push path a given delivery takes.

## Decision

**Treat everything that enters a notification as public, and stop shipping personal
data ourselves.**

1. The server embeds **no** caller-identifying data in a notification. The IP-address
   default title is removed. The `title` becomes a **required** argument the agent
   supplies, so the notification carries only what the agent chose to say.
2. The server instructs agents — in both the tool description and the server
   instructions — that notifications may be read by third parties, and that IP
   addresses, hostnames, secrets, tokens, and personal or private information must
   never be sent through them.
3. This is a property of the notification path in general, not of the MCP tool alone:
   any future surface that posts to a room inherits "content may be seen by a
   third party; do not put personal data in it."

## Consequences

- The endpoint no longer leaks a client IP in a notification, so it can be used from
  behind shared egress or a VPN without the address surfacing on the operator's phone.
- Agents now must choose a title. That is a deliberate nudge to make the message
  self-describing ("Nightly build", "Blocked: needs DB password") rather than
  self-identifying.
- The privacy note is advisory: the server cannot reliably detect or redact a secret
  an agent pastes into a body, and does not try to. It states the threat model and
  refuses the data it controls (the IP). A future content-scanning pass is explicitly
  out of scope; the capability boundary is that we do not *ourselves* inject personal
  data and we tell agents not to send it.
- The room secret still lives in the URL (ADR-002). This ADR does not undo that
  tradeoff; it only removes the IP the server used to add on top. Treating the URL as
  a secret remains the operator's job.
