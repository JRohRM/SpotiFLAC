package backend

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type SongLinkClient struct {
	mu               sync.Mutex
	client           *http.Client
	lastAPICallTime  time.Time
	apiCallCount     int
	apiCallResetTime time.Time
}

type SongLinkURLs struct {
	TidalURL  string `json:"tidal_url"`
	AmazonURL string `json:"amazon_url"`
	ISRC      string `json:"isrc"`
}

type TrackAvailability struct {
	SpotifyID string `json:"spotify_id"`
	Tidal     bool   `json:"tidal"`
	Amazon    bool   `json:"amazon"`
	Qobuz     bool   `json:"qobuz"`
	Deezer    bool   `json:"deezer"`
	TidalURL  string `json:"tidal_url,omitempty"`
	AmazonURL string `json:"amazon_url,omitempty"`
	QobuzURL  string `json:"qobuz_url,omitempty"`
	DeezerURL string `json:"deezer_url,omitempty"`
}

// _shared is the process-wide singleton used by SharedSongLinkClient().
var (
	_sharedOnce   sync.Once
	_sharedClient *SongLinkClient
)

// SharedSongLinkClient returns the process-wide singleton SongLinkClient.
// All callers share the same rate-limit state, preventing bursts that
// would otherwise trigger HTTP 429 responses from song.link.
func SharedSongLinkClient() *SongLinkClient {
	_sharedOnce.Do(func() {
		_sharedClient = NewSongLinkClient()
	})
	return _sharedClient
}

func NewSongLinkClient() *SongLinkClient {
	return &SongLinkClient{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		apiCallResetTime: time.Now(),
	}
}

// doRequest is the single entry point for all song.link API calls.
// It enforces a 7 s minimum inter-call delay and a 9 calls/minute cap,
// retries up to 3 times on HTTP 429, and holds the mutex for the entire
// operation so that concurrent callers are serialised correctly.
func (s *SongLinkClient) doRequest(spotifyTrackID, region string) (map[string]struct {
	URL string `json:"url"`
}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// ── per-minute call cap ───────────────────────────────────────────────────
	now := time.Now()
	if now.Sub(s.apiCallResetTime) >= time.Minute {
		s.apiCallCount = 0
		s.apiCallResetTime = now
	}
	if s.apiCallCount >= 9 {
		waitTime := time.Minute - now.Sub(s.apiCallResetTime)
		if waitTime > 0 {
			fmt.Printf("Rate limit reached, waiting %v...\n", waitTime.Round(time.Second))
			time.Sleep(waitTime)
			s.apiCallCount = 0
			s.apiCallResetTime = time.Now()
		}
	}

	// ── minimum inter-call delay ──────────────────────────────────────────────
	if !s.lastAPICallTime.IsZero() {
		if elapsed := time.Since(s.lastAPICallTime); elapsed < 7*time.Second {
			wait := 7*time.Second - elapsed
			fmt.Printf("Rate limiting: waiting %v...\n", wait.Round(time.Second))
			time.Sleep(wait)
		}
	}

	// ── build URL ─────────────────────────────────────────────────────────────
	spotifyURL := fmt.Sprintf("https://open.spotify.com/track/%s", spotifyTrackID)
	apiURL := fmt.Sprintf("https://api.song.link/v1-alpha.1/links?url=%s", url.QueryEscape(spotifyURL))
	if region != "" {
		apiURL += fmt.Sprintf("&userCountry=%s", region)
	}

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// ── HTTP + 429 retry ──────────────────────────────────────────────────────
	maxRetries := 3
	var resp *http.Response
	for i := 0; i < maxRetries; i++ {
		resp, err = s.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request failed: %w", err)
		}
		s.lastAPICallTime = time.Now()
		s.apiCallCount++

		if resp.StatusCode == 429 {
			resp.Body.Close()
			if i < maxRetries-1 {
				wait := 15 * time.Second
				fmt.Printf("Rate limited by API, waiting %v before retry...\n", wait)
				time.Sleep(wait)
				continue
			}
			return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
		}
		break
	}
	defer resp.Body.Close()

	// ── parse ─────────────────────────────────────────────────────────────────
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("API returned empty response")
	}

	var parsed struct {
		LinksByPlatform map[string]struct {
			URL string `json:"url"`
		} `json:"linksByPlatform"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		bodyStr := string(body)
		if len(bodyStr) > 200 {
			bodyStr = bodyStr[:200] + "..."
		}
		return nil, fmt.Errorf("failed to decode response: %w (body: %s)", err, bodyStr)
	}
	return parsed.LinksByPlatform, nil
}

// GetTidalAndDeezerURLs fetches both the Tidal and Deezer URLs for a Spotify
// track in a single song.link API call.
func (s *SongLinkClient) GetTidalAndDeezerURLs(spotifyTrackID string) (tidalURL, deezerURL string, err error) {
	links, err := s.doRequest(spotifyTrackID, "")
	if err != nil {
		return "", "", err
	}
	if link, ok := links["tidal"]; ok {
		tidalURL = link.URL
	}
	if tidalURL == "" {
		return "", "", fmt.Errorf("tidal link not found")
	}
	if link, ok := links["deezer"]; ok {
		deezerURL = link.URL
	}
	return tidalURL, deezerURL, nil
}

func (s *SongLinkClient) GetAllURLsFromSpotify(spotifyTrackID string, region string) (*SongLinkURLs, error) {
	fmt.Println("Getting streaming URLs from song.link...")
	links, err := s.doRequest(spotifyTrackID, region)
	if err != nil {
		return nil, err
	}

	urls := &SongLinkURLs{}
	if link, ok := links["tidal"]; ok && link.URL != "" {
		urls.TidalURL = link.URL
		fmt.Printf("✓ Tidal URL found\n")
	}
	if link, ok := links["amazonMusic"]; ok && link.URL != "" {
		urls.AmazonURL = link.URL
		fmt.Printf("✓ Amazon URL found\n")
	}
	if link, ok := links["deezer"]; ok && link.URL != "" {
		if isrc, err := getDeezerISRC(link.URL); err == nil && isrc != "" {
			urls.ISRC = isrc
		}
	}
	if urls.TidalURL == "" && urls.AmazonURL == "" {
		return nil, fmt.Errorf("no streaming URLs found")
	}
	return urls, nil
}

func (s *SongLinkClient) CheckTrackAvailability(spotifyTrackID string) (*TrackAvailability, error) {
	fmt.Printf("Checking availability for track: %s\n", spotifyTrackID)
	links, err := s.doRequest(spotifyTrackID, "")
	if err != nil {
		return nil, err
	}

	availability := &TrackAvailability{SpotifyID: spotifyTrackID}
	if link, ok := links["tidal"]; ok && link.URL != "" {
		availability.Tidal = true
		availability.TidalURL = link.URL
	}
	if link, ok := links["amazonMusic"]; ok && link.URL != "" {
		availability.Amazon = true
		availability.AmazonURL = link.URL
	}
	if link, ok := links["deezer"]; ok && link.URL != "" {
		availability.Deezer = true
		availability.DeezerURL = link.URL
		if isrc, err := getDeezerISRC(link.URL); err == nil && isrc != "" {
			availability.Qobuz = checkQobuzAvailability(isrc)
		}
	}
	return availability, nil
}

func (s *SongLinkClient) GetDeezerURLFromSpotify(spotifyTrackID string) (string, error) {
	fmt.Println("Getting Deezer URL from song.link...")
	links, err := s.doRequest(spotifyTrackID, "")
	if err != nil {
		return "", err
	}
	if link, ok := links["deezer"]; ok && link.URL != "" {
		fmt.Printf("Found Deezer URL: %s\n", link.URL)
		return link.URL, nil
	}
	return "", fmt.Errorf("deezer link not found")
}

func (s *SongLinkClient) GetISRC(spotifyID string) (string, error) {
	deezerURL, err := s.GetDeezerURLFromSpotify(spotifyID)
	if err != nil {
		return "", err
	}
	return getDeezerISRC(deezerURL)
}

func checkQobuzAvailability(isrc string) bool {
	client := &http.Client{Timeout: 10 * time.Second}
	appID := "798273057"

	searchURL := fmt.Sprintf("https://www.qobuz.com/api.json/0.2/track/search?query=%s&limit=1&app_id=%s", isrc, appID)
	resp, err := client.Get(searchURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return false
	}

	var searchResp struct {
		Tracks struct {
			Total int `json:"total"`
		} `json:"tracks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return false
	}
	return searchResp.Tracks.Total > 0
}

func getDeezerISRC(deezerURL string) (string, error) {
	var trackID string
	if strings.Contains(deezerURL, "/track/") {
		parts := strings.Split(deezerURL, "/track/")
		if len(parts) > 1 {
			trackID = strings.Split(parts[1], "?")[0]
			trackID = strings.TrimSpace(trackID)
		}
	}
	if trackID == "" {
		return "", fmt.Errorf("could not extract track ID from Deezer URL: %s", deezerURL)
	}

	apiURL := fmt.Sprintf("https://api.deezer.com/track/%s", trackID)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(apiURL)
	if err != nil {
		return "", fmt.Errorf("failed to call Deezer API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Deezer API returned status %d", resp.StatusCode)
	}

	var deezerTrack struct {
		ID    int64  `json:"id"`
		ISRC  string `json:"isrc"`
		Title string `json:"title"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&deezerTrack); err != nil {
		return "", fmt.Errorf("failed to decode Deezer API response: %w", err)
	}
	if deezerTrack.ISRC == "" {
		return "", fmt.Errorf("ISRC not found in Deezer API response for track %s", trackID)
	}

	fmt.Printf("Found ISRC from Deezer: %s (track: %s)\n", deezerTrack.ISRC, deezerTrack.Title)
	return deezerTrack.ISRC, nil
}
