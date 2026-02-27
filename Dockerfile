# ── Build stage ───────────────────────────────────────────────────────────────
FROM golang:1.23-bookworm AS builder

ENV GOTOOLCHAIN=auto

WORKDIR /src
COPY . .

# Add the uuid dependency that server.go needs
RUN go get github.com/google/uuid

# Build with -tags server so that:
#   - server_main.go provides main() instead of main.go
#   - server.go is compiled in (it has the //go:build server tag)
#   - main.go is excluded (it has NO build tag, but its main() conflicts)
#
# We exclude main.go by renaming it before the build, then restore it.
# This keeps the repo clean for the normal Wails build.
RUN mv main.go main.go.wails && \
    CGO_ENABLED=0 GOOS=linux go build \
        -tags server \
        -ldflags="-s -w" \
        -o /spotiflac-server \
        . && \
    mv main.go.wails main.go

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ffmpeg \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /spotiflac-server /usr/local/bin/spotiflac-server

RUN mkdir -p /music

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/spotiflac-server"]
