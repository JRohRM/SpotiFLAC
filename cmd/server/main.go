package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"spotiflac/backend"

	"github.com/google/uuid"
)

// ── Config ────────────────────────────────────────────────────────────────────

var (
	apiToken  = mustEnv("API_TOKEN")
	outputDir = mustEnv("OUTPUT_DIR")
	port      = getEnv("PORT", "8080")
)

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required environment variable %s is not set", key)
	}
	return v
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ── Job store ────────────────────────────────────────────────────────────────

type JobStatus string

const (
	StatusQueued     JobStatus = "queued"
	StatusProcessing JobStatus = "processing"
	StatusDone       JobStatus = "done"
	StatusFailed     JobStatus = "failed"
)

type Job struct {
	ID        string    `json:"id"`
	Status    JobStatus `json:"status"`
	SpotifyURL string   `json:"spotify_url"`
	Filename  string    `json:"filename,omitempty"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type JobStore struct {
	mu   sync.RWMutex
	jobs map[string]*Job
}

func newJobStore() *JobStore {
	return &JobStore{jobs: make(map[string]*Job)}
}

func (s *JobStore) create(spotifyURL string) *Job {
	job := &Job{
		ID:         uuid.New().String(),
		Status:     StatusQueued,
		SpotifyURL: spotifyURL,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	s.mu.Lock()
	s.jobs[job.ID] = job
	s.mu.Unlock()
	return job
}

func (s *JobStore) get(id string) (*Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	return j, ok
}

func (s *JobStore) update(id string, fn func(*Job)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		fn(j)
		j.UpdatedAt = time.Now()
	}
}

// ── Request / Response types ──────────────────────────────────────────────────

type DownloadRequest struct {
	URL     string `json:"url"`
	Service string `json:"service"` // "tidal", "qobuz", "deezer", "amazon", "auto"
}

type DownloadResponse struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != apiToken {
			writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "unauthorized"})
			return
		}
		next(w, r)
	}
}

// ── Download worker ───────────────────────────────────────────────────────────

// processJob runs in a goroutine. It mirrors exactly what app.go does:
//  1. Fetch Spotify metadata  →  get track list
//  2. For each track, call backend downloaders with "auto" fallback
//
// We keep it simple here: single-track downloads (albums/playlists produce
// multiple jobs, one per track, enqueued by the caller – or we handle the
// full list here, which is what we do below for convenience).
func processJob(store *JobStore, job *Job, service string) {
	store.update(job.ID, func(j *Job) { j.Status = StatusProcessing })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// ── Step 1: fetch Spotify metadata ────────────────────────────────────────
	metaCtx, metaCancel := context.WithTimeout(ctx, 30*time.Second)
	defer metaCancel()

	data, err := backend.GetFilteredSpotifyData(metaCtx, job.SpotifyURL, false, 1*time.Second)
	if err != nil {
		store.update(job.ID, func(j *Job) {
			j.Status = StatusFailed
			j.Error = fmt.Sprintf("metadata fetch failed: %v", err)
		})
		log.Printf("[%s] metadata error: %v", job.ID, err)
		return
	}

	// GetFilteredSpotifyData returns interface{} — marshal/unmarshal to extract
	// the fields we need (same pattern used in the original app.go).
	raw, err := json.Marshal(data)
	if err != nil {
		store.update(job.ID, func(j *Job) {
			j.Status = StatusFailed
			j.Error = "could not serialize metadata"
		})
		return
	}

	// We accept both a single track object and a batch (array under "tracks").
	// Normalize to a slice of track items.
	var metaEnvelope struct {
		Track *trackItem   `json:"track"`
		Tracks []trackItem `json:"tracks"`
	}
	if err := json.Unmarshal(raw, &metaEnvelope); err != nil {
		store.update(job.ID, func(j *Job) {
			j.Status = StatusFailed
			j.Error = "could not parse metadata structure"
		})
		return
	}

	var tracks []trackItem
	if metaEnvelope.Track != nil {
		tracks = []trackItem{*metaEnvelope.Track}
	} else {
		tracks = metaEnvelope.Tracks
	}

	if len(tracks) == 0 {
		store.update(job.ID, func(j *Job) {
			j.Status = StatusFailed
			j.Error = "no tracks found in metadata response"
		})
		return
	}

	// ── Step 2: download ───────────────────────────────────────────────────────
	// For simplicity we only process the first track when job was for a single
	// URL. Playlist support can be added later.
	track := tracks[0]

	if service == "" || service == "auto" {
		service = "tidal"
	}

	req := backend.DownloadRequest{
		Service:             service,
		SpotifyID:           track.ID,
		TrackName:           track.Name,
		ArtistName:          track.Artists,
		AlbumName:           track.Album.Name,
		AlbumArtist:         track.Album.Artist,
		ReleaseDate:         track.Album.ReleaseDate,
		CoverURL:            track.Album.CoverURL,
		ISRC:                track.ISRC,
		Duration:            track.DurationMs,
		OutputDir:           outputDir,
		AudioFormat:         "LOSSLESS",
		FilenameFormat:      "title-artist",
		AllowFallback:       true,
		SpotifyTrackNumber:  track.TrackNumber,
		SpotifyDiscNumber:   track.DiscNumber,
		SpotifyTotalTracks:  track.Album.TotalTracks,
		SpotifyTotalDiscs:   track.Album.TotalDiscs,
		Copyright:           track.Album.Copyright,
		Publisher:           track.Album.Publisher,
	}

	filename, dlErr := backend.DownloadTrack(ctx, req)
	if dlErr != nil {
		store.update(job.ID, func(j *Job) {
			j.Status = StatusFailed
			j.Error = fmt.Sprintf("download failed: %v", dlErr)
		})
		log.Printf("[%s] download error: %v", job.ID, dlErr)
		return
	}

	store.update(job.ID, func(j *Job) {
		j.Status = StatusDone
		j.Filename = filename
	})
	log.Printf("[%s] done → %s", job.ID, filename)
}

// trackItem mirrors the shape of data returned by GetFilteredSpotifyData.
// Field names match the JSON tags visible in app.go.
type trackItem struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Artists    string `json:"artists"`
	ISRC       string `json:"isrc"`
	DurationMs int    `json:"duration_ms"`
	TrackNumber int   `json:"track_number"`
	DiscNumber  int   `json:"disc_number"`
	Album      struct {
		Name        string `json:"name"`
		Artist      string `json:"artist"`
		ReleaseDate string `json:"release_date"`
		CoverURL    string `json:"cover_url"`
		TotalTracks int    `json:"total_tracks"`
		TotalDiscs  int    `json:"total_discs"`
		Copyright   string `json:"copyright"`
		Publisher   string `json:"publisher"`
	} `json:"album"`
}

// ── Handlers ──────────────────────────────────────────────────────────────────

func handleDownload(store *JobStore) http.HandlerFunc {
	return authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "method not allowed"})
			return
		}

		var req DownloadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid JSON body"})
			return
		}

		if req.URL == "" {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "url is required"})
			return
		}

		if !strings.Contains(req.URL, "open.spotify.com") {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "url must be a Spotify URL"})
			return
		}

		job := store.create(req.URL)

		// Fire and forget — the iOS Shortcut gets an immediate response.
		go processJob(store, job, req.Service)

		log.Printf("[%s] queued %s", job.ID, req.URL)
		writeJSON(w, http.StatusAccepted, DownloadResponse{
			JobID:  job.ID,
			Status: string(StatusQueued),
		})
	})
}

func handleStatus(store *JobStore) http.HandlerFunc {
	return authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, ErrorResponse{Error: "method not allowed"})
			return
		}

		// Path: /status/{id}
		id := strings.TrimPrefix(r.URL.Path, "/status/")
		if id == "" {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "job id required"})
			return
		}

		job, ok := store.get(id)
		if !ok {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "job not found"})
			return
		}

		writeJSON(w, http.StatusOK, job)
	})
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	if err := backend.InitHistoryDB("SpotiFLAC"); err != nil {
		log.Printf("warning: could not init history DB: %v", err)
	}
	defer backend.CloseHistoryDB()

	store := newJobStore()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/download", handleDownload(store))
	mux.HandleFunc("/status/", handleStatus(store))

	addr := ":" + port
	log.Printf("spotiflac-server listening on %s", addr)
	log.Printf("output directory: %s", outputDir)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
