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

## Run it

```sh
go build -o notifpwa ./cmd/notifpwa
./notifpwa
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
docker build -t notifpwa .
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

## API

| Endpoint | Auth | Body | Description |
|----------|------|------|-------------|
| `POST /api/send` | `send` | `{"title","body","url"?,"tag"?,"image"?,"actions"?,"urgency"?}` | Push to all devices. `actions` is up to 2 `{title,url}` buttons; `urgency` is `very-low`/`low`/`normal`/`high`. Returns `{"sent","failed","pruned"}`. |
| `GET /api/devices` | `admin` | — | List subscribed devices with label, user-agent, and timestamps. |
| `POST /api/devices/label` | `admin` | `{"endpoint","label"}` | Set a friendly label for a device. |
| `DELETE /api/devices` | `admin` | `{"endpoint"}` | Remove one device. |
| `GET /api/tokens` | `admin` | — | List tokens (label, prefix, timestamps). Secrets are never returned. |
| `POST /api/tokens` | `admin` | `{"label"}` | Create an admin token; the response contains the full `secret` **once**. |
| `PATCH /api/tokens/{id}` | `admin` | `{"label"?}` | Rename a token. |
| `DELETE /api/tokens/{id}` | `admin` | — | Revoke a token. Refuses (409) to delete the last token unless `API_TOKEN` is set. |
| `POST /api/config` | `admin` | multipart (`name`, `icon`) | Update app name / icon. |
| `POST /api/subscribe` | none | PushSubscription JSON | Register a device (called by the page). |
| `POST /n/{room}` | secret* | plaintext, or `{"title","body",…}` (JSON) | Post to a room. Secret via `X-Room-Secret` header or `?secret=`. Delivered to room devices whose secret matches. Returns `{"sent","failed","pruned","recipients"}`. Rate-limited. |
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
  push.go            #   push delivery to a subscription list + prune dead endpoints
  rooms.go           #   rooms: schema, membership, room broadcast + handlers
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
