# syntax=docker/dockerfile:1

# ---- build stage ----
FROM golang:1.26-alpine AS build
WORKDIR /src

# Cache module downloads separately from source.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Version to stamp into the binary. Pass it in with:
#   docker build --build-arg VERSION="$(git describe --tags --always --dirty)" .
# (The build context excludes .git, so the version cannot be derived here.)
ARG VERSION=dev
# Pure-Go (CGO_ENABLED=0) => a fully static binary that runs on any base image.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" -o /out/notifpwa ./cmd/notifpwa

# ---- runtime stage ----
FROM alpine:3.20
# ca-certificates is required: sending a push means an outbound HTTPS request to
# Apple/Google/Mozilla push services.
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 app

COPY --from=build /out/notifpwa /usr/local/bin/notifpwa

# The SQLite database lives here so it survives container restarts.
RUN mkdir -p /data && chown app:app /data
USER app

ENV PORT=8080 DB_PATH=/data/data.db
VOLUME /data
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD ["notifpwa", "-healthcheck"]

ENTRYPOINT ["notifpwa"]
