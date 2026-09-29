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
// optionality comes from the omitempty in the json tag, which is what the SDK's
// schema inference reads.
type notifyInput struct {
	Body    string `json:"body" jsonschema:"What to tell the operator."`
	Title   string `json:"title,omitempty" jsonschema:"Notification title. Defaults to who is calling."`
	URL     string `json:"url,omitempty" jsonschema:"Link the notification opens. Defaults to this app."`
	Urgency string `json:"urgency,omitempty" jsonschema:"very-high, high, normal or low. Defaults to high."`
	Secret  string `json:"secret,omitempty" jsonschema:"Room secret, if the room has one."`
}

// mcpRoom is what the request URL said: which room to post to, the secret baked
// into the query string, and the address to name in the notification title.
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
