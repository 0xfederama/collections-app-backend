// Package tmdb is a bounded, server-side client for the TMDB v3 API.
package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	apiOrigin        = "https://api.themoviedb.org/3"
	imageOrigin      = "https://image.tmdb.org/t/p"
	requestTimeout   = 10 * time.Second
	maxJSONBytes     = 2 << 20
	maxPosterBytes   = 10 << 20
	searchTTL        = 5 * time.Minute
	maxSearchEntries = 128
	requestInterval  = 250 * time.Millisecond
	maxRetryAfter    = time.Hour
	mockMovieQuery   = "[mock] movie name"
	mockMovieID      = 900001
	// maxPersonLookups bounds the /person calls one Details makes for non-Latin directors.
	maxPersonLookups = 3
)

var (
	ErrDisabled = errors.New("TMDB lookup is disabled")
	ErrInvalid  = errors.New("invalid TMDB request")
	// ErrRateLimited is returned for an HTTP 429 (the client has also paused itself).
	ErrRateLimited = errors.New("TMDB rate limit exceeded")
	// ErrNotFound is returned for an HTTP 404: the title no longer exists upstream.
	ErrNotFound = errors.New("TMDB resource not found")
)

// Options allows tests to point the client at local fake servers.
type Options struct {
	HTTPClient   *http.Client
	APIBaseURL   string
	ImageBaseURL string
	Timeout      time.Duration
	MockFixture  bool
}

// Match is one upstream-ranked search result.
type Match struct {
	ID            int    `json:"id"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title"`
	Year          int    `json:"year"`
	PosterURL     string `json:"poster_url"`
	Overview      string `json:"overview"`
}

type cacheEntry struct {
	expires time.Time
	results []Match
}

// Client is safe for concurrent use.
type Client struct {
	key          string
	http         *http.Client
	apiBase      *url.URL
	imageBase    *url.URL
	timeout      time.Duration
	mockFixture  bool
	mu           sync.Mutex
	nextRequest  time.Time
	blockedUntil time.Time
	cache        map[string]cacheEntry
}

// New creates a client using TMDB's fixed production HTTPS origins.
func New(apiKey string) *Client { return NewWithOptions(apiKey, Options{}) }

// NewWithOptions creates a client with optional endpoint overrides for tests.
// The production constructor always uses TMDB's fixed origins.
func NewWithOptions(apiKey string, opts Options) *Client {
	if opts.Timeout <= 0 {
		opts.Timeout = requestTimeout
	}
	if opts.APIBaseURL == "" {
		opts.APIBaseURL = apiOrigin
	}
	if opts.ImageBaseURL == "" {
		opts.ImageBaseURL = imageOrigin
	}
	api, apiOK := parseBaseURL(opts.APIBaseURL)
	image, imageOK := parseBaseURL(opts.ImageBaseURL)
	if !apiOK || !imageOK {
		api, _ = url.Parse(apiOrigin)
		image, _ = url.Parse(imageOrigin)
		apiKey = ""
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: opts.Timeout}
	} else {
		copy := *hc
		hc = &copy
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{key: strings.TrimSpace(apiKey), http: hc, apiBase: api, imageBase: image, timeout: opts.Timeout, mockFixture: opts.MockFixture, cache: make(map[string]cacheEntry)}
}

// Enabled reports whether TMDB-backed suggestions are available. The explicit
// test fixture keeps the suggestions endpoint available without a credential.
func (c *Client) Enabled() bool { return c != nil && (c.key != "" || c.mockFixture) }

func (c *Client) getJSON(ctx context.Context, route string, query url.Values, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if err := c.wait(ctx); err != nil {
		return err
	}
	u := *c.apiBase
	u.Path = path.Join(u.Path, route)
	query.Set("api_key", c.key)
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return ErrInvalid
	}
	req.Header.Set("Accept", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return safeNetworkError(ctx, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusTooManyRequests {
		c.note429(response.Header.Get("Retry-After"))
		return ErrRateLimited
	}
	if response.StatusCode == http.StatusNotFound {
		return fmt.Errorf("TMDB request failed (HTTP %d): %w", response.StatusCode, ErrNotFound)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("TMDB request failed (HTTP %d)", response.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, maxJSONBytes+1))
	if err != nil {
		return safeNetworkError(ctx, err)
	}
	if len(b) > maxJSONBytes {
		return errors.New("TMDB response exceeds size limit")
	}
	if err := json.Unmarshal(b, out); err != nil {
		return errors.New("TMDB returned an invalid response")
	}
	return nil
}

func (c *Client) wait(ctx context.Context) error {
	for {
		c.mu.Lock()
		now := time.Now()
		target := c.nextRequest
		if c.blockedUntil.After(target) {
			target = c.blockedUntil
		}
		if !target.After(now) {
			c.nextRequest = now.Add(requestInterval)
			c.mu.Unlock()
			return nil
		}
		c.mu.Unlock()
		timer := time.NewTimer(time.Until(target))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *Client) note429(value string) {
	delay := time.Second
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && seconds > 0 {
		if seconds >= int64(maxRetryAfter/time.Second) {
			delay = maxRetryAfter
		} else {
			delay = time.Duration(seconds) * time.Second
		}
	} else if when, err := http.ParseTime(value); err == nil && time.Until(when) > delay {
		delay = time.Until(when)
	}
	if delay > maxRetryAfter {
		delay = maxRetryAfter
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	until := time.Now().Add(delay)
	if until.After(c.blockedUntil) {
		c.blockedUntil = until
	}
}

func typeEndpoint(typ string) (string, error) {
	switch typ {
	case "movie":
		return "movie", nil
	case "tv_show":
		return "tv", nil
	default:
		return "", ErrInvalid
	}
}

func parseBaseURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || u == nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" {
		return nil, false
	}
	return u, true
}

func safeNetworkError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("TMDB request failed")
}
