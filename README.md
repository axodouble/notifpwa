# notifpwa

A tiny self-hosted web app for sending push notifications to your own phone.

Install the site as a PWA (Add to Home Screen), then `POST` to it to push a
notification to every device that installed it. One Go binary, one SQLite file.
Works on iOS 16.4+, Android, and desktop browsers.

## How it works

1. You run the app behind HTTPS.
2. On each device, open the site and **Add to Home Screen**, then open the
   installed app and tap **Enable notifications**.
3. Send a notification to all your devices:

   ```sh
   curl -X POST https://notify.example.com/api/send \
     -H "Authorization: Bearer YOUR_TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"title":"Hello","body":"It works","url":"/"}'
   ```

### Health check

`GET /healthz` returns `200 ok` when the app and database are reachable. The
Docker image runs this via a `HEALTHCHECK` so `docker ps` reports health.

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
| `GET /api/tokens` | `admin` | — | List tokens (label, prefix, scopes, timestamps). Secrets are never returned. |
| `POST /api/tokens` | `admin` | `{"label","admin","send"}` | Create a token; the response contains the full `secret` **once**. |
| `PATCH /api/tokens/{id}` | `admin` | `{"label"?,"admin"?,"send"?}` | Rename or re-scope. Refuses (409) to drop the last admin token unless `API_TOKEN` is set. |
| `DELETE /api/tokens/{id}` | `admin` | — | Revoke a token. Same last-admin guard as above. |
| `POST /api/config` | `admin` | multipart (`name`, `icon`) | Update app name / icon. |
| `POST /api/subscribe` | none | PushSubscription JSON | Register a device (called by the page). |

**Auth column:** `send` = a token with the send scope (or an admin session);
`admin` = a token with the admin scope (or a logged-in admin session).
`Bearer` tokens go in `Authorization: Bearer <secret>`.

## Project structure

```
cmd/notifpwa/        # entrypoint: reads env, starts the HTTP server
internal/server/     # the application package
  server.go          #   New(), config, VAPID/token bootstrap
  store.go           #   SQLite: settings + subscriptions
  handlers.go        #   HTTP routes and handlers
  push.go            #   broadcast to all devices + prune dead endpoints
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
