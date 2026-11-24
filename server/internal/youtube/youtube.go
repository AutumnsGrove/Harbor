// Package youtube is the one place that knows YouTube's URL shapes and its
// public metadata endpoint.
//
// It exists because the page is not always readable: YouTube answers a plain
// fetch from a datacenter IP with /sorry/ (spike S8 measured it), so 371 of the
// potato's 421 video saves failed their first enrichment with nothing to show
// for it. The oEmbed endpoint is public, keyless and unblocked, and it knows the
// video's title, channel and thumbnail — which is enough to file the item
// properly and to leave the transcript to phase 7.
//
// It deliberately does not fetch captions: transcripts are phase 7's, and the
// id extraction here is what that phase will reuse.
package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is YouTube's public oEmbed endpoint. It takes ?url= (the
	// watch URL) and ?format=json, and needs no credential.
	DefaultBaseURL = "https://www.youtube.com/oembed"

	// maxAnswerBytes bounds the JSON. The real answer is under a kilobyte; a
	// megabyte is a redirect to something that is not oEmbed.
	maxAnswerBytes = 64 << 10

	// defaultTimeout is the whole call, not the dial: this runs inside an
	// enrichment pass that has its own budget.
	defaultTimeout = 10 * time.Second
)

// ErrNoMetadata is the definitive answer: the endpoint knows this id and has
// nothing for it (deleted, private, age-gated) or answers an empty document.
// Retrying will not change it, so a caller can park the item on it.
//
// Everything else — a timeout, a 429, a 5xx, a body that is not JSON — is
// transient by nature and must stay retryable: harbor's first drain asked this
// endpoint 371 times in a burst, and a rate limit on one of those is not a
// reason to give up on a video forever.
var ErrNoMetadata = errors.New("youtube: no metadata for this video")

// videoIDLength is YouTube's id length, and its alphabet is base64-url without
// padding. Both are checked so a path segment like "playlist" cannot pass.
const videoIDLength = 11

// Metadata is what the public endpoint answers, reduced to what Harbor stores.
type Metadata struct {
	VideoID   string `json:"videoId"`
	Title     string `json:"title"`
	Author    string `json:"author"`
	AuthorURL string `json:"authorUrl"`
	Thumbnail string `json:"thumbnail"`
}

// videoHosts are the hosts that serve a watchable video. music.youtube.com is
// included because a saved song is a video item, not an article.
var videoHosts = map[string]bool{
	"youtube.com":              true,
	"www.youtube.com":          true,
	"m.youtube.com":            true,
	"music.youtube.com":        true,
	"youtube-nocookie.com":     true,
	"www.youtube-nocookie.com": true,
	"youtu.be":                 true,
}

// VideoID extracts the video id from a watch, youtu.be, shorts, embed, live or
// /v/ URL. A playlist, a channel or any other YouTube page is not a video and
// answers false — those are pages with readable HTML.
func VideoID(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if !videoHosts[host] {
		return "", false
	}

	path := strings.TrimSuffix(parsed.Path, "/")
	// youtu.be/<id> is the whole URL.
	if host == "youtu.be" {
		return validID(strings.TrimPrefix(path, "/"))
	}
	if id := parsed.Query().Get("v"); id != "" && (path == "/watch" || path == "") {
		return validID(id)
	}
	for _, prefix := range []string{"/shorts/", "/embed/", "/live/", "/v/"} {
		if rest, found := strings.CutPrefix(path, prefix); found {
			return validID(strings.SplitN(rest, "/", 2)[0])
		}
	}
	return "", false
}

func validID(id string) (string, bool) {
	if len(id) != videoIDLength {
		return "", false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return "", false
		}
	}
	return id, true
}

// WatchURL is the canonical page for an id, which is what oEmbed wants: it
// refuses youtu.be and the embed form.
func WatchURL(id string) string {
	return "https://www.youtube.com/watch?v=" + url.QueryEscape(id)
}

// Config is everything the client needs. The endpoint is public, so there is no
// key here by design: a video's own metadata is not behind an account.
type Config struct {
	// HTTP is the client. Nil gets a sensible one with the package timeout.
	HTTP *http.Client
	// BaseURL overrides the endpoint, for a test server.
	BaseURL string
	// UserAgent identifies Harbor. It defaults to the plain name.
	UserAgent string
}

// Client fetches a video's public metadata.
type Client struct {
	http      *http.Client
	baseURL   string
	userAgent string
}

// New builds the client. It never fails; a bad endpoint is the first call's
// problem, which the enrichment pass records like any other fetch failure.
func New(cfg Config) *Client {
	httpClient := cfg.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	agent := strings.TrimSpace(cfg.UserAgent)
	if agent == "" {
		agent = "Harbor"
	}
	return &Client{http: httpClient, baseURL: baseURL, userAgent: agent}
}

// Metadata answers for any video URL, or reports why it could not. A non-video
// URL is an error rather than a false: the caller only reaches here for a
// video it already identified, so a mismatch is a bug worth seeing.
func (c *Client) Metadata(ctx context.Context, raw string) (Metadata, error) {
	id, ok := VideoID(raw)
	if !ok {
		return Metadata{}, fmt.Errorf("youtube: %q is not a video URL", raw)
	}

	endpoint := c.baseURL + "?format=json&url=" + url.QueryEscape(WatchURL(id))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Metadata{}, fmt.Errorf("youtube: building the oEmbed request: %w", err)
	}
	request.Header.Set("User-Agent", c.userAgent)
	request.Header.Set("Accept", "application/json")

	response, err := c.http.Do(request)
	if err != nil {
		return Metadata{}, fmt.Errorf("youtube: oEmbed: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		// 401 and 404 mean "this id has nothing": a private, deleted or
		// age-gated video, and no retry will change it. 429 and 5xx are the
		// endpoint being busy or broken, which is worth another attempt.
		if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
			return Metadata{}, fmt.Errorf("youtube: oEmbed answered %d for %s", response.StatusCode, id)
		}
		return Metadata{}, fmt.Errorf("%w: oEmbed answered %d for %s", ErrNoMetadata, response.StatusCode, id)
	}

	answer, err := io.ReadAll(io.LimitReader(response.Body, maxAnswerBytes))
	if err != nil {
		return Metadata{}, fmt.Errorf("youtube: reading the oEmbed answer: %w", err)
	}
	return parseAnswer(id, answer)
}

// oEmbedAnswer is the wire shape. Only the fields Harbor uses are read; the
// provider's version, width and height are not part of a save.
type oEmbedAnswer struct {
	Title       string `json:"title"`
	AuthorName  string `json:"author_name"`
	AuthorURL   string `json:"author_url"`
	Thumbnail   string `json:"thumbnail_url"`
	Type        string `json:"type"`
	ProviderURL string `json:"provider_url"`
}

// parseAnswer is separate from the call so the fuzz target can reach it
// without a server: hostile JSON must be a typed error, never a panic.
func parseAnswer(id string, body []byte) (Metadata, error) {
	var answer oEmbedAnswer
	if err := json.Unmarshal(body, &answer); err != nil {
		return Metadata{}, fmt.Errorf("youtube: the oEmbed answer is not JSON: %w", err)
	}
	title := strings.TrimSpace(answer.Title)
	if title == "" {
		// An answer without a title is not metadata; it is a page that happened
		// to parse. The endpoint answered, so this is definitive: retrying would
		// ask the same question and get the same nothing.
		return Metadata{}, fmt.Errorf("%w: the oEmbed answer has no title for %s", ErrNoMetadata, id)
	}
	if answer.Type != "" && answer.Type != "video" {
		return Metadata{}, fmt.Errorf("%w: oEmbed answered type %q for %s", ErrNoMetadata, answer.Type, id)
	}
	return Metadata{
		VideoID:   id,
		Title:     title,
		Author:    strings.TrimSpace(answer.AuthorName),
		AuthorURL: strings.TrimSpace(answer.AuthorURL),
		Thumbnail: strings.TrimSpace(answer.Thumbnail),
	}, nil
}
