package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

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
	"say exactly what you need. Notifications may be read by third parties: never send " +
	"IP addresses, hostnames, secrets, tokens, or personal or private information " +
	"through them."

// notifyInput is the argument object of notify_operator. Body and Title are
// required; optionality comes from the omitempty in the json tag, which is what the
// SDK's schema inference reads. Title has no server-side default on purpose: the
// server embeds nothing in a notification, not even the caller's address (ADR-004).
type notifyInput struct {
	Body    string `json:"body" jsonschema:"What to tell the operator."`
	Title   string `json:"title" jsonschema:"Short title naming the task or result, e.g. the build that finished or the decision you need. Required. Never an IP address, hostname, secret, or piece of private information."`
	URL     string `json:"url,omitempty" jsonschema:"Link the notification opens. Defaults to this app."`
	Urgency string `json:"urgency,omitempty" jsonschema:"very-high, high, normal or low. Defaults to high."`
	Secret  string `json:"secret,omitempty" jsonschema:"Room secret, if the room has one."`
}

// mcpRoom is what the request URL said: which room to post to and the secret baked
// into the query string. It deliberately carries no caller address — nothing that
// identifies who called reaches the notification (ADR-004).
type mcpRoom struct {
	name   string
	secret string
}

func roomFromRequest(r *http.Request) mcpRoom {
	return mcpRoom{
		name:   r.PathValue("room"),
		secret: r.URL.Query().Get("secret"),
	}
}

// serveMCP is the MCP endpoint for the room in the path. It is unauthenticated
// by design: posting to a room is unauthenticated already, and the room plus its
// secret is the capability. See docs/adr/ADR-002.
//
// It is deliberately not rate limited at the HTTP layer. The transport costs the
// server nothing until a notification is actually sent, and clients such as
// OpenCode re-list tools on every turn (this server answers with ttlMs 0), so a
// limiter here rejected harmless handshakes and made those clients drop the tool
// mid-session. The limit belongs to the send, and is applied in the tool handler.
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
			func(r *http.Request) *mcp.Server {
				return s.mcpServer(roomFromRequest(r), clientIP(r))
			},
			&mcp.StreamableHTTPOptions{
				Stateless:    true, // no Mcp-Session-Id; GET and DELETE answer 405
				JSONResponse: true, // application/json rather than text/event-stream
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
// from process-wide state. callerIP is the send-limiter key and nothing else:
// it reaches no notification, log row, or tool result (ADR-004).
func (s *Server) mcpServer(room mcpRoom, callerIP string) *mcp.Server {
	srv := mcp.NewServer(
		&mcp.Implementation{Name: "notifpwa", Version: s.appVersion()},
		&mcp.ServerOptions{Instructions: mcpInstructions},
	)
	mcp.AddTool(srv, &mcp.Tool{
		Name:  "notify_operator",
		Title: "Notify the operator",
		Description: "Send the operator a push notification. Use it when your task is " +
			"finished, and again when you need their input. Provide a short, " +
			"self-describing title. Notifications may be read by third parties: never " +
			"include IP addresses, hostnames, secrets, tokens, or personal or private " +
			"information.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			DestructiveHint: boolRef(false),
			IdempotentHint:  false,
			OpenWorldHint:   boolRef(true),
		},
	}, func(_ context.Context, _ *mcp.CallToolRequest, in notifyInput) (*mcp.CallToolResult, any, error) {
		// The only limit on this endpoint, and it sits here because this is the
		// one thing it does that costs anything: a push to real devices. Answered
		// as a tool error rather than an HTTP status so the agent reads why and
		// backs off, instead of a transport failure it cannot interpret.
		if !s.sendLimiter.allow(callerIP, time.Now()) {
			return toolError("send rate limited: wait a few seconds and notify again. " +
				"Do not retry in a loop."), nil, nil
		}
		return s.notifyRoom(room, in), nil, nil
	})
	return srv
}

// notifyRoom delivers one agent message to the room and reports the outcome in
// words an agent can act on.
func (s *Server) notifyRoom(room mcpRoom, in notifyInput) *mcp.CallToolResult {
	secret := strings.TrimSpace(in.Secret)
	if secret == "" {
		secret = room.secret
	}
	// The schema requires the title field to be present, but a present-but-empty
	// string passes it, so guard here too. The server supplies no default: a
	// notification carries only what the agent chose to say (ADR-004).
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return toolError("title is required: say what the notification is about, e.g. " +
			"what finished or what you need. Do not put an IP address or private data in it.")
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
