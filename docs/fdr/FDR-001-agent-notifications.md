# FDR-001: Agent notifications (MCP endpoint)

**Status:** Active
**Author:** jasper
**Last reviewed:** 2026-09-29 — jasper

## Overview

A coding agent or assistant that works unattended needs a way to tell the person
whose machine it is on: *"finished"*, or *"I need you to answer something first"*.
This feature gives the operator one URL to hand to such an agent, per room, and turns
that URL into a single tool the agent can call. The agent's message arrives as an
ordinary push notification on every device that has joined the room — the same path a
`curl` against the room already takes, so there is no notion of an "agent
notification" distinct from any other post.

## Behavior

- The operator gives the agent `https://<host>/mcp/<room>`. Nothing else is
  configured, per agent or per client.
- The agent finds one tool whose purpose is stated as reaching the operator, and whose
  instructions say to use it when a task finishes **and** when it needs the operator's
  input. That framing is what makes the agent reach for it at the right moments.
- The agent must supply the message. Everything else has a default the operator chose:
  the title names the client that called plus the address it came from, the
  notification opens the app, and it arrives marked urgent so it is not buried.
  Each default can be overridden by the agent — the title, the link, and the urgency
  are all arguments.
- A notification reaching nobody is reported to the agent as a failure, with the reason
  ("no subscriber in this room matched — check the room name and the secret"), so the
  agent never tells the operator it notified them when it did not.
- Agents that deliver to a secret-protected room can pass the secret as an argument; if
  the operator baked it into the URL, the argument is not needed. An explicit argument
  overrides the URL.
- No screen in the app shows an agent URL. The operator composes it from the room name
  (and the secret, if the room has one) using the README.

## Design Decisions

### 1. One tool, described as reaching the operator

**Decision:** A single tool, and the server-level instructions lead with the operative
"notify the operator when you are done or need input" sentence inside the first 512
characters.
**Why:** Agents choose tools from descriptions, and OpenAI's guidance for how much of a
server's instructions clients actually carry is the first 512 characters. Naming the
human rather than the mechanism matches how the operator thinks about it. A larger tool
set would cost context budget on an app whose whole surface is one action.
**Tradeoff:** An agent wanting anything else — listing rooms, targeting one device —
has no way to ask, and must be told the room in the prompt.

### 2. The room lives in the URL, not the arguments

**Decision:** The room is a path segment; the bare host serves nothing.
**Why:** The operator's job is "hand the agent a URL and call it a day". A `room`
argument would put the burden on the agent to know something only the operator knows,
which is the same failure as making them pass a token.
**Tradeoff:** One URL per room; an agent working across several rooms needs several
connections.

### 3. The title defaults to the caller's address, naming the client when it can

**Decision:** Default title is the address the request arrived from, e.g.
`203.0.113.7`, prefixed with the client's own name when the request carries one —
`opencode (203.0.113.7)`.
**Why:** The room name would tell the operator nothing they did not already know; the
useful question is *who is pinging me*. Client naming is opportunistic rather than
guaranteed: the 2026-07-28 protocol carries the client's identity on every request, but
the older protocol revision that Claude and ChatGPT speak carries it only during the
handshake — and a stateless server has no handshake to look back on. Verified against
the SDK: a 2025-06-18 `tools/call` arrives with no client identity attached.
**Tradeoff:** Through ChatGPT or Claude's hosted connectors the title is that vendor's
shared egress address and nothing more — it identifies the vendor, not the machine or
the agent. Naming the client reliably would mean holding session state across requests,
which is the thing stateless mode exists to avoid.

### 4. The link defaults to the app root, not a URL assembled from the request

**Decision:** The notification's tap target defaults to `/`, resolved by the service
worker against the app's own origin.
**Why:** The obvious implementation — build an absolute URL from the request's `Host` or
`X-Forwarded-Host` — lets anyone who can reach the endpoint steer the link inside a
notification the operator trusts. `/` is the instance URL for every practical purpose.
**Tradeoff:** An agent cannot deep-link without saying so explicitly, which is the
intended behaviour.

### 5. Urgent unless the agent says otherwise

**Decision:** Urgency defaults to high and remains overridable.
**Why:** The point of the feature is that the operator sees it soon; a notification that
quietly batches into a low-priority group is the failure mode.
**Tradeoff:** "Just letting you know" traffic arrives as loudly as "I am blocked", until
an agent learns to downgrade it.

### 6. Zero recipients is an error, not a success with a caveat

**Decision:** When no device matches, the tool reports failure and explains why.
**Why:** Room posts deliberately succeed with zero recipients so strangers cannot mint
empty rooms or evict a room's log history. Reusing that path unchanged would let an
agent report success having reached nobody — the one outcome this feature exists to
prevent.
**Tradeoff:** A deliberate broadcast to a room that happens to be empty now looks like a
problem to the agent.

### 7. The secret rides on the URL, and the argument wins

**Decision:** `?secret=` on the endpoint URL is the default; the tool argument overrides
it.
**Why:** Keeps the URL-as-config promise true for protected rooms without inventing a
path scheme for secrets, and follows the existing room-post precedent.
**Tradeoff:** A secret in a URL appears in proxy access logs and in whatever the operator
pasted it into. Curing that would mean an argument only, which breaks the one-URL story.

### 8. No UI surface for the agent URL

**Decision:** Neither the app nor the admin page displays an agent URL.
**Why:** Room secrets are stored hashed, so the server cannot reconstruct a guarded URL.
Any URL the UI printed would be the unguarded one, which under-delivers silently for
exactly the protected rooms where being wrong matters.
**Tradeoff:** Discovery depends on the README, and the operator types the URL once.

## Related

- **ADRs:** [ADR-001](../adr/ADR-001-remote-mcp-over-browser-webmcp.md),
  [ADR-002](../adr/ADR-002-authless-mcp-endpoint.md),
  [ADR-003](../adr/ADR-003-official-go-mcp-sdk.md)
- **FDRs:** none

## Open Questions

- Per-person notification is expressed as "a room only that person joined". If
  addressing one device by name ever becomes a requirement, it needs a device lookup,
  which is an admin capability, which collides with the endpoint being unauthenticated
  (ADR-002).
- Agents cannot list rooms. An agent-visible list would leak room names to anyone who
  reaches the endpoint.
- The endpoint shares the public post bucket. Because hosted clients connect from shared
  egress IPs, a stranger's traffic can produce a 429 for the operator; a bucket of its
  own is the obvious fix if it turns out to matter in practice.
