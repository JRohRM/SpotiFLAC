package backend

import (
	"bytes"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// NavidromeClient talks to a Navidrome server via the OpenSubsonic API.
type NavidromeClient struct {
	BaseURL  string
	Username string
	Password string
	client   *http.Client
}

func NewNavidromeClient(baseURL, username, password string) *NavidromeClient {
	return &NavidromeClient{
		BaseURL:  strings.TrimRight(baseURL, "/"),
		Username: username,
		Password: password,
		client:   &http.Client{Timeout: 30 * time.Second},
	}
}

// authParams generates Subsonic API MD5-token authentication parameters.
func (c *NavidromeClient) authParams() url.Values {
	salt := fmt.Sprintf("%x", rand.Int63())
	h := md5.Sum([]byte(c.Password + salt))
	token := fmt.Sprintf("%x", h)
	return url.Values{
		"u": {c.Username},
		"t": {token},
		"s": {salt},
		"c": {"SpotiFLAC"},
		"v": {"1.16.1"},
		"f": {"json"},
	}
}

type subsonicResponse struct {
	Response struct {
		Status string `json:"status"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		SearchResult3 *struct {
			Song []struct {
				ID     string `json:"id"`
				Title  string `json:"title"`
				Artist string `json:"artist"`
			} `json:"song"`
		} `json:"searchResult3"`
		ScanStatus *struct {
			Scanning bool  `json:"scanning"`
			Count    int64 `json:"count"`
		} `json:"scanStatus"`
		Playlist *struct {
			ID string `json:"id"`
		} `json:"playlist"`
	} `json:"subsonic-response"`
}

func (c *NavidromeClient) call(endpoint string, params url.Values) (*subsonicResponse, error) {
	auth := c.authParams()
	for k, vs := range params {
		auth[k] = append(auth[k], vs...)
	}
	reqURL := fmt.Sprintf("%s/rest/%s?%s", c.BaseURL, endpoint, auth.Encode())
	resp, err := c.client.Get(reqURL)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	var result subsonicResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if e := result.Response.Error; e != nil {
		return nil, fmt.Errorf("subsonic error %d: %s", e.Code, e.Message)
	}
	return &result, nil
}

// StartScan triggers a Navidrome library rescan.
func (c *NavidromeClient) StartScan() error {
	_, err := c.call("startScan", nil)
	return err
}

// WaitForScan polls getScanStatus until scanning is finished or timeout is hit.
func (c *NavidromeClient) WaitForScan(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		result, err := c.call("getScanStatus", nil)
		if err != nil {
			return err
		}
		if result.Response.ScanStatus != nil && !result.Response.ScanStatus.Scanning {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("scan timed out after %s", timeout)
		}
		time.Sleep(5 * time.Second)
	}
}

// SearchSong searches for a song by title and artist and returns the first
// matching Navidrome song ID, or "" if none is found.
func (c *NavidromeClient) SearchSong(title, artist string) (string, error) {
	result, err := c.call("search3", url.Values{
		"query":       {title + " " + artist},
		"songCount":   {"5"},
		"albumCount":  {"0"},
		"artistCount": {"0"},
	})
	if err != nil {
		return "", err
	}
	if result.Response.SearchResult3 == nil {
		return "", nil
	}
	for _, song := range result.Response.SearchResult3.Song {
		return song.ID, nil
	}
	return "", nil
}

// nativeToken authenticates with Navidrome's native REST API and returns a JWT.
func (c *NavidromeClient) nativeToken() (string, error) {
	payload, _ := json.Marshal(map[string]string{
		"username": c.Username,
		"password": c.Password,
	})
	resp, err := c.client.Post(c.BaseURL+"/auth/login", "application/json", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("auth: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("auth: server returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("auth parse: %w", err)
	}
	if result.Token == "" {
		return "", fmt.Errorf("auth: empty token in response")
	}
	return result.Token, nil
}

// SetPlaylistCover downloads imageURL and uploads it as the playlist's cover art
// using Navidrome's native REST API.
func (c *NavidromeClient) SetPlaylistCover(playlistID, imageURL string) error {
	if imageURL == "" {
		return nil
	}

	// Download the cover image.
	imgResp, err := c.client.Get(imageURL)
	if err != nil {
		return fmt.Errorf("download cover: %w", err)
	}
	defer imgResp.Body.Close()
	imgData, err := io.ReadAll(imgResp.Body)
	if err != nil {
		return fmt.Errorf("read cover: %w", err)
	}

	// Detect the image content-type from the response header or the bytes.
	ct := imgResp.Header.Get("Content-Type")
	if ct == "" {
		ct = http.DetectContentType(imgData)
	}
	// Normalise to a plain MIME type (strip parameters like charset).
	if i := strings.Index(ct, ";"); i != -1 {
		ct = strings.TrimSpace(ct[:i])
	}

	// Authenticate with Navidrome's native REST API.
	token, err := c.nativeToken()
	if err != nil {
		return err
	}

	// PUT the raw image bytes directly — Navidrome's image endpoint is
	// registered with Consumes(image/*), so multipart/form-data returns 404.
	req, err := http.NewRequest(http.MethodPut,
		fmt.Sprintf("%s/api/playlist/%s/image", c.BaseURL, playlistID),
		bytes.NewReader(imgData),
	)
	if err != nil {
		return err
	}
	// Send both headers: Traefik and other reverse proxies often strip the
	// standard Authorization header, so Navidrome also accepts
	// X-ND-Authorization as a proxy-safe alternative.
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-ND-Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", ct)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		rbody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(rbody)))
	}
	return nil
}

// CreatePlaylist creates a new playlist in Navidrome and returns its ID.
func (c *NavidromeClient) CreatePlaylist(name string, songIDs []string) (string, error) {
	params := url.Values{"name": {name}}
	for _, id := range songIDs {
		params.Add("songId", id)
	}
	result, err := c.call("createPlaylist", params)
	if err != nil {
		return "", err
	}
	if result.Response.Playlist != nil {
		return result.Response.Playlist.ID, nil
	}
	return "", nil
}
