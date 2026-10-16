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
)

var (
	ErrDisabled = errors.New("TMDB lookup is disabled")
	ErrInvalid  = errors.New("invalid TMDB request")
)

// Options allows tests to point the client at local fake servers.
type Options struct {
	HTTPClient   *http.Client
	APIBaseURL   string
	ImageBaseURL string
	Timeout      time.Duration
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

// Details contains editable metadata and the poster path returned by TMDB.
type Details struct {
	ID         int            `json:"id"`
	Title      string         `json:"title"`
	Metadata   map[string]any `json:"metadata"`
	PosterPath string         `json:"poster_path"`
	WatchURL   string         `json:"watch_url,omitempty"`
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
	api, _ := url.Parse(strings.TrimRight(opts.APIBaseURL, "/"))
	image, _ := url.Parse(strings.TrimRight(opts.ImageBaseURL, "/"))
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: opts.Timeout}
	} else {
		copy := *hc
		hc = &copy
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{key: strings.TrimSpace(apiKey), http: hc, apiBase: api, imageBase: image, timeout: opts.Timeout, cache: make(map[string]cacheEntry)}
}

// Enabled reports whether a server-side TMDB credential is configured.
func (c *Client) Enabled() bool { return c != nil && c.key != "" }

// Search returns TMDB results in upstream ranking order, localized by language.
func (c *Client) Search(ctx context.Context, kind, query, language string) ([]Match, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	endpoint, err := kindEndpoint(kind)
	if err != nil || strings.TrimSpace(query) == "" {
		return nil, ErrInvalid
	}
	query = strings.TrimSpace(query)
	key := strings.Join([]string{kind, strings.ToLower(query), language}, "\x00")
	if results, ok := c.cached(key); ok {
		return results, nil
	}
	var data struct {
		Results []struct {
			ID            int    `json:"id"`
			Title         string `json:"title"`
			Name          string `json:"name"`
			OriginalTitle string `json:"original_title"`
			OriginalName  string `json:"original_name"`
			ReleaseDate   string `json:"release_date"`
			FirstAirDate  string `json:"first_air_date"`
			PosterPath    string `json:"poster_path"`
			Overview      string `json:"overview"`
		} `json:"results"`
	}
	if err := c.getJSON(ctx, endpoint+"/search/"+map[string]string{"movie": "movie", "tv_show": "tv"}[kind], url.Values{"query": {query}, "language": {language}}, &data); err != nil {
		return nil, err
	}
	results := make([]Match, 0, len(data.Results))
	for _, item := range data.Results {
		title, original, date := item.Title, item.OriginalTitle, item.ReleaseDate
		if kind == "tv_show" {
			title, original, date = item.Name, item.OriginalName, item.FirstAirDate
		}
		if title == "" {
			title = original
		}
		if original == "" {
			original = title
		}
		results = append(results, Match{ID: item.ID, Title: title, OriginalTitle: original, Year: year(date), PosterURL: c.posterURL(item.PosterPath), Overview: item.Overview})
	}
	c.store(key, results)
	return results, nil
}

// Details fetches localized metadata, credits, IMDb id and regional providers.
func (c *Client) Details(ctx context.Context, kind string, id int, language, country string) (Details, error) {
	if !c.Enabled() {
		return Details{}, ErrDisabled
	}
	endpoint, err := kindEndpoint(kind)
	if err != nil || id <= 0 {
		return Details{}, ErrInvalid
	}
	var data struct {
		ID             int    `json:"id"`
		Title          string `json:"title"`
		Name           string `json:"name"`
		ReleaseDate    string `json:"release_date"`
		FirstAirDate   string `json:"first_air_date"`
		LastAirDate    string `json:"last_air_date"`
		Runtime        *int   `json:"runtime"`
		EpisodeRunTime []int  `json:"episode_run_time"`
		Seasons        int    `json:"number_of_seasons"`
		PosterPath     string `json:"poster_path"`
		Credits        struct {
			Crew []struct {
				Job  string `json:"job"`
				Name string `json:"name"`
			} `json:"crew"`
		} `json:"credits"`
		External struct {
			IMDBID string `json:"imdb_id"`
		} `json:"external_ids"`
		Providers struct {
			Results map[string]struct {
				Link     string     `json:"link"`
				Flatrate []provider `json:"flatrate"`
				Free     []provider `json:"free"`
				Ads      []provider `json:"ads"`
			} `json:"results"`
		} `json:"watch/providers"`
	}
	appendTo := "external_ids,watch/providers"
	if kind == "movie" {
		appendTo += ",credits"
	}
	if err := c.getJSON(ctx, endpoint+"/"+strconv.Itoa(id), url.Values{"language": {language}, "append_to_response": {appendTo}}, &data); err != nil {
		return Details{}, err
	}
	title, date := data.Title, data.ReleaseDate
	if kind == "tv_show" {
		title, date = data.Name, data.FirstAirDate
	}
	metadata := make(map[string]any)
	if y := year(date); y != 0 {
		if kind == "movie" {
			metadata["release_year"] = y
		} else {
			metadata["first_air_year"] = y
		}
	}
	if data.External.IMDBID != "" {
		metadata["imdb_url"] = "https://www.imdb.com/title/" + data.External.IMDBID + "/"
	}
	if kind == "movie" {
		if data.Runtime != nil && *data.Runtime > 0 {
			metadata["runtime_minutes"] = *data.Runtime
		}
		directors := make([]string, 0)
		for _, person := range data.Credits.Crew {
			if person.Job == "Director" && person.Name != "" && !contains(directors, person.Name) {
				directors = append(directors, person.Name)
			}
		}
		if len(directors) > 0 {
			metadata["director"] = strings.Join(directors, ", ")
		}
	} else {
		if data.Seasons > 0 {
			metadata["seasons"] = data.Seasons
		}
		if len(data.EpisodeRunTime) > 0 && data.EpisodeRunTime[0] > 0 {
			metadata["episode_minutes"] = data.EpisodeRunTime[0]
		}
		if y := year(data.LastAirDate); y != 0 {
			metadata["end_year"] = y
		}
	}
	if country != "" {
		providers := data.Providers.Results[strings.ToUpper(country)]
		platforms, paid := providerNames(providers.Flatrate, providers.Free, providers.Ads)
		streaming := map[string]any{"country": strings.ToUpper(country)}
		if len(platforms) > 0 {
			streaming["platforms"] = platforms
		}
		if len(paid) > 0 {
			streaming["paid_platforms"] = paid
		}
		metadata["streaming"] = streaming
		return Details{ID: data.ID, Title: title, Metadata: metadata, PosterPath: data.PosterPath, WatchURL: providers.Link}, nil
	}
	return Details{ID: data.ID, Title: title, Metadata: metadata, PosterPath: data.PosterPath}, nil
}

type provider struct {
	Name string `json:"provider_name"`
	ID   int    `json:"provider_id"`
}

// DownloadPoster downloads a TMDB poster path from the configured image origin.
func (c *Client) DownloadPoster(ctx context.Context, posterPath string) ([]byte, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	if !safePosterPath(posterPath) {
		return nil, ErrInvalid
	}
	if err := c.wait(ctx); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	u := *c.imageBase
	u.Path = path.Join(u.Path, "w500", strings.TrimPrefix(posterPath, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, ErrInvalid
	}
	response, err := c.http.Do(req)
	if err != nil {
		return nil, safeNetworkError(ctx, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests {
		c.note429(response.Header.Get("Retry-After"))
		return nil, errors.New("TMDB rate limit exceeded")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("TMDB image request failed (HTTP %d)", response.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, maxPosterBytes+1))
	if err != nil {
		return nil, safeNetworkError(ctx, err)
	}
	if len(b) > maxPosterBytes {
		return nil, errors.New("TMDB image exceeds size limit")
	}
	return b, nil
}

func (c *Client) getJSON(ctx context.Context, route string, query url.Values, out any) error {
	if err := c.wait(ctx); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
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
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests {
		c.note429(response.Header.Get("Retry-After"))
		return errors.New("TMDB rate limit exceeded")
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
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds > 0 {
		delay = time.Duration(seconds) * time.Second
	} else if when, err := http.ParseTime(value); err == nil && time.Until(when) > delay {
		delay = time.Until(when)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	until := time.Now().Add(delay)
	if until.After(c.blockedUntil) {
		c.blockedUntil = until
	}
}

func (c *Client) cached(key string) ([]Match, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.cache[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expires) {
		delete(c.cache, key)
		return nil, false
	}
	return append([]Match(nil), entry.results...), true
}
func (c *Client) store(key string, matches []Match) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, v := range c.cache {
		if now.After(v.expires) {
			delete(c.cache, k)
		}
	}
	if len(c.cache) >= maxSearchEntries {
		for k := range c.cache {
			delete(c.cache, k)
			break
		}
	}
	c.cache[key] = cacheEntry{expires: now.Add(searchTTL), results: append([]Match(nil), matches...)}
}
func (c *Client) posterURL(p string) string {
	if !safePosterPath(p) {
		return ""
	}
	u := *c.imageBase
	u.Path = path.Join(u.Path, "w185", strings.TrimPrefix(p, "/"))
	return u.String()
}
func kindEndpoint(kind string) (string, error) {
	if kind != "movie" && kind != "tv_show" {
		return "", ErrInvalid
	}
	return "", nil
}
func safePosterPath(p string) bool {
	return strings.HasPrefix(p, "/") && len(p) > 1 && !strings.ContainsAny(p, "\\?#") && !strings.Contains(p, "..") && path.Clean(p) == p
}
func year(date string) int {
	if len(date) < 4 {
		return 0
	}
	n, _ := strconv.Atoi(date[:4])
	return n
}
func contains(items []string, item string) bool {
	for _, v := range items {
		if v == item {
			return true
		}
	}
	return false
}
func providerNames(groups ...[]provider) ([]string, []string) {
	all, paid := make([]string, 0), make([]string, 0)
	seen := map[string]bool{}
	paidSeen := map[string]bool{}
	for i, group := range groups {
		for _, p := range group {
			k := strings.ToLower(strings.TrimSpace(p.Name))
			if k == "" {
				continue
			}
			if !seen[k] {
				seen[k] = true
				all = append(all, p.Name)
			}
			if i == 0 && !paidSeen[k] {
				paidSeen[k] = true
				paid = append(paid, p.Name)
			}
		}
	}
	return all, paid
}
func safeNetworkError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("TMDB request failed")
}
