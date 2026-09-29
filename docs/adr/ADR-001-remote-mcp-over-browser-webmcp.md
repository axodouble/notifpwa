# ADR-001: Agent reachability is a remote MCP server, not browser WebMCP

**Date:** 2026-09-29
**Author:** jasper
**Status:** Accepted

## Context

The ask was "add WebMCP support". The goal behind it is narrower and older than the
API: an AI agent doing long work (Claude Code, ChatGPT, a coding agent on a laptop)
should be able to buzz the operator's phone when it finishes, and again when it is
blocked waiting for input. The operator configures it by handing the agent a URL.

WebMCP is the in-browser proposal exposing `document.modelContext.registerTool()`,
which lets a page publish tools to an agent driving that page
([spec](https://github.com/webmachinelearning/webmcp)). Checking it against this app
made it the wrong instrument:

- It is an origin trial in Chrome 149 and Edge 150, with ChatGPT Desktop and Brave
  Leo partway behind. Firefox and Safari/WebKit have no implementation
  ([status](https://github.com/webmachinelearning/webmcp/blob/main/implementation-status.md))
  — and iOS is this app's flagship platform.
- Origin-trial tokens are bound to one origin, so a self-hosted app cannot ship a
  working token for its users' hosts. Google's own demo token decodes to
  `https://googlechromelabs.github.io`.
- It is tab-bound: tools exist only while a human has the page open, which is the
  opposite of an agent finishing a job unattended.
- The spec explicitly scopes out fully autonomous, UI-less workflows and points
  server-side work at backend MCP instead
  ([README, Background and Motivation](https://github.com/webmachinelearning/webmcp#backend-integrations-vs-in-browser-webmcp-tools)).

## Decision

Expose a **remote MCP server over the Streamable HTTP transport** at
`POST /mcp/{room}` and do not implement browser WebMCP page tools. The room is
carried in the path, so the URL handed to an agent is complete configuration.

The server adapts MCP onto the existing room broadcast path, so it is a second
front door to delivery rather than a second delivery system.

## Consequences

- Agents are reachable headless, from any MCP client, on any platform — including iOS,
  where no page-tools implementation exists to reach.
- Hosted clients (ChatGPT, Claude's connector surfaces) connect from their own
  infrastructure, so the app must be publicly reachable over HTTPS. It already has
  to be, for iOS push.
- Nothing about WebMCP needs to be revisited before this works. A page-tools layer
  can be added later, for a human-in-the-loop agent inside a browser tab, without
  disturbing this endpoint — it would be a new decision and would supersede nothing
  here.
- The protocol has no auth of its own at this layer, which forces the credential
  question to be answered on its own terms; see ADR-002.
- Protocol conformance across client generations becomes a dependency question; see
  ADR-003.
