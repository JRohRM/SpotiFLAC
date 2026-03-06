//go:build server
// +build server

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend"
	"github.com/google/uuid"
)

// ── Config ────────────────────────────────────────────────────────────────────

func serverMustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required environment variable %q is not set", key)
	}
	return v
}

func serverGetEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// normalizeSpotifyURL strips locale prefixes (intl-fr, intl-de, …) and
// tracking query parameters (?si=…) so the metadata fetcher always receives
// a clean canonical URL like https://open.spotify.com/track/<id>
var localeRe = regexp.MustCompile(`/intl-[a-z]{2}/`)

func normalizeSpotifyURL(raw string) string {
	clean := localeRe.ReplaceAllString(raw, "/")
	if u, err := url.Parse(clean); err == nil {
		u.RawQuery = ""
		u.Fragment = ""
		clean = u.String()
	}
	return clean
}

// ── Job store ─────────────────────────────────────────────────────────────────

type JobStatus string

const (
	StatusQueued     JobStatus = "queued"
	StatusProcessing JobStatus = "processing"
	StatusDone       JobStatus = "done"
	StatusFailed     JobStatus = "failed"
)

type Job struct {
	ID                  string    `json:"id"`
	Status              JobStatus `json:"status"`
	SpotifyURL          string    `json:"spotify_url"`
	Filename            string    `json:"filename,omitempty"`
	Files               []string  `json:"files,omitempty"`
	Total               int       `json:"total,omitempty"`
	Done                int       `json:"done,omitempty"`
	Error               string    `json:"error,omitempty"`
	NavidromePlaylistID string    `json:"navidrome_playlist_id,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type navidromeConfig struct {
	URL          string
	Username     string
	Password     string
	PlaylistName string
}

type jobStore struct {
	mu   sync.RWMutex
	jobs map[string]*Job
}

func newJobStore() *jobStore { return &jobStore{jobs: make(map[string]*Job)} }

func (s *jobStore) create(spotURL string) *Job {
	j := &Job{
		ID:         uuid.New().String(),
		Status:     StatusQueued,
		SpotifyURL: spotURL,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	s.mu.Lock()
	s.jobs[j.ID] = j
	s.mu.Unlock()
	return j
}

func (s *jobStore) get(id string) (*Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[id]
	return j, ok
}

func (s *jobStore) update(id string, fn func(*Job)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		fn(j)
		j.UpdatedAt = time.Now()
	}
}

// ── Worker ────────────────────────────────────────────────────────────────────

type trackEnvItem struct {
	SpotifyID   string `json:"spotify_id"`
	Name        string `json:"name"`
	Artists     string `json:"artists"`
	ISRC        string `json:"isrc"`
	DurationMs  int    `json:"duration_ms"`
	TrackNumber int    `json:"track_number"`
	DiscNumber  int    `json:"disc_number"`
	TotalTracks int    `json:"total_tracks"`
	TotalDiscs  int    `json:"total_discs"`
	AlbumName   string `json:"album_name"`
	AlbumArtist string `json:"album_artist"`
	ReleaseDate string `json:"release_date"`
	Images      string `json:"images"`
	Copyright   string `json:"copyright"`
	Publisher   string `json:"publisher"`
}

func runJob(store *jobStore, app *App, job *Job, service string, outputDir string, naviCfg *navidromeConfig) {
	store.update(job.ID, func(j *Job) { j.Status = StatusProcessing })

	metaReq := SpotifyMetadataRequest{
		URL:   job.SpotifyURL,
		Batch: false,
		Delay: 1.0,
	}
	metaData, err := app.GetSpotifyMetadata(metaReq)
	if err != nil {
		jobFail(store, job, "metadata fetch failed: "+err.Error())
		return
	}

	// Log raw shape to help debug unexpected metadata structures
	raw := []byte(metaData)
	log.Printf("[%s] raw metadata: %s", job.ID, metaData)

	var envelope struct {
		Track        *trackEnvItem  `json:"track"`
		TrackList    []trackEnvItem `json:"track_list"`
		PlaylistInfo *struct {
			Owner struct {
				Name string `json:"name"`
			} `json:"owner"`
			Cover string `json:"cover"`
		} `json:"playlist_info"`
		AlbumInfo *struct {
			Name  string `json:"name"`
			Cover string `json:"cover"`
		} `json:"album_info"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		jobFail(store, job, "could not parse metadata: "+err.Error())
		return
	}

	var tracks []trackEnvItem
	if envelope.Track != nil {
		tracks = []trackEnvItem{*envelope.Track}
	} else {
		tracks = envelope.TrackList
	}
	if len(tracks) == 0 {
		jobFail(store, job, "no tracks found")
		return
	}

	if service == "" || service == "auto" {
		service = "tidal"
	}

	store.update(job.ID, func(j *Job) { j.Total = len(tracks) })

	for i, t := range tracks {
		dlReq := DownloadRequest{
			Service:            service,
			SpotifyID:          t.SpotifyID,
			TrackName:          t.Name,
			ArtistName:         t.Artists,
			AlbumName:          t.AlbumName,
			AlbumArtist:        t.AlbumArtist,
			ReleaseDate:        t.ReleaseDate,
			CoverURL:           t.Images,
			Duration:           t.DurationMs,
			OutputDir:          outputDir,
			AudioFormat:        "LOSSLESS",
			FilenameFormat:     "title-artist",
			AllowFallback:      true,
			SpotifyTrackNumber: t.TrackNumber,
			SpotifyDiscNumber:  t.DiscNumber,
			SpotifyTotalTracks: t.TotalTracks,
			SpotifyTotalDiscs:  t.TotalDiscs,
			Copyright:          t.Copyright,
			Publisher:          t.Publisher,
		}

		resp, dlErr := app.DownloadTrack(dlReq)
		if dlErr != nil || !resp.Success {
			msg := ""
			if dlErr != nil {
				msg = dlErr.Error()
			} else {
				msg = resp.Error
			}
			log.Printf("[%s] track %d/%d failed: %s", job.ID, i+1, len(tracks), msg)
			continue
		}

		log.Printf("[%s] %d/%d done → %s", job.ID, i+1, len(tracks), resp.File)
		store.update(job.ID, func(j *Job) {
			j.Done++
			j.Files = append(j.Files, resp.File)
			j.Filename = resp.File
		})
	}

	store.update(job.ID, func(j *Job) { j.Status = StatusDone })
	log.Printf("[%s] all done", job.ID)

	if naviCfg != nil {
		nc := backend.NewNavidromeClient(naviCfg.URL, naviCfg.Username, naviCfg.Password)

		log.Printf("[%s] navidrome: starting library scan", job.ID)
		if err := nc.StartScan(); err != nil {
			log.Printf("[%s] navidrome: scan start failed: %s", job.ID, err)
		} else {
			log.Printf("[%s] navidrome: waiting for scan to finish…", job.ID)
			if err := nc.WaitForScan(5 * time.Minute); err != nil {
				log.Printf("[%s] navidrome: %s", job.ID, err)
			}
		}

		var songIDs []string
		for _, t := range tracks {
			id, err := nc.SearchSong(t.Name, t.Artists)
			if err != nil {
				log.Printf("[%s] navidrome: search failed for %q: %s", job.ID, t.Name, err)
				continue
			}
			if id != "" {
				songIDs = append(songIDs, id)
				log.Printf("[%s] navidrome: found %q → %s", job.ID, t.Name, id)
			} else {
				log.Printf("[%s] navidrome: not found: %q by %s", job.ID, t.Name, t.Artists)
			}
		}

		if len(songIDs) == 0 {
			log.Printf("[%s] navidrome: no songs found, skipping playlist creation", job.ID)
			return
		}

		playlistName := naviCfg.PlaylistName
		if playlistName == "" && envelope.PlaylistInfo != nil {
			playlistName = envelope.PlaylistInfo.Owner.Name
		}
		if playlistName == "" && envelope.AlbumInfo != nil {
			playlistName = envelope.AlbumInfo.Name
		}
		if playlistName == "" {
			playlistName = "SpotiFLAC Import"
		}

		existingID, err := nc.FindPlaylistByName(playlistName)
		if err != nil {
			log.Printf("[%s] navidrome: playlist lookup failed: %s", job.ID, err)
		}

		var plID string
		if existingID != "" {
			log.Printf("[%s] navidrome: updating existing playlist %q (id=%s) with %d/%d songs",
				job.ID, playlistName, existingID, len(songIDs), len(tracks))
			if err := nc.UpdatePlaylist(existingID, songIDs); err != nil {
				log.Printf("[%s] navidrome: update playlist failed: %s", job.ID, err)
			} else {
				plID = existingID
				log.Printf("[%s] navidrome: playlist updated (id=%s)", job.ID, plID)
			}
		} else {
			log.Printf("[%s] navidrome: creating playlist %q with %d/%d songs",
				job.ID, playlistName, len(songIDs), len(tracks))
			plID, err = nc.CreatePlaylist(playlistName, songIDs)
			if err != nil {
				log.Printf("[%s] navidrome: create playlist failed: %s", job.ID, err)
			} else {
				log.Printf("[%s] navidrome: playlist created (id=%s)", job.ID, plID)
			}
		}
		if plID != "" {
			store.update(job.ID, func(j *Job) { j.NavidromePlaylistID = plID })

			// Migrate cover art if available.
			coverURL := ""
			if envelope.PlaylistInfo != nil {
				coverURL = envelope.PlaylistInfo.Cover
			} else if envelope.AlbumInfo != nil {
				coverURL = envelope.AlbumInfo.Cover
			}
			if coverURL != "" {
				log.Printf("[%s] navidrome: uploading cover art", job.ID)
				if err := nc.SetPlaylistCover(plID, coverURL); err != nil {
					log.Printf("[%s] navidrome: cover upload failed: %s", job.ID, err)
				} else {
					log.Printf("[%s] navidrome: cover uploaded", job.ID)
				}
			}
		}
	}
}

func jobFail(store *jobStore, job *Job, msg string) {
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

func withAuth(token string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if t == "" || t != token {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

// ── Entry point ───────────────────────────────────────────────────────────────

func StartServer() {
	apiToken := serverMustEnv("API_TOKEN")
	outputDir := serverMustEnv("OUTPUT_DIR")
	port := serverGetEnv("PORT", "8080")

	if err := backend.InitHistoryDB("SpotiFLAC"); err != nil {
		log.Printf("warning: history DB init failed: %v", err)
	}
	defer backend.CloseHistoryDB()

	app := NewApp()
	app.ctx = context.Background()

	store := newJobStore()
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/download", withAuth(apiToken, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
			return
		}
		var body struct {
			URL       string `json:"url"`
			Service   string `json:"service"`
			Navidrome *struct {
				URL          string `json:"url"`
				Username     string `json:"username"`
				Password     string `json:"password"`
				PlaylistName string `json:"playlist_name"`
			} `json:"navidrome"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.URL == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json body with url required"})
			return
		}
		if !strings.Contains(body.URL, "open.spotify.com") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "must be a spotify URL"})
			return
		}

		var naviCfg *navidromeConfig
		if body.Navidrome != nil && body.Navidrome.URL != "" && body.Navidrome.Username != "" {
			naviCfg = &navidromeConfig{
				URL:          body.Navidrome.URL,
				Username:     body.Navidrome.Username,
				Password:     body.Navidrome.Password,
				PlaylistName: body.Navidrome.PlaylistName,
			}
		}

		body.URL = normalizeSpotifyURL(body.URL)
		job := store.create(body.URL)
		go runJob(store, app, job, body.Service, outputDir, naviCfg)
		log.Printf("[%s] queued %s", job.ID, body.URL)
		writeJSON(w, http.StatusAccepted, map[string]string{
			"job_id": job.ID,
			"status": string(StatusQueued),
		})
	}))

	mux.HandleFunc("/status/", withAuth(apiToken, func(w http.ResponseWriter, r *http.Request) {
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
	}))

	fmt.Printf("spotiflac-server on :%s  output→%s\n", port, outputDir)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("server: %v", err)
	}
}
