# SpotiFLAC — Server

Download Spotify tracks, albums, and playlists as true FLAC via Tidal, Qobuz, Amazon Music, or Deezer. Runs as a headless HTTP API in Docker.

---

## Setup

### 1. Configure the environment

Create a `.env` file next to `docker-compose.yml`:

```env
# Required — token clients must send as: Authorization: Bearer <value>
API_TOKEN=changeme

# Path inside the container where FLACs are written.
# Must match the volume mount target below.
OUTPUT_DIR=/music

# Optional — defaults to 8080
PORT=8080
```

### 2. Edit the volume mount

Open `docker-compose.yml` and set the host path on the left side of the volume to wherever your music library lives:

```yaml
volumes:
  - /your/music/library:/music
```

### 3. Start the container

```bash
docker compose up -d
```

The server is ready when you see:

```
spotiflac-server on :8080  output→/music
```

---

## API

All endpoints except `/health` require the header:

```
Authorization: Bearer <API_TOKEN>
```

### `POST /download`

Queue a download job. Returns immediately with a job ID.

**Request body**

| Field | Type | Required | Description |
|---|---|---|---|
| `url` | string | yes | Spotify URL (track, album, or playlist) |
| `service` | string | no | Source service: `tidal`, `qobuz`, `amazon`, `deezer`. Defaults to `tidal` |
| `navidrome` | object | no | See [Navidrome integration](#navidrome-integration) |

**Response**

```json
{
  "job_id": "d290f1ee-6c54-4b01-90e6-d701748f0851",
  "status": "queued"
}
```

**Examples**

Single track:
```bash
curl -X POST http://localhost:8080/download \
  -H "Authorization: Bearer changeme" \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://open.spotify.com/track/4cOdK2wGLETKBW3PvgPWqT"
  }'
```

Full playlist via Qobuz:
```bash
curl -X POST http://localhost:8080/download \
  -H "Authorization: Bearer changeme" \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M",
    "service": "qobuz"
  }'
```

---

### `GET /status/{job_id}`

Poll the status of a queued or completed job.

**Response fields**

| Field | Type | Description |
|---|---|---|
| `id` | string | Job UUID |
| `status` | string | `queued` · `processing` · `done` · `failed` |
| `spotify_url` | string | Normalized input URL |
| `total` | int | Total tracks in the request |
| `done` | int | Tracks successfully downloaded so far |
| `files` | string[] | Absolute container paths of downloaded files |
| `filename` | string | Path of the last downloaded file |
| `error` | string | Present only when `status` is `failed` |
| `navidrome_playlist_id` | string | Set when a Navidrome playlist was created (see below) |
| `created_at` | string | ISO 8601 timestamp |
| `updated_at` | string | ISO 8601 timestamp |

**Example**

```bash
curl http://localhost:8080/status/d290f1ee-6c54-4b01-90e6-d701748f0851 \
  -H "Authorization: Bearer changeme"
```

```json
{
  "id": "d290f1ee-6c54-4b01-90e6-d701748f0851",
  "status": "done",
  "spotify_url": "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M",
  "total": 50,
  "done": 50,
  "files": [
    "/music/Blinding Lights - The Weeknd.flac",
    "/music/Levitating - Dua Lipa.flac"
  ],
  "created_at": "2025-01-15T12:00:00Z",
  "updated_at": "2025-01-15T12:04:32Z"
}
```

---

### `GET /health`

No authentication required. Returns `200 OK` when the server is running.

```json
{ "status": "ok" }
```

---

## Navidrome integration

When downloading a playlist or album, you can pass Navidrome credentials and SpotiFLAC will automatically create the playlist in your Navidrome library after the download finishes.

**What happens:**
1. All tracks are downloaded to the output directory
2. A Navidrome library scan is triggered and waited on (up to 5 minutes)
3. Each track is searched in Navidrome by title and artist
4. A playlist is created with all found tracks

**`navidrome` object fields**

| Field | Type | Required | Description |
|---|---|---|---|
| `url` | string | yes | Base URL of your Navidrome instance |
| `username` | string | yes | Navidrome username |
| `password` | string | yes | Navidrome password |
| `playlist_name` | string | no | Override the playlist name. Defaults to the Spotify playlist/album name |

**Example**

```bash
curl -X POST http://localhost:8080/download \
  -H "Authorization: Bearer changeme" \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M",
    "service": "tidal",
    "navidrome": {
      "url": "http://navidrome.example.com",
      "username": "admin",
      "password": "secret"
    }
  }'
```

Once the job completes, `GET /status/{job_id}` will include:

```json
{
  "status": "done",
  "navidrome_playlist_id": "42"
}
```

> Navidrome failures (wrong credentials, scan timeout, songs not yet indexed) are logged server-side but do not fail the job — downloaded files are always kept.

---

## Disclaimer

This project is for **educational and private use only**. The developer does not condone or encourage copyright infringement.

**SpotiFLAC** is not affiliated with, endorsed by, or connected to Spotify, Tidal, Qobuz, Amazon Music, Deezer, or any other streaming service. You are solely responsible for ensuring your use complies with your local laws and the Terms of Service of the respective platforms.
