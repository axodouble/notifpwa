# Architecture Decision Records

Decisions about stack, protocols, data models, security and deployment — the choices
other code and features inherit. Feature behaviour lives in [`../fdr/INDEX.md`](../fdr/INDEX.md).

| # | Decision | Status | Author | Date |
|---|----------|--------|--------|------|
| [ADR-001](ADR-001-remote-mcp-over-browser-webmcp.md) | Agent reachability is a remote MCP server, not browser WebMCP | Accepted | jasper | 2026-09-29 |
| [ADR-002](ADR-002-authless-mcp-endpoint.md) | The MCP endpoint is unauthenticated; the room and its secret are the capability | Accepted | jasper | 2026-09-29 |
| [ADR-003](ADR-003-official-go-mcp-sdk.md) | Use the official MCP Go SDK in stateless mode | Accepted | jasper | 2026-09-29 |
| [ADR-004](ADR-004-no-personal-data-in-agent-notifications.md) | Notification content is public by default — no IP or personal data is embedded or accepted | Accepted | jasper | 2026-09-29 |
| [ADR-005](ADR-005-rate-limit-the-send-not-the-mcp-transport.md) | Rate limiting gates the send, not the MCP transport | Accepted | jasper | 2026-10-01 |
