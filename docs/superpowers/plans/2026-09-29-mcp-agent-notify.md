# Agent notifications via MCP — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an unauthenticated remote MCP server at `POST /mcp/{room}` whose single tool lets an AI agent push a notification to the operator.

**Architecture:** One new file adapts MCP onto the existing room broadcast path (`Server.broadcastRoom`), so delivery, secret filtering, logging and endpoint pruning are shared with `POST /n/{room}`. The official MCP Go SDK serves the protocol in stateless mode; the room and its secret come from the URL through the handler's per-request `getServer` callback.

**Tech Stack:** Go 1.25+, `github.com/modelcontextprotocol/go-sdk/mcp` v1.8.0, existing `net/http` ServeMux, modernc SQLite, `go test`.

**Spec:** `docs/superpowers/specs/2026-09-29-mcp-agent-notify-design.md` (read it; this plan implements it)
**Records:** `docs/adr/ADR-001`, `docs/adr/ADR-002`, `docs/adr/ADR-003`, `docs/fdr/FDR-001`

## Global Constraints

- **Exactly one commit for the whole plan**, made in Task 4, on branch `feat/mcp-notify`, created with `git commit --no-gpg-sign`. **Do not push. Never `git push`.** Do not commit in Tasks 1–3 even though the TDD cadence suggests it — this is an explicit operator instruction that overrides the usual commit-per-task habit.
- Exactly one new module dependency: `github.com/modelcontextprotocol/go-sdk v1.8.0`. No others. Do not touch `webpush-go` or `modernc.org/sqlite`.
- Do **not** raise the `go` directive in `go.mod` (stays `go 1.25.0`). Specifically do not use `http.NewCrossOriginProtection()` / `StreamableHTTPOptions.CrossOriginProtection`: that `net/http` type needs the Go 1.26 stdlib, and self-hosters build with whatever Go they have. The `Origin` check is written by hand instead.
- No authentication on `/mcp`. No new rate limiter: reuse the existing `s.rateLimitPost` middleware unchanged.
- The notification's `url` must never be derived from `Host` or `X-Forwarded-Host`. Default is the literal `"/"`.
- Tool name is `notify_operator`. Endpoint path pattern is `POST /mcp/{room}`. Room names are validated with the existing `validRoomName`.
- No frontend, JS, Docker, or schema changes. `data.db` schema is untouched.
- Test idiom in this repo: build the app with `newTestApp(t)`, drive it with `httptest.NewRequest` + `httptest.NewRecorder` + `s.Handler().ServeHTTP`, stub push by swapping the package var `sendOne`. There is no `httptest.NewServer` usage anywhere; keep it that way.

## File Structure

| File | Responsibility |
|---|---|
| `internal/server/mcp.go` (create) | Everything MCP: the room-scoped `*mcp.Server`, the `notify_operator` tool definition, the argument defaults, the result wording, the hand-written `Origin` guard, and the lazily built SDK handler. |
| `internal/server/mcp_test.go` (create) | Protocol-level tests (handshake, `tools/list`, `405`, `400`, `403`) and behaviour tests (defaults, secret precedence, zero-recipient error). |
| `internal/server/server.go` (modify) | Two new fields on `Server`: `mcpOnce sync.Once`, `mcpHandler http.Handler`. Nothing else. |
| `internal/server/handlers.go` (modify) | One route line in the public block. |
| `README.md` (modify) | API table row + an "Agents (MCP)" section. |
| `docs/**` (already on disk) | Spec, three ADRs, FDR-001, both `INDEX.md`. They join the same commit. |
| `go.mod`, `go.sum` (modify) | The one new dependency. |

Why `mcp.go` holds the handler instead of `server.go`: `newTestApp` constructs `&Server{...}` directly rather than calling `New()`, so the SDK handler must be built lazily on first request. Keeping the `sync.Once` next to the code it initialises keeps that subtlety in one place.

---

### Task 1: The endpoint, and a client discovering the tool

Deliverable: an MCP client can complete the handshake against `POST /mcp/{room}` and `tools/list` returns one correctly-shaped tool. No notification is sent yet.

**Files:**
- Create: `internal/server/mcp.go`
- Create: `internal/server/mcp_test.go`
- Modify: `internal/server/server.go:26-37` (the `Server` struct)
- Modify: `internal/server/handlers.go:25-29` (public route block)
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: `validRoomName(string) bool` (`rooms.go:255`), `clientIP(*http.Request) string` (`ratelimit.go:101`), `(*Server).appVersion() string` (`server.go:120`), `s.rateLimitPost(http.HandlerFunc)` (`rooms.go:279`).
- Produces: `func (s *Server) serveMCP(w http.ResponseWriter, r *http.Request)` — the route handler; `func (s *Server) mcpServer(room mcpRoom) *mcp.Server`; `type mcpRoom struct { name, secret, caller string }`; `type notifyInput struct{...}` with fields `Body, Title, URL, Urgency, Secret string`; `func toolText(string) *mcp.CallToolResult`; `func toolError(string) *mcp.CallToolResult`. Task 2 fills in the behaviour behind the tool handler.

- [ ] **Step 1: Add the dependency**

Run:

```bash
cd /home/jasper/Projects/notifpwa && go get github.com/modelcontextprotocol/go-sdk/mcp@v1.8.0
```

Expected: go.mod gains a `require github.com/modelcontextprotocol/go-sdk v1.8.0` block. Verify the `go` directive is still `go 1.25.0`:

```bash
head -5 go.mod
```

If `go get` bumped it to `go 1.26.0`, set it back to `1.25.0` by editing `go.mod` — the SDK declares `go 1.25.0` and so must we.

- [ ] **Step 2: Write the failing test**

Create `internal/server/mcp_test.go`. The helper `mcpRPC` is used by every test in this file and by Task 2, so write it now:

```go
package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// mcpRPC posts one JSON-RPC message at an MCP path and returns the HTTP status
// plus the decoded envelope. Responses are plain JSON because the handler sets
// JSONResponse; no SSE parsing is needed.
func mcpRPC(t *testing.T, s *Server, path, proto string, msg map[string]any) (int, map[string]any) {
	t.Helper()
	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest("POST", path, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if proto != "" {
		req.Header.Set("MCP-Protocol-Version", proto)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("status %d, undecodable body %q: %v", rec.Code, rec.Body.String(), err)
		}
	}
	return rec.Code, out
}

// initialize is the request every client sends first.
func initializeMsg() map[string]any {
	return map[string]any{
		"jsonrpc": "2.0", "id": float64(1), "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "testclient", "version": "0"},
		},
	}
}

func TestMCPHandshakeAndToolsList(t *testing.T) {
	s := newTestApp(t)

	code, res := mcpRPC(t, s, "/mcp/alerts", "", initializeMsg())
	if code != 200 {
		t.Fatalf("initialize status = %d, want 200 (body %v)", code, res)
	}
	result, _ := res["result"].(map[string]any)
	if result == nil {
		t.Fatalf("initialize result missing: %v", res)
	}
	if got := result["protocolVersion"]; got != "2025-06-18" {
		t.Fatalf("protocolVersion = %v, want the client's 2025-06-18", got)
	}
	info, _ := result["serverInfo"].(map[string]any)
	if info["name"] != "notifpwa" {
		t.Fatalf("serverInfo.name = %v, want notifpwa", info["name"])
	}
	instr, _ := result["instructions"].(string)
	if !strings.Contains(instr, "notify_operator") {
		t.Fatalf("instructions do not name the tool: %q", instr)
	}
	// The operative sentence must lead: OpenAI's clients use roughly the first
	// 512 characters of a server's instructions.
	if i := strings.Index(instr, "notify_operator"); i < 0 || i > 512 {
		t.Fatalf("notify_operator mentioned at offset %d, want within the first 512 chars", i)
	}
	caps, _ := result["capabilities"].(map[string]any)
	if _, ok := caps["tools"]; !ok {
		t.Fatalf("capabilities missing tools: %v", caps)
	}

	// The initialized notification is acknowledged with 202 and no body.
	code, res = mcpRPC(t, s, "/mcp/alerts", "2025-06-18", map[string]any{
		"jsonrpc": "2.0", "method": "notifications/initialized",
	})
	if code != 202 {
		t.Fatalf("notifications/initialized status = %d, want 202 (body %v)", code, res)
	}

	code, res = mcpRPC(t, s, "/mcp/alerts", "2025-06-18", map[string]any{
		"jsonrpc": "2.0", "id": float64(2), "method": "tools/list", "params": map[string]any{},
	})
	if code != 200 {
		t.Fatalf("tools/list status = %d, want 200", code)
	}
	tools := resultOf(t, res)["tools"]
	list, _ := tools.([]any)
	if len(list) != 1 {
		t.Fatalf("got %d tools, want 1", len(list))
	}
	tool, _ := list[0].(map[string]any)
	if tool["name"] != "notify_operator" {
		t.Fatalf("tool name = %v, want notify_operator", tool["name"])
	}
	schema, _ := tool["inputSchema"].(map[string]any)
	req, _ := schema["required"].([]any)
	if len(req) != 1 || req[0] != "body" {
		t.Fatalf("required = %v, want exactly [body]", req)
	}
	props, _ := schema["properties"].(map[string]any)
	for _, want := range []string{"body", "title", "url", "urgency", "secret"} {
		if _, ok := props[want]; !ok {
			t.Fatalf("inputSchema.properties missing %q in %v", want, props)
		}
	}
}

// resultOf fails the test if the envelope carries a JSON-RPC error instead of a result.
func resultOf(t *testing.T, envelope map[string]any) map[string]any {
	t.Helper()
	if e, ok := envelope["error"]; ok && e != nil {
		t.Fatalf("jsonrpc error: %v", e)
	}
	result, ok := envelope["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result object in %v", envelope)
	}
	return result
}

func TestMCPRejectsInvalidRoomForeignOriginAndGET(t *testing.T) {
	s := newTestApp(t)

	// A room name validRoomName refuses never reaches the protocol layer. The
	// space is percent-encoded: httptest.NewRequest panics on a raw space.
	req := httptest.NewRequest("POST", "/mcp/not%20a%20room", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid room status = %d, want 400", rec.Code)
	}

	// A browser Origin that is not this host is refused: the MCP transport's
	// DNS-rebinding rule, hand-written to keep the go directive at 1.25.
	req = httptest.NewRequest("POST", "/mcp/alerts", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign origin status = %d, want 403", rec.Code)
	}

	// Matching Origin is allowed through, and non-browser clients send none.
	req = httptest.NewRequest("POST", "/mcp/alerts", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://example.com")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden {
		t.Fatalf("same-host Origin refused; body %q", rec.Body.String())
	}

	// Stateless mode offers no server-initiated stream, so GET is 405.
	req = httptest.NewRequest("GET", "/mcp/alerts", nil)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", rec.Code)
	}
}
```

Note: `httptest.NewRequest` sets `Host` to `example.com`, which is why the same-host `Origin` above is `http://example.com`.

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/server/ -run TestMCP -v`
Expected: compile failure — `s.serveMCP undefined` (or `declare mcpRoom` etc.). That is the failing state; the route does not exist yet, so `POST /mcp/alerts` would be 404.

- [ ] **Step 4: Write the implementation**

Add the two fields to the `Server` struct in `internal/server/server.go` (after `sessions`), and add `"sync"` to the import block:

```go
	mcpOnce    sync.Once    // guards the lazy build of mcpHandler below
	mcpHandler http.Handler // built on first request: tests construct Server without New()
```

`server.go` imports `net/http` only if the compiler asks — it is not currently imported, so add it alongside `"sync"`.

Create `internal/server/mcp.go`:

```go
package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpInstructions is handed to every client that connects. Clients may fold it
// into the system prompt, and OpenAI's guidance is that only about the first 512
// characters are used, so the operative instruction comes first: an agent should
// reach the operator when it finishes and when it is blocked.
const mcpInstructions = "This server reaches the operator of this notifpwa instance. " +
	"Call notify_operator when your task is finished, and call it again when you are " +
	"blocked and need the operator to decide something. The message arrives as a push " +
	"notification on their phone. Keep body short and concrete: say what finished, or " +
	"say exactly what you need."

// notifyInput is the argument object of notify_operator. Only Body is required:
// optionality comes from `omitempty` in the json tag, which is what the SDK's
// schema inference reads.
type notifyInput struct {
	Body    string `json:"body" jsonschema:"What to tell the operator."`
	Title   string `json:"title,omitempty" jsonschema:"Notification title. Defaults to who is calling."`
	URL     string `json:"url,omitempty" jsonschema:"Link the notification opens. Defaults to this app."`
	Urgency string `json:"urgency,omitempty" jsonschema:"very-high, high, normal or low. Defaults to high."`
	Secret  string `json:"secret,omitempty" jsonschema:"Room secret, if the room has one."`
}

// mcpRoom is what the request URL said: which room to post to, the secret baked
// into the query string, and the address to blame in the notification title.
type mcpRoom struct {
	name   string
	secret string
	caller string
}

func roomFromRequest(r *http.Request) mcpRoom {
	return mcpRoom{
		name:   r.PathValue("room"),
		secret: r.URL.Query().Get("secret"),
		caller: clientIP(r),
	}
}

// serveMCP is the MCP endpoint for the room in the path. It is unauthenticated
// by design: posting to a room is unauthenticated already, and the room plus its
// secret is the capability. See docs/adr/ADR-002.
func (s *Server) serveMCP(w http.ResponseWriter, r *http.Request) {
	if !validRoomName(r.PathValue("room")) {
		http.Error(w, "invalid room name", http.StatusBadRequest)
		return
	}
	if !originAllowed(r) {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return
	}
	s.mcpHandlerFor().ServeHTTP(w, r)
}

// mcpHandlerFor builds the SDK handler once. It cannot be built in New: the test
// harness constructs Server values directly.
func (s *Server) mcpHandlerFor() http.Handler {
	s.mcpOnce.Do(func() {
		s.mcpHandler = mcp.NewStreamableHTTPHandler(
			func(r *http.Request) *mcp.Server { return s.mcpServer(roomFromRequest(r)) },
			&mcp.StreamableHTTPOptions{
				Stateless:    true,  // no Mcp-Session-Id; GET and DELETE answer 405
				JSONResponse: true,  // application/json rather than text/event-stream
			},
		)
	})
	return s.mcpHandler
}

// originAllowed enforces the MCP transport's DNS-rebinding rule: a request that
// claims an Origin must claim this host. Non-browser clients send no Origin at
// all. The SDK's equivalent option needs the Go 1.26 standard library, so this
// stays hand-written to keep the module's go directive at 1.25.
func originAllowed(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// mcpServer is the room-scoped view an agent sees. A fresh one is built per
// request so the tool closes over the room and secret from the URL rather than
// from process-wide state.
func (s *Server) mcpServer(room mcpRoom) *mcp.Server {
	srv := mcp.NewServer(
		&mcp.Implementation{Name: "notifpwa", Version: s.appVersion()},
		&mcp.ServerOptions{Instructions: mcpInstructions},
	)
	mcp.AddTool(srv, &mcp.Tool{
		Name:  "notify_operator",
		Title: "Notify the operator",
		Description: "Send the operator a push notification. Use it when your task is " +
			"finished, and again when you need their input.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			DestructiveHint: boolRef(false),
			IdempotentHint:  false,
			OpenWorldHint:   boolRef(true),
		},
	}, func(_ context.Context, req *mcp.CallToolRequest, in notifyInput) (*mcp.CallToolResult, any, error) {
		return s.notifyRoom(room, req, in), nil, nil
	})
	return srv
}

// notifyRoom is the tool body. Task 2 replaces the placeholder below.
func (s *Server) notifyRoom(room mcpRoom, req *mcp.CallToolRequest, in notifyInput) *mcp.CallToolResult {
	return toolText("not implemented yet")
}

func toolText(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// toolError reports a failure inside the tool result rather than as a protocol
// error, so the agent can read it and self-correct.
func toolError(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
		IsError: true,
	}
}

func boolRef(b bool) *bool { return &b }
```

Task 2 has not landed yet, so `notifyRoom` above is a stub with unused parameters. If the linter objects to `room`, `req` and `in`, keep the names as they are — Task 2 uses all three. Do not delete them and do not name them `_`.

Add the route to `internal/server/handlers.go`, in the public block, immediately after the `POST /n/{room}` line:

```go
	mux.HandleFunc("POST /mcp/{room}", s.rateLimitPost(s.serveMCP))
```

The shared post bucket is deliberate (ADR-002); no new limiter. Registering only `POST` is what makes `GET` answer 405 — ServeMux does that for a matched path with a mismatched method.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/server/ -run TestMCP -v`
Expected: `TestMCPHandshakeAndToolsList` PASS, `TestMCPRejectsInvalidRoomForeignOriginAndGET` PASS.

- [ ] **Step 6: Check nothing else broke, and that the build is clean**

Run: `go vet ./... && go test ./... && gofmt -l .`
Expected: vet silent, all tests pass, `gofmt -l` prints nothing (if it prints `internal/server/mcp.go`, run `gofmt -w internal/server/mcp.go`).

- [ ] **Step 7: Stop here — do not commit**

Tasks 1 to 3 accumulate into one commit created in Task 4. Leave the working tree dirty.

---

### Task 2: What the tool actually does

Deliverable: calling `notify_operator` delivers a push notification with the operator's chosen defaults, and an agent is never told it succeeded when it reached nobody.

**Files:**
- Modify: `internal/server/mcp.go` (replace the `notifyRoom` stub, add `callerLabel`)
- Modify: `internal/server/mcp_test.go` (append behaviour tests)

**Interfaces:**
- Consumes: `(*Server).broadcastRoom(room, secret string, p pushPayload) (roomSendResult, error)` (`rooms.go:233`), `roomSendResult{sendResult{Sent, Failed, Pruned int}, Recipients int}` (`rooms.go:224`), `pushPayload{Title, Body, URL, Tag, Image, Actions, Urgency}` (`push.go:20`), `mcpRoom`, `notifyInput`, `toolText`, `toolError` from Task 1, `mcpRPC` from Task 1.
- Produces: `func (s *Server) notifyRoom(room mcpRoom, req *mcp.CallToolRequest, in notifyInput) *mcp.CallToolResult` (real behaviour), `func callerLabel(caller string, req *mcp.CallToolRequest) string`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/server/mcp_test.go`:

```go
// callNotify performs the handshake and then one tools/call, returning the
// result object of the tool response.
func callNotify(t *testing.T, s *Server, path string, args map[string]any) map[string]any {
	t.Helper()
	if code, res := mcpRPC(t, s, path, "", initializeMsg()); code != 200 {
		t.Fatalf("initialize: status %d, %v", code, res)
	}
	code, res := mcpRPC(t, s, path, "2025-06-18", map[string]any{
		"jsonrpc": "2.0", "id": float64(2), "method": "tools/call",
		"params": map[string]any{"name": "notify_operator", "arguments": args},
	})
	if code != 200 {
		t.Fatalf("tools/call status = %d, want 200 (body %v)", code, res)
	}
	return resultOf(t, res)
}

// firstText pulls the single text content item out of a tool result.
func firstText(t *testing.T, result map[string]any) string {
	t.Helper()
	content, _ := result["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %v, want one item", content)
	}
	item, _ := content[0].(map[string]any)
	text, _ := item["text"].(string)
	return text
}

// stubPush records the payload of every send and reports success.
func stubPush(t *testing.T) *[]pushPayload {
	t.Helper()
	orig := sendOne
	t.Cleanup(func() { sendOne = orig })
	var sent []pushPayload
	sendOne = func(msg []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		var p pushPayload
		if err := json.Unmarshal(msg, &p); err != nil {
			t.Fatalf("payload not JSON: %v (%s)", err, msg)
		}
		sent = append(sent, p)
		return stubResp(201), nil
	}
	return &sent
}

func TestMCPNotifyDeliversWithOperatorDefaults(t *testing.T) {
	s := newTestApp(t)
	s.store.upsertSubscription(mkSub("https://push/mcp1"), "")
	if err := s.store.joinRoom("alerts", "https://push/mcp1", nil); err != nil {
		t.Fatalf("joinRoom: %v", err)
	}
	sent := stubPush(t)

	result := callNotify(t, s, "/mcp/alerts", map[string]any{"body": "build failed"})
	if isError, ok := result["isError"].(bool); ok && isError {
		t.Fatalf("tool reported an error: %s", firstText(t, result))
	}
	if *sent == nil || len(*sent) != 1 {
		t.Fatalf("got %d sends, want 1", len(*sent))
	}
	p := (*sent)[0]
	// Title defaults to naming the caller: httptest sets RemoteAddr to a
	// 127.0.0.1 form, which clientIP reduces to the bare host.
	if p.Title == "" || p.Title == "alerts" {
		t.Fatalf("title = %q, want the caller named rather than the room", p.Title)
	}
	// The link is the app itself, never a URL assembled from request headers.
	if p.URL != "/" {
		t.Fatalf("url = %q, want /", p.URL)
	}
	// Loud by default so the operator sees it soon.
	if p.Urgency != "high" {
		t.Fatalf("urgency = %q, want high", p.Urgency)
	}
	if p.Body != "build failed" {
		t.Fatalf("body = %q", p.Body)
	}
	// It reached the room log, like any other room post.
	posts, _ := s.store.deviceRoomLog("https://push/mcp1", "alerts")
	if len(posts) != 1 || posts[0].Body != "build failed" {
		t.Fatalf("deviceRoomLog = %+v, want the one posted message", posts)
	}
	text := firstText(t, result)
	if !strings.Contains(text, "1") || !strings.Contains(text, "alerts") {
		t.Fatalf("result text %q does not report one device in alerts", text)
	}
}

func TestMCPNotifyArgumentsOverrideDefaults(t *testing.T) {
	s := newTestApp(t)
	s.store.upsertSubscription(mkSub("https://push/mcp2"), "")
	s.store.joinRoom("alerts", "https://push/mcp2", nil)
	sent := stubPush(t)

	callNotify(t, s, "/mcp/alerts", map[string]any{
		"body": "hello", "title": "Nightly build", "url": "https://ci.example/build/9",
		"urgency": "low",
	})
	p := (*sent)[0]
	if p.Title != "Nightly build" || p.URL != "https://ci.example/build/9" || p.Urgency != "low" {
		t.Fatalf("arguments did not override defaults: %+v", p)
	}
}

func TestMCPSecretFromURLAndArgument(t *testing.T) {
	s := newTestApp(t)
	s.store.upsertSubscription(mkSub("https://push/mcp3"), "")
	sec := "hunter2"
	if err := s.store.joinRoom("alerts", "https://push/mcp3", &sec); err != nil {
		t.Fatalf("joinRoom: %v", err)
	}
	sent := stubPush(t)

	// The URL alone carries the capability: no secret argument needed.
	callNotify(t, s, "/mcp/alerts?secret=hunter2", map[string]any{"body": "via url"})
	if len(*sent) != 1 {
		t.Fatalf("secret in URL reached %d devices, want 1", len(*sent))
	}

	// No secret anywhere reaches nobody, and says so as an error.
	res := callNotify(t, s, "/mcp/alerts", map[string]any{"body": "no secret"})
	if isError, _ := res["isError"].(bool); !isError {
		t.Fatalf("secretless post to a guarded room should be an error: %v", res)
	}

	// An explicit argument wins over a URL secret.
	callNotify(t, s, "/mcp/alerts?secret=wrong", map[string]any{"body": "via arg", "secret": "hunter2"})
	if len(*sent) != 2 {
		t.Fatalf("secret argument override reached %d devices total, want 2", len(*sent))
	}
}

func TestMCPNotifyReachesNobodyAndSaysSo(t *testing.T) {
	s := newTestApp(t)
	sent := stubPush(t)

	// Room "ghost" has no subscribers. broadcastRoom returns success with zero
	// recipients; the tool must not let an agent read that as a notification sent.
	result := callNotify(t, s, "/mcp/ghost", map[string]any{"body": "anyone there?"})
	isError, _ := result["isError"].(bool)
	if !isError {
		t.Fatalf("zero-recipient post reported as success: %v", result)
	}
	text := firstText(t, result)
	if !strings.Contains(text, "0") || !strings.Contains(text, "ghost") {
		t.Fatalf("error text %q should name the count and the room", text)
	}
	if !strings.Contains(text, "secret") {
		t.Fatalf("error text %q should name the secret as a likely cause", text)
	}
	if *sent != nil {
		t.Fatalf("nothing should have been sent, got %d", len(*sent))
	}
}

func TestMCPCallerLabelNamesClientWhenPresent(t *testing.T) {
	s := newTestApp(t)
	s.store.upsertSubscription(mkSub("https://push/mcp4"), "")
	s.store.joinRoom("alerts", "https://push/mcp4", nil)
	sent := stubPush(t)

	// 2026-07-28-era clients carry their identity on every request; older ones
	// (Claude, ChatGPT) do not, and are labelled by address alone.
	if code, res := mcpRPC(t, s, "/mcp/alerts", "", initializeMsg()); code != 200 {
		t.Fatalf("initialize: %d %v", code, res)
	}
	code, res := mcpRPC(t, s, "/mcp/alerts", "2026-07-28", map[string]any{
		"jsonrpc": "2.0", "id": float64(2), "method": "tools/call",
		"params": map[string]any{
			"name": "notify_operator",
			"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28",
				"io.modelcontextprotocol/clientInfo": map[string]any{
					"name": "testclient", "version": "0"}},
			"arguments": map[string]any{"body": "hi"},
		},
	})
	if code != 200 {
		t.Fatalf("tools/call status = %d, want 200 (body %v)", code, res)
	}
	if err, ok := res["error"]; ok && err != nil {
		t.Fatalf("2026-07-28 call failed: %v", err)
	}
	if *sent == nil || !strings.Contains((*sent)[0].Title, "testclient") {
		t.Fatalf("title = %q, want the client name when the request carries it", (*sent)[0].Title)
	}
}
```

Add these imports to `internal/server/mcp_test.go`'s import block: `"net/http"`, `webpush "github.com/SherClockHolmes/webpush-go"`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/server/ -run 'TestMCPNotify|TestMCPSecret|TestMCPCaller' -v`
Expected: FAIL on every case — the stub returns `"not implemented yet"` and sends nothing. `TestMCPNotifyReachesNobodyAndSaysSo` fails on `isError` being absent, which is exactly the behaviour this task exists to add.

- [ ] **Step 3: Replace the stub with the real tool body**

In `internal/server/mcp.go`, replace `notifyRoom` and add `callerLabel` beneath it:

```go
// notifyRoom delivers one agent message to the room and reports the outcome in
// words an agent can act on.
func (s *Server) notifyRoom(room mcpRoom, req *mcp.CallToolRequest, in notifyInput) *mcp.CallToolResult {
	secret := strings.TrimSpace(in.Secret)
	if secret == "" {
		secret = room.secret
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = callerLabel(room.caller, req)
	}
	link := strings.TrimSpace(in.URL)
	if link == "" {
		// Relative: the service worker resolves it against the app's own origin.
		// Never assembled from Host or X-Forwarded-Host, either of which an
		// attacker could steer to put a link they control in a trusted notification.
		link = "/"
	}
	urgency := strings.TrimSpace(in.Urgency)
	if urgency == "" {
		urgency = "high"
	}

	res, err := s.broadcastRoom(room.name, secret, pushPayload{
		Title: title, Body: strings.TrimSpace(in.Body), URL: link, Urgency: urgency,
	})
	if err != nil {
		return toolError("could not deliver the notification: " + err.Error())
	}
	if res.Recipients == 0 {
		// broadcastRoom succeeds silently when nothing matches, so that strangers
		// cannot mint empty rooms. An agent that reads that as success would tell
		// the operator it had notified them, which is the one outcome this feature
		// exists to prevent.
		return toolError(fmt.Sprintf(
			"delivered to 0 devices: no subscriber in %q matched. Check the room name, "+
				"and if the room has a secret check that too.", room.name))
	}
	return toolText(fmt.Sprintf(
		"notified %d device(s) in %q (sent %d, failed %d, pruned %d).",
		res.Recipients, room.name, res.Sent, res.Failed, res.Pruned))
}

// callerLabel names who sent the notification. Client identity only travels with
// the 2026-07-28 protocol; clients still on 2025 revisions get the address alone.
func callerLabel(caller string, req *mcp.CallToolRequest) string {
	if meta, ok := req.Params.Meta.GetMeta()[mcp.MetaKeyClientInfo].(map[string]any); ok {
		if name, ok := meta["name"].(string); ok && strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name) + " (" + caller + ")"
		}
	}
	return caller
}
```

`req.Params.Meta.GetMeta()` returns a nil map when the client sent no `_meta`; indexing a nil map is safe in Go, so there is nothing to guard beyond the type assertion. Do not drop the `ok` from that assertion — asserting on a missing key panics, and a panic in a tool handler takes the request down.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/server/ -run TestMCP -v`
Expected: all MCP tests PASS, including the two from Task 1.

- [ ] **Step 5: Run the whole suite, vet, and format**

Run: `go vet ./... && go test ./... && gofmt -l .`
Expected: vet silent, everything passes, no files listed.

- [ ] **Step 6: Stop here — do not commit**

---

### Task 3: Telling the operator it exists

Deliverable: the README documents the endpoint, the connect command, and the fact that the URL is the capability.

**Files:**
- Modify: `README.md` (API table around lines 136-156; new section after the Rooms section)

**Interfaces:**
- Consumes: nothing new.
- Produces: nothing other code consumes.

- [ ] **Step 1: Add the API table row**

In `README.md`, in the API table, directly after the `POST /n/{room}` row, insert:

```markdown
| `POST /mcp/{room}` | none | — | [MCP](https://modelcontextprotocol.io) endpoint for the room: one tool, `notify_operator`, which posts to the room. Secret via `?secret=` or the tool's `secret` argument. See [Agents (MCP)](#agents-mcp). |
```

- [ ] **Step 2: Add the Agents section**

Insert after the `## Rooms (topics)` section (before `## Run it`):

````markdown
## Agents (MCP)

Coding agents and assistants that speak [MCP](https://modelcontextprotocol.io) can
buzz you when they finish, or when they need you to decide something. Point the agent
at a room URL — that URL is the whole configuration:

```
https://notify.example.com/mcp/alerts
```

The agent gets one tool, `notify_operator`. Everything but the message has a default:
the title names whoever called, the notification opens this app, and it arrives marked
urgent. The agent can override the title, the link, and the urgency, and can pass a
room `secret` as an argument — or bake it into the URL as `?secret=...` so the URL
alone is enough.

With Claude Code:

```sh
claude mcp add --transport http notifpwa https://notify.example.com/mcp/alerts
```

**Treat the room URL as a capability.** This endpoint has no login and no API key, the
same as posting to a room with `curl` (see [Rooms](#rooms-topics)): anyone who knows
`/mcp/alerts` can raise a notification on devices in `alerts` that have no secret set.
If that matters to you, set a secret on the room and hand out
`/mcp/alerts?secret=...` instead. A secret in a URL does appear in your proxy's access
log, so share the link accordingly.

Two client-side realities worth knowing:

- ChatGPT and Claude's hosted connectors call you **from their own infrastructure**, so
  the app must be publicly reachable over HTTPS (you already need that for iOS), and the
  address shown in the notification title is theirs, not the agent's machine.
- ChatGPT accepts an unauthenticated server, but it will not accept a static API key —
  an authenticated one would have to implement full OAuth 2.1. Claude and Claude Code
  take either. Keeping this endpoint unauthenticated is what makes one URL work for all
  of them.
````

- [ ] **Step 3: Verify the links and the prose render**

Run: `grep -n "mcp" README.md`
Expected: the table row and the new section both appear; the anchor `#agents-mcp` used by the table row matches the heading `## Agents (MCP)` (GitHub renders that heading as `#agents-mcp`).

Run: `go test ./...`
Expected: still passes — this task touches no Go code.

- [ ] **Step 4: Stop here — do not commit**

---

### Task 4: The one commit

Deliverable: everything on `feat/mcp-notify` in a single unsigned commit, not pushed.

**Files:**
- Everything from Tasks 1-3 plus the `docs/` tree already on disk: `docs/adr/INDEX.md`, three ADRs, `docs/fdr/INDEX.md`, `FDR-001-agent-notifications.md`, `docs/superpowers/specs/2026-09-29-mcp-agent-notify-design.md`, and this plan.

**Interfaces:** none.

- [ ] **Step 1: Confirm the working tree holds only intended changes**

Run: `git status --short`
Expected: `M go.mod`, `M go.sum`, `M internal/server/handlers.go`, `M internal/server/server.go`, `M README.md`, `?? docs/`, `?? internal/server/mcp.go`, `?? internal/server/mcp_test.go`. Nothing else. If anything unexpected appears, stop and ask — do not add it.

Run: `git diff go.mod`
Expected: the new `require github.com/modelcontextprotocol/go-sdk v1.8.0` (and its transitive requirements), `go 1.25.0` unchanged.

- [ ] **Step 2: Full gate**

Run: `gofmt -l . ; go vet ./... && go test ./... -count=1`
Expected: no files from gofmt, vet silent, `ok notifpwa/internal/server`. Any failure here is fixed now, not committed.

- [ ] **Step 3: Confirm the branch**

`feat/mcp-notify` was created before Task 1 so that no implementation would land on
`master`; verify you are on it with `git branch --show-current`.

Expected: `feat/mcp-notify`.

- [ ] **Step 4: Stage and commit, unsigned**

The repo config sets `commit.gpgsign=true`, so the commit must be signed-off explicitly:

```bash
git add go.mod go.sum README.md internal/server/mcp.go internal/server/mcp_test.go \
  internal/server/handlers.go internal/server/server.go docs
git commit --no-gpg-sign -m "feat: MCP server so agents can notify the operator

Adds POST /mcp/{room}: an unauthenticated MCP endpoint exposing one tool,
notify_operator, that posts to the room through the existing broadcast path.
The room URL is the whole configuration; the secret rides in ?secret= or the
tool argument. No new credential and no new rate limiter, because posting to a
room is already unauthenticated and the room plus its secret is the capability.

Records the decision trail in docs/adr (remote MCP over browser WebMCP, the
authless endpoint, the go-sdk dependency) and the feature behaviour in
docs/fdr/FDR-001."
```

Expected: one new commit. Verify it is unsigned and that the tree is clean:

```bash
git log --show-signature -1 | head -6 ; git status --short
```

Expected: no `gpg: Signature made` line in that output, and empty `git status --short`.

- [ ] **Step 5: Do not push**

The operator asked for no push: remote interactive auth is not available in this
session. Stop after the commit. If a push is requested later, it is
`git push -u origin feat/mcp-notify` and it needs signing decided at that moment —
do not change `commit.gpgsign` in config to work around anything.

---

## Self-Review Notes

- **Spec coverage:** endpoint + tool + per-room resolution (Task 1), SDK in stateless mode (Task 1), defaults for title/url/urgency/secret and the zero-recipient error (Task 2), no new rate limiter (Task 1's route line uses `rateLimitPost`), README + ADRs + FDR (Task 3, Task 4), single unsigned unpushed commit (Task 4). Every spec section has a task.
- **Deliberately unverified → now verified during execution:** the 2026-07-28 test
  request needs more than `_meta`. The SDK also demands the `Mcp-Method: tools/call` and
  `Mcp-Name: notify_operator` headers, and `_meta` must carry
  `io.modelcontextprotocol/protocolVersion` and `io.modelcontextprotocol/clientCapabilities`
  alongside `clientInfo` — each omission is a separate 400. A real client of that revision
  sends all of it; only hand-built test requests have to.
- Two other corrections found while executing: `httptest.NewRequest` panics on a URL with
  a raw space (the invalid-room case uses `%20`), and indexing `(*sent)[0]` needs a length
  guard first so a red test fails cleanly instead of panicking and hiding sibling results.
- **Task 1/2 split collapsed in practice:** the tool body landed with the endpoint because
  Go rejects the stub's otherwise-unused `fmt` import. Both red states were still observed —
  first 404s for every MCP route, then `not implemented yet` for the five behaviour tests —
  so the gates did their job.
- **Type consistency:** `mcpRoom{name,secret,caller}`, `notifyInput{Body,Title,URL,Urgency,Secret}`, `toolText`/`toolError`, `callerLabel(caller string, req *mcp.CallToolRequest)` are used identically in both tasks; `serveMCP`/`mcpServer`/`mcpHandlerFor`/`originAllowed`/`roomFromRequest`/`boolRef` are defined once, in `mcp.go`.
