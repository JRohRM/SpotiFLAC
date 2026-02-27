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

	"github.com/google/uuid"
	"spotiflac/backend"
)

// ── Config ────────────────────────────────────────────────────────────────────

var (
	apiToken  = mustEnv("API_TOKEN")
	outputDir = mustEnv("OUTPUT_DIR")
	listenPort = getEnv("PORT", "8080")
)

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required environment variable %q is not set", key)
	}
	return v
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ── Job store (in-memory) ─────────────────────────────────────────────────────

type JobStatus string

const (
	StatusQueued     JobStatus = "queued"
	StatusProcessing JobStatus = "processing"
	StatusDone       JobStatus = "done"
	StatusFailed     JobStatus = "failed"
)

type Job struct {
	ID         string    `json:"id"`
	Status     JobStatus `json:"status"`
	SpotifyURL string    `json:"spotify_url"`
	Filename   string    `json:"filename,omitempty"`
	Error      string    `json:"error,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type JobStore struct {
	mu   sync.RWMutex
	jobs map[string]*Job
}

func newJobStore() *JobStore { return &JobStore{jobs: make(map[string]*Job)} }

func (s *JobStore) create(url string) *Job {
	j := &Job{
		ID:         uuid.New().String(),
		Status:     StatusQueued,
		SpotifyURL: url,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	s.mu.Lock()
	s.jobs[j.ID] = j
	s.mu.Unlock()
	return j
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

// ── Spotify metadata types ────────────────────────────────────────────────────
// These mirror the JSON shape returned by backend.GetFilteredSpotifyData.

type spotifyAlbum struct {
	Name        string `json:"name"`
	Artist      string `json:"artist"`
	ReleaseDate string `json:"release_date"`
	CoverURL    string `json:"cover_url"`
	TotalTracks int    `json:"total_tracks"`
	TotalDiscs  int    `json:"total_discs"`
	Copyright   string `json:"copyright"`
	Publisher   string `json:"publisher"`
}

type spotifyTrack struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Artists     string       `json:"artists"`
	ISRC        string       `json:"isrc"`
	DurationMs  int          `json:"duration_ms"`
	TrackNumber int          `json:"track_number"`
	DiscNumber  int          `json:"disc_number"`
	Album       spotifyAlbum `json:"album"`
}

type spotifyMetaEnvelope struct {
	Track  *spotifyTrack  `json:"track"`
	Tracks []spotifyTrack `json:"tracks"`
}

// ── Download worker ───────────────────────────────────────────────────────────

func processJob(store *JobStore, job *Job, service string) {
	store.update(job.ID, func(j *Job) { j.Status = StatusProcessing })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Step 1 — fetch Spotify metadata
	metaCtx, metaCancel := context.WithTimeout(ctx, 30*time.Second)
	defer metaCancel()

	rawData, err := backend.GetFilteredSpotifyData(metaCtx, job.SpotifyURL, false, time.Second)
	if err != nil {
		fail(store, job, fmt.Sprintf("metadata fetch failed: %v", err))
		return
	}

	b, err := json.Marshal(rawData)
	if err != nil {
		fail(store, job, "could not re-marshal metadata")
		return
	}

	var envelope spotifyMetaEnvelope
	if err := json.Unmarshal(b, &envelope); err != nil {
		fail(store, job, "could not parse metadata envelope")
		return
	}

	var tracks []spotifyTrack
	if envelope.Track != nil {
		tracks = []spotifyTrack{*envelope.Track}
	} else {
		tracks = envelope.Tracks
	}

	if len(tracks) == 0 {
		fail(store, job, "no tracks found in metadata response")
		return
	}

	t := tracks[0]

	if service == "" || service == "auto" {
		service = "tidal"
	}

	// Step 2 — build DownloadRequest (matches the struct in app.go)
	req := backend.DownloadRequest{
		Service:             service,
		SpotifyID:           t.ID,
		TrackName:           t.Name,
		ArtistName:          t.Artists,
		AlbumName:           t.Album.Name,
		AlbumArtist:         t.Album.Artist,
		ReleaseDate:         t.Album.ReleaseDate,
		CoverURL:            t.Album.CoverURL,
		ISRC:                t.ISRC,
		Duration:            t.DurationMs,
		OutputDir:           outputDir,
		AudioFormat:         "LOSSLESS",
		FilenameFormat:      "title-artist",
		AllowFallback:       true,
		SpotifyTrackNumber:  t.TrackNumber,
		SpotifyDiscNumber:   t.DiscNumber,
		SpotifyTotalTracks:  t.Album.TotalTracks,
		SpotifyTotalDiscs:   t.Album.TotalDiscs,
		Copyright:           t.Album.Copyright,
		Publisher:           t.Album.Publisher,
	}

	filename, dlErr := backend.DownloadTrack(ctx, req)
	if dlErr != nil {
		fail(store, job, fmt.Sprintf("download failed: %v", dlErr))
		return
	}

	store.update(job.ID, func(j *Job) {
		j.Status = StatusDone
		j.Filename = filename
	})
	log.Printf("[%s] done → %s", job.ID, filename)
}

func fail(store *JobStore, job *Job, msg string) {
	store.update(job.ID, func(j *Job) {
		j.Status = StatusFailed
		j.Error = msg
	})
	log.Printf("[%s] failed: %s", job.ID, msg)
}

// ── HTTP helpers ──────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token != apiToken {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

// ── Handlers ──────────────────────────────────────────────────────────────────

func handleDownload(store *JobStore) http.HandlerFunc {
	return auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
			return
		}

		var body struct {
			URL     string `json:"url"`
			Service string `json:"service"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.URL == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json body with url required"})
			return
		}
		if !strings.Contains(body.URL, "open.spotify.com") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "must be a spotify URL"})
			return
		}

		job := store.create(body.URL)
		go processJob(store, job, body.Service)

		log.Printf("[%s] queued %s", job.ID, body.URL)
		writeJSON(w, http.StatusAccepted, map[string]string{
			"job_id": job.ID,
			"status": string(StatusQueued),
		})
	})
}

func handleStatus(store *JobStore) http.HandlerFunc {
	return auth(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/status/")
		if id == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing job id"})
			return
		}
		job, ok := store.get(id)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
			return
		}
		writeJSON(w, http.StatusOK, job)
	})
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	if err := backend.InitHistoryDB("SpotiFLAC"); err != nil {
		log.Printf("warning: history DB init failed: %v", err)
	}
	defer backend.CloseHistoryDB()

	store := newJobStore()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/download", handleDownload(store))
	mux.HandleFunc("/status/", handleStatus(store))

	log.Printf("spotiflac-server on :%s  output→%s", listenPort, outputDir)
	if err := http.ListenAndServe(":"+listenPort, mux); err != nil {
		log.Fatalf("server: %v", err)
	}
}
