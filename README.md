# notifpwa

A tiny self-hosted web app for sending push notifications to your own phone.

Install the site as a PWA (Add to Home Screen), join it to one or more named
**rooms**, then `POST` to a room to push a notification to the devices subscribed
there. One Go binary, one SQLite file.
Works on iOS 16.4+, Android, and desktop browsers.

## How it works

1. You run the app behind HTTPS.
2. On each device, open the site and **Add to Home Screen**, then open the
   installed app and tap **Enable notifications**.
3. In the installed app, join a room (e.g. `alerts`). Then post to that room — anyone can, no token needed:

   ```sh
   curl -X POST https://notify.example.com/n/alerts \
     -H "Content-Type: application/json" \
     -d '{"title":"Hello","body":"It works","url":"/"}'
   ```

### Health check

`GET /healthz` returns `200 ok` when the app and database are reachable. The
Docker image runs this via a `HEALTHCHECK` so `docker ps` reports health.

## Rooms (topics)

Devices subscribe to named **rooms**. In the installed app, open the Rooms
section, join a room by name, and optionally set a personal **secret**. Anyone can post to a room — no login — but a notification
only reaches your device if the post's secret matches the one you set (a device
with no secret receives only secret-less posts). Every post is logged per room.

```sh
# plaintext body (title defaults to the room name)
curl -X POST "https://notify.example.com/n/alerts" \
  -H "X-Room-Secret: my-secret" \
  -d "Backup finished"

# structured JSON
curl -X POST "https://notify.example.com/n/alerts?secret=my-secret" \
  -H "Content-Type: application/json" \
  -d '{"title":"Alert","body":"CPU high","url":"/","urgency":"high"}'
```

## Agents (MCP)

Coding agents and assistants that speak [MCP](https://modelcontextprotocol.io) can
buzz you when they finish, or when they need you to decide something. Point the agent
at a room URL — that URL is the whole configuration:

```
https://notify.example.com/mcp/alerts
```

The agent gets one tool, `notify_operator`. It must supply the message and a short
title (what finished, or what it needs from you); the notification opens this app and
arrives marked urgent unless the agent overrides the link or the urgency. It can pass a
room secret as an argument — or bake it into the URL as `?secret=...` so the URL alone
is enough. The server deliberately adds nothing to a notification, not even the
caller's address.

With Claude Code:

```sh
claude mcp add --transport http notifpwa https://notify.example.com/mcp/alerts
```

With [OpenCode](https://opencode.ai/docs/mcp-servers/), add it under `mcp` in the global
config — `~/.config/opencode/opencode.json` — because this notifies *you*, not the
project you happen to be sitting in:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "notifpwa": {
      "type": "remote",
      "url": "https://notify.example.com/mcp/alerts",
      "enabled": true
    }
  }
}
```

No `headers` and no `oauth`: there is no credential to pass. OpenCode only starts an
OAuth flow when a server answers with a `401`, which this endpoint never does; add
`"oauth": false` if you would rather it did not probe. Tools are namespaced by the name
you gave the server, so `notifpwa` here is what to say in a prompt — *"when you're done,
notify me"* — and `"tools": { "notifpwa_*": false }` is how you turn it off without
deleting the entry.

**Treat the room URL as a capability.** This endpoint has no login and no API key, the
same as posting to a room with `curl` (see [Rooms](#rooms-topics)): anyone who knows
`/mcp/alerts` can raise a notification on devices in `alerts` that have no secret set.
If that matters to you, set a secret on the room and hand out
`/mcp/alerts?secret=...` instead — and remember a secret in a URL lands in your proxy's
access log, so share the link accordingly.

Three realities worth knowing:

- ChatGPT and Claude's hosted connectors call you **from their own infrastructure**, so
  the app must be publicly reachable over HTTPS (you already need that for iOS).
- **A notification may be read by a third party.** It leaves your server through a push
  provider you do not control, and room URLs and `?secret=` cross proxy logs. The agent
  is told never to send IP addresses, hostnames, secrets, tokens, or personal or private
  information in a title or body, and the server itself embeds none — not even the
  caller's address. Still, treat any notification as text you would be comfortable
  reading on a billboard.
- ChatGPT accepts an unauthenticated server but will not accept a static API key — an
  authenticated one would have to implement full OAuth 2.1. Claude, Claude Code and
  OpenCode take either. Leaving this endpoint unauthenticated is what makes one URL work
  for all of them.

## Run it

```sh
go build -o notifpwa ./cmd/notifpwa
./notifpwa
```

The app shows its build version on the pages and at startup. A plain `go build`
in a git checkout stamps the commit automatically; to show a release tag, pass
it in:

```sh
go build -ldflags "-X main.version=$(git describe --tags --always --dirty)" \
  -o notifpwa ./cmd/notifpwa
```

On first run it creates `data.db`, generates a VAPID keypair and an initial
admin token, and prints the admin URL + token to the console:

```
notifpwa listening on :8080
Open the admin page: http://localhost:8080/admin?token=<TOKEN>
API token (send with 'Authorization: Bearer <token>'): <TOKEN>
```

Open `/admin?token=...` to set the app name, upload a custom icon, see how many
devices are subscribed, and send a test notification.

### Configuration (env vars)

| Var | Default | Purpose |
|-----|---------|---------|
| `PORT` | `8080` | HTTP port |
| `DB_PATH` | `./data.db` | SQLite file location |
| `VAPID_SUBSCRIBER` | `mailto:admin@localhost` | Contact in the VAPID JWT (a `mailto:` or URL) |

## Run with Docker

```sh
docker build --build-arg VERSION="$(git describe --tags --always --dirty)" -t notifpwa .
docker run -d --name notifpwa -p 8080:8080 -v notifpwa-data:/data notifpwa
docker logs notifpwa   # prints the admin URL + API token
```

The database lives in the `/data` volume, so your keys, token, icon, and
subscriptions survive restarts and upgrades. The image is a static binary on
Alpine, runs as a non-root user, and includes `ca-certificates` (needed for the
outbound HTTPS calls to the push services).

### Docker Compose with automatic HTTPS

iOS needs HTTPS (see below). The included [`docker-compose.yml`](docker-compose.yml)
runs the app plus [Caddy](https://caddyserver.com), which fetches and renews a
certificate for you. Edit it to set your domain and email, then:

```sh
docker compose up -d
docker compose logs app   # grab the admin URL + API token
```

## HTTPS is required

iOS requires a valid HTTPS certificate for both installing a PWA and receiving
Web Push. The app itself serves plain HTTP — terminate TLS in front of it with a
reverse proxy. Example with [Caddy](https://caddyserver.com):

```
notify.example.com {
    reverse_proxy localhost:8080
}
```

Any equivalent (nginx + certbot, Cloudflare Tunnel, etc.) works too.

## iOS notes

- Requires iOS/iPadOS **16.4 or newer**.
- Push only works **after** the user taps *Share → Add to Home Screen* and opens
  the app from the Home Screen. The permission prompt does not appear in Safari
  itself — only in the installed PWA. The app shows this hint on iOS.
- **iOS revokes push subscriptions on its own**, typically after the app has sat
  unopened for a while. Notification permission stays granted, but the
  subscription is gone and its endpoint is dead. iOS does not implement
  `pushsubscriptionchange`, and Safari wants a user gesture to create the
  replacement, so one tap of *Enable* is unavoidable when this happens.
  It is only a tap: the app keeps a stable `device_id`, so the new subscription
  is recognised as the same device and **rooms and secrets are kept**. The app
  re-posts its subscription on every launch, which is what performs the repair.

## API

| Endpoint | Auth | Body | Description |
|----------|------|------|-------------|
| `GET /api/devices` | `admin` | — | List subscribed devices with label, user-agent, and timestamps. |
| `POST /api/devices/label` | `admin` | `{"endpoint","label"}` | Set a friendly label for a device. |
| `DELETE /api/devices` | `admin` | `{"endpoint"}` | Remove one device. |
| `GET /api/tokens` | `admin` | — | List tokens (label, prefix, timestamps). Secrets are never returned. |
| `POST /api/tokens` | `admin` | `{"label"}` | Create an admin token; the response contains the full `secret` **once**. |
| `PATCH /api/tokens/{id}` | `admin` | `{"label"?}` | Rename a token. |
| `DELETE /api/tokens/{id}` | `admin` | — | Revoke a token. Refuses (409) to delete the last token unless `API_TOKEN` is set. |
| `POST /api/config` | `admin` | multipart (`name`, `icon`) | Update app name / icon. |
| `POST /api/subscribe` | none | PushSubscription JSON + `device_id`, or `old_endpoint` | Register a device (called by the page on every launch). `device_id` is a stable client id: when a push endpoint rotates, the device's rooms move to the new endpoint instead of being lost. `old_endpoint` does the same for the service worker, which cannot read `device_id`. |
| `POST /n/{room}` | secret* | plaintext, or `{"title","body",…}` (JSON) | Post to a room. Secret via `X-Room-Secret` header or `?secret=`. Delivered to room devices whose secret matches. Returns `{"sent","failed","pruned","recipients"}`. Rate-limited. |
| `POST /mcp/{room}` | none | — | [MCP](https://modelcontextprotocol.io) endpoint for the room: one tool, `notify_operator`, which posts to the room. Secret via `?secret=` or the tool's `secret` argument. The handshake and `tools/list` are never rate limited — clients re-list tools every turn, and refusing them makes clients drop the tool. Only the send is, on the same per-IP bucket as `POST /n/{room}`; when it is exhausted the tool returns "rate limited" so the agent backs off. See [Agents (MCP)](#agents-mcp). |
| `GET /api/rooms` | none | `?endpoint=` | List the rooms a device belongs to (`[{"room","has_secret"}]`). |
| `POST /api/rooms` | none | `{"endpoint","room","secret"?}` | Join a room / set-or-clear its secret. `secret:""` clears; omit to leave unchanged. |
| `DELETE /api/rooms` | none | `{"endpoint","room"}` | Leave a room. |
| `GET /api/rooms/log` | none | `?endpoint=&room=` | Posts this device received in a room. |
| `GET /api/admin/rooms` | `admin` | — | All rooms with subscriber counts. |
| `GET /api/admin/rooms/log` | `admin` | `?room=` | Full post history for a room. |

*the room "secret" is a per-subscriber delivery filter set by the device, not an account credential — posting itself needs no auth.

**Auth column:** `admin` = a token (or a logged-in admin session). `none` = no
auth. `secret*` = the per-subscriber room secret. `Bearer` tokens go in
`Authorization: Bearer <secret>`.

## Project structure

```
cmd/notifpwa/        # entrypoint: reads env, starts the HTTP server
internal/server/     # the application package
  server.go          #   New(), config, VAPID/token bootstrap
  store.go           #   SQLite: settings + subscriptions
  handlers.go        #   HTTP routes and handlers
  push.go            #   push delivery to a subscription list + expire dead endpoints
  rooms.go           #   rooms: schema, membership, room broadcast + handlers
  mcp.go             #   the MCP endpoint: room-scoped server and the notify_operator tool
  web/               #   embedded PWA frontend (html/js/service worker/icon)
```

## Development

```sh
go test ./...
```

## Data & backup

Everything (keys, hashed tokens, icon, subscriptions) lives in `data.db`. Back up
or move that single file to preserve your setup. Deleting it resets the app (new
keys and tokens; devices must re-subscribe). Set `API_TOKEN` for a guaranteed
always-valid root admin token.
