package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// mcpRPC posts one JSON-RPC message at an MCP path and returns the HTTP status
// plus the decoded envelope. Responses are plain JSON because the handler sets
// JSONResponse; no SSE parsing is needed.
func mcpRPC(t *testing.T, s *Server, path, proto string, msg map[string]any) (int, map[string]any) {
	t.Helper()
	return mcpRPCHeaders(t, s, path, proto, msg, nil)
}

func mcpRPCHeaders(t *testing.T, s *Server, path, proto string, msg map[string]any, headers map[string]string) (int, map[string]any) {
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
	for k, v := range headers {
		req.Header.Set(k, v)
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

// initializeMsg is the request every client sends first.
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
	list, _ := resultOf(t, res)["tools"].([]any)
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
	// Title defaults to naming the caller: httptest sets RemoteAddr, which
	// clientIP reduces to the bare host.
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
	if len(*sent) != 1 {
		t.Fatalf("got %d sends, want 1", len(*sent))
	}
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
	// (Claude, ChatGPT) do not, and are labelled by address alone. That revision
	// also moves the method name into a header, so the request says so twice.
	if code, res := mcpRPC(t, s, "/mcp/alerts", "", initializeMsg()); code != 200 {
		t.Fatalf("initialize: %d %v", code, res)
	}
	code, res := mcpRPCHeaders(t, s, "/mcp/alerts", "2026-07-28", map[string]any{
		"jsonrpc": "2.0", "id": float64(2), "method": "tools/call",
		"params": map[string]any{
			"name": "notify_operator",
			"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28",
				"io.modelcontextprotocol/clientCapabilities": map[string]any{},
				"io.modelcontextprotocol/clientInfo": map[string]any{
					"name": "testclient", "version": "0"}},
			"arguments": map[string]any{"body": "hi"},
		},
	}, map[string]string{"Mcp-Method": "tools/call", "Mcp-Name": "notify_operator"})
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
