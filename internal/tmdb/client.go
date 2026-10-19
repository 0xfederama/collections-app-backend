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

// Search returns TMDB results in upstream ranking order, localized by language.
func (c *Client) Search(ctx context.Context, typ, query, language string) ([]Match, error) {
	endpoint, err := typeEndpoint(typ)
	if err != nil || strings.TrimSpace(query) == "" {
		return nil, ErrInvalid
	}
	if c != nil && c.mockFixture && typ == "movie" && query == mockMovieQuery {
		return []Match{{ID: mockMovieID, Title: mockMovieQuery, OriginalTitle: mockMovieQuery, Year: 2001, Overview: "Deterministic TMDB fixture for frontend end-to-end tests."}}, nil
	}
	if c == nil || c.key == "" {
		return nil, ErrDisabled
	}
	query = strings.TrimSpace(query)
	key := strings.Join([]string{typ, strings.ToLower(query), language}, "\x00")
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
	if err := c.getJSON(ctx, "/search/"+endpoint, url.Values{"query": {query}, "language": {language}}, &data); err != nil {
		return nil, err
	}
	results := make([]Match, 0, len(data.Results))
	for _, item := range data.Results {
		title, original, date := item.Title, item.OriginalTitle, item.ReleaseDate
		if typ == "tv_show" {
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
func (c *Client) Details(ctx context.Context, typ string, id int, language, country string) (Details, error) {
	if c != nil && c.mockFixture && typ == "movie" && id == mockMovieID {
		metadata := map[string]any{
			"release_year":    2001,
			"runtime_minutes": 101,
			"director":        "Mock Director",
		}
		if country != "" {
			// A deterministic JustWatch page so frontend e2e can follow the link.
			metadata["streaming"] = map[string]any{
				"country": strings.ToUpper(country),
				"link":    "https://www.justwatch.com/" + strings.ToLower(country) + "/movie/mock",
			}
		}
		return Details{ID: mockMovieID, Title: mockMovieQuery, Metadata: metadata}, nil
	}
	if c == nil || c.key == "" {
		return Details{}, ErrDisabled
	}
	endpoint, err := typeEndpoint(typ)
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
		Status         string `json:"status"`
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
				Rent     []provider `json:"rent"`
				Buy      []provider `json:"buy"`
				Free     []provider `json:"free"`
				Ads      []provider `json:"ads"`
			} `json:"results"`
		} `json:"watch/providers"`
	}
	appendTo := "external_ids,watch/providers"
	if typ == "movie" {
		appendTo += ",credits"
	}
	if err := c.getJSON(ctx, "/"+endpoint+"/"+strconv.Itoa(id), url.Values{"language": {language}, "append_to_response": {appendTo}}, &data); err != nil {
		return Details{}, err
	}
	if data.ID <= 0 || data.ID != id {
		return Details{}, errors.New("TMDB returned invalid details")
	}
	title, date := data.Title, data.ReleaseDate
	if typ == "tv_show" {
		title, date = data.Name, data.FirstAirDate
	}
	metadata := make(map[string]any)
	if y := year(date); y != 0 {
		if typ == "movie" {
			metadata["release_year"] = y
		} else {
			metadata["first_air_year"] = y
		}
	}
	if data.External.IMDBID != "" {
		metadata["imdb_url"] = "https://www.imdb.com/title/" + data.External.IMDBID + "/"
	}
	if typ == "movie" {
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
		if data.Status == "Ended" || data.Status == "Canceled" {
			if y := year(data.LastAirDate); y != 0 {
				metadata["end_year"] = y
			}
		}
	}
	if country != "" {
		providers := data.Providers.Results[strings.ToUpper(country)]
		platforms, paid := providerNames(providers.Flatrate, providers.Rent, providers.Buy, providers.Free, providers.Ads)
		streaming := map[string]any{"country": strings.ToUpper(country)}
		if len(platforms) > 0 {
			streaming["platforms"] = platforms
		}
		if len(paid) > 0 {
			streaming["paid_platforms"] = paid
		}
		if link := justWatchLink(providers.Link); link != "" {
			streaming["link"] = link
		}
		metadata["streaming"] = streaming
		return Details{ID: data.ID, Title: title, Metadata: metadata, PosterPath: data.PosterPath, WatchURL: providers.Link}, nil
	}
	return Details{ID: data.ID, Title: title, Metadata: metadata, PosterPath: data.PosterPath}, nil
}

// WatchLink fetches only the JustWatch page of one title for a country, with the
// cheapest call that carries it (`/{movie|tv}/{id}/watch/providers`, one request).
// It returns "" with a nil error when TMDB lists no (valid) link for the country.
// An unknown title is ErrNotFound, a 429 is ErrRateLimited; both share the pacing
// and timeout rules of every other call.
func (c *Client) WatchLink(ctx context.Context, typ string, id int, country string) (string, error) {
	if c != nil && c.mockFixture && typ == "movie" && id == mockMovieID {
		if country == "" {
			return "", nil
		}
		return "https://www.justwatch.com/" + strings.ToLower(country) + "/movie/mock", nil
	}
	if c == nil || c.key == "" {
		return "", ErrDisabled
	}
	endpoint, err := typeEndpoint(typ)
	country = strings.ToUpper(strings.TrimSpace(country))
	if err != nil || id <= 0 || country == "" {
		return "", ErrInvalid
	}
	var data struct {
		ID      int `json:"id"`
		Results map[string]struct {
			Link string `json:"link"`
		} `json:"results"`
	}
	if err := c.getJSON(ctx, "/"+endpoint+"/"+strconv.Itoa(id)+"/watch/providers", url.Values{}, &data); err != nil {
		return "", err
	}
	if data.ID != id {
		return "", errors.New("TMDB returned invalid watch providers")
	}
	return justWatchLink(data.Results[country].Link), nil
}

// justWatchLink returns raw when it is an https URL on justwatch.com (the one
// page TMDB links watch providers to), otherwise "": nothing else is stored.
func justWatchLink(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || (u.Host != "www.justwatch.com" && u.Host != "justwatch.com") {
		return ""
	}
	return raw
}

type provider struct {
	Name string `json:"provider_name"`
	ID   int    `json:"provider_id"`
}

// DownloadPoster downloads a TMDB poster path from the configured image origin.
func (c *Client) DownloadPoster(ctx context.Context, posterPath string) ([]byte, error) {
	if c == nil || c.key == "" {
		return nil, ErrDisabled
	}
	if !safePosterPath(posterPath) {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if err := c.wait(ctx); err != nil {
		return nil, err
	}
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
	defer func() { _ = response.Body.Close() }()
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

// providerNames maps TMDB watch-provider availability into the UI's platform
// lists. A subscription, free, or ad-supported offer wins over rent/buy: a
// platform is paid only when its availability is per-title payment only.
func providerNames(flatrate, rent, buy, free, ads []provider) ([]string, []string) {
	all, paid := make([]string, 0), make([]string, 0)
	seen := map[string]bool{}
	paidSeen := map[string]bool{}
	nonPaid := map[string]bool{}
	for _, group := range [][]provider{flatrate, free, ads} {
		for _, p := range group {
			k := strings.ToLower(strings.TrimSpace(p.Name))
			if k == "" {
				continue
			}
			nonPaid[k] = true
		}
	}
	for _, group := range [][]provider{flatrate, rent, buy, free, ads} {
		for _, p := range group {
			k := strings.ToLower(strings.TrimSpace(p.Name))
			if k == "" {
				continue
			}
			if !seen[k] {
				seen[k] = true
				all = append(all, p.Name)
			}
		}
	}
	for _, group := range [][]provider{rent, buy} {
		for _, p := range group {
			k := strings.ToLower(strings.TrimSpace(p.Name))
			if k == "" || nonPaid[k] || paidSeen[k] {
				continue
			}
			if !paidSeen[k] {
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
