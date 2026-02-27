# ── Build stage ───────────────────────────────────────────────────────────────
# We use a full Go image to compile the server binary.
# Wails and the frontend are NOT needed — we only compile cmd/server.
FROM golang:1.23-bookworm AS builder

WORKDIR /src

# Copy go module files first for better layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy the full source (backend package + our server cmd)
COPY ../../Downloads .

# Build only our server entrypoint — no CGO, static binary
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /spotiflac-server \
    ./cmd/server

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM debian:bookworm-slim

# ffmpeg is required by SpotiFLAC's backend for audio conversion
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ffmpeg \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /spotiflac-server /usr/local/bin/spotiflac-server

# Default output dir — override via OUTPUT_DIR env var / volume mount
RUN mkdir -p /music

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/spotiflac-server"]
