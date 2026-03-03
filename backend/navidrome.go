package backend

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
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
