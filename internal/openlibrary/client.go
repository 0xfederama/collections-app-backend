// Package openlibrary is a bounded, server-side client for the Open Library search
// and covers APIs (F-016, D-095). It needs no key; the app identifies itself with a
// User-Agent carrying an optional contact so it is rate limited as an identified
// client (3 requests/second, which this client stays well below).
package openlibrary

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	apiOrigin        = "https://openlibrary.org"
	coverOrigin      = "https://covers.openlibrary.org"
	requestTimeout   = 10 * time.Second
	maxJSONBytes     = 2 << 20
	maxCoverBytes    = 10 << 20
	searchTTL        = 5 * time.Minute
	maxSearchEntries = 128
	searchLimit      = 10
	requestInterval  = 1100 * time.Millisecond // above the 1 request/second limit of unidentified clients
	maxRetryAfter    = time.Hour
	searchFields     = "key,title,author_name,first_publish_year,number_of_pages_median,isbn,cover_i"

	mockBookQuery = "[mock] book name"
	mockWorkID    = "OL900001W"
	mockCoverID   = 900001
)

var (
	ErrDisabled = errors.New("open library lookup is disabled")
	ErrInvalid  = errors.New("invalid Open Library request")
	// ErrNoCover means the work has no cover (the covers API answers 404 for default=false).
	ErrNoCover = errors.New("open library has no cover")

	workIDRe  = regexp.MustCompile(`^OL[0-9]+W$`)
	workKeyRe = regexp.MustCompile(`^/works/(OL[0-9]+W)$`)
	isbnRe    = regexp.MustCompile(`^([0-9]{9}[0-9Xx]|[0-9]{13})$`)
	langRe    = regexp.MustCompile(`^[a-z]{2}$`)
)

// Options allows tests to point the client at local fake servers. The production
// constructor always uses the fixed Open Library HTTPS origins.
type Options struct {
	HTTPClient      *http.Client
	APIBaseURL      string
	CoverBaseURL    string
	Timeout         time.Duration
	RequestInterval time.Duration
	// Contact (an e-mail or URL) and Version go in the User-Agent.
	Contact string
	Version string
	// Disabled turns the client off (tests only): no request is ever made.
	Disabled bool
	// MockFixture serves the deterministic test-stack book for the exact query
	// "[mock] book name" without any HTTP request (D-090, D-096). It works even
	// when Disabled, so a test stack can be network-free yet exercise the real path.
	MockFixture bool
}

// Match is one Open Library work as returned by the search API.
type Match struct {
	WorkID  string
	Title   string
	Authors []string
	Year    int
	Pages   int
	// ISBN is the first 13-digit ISBN of the work's editions, else the first valid one.
	ISBN    string
	CoverID int
}

type cacheEntry struct {
	expires time.Time
	results []Match
}

// Client is safe for concurrent use.
type Client struct {
	http         *http.Client
	apiBase      *url.URL
	coverBase    *url.URL
	timeout      time.Duration
	interval     time.Duration
	userAgent    string
	disabled     bool
	mockFixture  bool
	mu           sync.Mutex
	nextRequest  time.Time
	blockedUntil time.Time
	cache        map[string]cacheEntry
}

// NewWithOptions creates a client with optional endpoint overrides for tests.
func NewWithOptions(opts Options) *Client {
	if opts.Timeout <= 0 {
		opts.Timeout = requestTimeout
	}
	if opts.RequestInterval <= 0 {
		opts.RequestInterval = requestInterval
	}
	if opts.APIBaseURL == "" {
		opts.APIBaseURL = apiOrigin
	}
	if opts.CoverBaseURL == "" {
		opts.CoverBaseURL = coverOrigin
	}
	api, apiOK := parseBaseURL(opts.APIBaseURL)
	cover, coverOK := parseBaseURL(opts.CoverBaseURL)
	if !apiOK || !coverOK {
		api, _ = url.Parse(apiOrigin)
		cover, _ = url.Parse(coverOrigin)
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: opts.Timeout}
	} else {
		copy := *hc
		hc = &copy
	}
	// Never follow a redirect: a 3xx could lead off the fixed origins.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{
		http: hc, apiBase: api, coverBase: cover, timeout: opts.Timeout, interval: opts.RequestInterval,
		userAgent: userAgent(opts.Version, opts.Contact), disabled: opts.Disabled, mockFixture: opts.MockFixture,
		cache: make(map[string]cacheEntry),
	}
}

// userAgent is "CollectionsApp/<version> (<contact>)"; control characters are
// dropped so a contact can never inject a header.
func userAgent(version, contact string) string {
	version = strings.Map(headerSafe, strings.TrimSpace(version))
	if version == "" {
		version = "1"
	}
	ua := "CollectionsApp/" + version
	if contact = strings.TrimSpace(strings.Map(headerSafe, contact)); contact != "" {
		ua += " (" + contact + ")"
	}
	return ua
}

func headerSafe(r rune) rune {
	if r < 0x20 || r == 0x7f || r == '(' || r == ')' {
		return -1
	}
	return r
}

// Enabled reports whether book lookup is on (it is by default and needs no key).
// The explicit test fixture keeps it available when the client is disabled, like
// the TMDB fixture without a key.
func (c *Client) Enabled() bool { return c != nil && (!c.disabled || c.mockFixture) }

// Search returns Open Library works in upstream ranking order. language is a
// metadata language such as it-IT; only its two-letter part is sent. A bare ISBN
// (10 or 13 digits, hyphens allowed) is searched as isbn:<digits>.
func (c *Client) Search(ctx context.Context, query, language string) ([]Match, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrInvalid
	}
	if c.Enabled() && c.mockFixture && query == mockBookQuery {
		return []Match{mockMatch()}, nil
	}
	if c == nil || c.disabled {
		return nil, ErrDisabled
	}
	q := searchQuery(query)
	lang := langOf(language)
	key := strings.Join([]string{strings.ToLower(q), lang}, "\x00")
	if results, ok := c.cached(key); ok {
		return results, nil
	}
	results, err := c.search(ctx, q, lang, searchLimit)
	if err != nil {
		return nil, err
	}
	c.store(key, results)
	return results, nil
}

// Work fetches one work by its id (OL…W) through the search API, so it returns
// exactly the fields Search does, in the same language preference.
func (c *Client) Work(ctx context.Context, workID, language string) (Match, error) {
	if !workIDRe.MatchString(workID) {
		return Match{}, ErrInvalid
	}
	if c.Enabled() && c.mockFixture && workID == mockWorkID {
		return mockMatch(), nil
	}
	if c == nil || c.disabled {
		return Match{}, ErrDisabled
	}
	results, err := c.search(ctx, "key:/works/"+workID, langOf(language), 1)
	if err != nil {
		return Match{}, err
	}
	for _, m := range results {
		if m.WorkID == workID {
			return m, nil
		}
	}
	return Match{}, errors.New("open library returned no such work")
}

func (c *Client) search(ctx context.Context, q, lang string, limit int) ([]Match, error) {
	params := url.Values{"q": {q}, "limit": {strconv.Itoa(limit)}, "fields": {searchFields}}
	if lang != "" {
		params.Set("lang", lang)
	}
	var data struct {
		Docs []struct {
			Key              string   `json:"key"`
			Title            string   `json:"title"`
			AuthorName       []string `json:"author_name"`
			FirstPublishYear int      `json:"first_publish_year"`
			Pages            int      `json:"number_of_pages_median"`
			ISBN             []string `json:"isbn"`
			CoverID          int      `json:"cover_i"`
		} `json:"docs"`
	}
	if err := c.getJSON(ctx, "/search.json", params, &data); err != nil {
		return nil, err
	}
	results := make([]Match, 0, len(data.Docs))
	for _, d := range data.Docs {
		m := workKeyRe.FindStringSubmatch(d.Key)
		title := strings.TrimSpace(d.Title)
		if m == nil || title == "" {
			continue
		}
		match := Match{WorkID: m[1], Title: title, Year: positive(d.FirstPublishYear), Pages: positive(d.Pages), ISBN: pickISBN(d.ISBN), CoverID: positive(d.CoverID)}
		for _, a := range d.AuthorName {
			if a = strings.TrimSpace(a); a != "" {
				match.Authors = append(match.Authors, a)
			}
		}
		results = append(results, match)
	}
	return results, nil
}

// CoverURL is the display URL of a work's cover for suggestions ("" without one).
func (c *Client) CoverURL(coverID int) string {
	if coverID <= 0 {
		return ""
	}
	u := *c.coverBase
	u.Path = path.Join(u.Path, "b", "id", strconv.Itoa(coverID)+"-M.jpg")
	return u.String()
}

// DownloadCover downloads the large cover of a cover id. A missing cover is
// ErrNoCover (default=false makes the covers API answer 404 instead of a blank
// image).
func (c *Client) DownloadCover(ctx context.Context, coverID int) ([]byte, error) {
	if coverID <= 0 {
		return nil, ErrInvalid
	}
	if c.Enabled() && c.mockFixture && coverID == mockCoverID {
		return mockCover(), nil
	}
	if c == nil || c.disabled {
		return nil, ErrDisabled
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if err := c.wait(ctx); err != nil {
		return nil, err
	}
	u := *c.coverBase
	u.Path = path.Join(u.Path, "b", "id", strconv.Itoa(coverID)+"-L.jpg")
	u.RawQuery = url.Values{"default": {"false"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, ErrInvalid
	}
	req.Header.Set("User-Agent", c.userAgent)
	response, err := c.http.Do(req)
	if err != nil {
		return nil, safeNetworkError(ctx, err)
	}
	defer func() { _ = response.Body.Close() }()
	switch {
	case response.StatusCode == http.StatusNotFound:
		return nil, ErrNoCover
	case response.StatusCode == http.StatusTooManyRequests:
		c.note429(response.Header.Get("Retry-After"))
		return nil, errors.New("open library rate limit exceeded")
	case response.StatusCode < 200 || response.StatusCode >= 300:
		return nil, fmt.Errorf("open library cover request failed (HTTP %d)", response.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, maxCoverBytes+1))
	if err != nil {
		return nil, safeNetworkError(ctx, err)
	}
	if len(b) > maxCoverBytes {
		return nil, errors.New("open library cover exceeds size limit")
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
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return ErrInvalid
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	response, err := c.http.Do(req)
	if err != nil {
		return safeNetworkError(ctx, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusTooManyRequests {
		c.note429(response.Header.Get("Retry-After"))
		return errors.New("open library rate limit exceeded")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("open library request failed (HTTP %d)", response.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, maxJSONBytes+1))
	if err != nil {
		return safeNetworkError(ctx, err)
	}
	if len(b) > maxJSONBytes {
		return errors.New("open library response exceeds size limit")
	}
	if err := json.Unmarshal(b, out); err != nil {
		return errors.New("open library returned an invalid response")
	}
	return nil
}

// wait paces requests (one per interval) and honours a Retry-After block.
func (c *Client) wait(ctx context.Context) error {
	for {
		c.mu.Lock()
		now := time.Now()
		target := c.nextRequest
		if c.blockedUntil.After(target) {
			target = c.blockedUntil
		}
		if !target.After(now) {
			c.nextRequest = now.Add(c.interval)
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
	if until := time.Now().Add(delay); until.After(c.blockedUntil) {
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

// searchQuery turns a bare ISBN into isbn:<digits>; any other text is searched as typed.
func searchQuery(query string) string {
	if digits := strings.NewReplacer("-", "", " ", "").Replace(query); isbnRe.MatchString(digits) &&
		strings.Trim(query, "0123456789Xx- ") == "" {
		return "isbn:" + strings.ToUpper(digits)
	}
	return query
}

// langOf is the two-letter language of a metadata language (it-IT -> it), "" if none.
func langOf(language string) string {
	lang := strings.ToLower(strings.TrimSpace(language))
	if i := strings.IndexAny(lang, "-_"); i >= 0 {
		lang = lang[:i]
	}
	if !langRe.MatchString(lang) {
		return ""
	}
	return lang
}

// pickISBN returns the first 13-digit ISBN, else the first well-formed one.
func pickISBN(all []string) string {
	first := ""
	for _, v := range all {
		v = strings.ToUpper(strings.TrimSpace(v))
		if !isbnRe.MatchString(v) {
			continue
		}
		if len(v) == 13 {
			return v
		}
		if first == "" {
			first = v
		}
	}
	return first
}

func positive(n int) int {
	if n < 0 {
		return 0
	}
	return n
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
	return errors.New("open library request failed")
}

func mockMatch() Match {
	return Match{WorkID: mockWorkID, Title: "Mock Book Title", Authors: []string{"Mock Author"}, Year: 2002, Pages: 321, ISBN: "9780000000002", CoverID: mockCoverID}
}

// mockCover is a tiny valid PNG generated in code for the test-stack fixture.
func mockCover() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 8, 12))
	for y := 0; y < 12; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: 40, G: 90, B: uint8(120 + x*10), A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
