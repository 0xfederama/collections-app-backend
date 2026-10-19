// Package hardcover is a small, bounded, server-side client for the Hardcover
// GraphQL search API. It is an optional gap-filler for the automatic book lookup
// (Open Library stays primary): it needs a token (HARDCOVER_API_KEY) and is
// disabled without one. Only a book's authors, page count, ISBNs, titles and
// cover are read. The token is never logged and never part of an error.
package hardcover

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	apiURL          = "https://api.hardcover.app/v1/graphql"
	imageHost       = "assets.hardcover.app"
	requestTimeout  = 5 * time.Second // their search limit is 2 s server side
	retryBackoff    = 300 * time.Millisecond
	requestInterval = time.Second // free tier: 60 requests/minute, burst 10
	maxJSONBytes    = 1 << 20
	maxImageBytes   = 10 << 20
	isbnPerPage     = 3
	titlePerPage    = 5
)

// searchQuery asks only for `results`, the Typesense payload with the documents.
const searchQuery = `query ($q: String!, $n: Int!) { search(query: $q, query_type: "Book", per_page: $n, page: 1) { results } }`

var (
	ErrDisabled = errors.New("hardcover lookup is disabled")
	ErrInvalid  = errors.New("invalid Hardcover request")
)

// Options allows tests to point the client at local fake servers. The production
// constructor always uses the fixed Hardcover HTTPS origins.
type Options struct {
	// Token is the API token (a leading "Bearer " and quotes are stripped); blank disables the client.
	Token      string
	HTTPClient *http.Client
	// BaseURL is the GraphQL endpoint; ImageOrigin ("scheme://host") the only origin covers are fetched from.
	BaseURL         string
	ImageOrigin     string
	Timeout         time.Duration
	RequestInterval time.Duration
	// Contact (an e-mail or URL) goes in the User-Agent, like the Open Library client's.
	Contact string
}

// Book is one search document, reduced to what the lookup uses.
type Book struct {
	Title             string
	AlternativeTitles []string
	Authors           []string
	Pages             int
	ISBNs             []string
	ImageURL          string
}

// Client is safe for concurrent use.
type Client struct {
	http        *http.Client
	endpoint    string
	imageOrigin *url.URL
	token       string
	timeout     time.Duration
	interval    time.Duration
	userAgent   string
	mu          sync.Mutex
	nextRequest time.Time
}

// New creates a production client. A blank token yields a disabled client.
func New(token, contact string) *Client {
	return NewWithOptions(Options{Token: token, Contact: contact})
}

// NewWithOptions creates a client with optional endpoint overrides for tests.
func NewWithOptions(opts Options) *Client {
	if opts.Timeout <= 0 {
		opts.Timeout = requestTimeout
	}
	if opts.RequestInterval <= 0 {
		opts.RequestInterval = requestInterval
	}
	if opts.BaseURL == "" {
		opts.BaseURL = apiURL
	}
	img, err := url.Parse(opts.ImageOrigin)
	if opts.ImageOrigin == "" || err != nil || img.Host == "" {
		img = &url.URL{Scheme: "https", Host: imageHost}
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	} else {
		copy := *hc
		hc = &copy
	}
	// Never follow a redirect: a 3xx could lead off the fixed origins (and carry the token).
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{
		http: hc, endpoint: opts.BaseURL, imageOrigin: img, token: cleanToken(opts.Token),
		timeout: opts.Timeout, interval: opts.RequestInterval, userAgent: userAgent(opts.Contact),
	}
}

// cleanToken strips surrounding whitespace and quotes and an accidental
// "Bearer " prefix from the configured value.
func cleanToken(raw string) string {
	t := strings.Trim(strings.TrimSpace(raw), `"'`)
	t = strings.TrimSpace(t)
	if strings.EqualFold(t, "bearer") {
		return ""
	}
	if len(t) >= 7 && strings.EqualFold(t[:7], "bearer ") {
		t = strings.TrimSpace(t[7:])
	}
	return strings.Trim(t, `"'`)
}

// userAgent is "CollectionsApp/1 (<contact>)", mirroring the Open Library client.
func userAgent(contact string) string {
	ua := "CollectionsApp/1"
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

// Enabled reports whether a token is configured.
func (c *Client) Enabled() bool { return c != nil && c.token != "" }

// FindByISBN searches by the bare ISBN digits and returns the first hit whose
// ISBN list contains the searched ISBN (or its ISBN-10/13 counterpart). The
// bool is false when no hit qualifies.
func (c *Client) FindByISBN(ctx context.Context, isbn string) (Book, bool, error) {
	clean := CleanISBN(isbn)
	if clean == "" {
		return Book{}, false, ErrInvalid
	}
	books, err := c.search(ctx, clean, isbnPerPage)
	if err != nil {
		return Book{}, false, err
	}
	want := map[string]bool{clean: true}
	if other := counterpartISBN(clean); other != "" {
		want[other] = true
	}
	for _, b := range books {
		for _, have := range b.ISBNs {
			if want[CleanISBN(have)] {
				return b, true, nil
			}
		}
	}
	return Book{}, false, nil
}

// SearchTitle returns the documents matching a title query, in upstream order.
// The caller decides which, if any, is an acceptable match.
func (c *Client) SearchTitle(ctx context.Context, title string) ([]Book, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, ErrInvalid
	}
	return c.search(ctx, title, titlePerPage)
}

type document struct {
	Title             string          `json:"title"`
	AlternativeTitles []string        `json:"alternative_titles"`
	AuthorNames       []string        `json:"author_names"`
	Pages             float64         `json:"pages"`
	ISBNs             []string        `json:"isbns"`
	Image             json.RawMessage `json:"image"`
}

func (c *Client) search(ctx context.Context, q string, perPage int) ([]Book, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	body, err := json.Marshal(map[string]any{"query": searchQuery, "variables": map[string]any{"q": q, "n": perPage}})
	if err != nil {
		return nil, ErrInvalid
	}
	var resp struct {
		Data struct {
			Search struct {
				Results json.RawMessage `json:"results"`
			} `json:"search"`
		} `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	err = c.withRetry(ctx, func(ctx context.Context) (bool, error) {
		return c.jsonOnce(ctx, body, &resp)
	})
	if err != nil {
		return nil, err
	}
	if len(resp.Errors) > 0 {
		return nil, errors.New("hardcover returned an error")
	}
	results := resp.Data.Search.Results
	// `results` is a JSON scalar: an object, or (tolerated) a JSON-encoded string of one.
	if len(results) > 0 && results[0] == '"' {
		var s string
		if json.Unmarshal(results, &s) != nil {
			return nil, errors.New("hardcover returned an invalid response")
		}
		results = json.RawMessage(s)
	}
	var payload struct {
		Hits []struct {
			Document document `json:"document"`
		} `json:"hits"`
	}
	if len(results) == 0 || string(results) == "null" {
		return nil, nil
	}
	if json.Unmarshal(results, &payload) != nil {
		return nil, errors.New("hardcover returned an invalid response")
	}
	books := make([]Book, 0, len(payload.Hits))
	for _, h := range payload.Hits {
		d := h.Document
		b := Book{Title: strings.TrimSpace(d.Title), ISBNs: d.ISBNs}
		if d.Pages >= 1 && d.Pages < 1e6 {
			b.Pages = int(d.Pages)
		}
		for _, t := range d.AlternativeTitles {
			if t = strings.TrimSpace(t); t != "" {
				b.AlternativeTitles = append(b.AlternativeTitles, t)
			}
		}
		for _, a := range d.AuthorNames {
			if a = strings.TrimSpace(a); a != "" {
				b.Authors = append(b.Authors, a)
			}
		}
		var img struct {
			URL string `json:"url"`
		}
		if len(d.Image) > 0 && json.Unmarshal(d.Image, &img) == nil {
			b.ImageURL = strings.TrimSpace(img.URL)
		}
		books = append(books, b)
	}
	return books, nil
}

func (c *Client) jsonOnce(ctx context.Context, body []byte, out any) (bool, error) {
	if err := c.wait(ctx); err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return false, ErrInvalid
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	response, err := c.http.Do(req)
	if err != nil {
		return true, safeNetworkError(ctx, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode >= 500, fmt.Errorf("hardcover request failed (HTTP %d)", response.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, maxJSONBytes+1))
	if err != nil {
		return true, safeNetworkError(ctx, err)
	}
	if len(b) > maxJSONBytes {
		return false, errors.New("hardcover response exceeds size limit")
	}
	// A decode error (e.g. raw control characters in a document) is "no result".
	if err := json.Unmarshal(b, out); err != nil {
		return false, errors.New("hardcover returned an invalid response")
	}
	return false, nil
}

// DownloadCover downloads a cover from the one allowed image origin
// (https://assets.hardcover.app); any other URL is ErrInvalid. No redirects.
func (c *Client) DownloadCover(ctx context.Context, imageURL string) ([]byte, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	u, err := url.Parse(imageURL)
	if err != nil || u.User != nil || u.Scheme != c.imageOrigin.Scheme || !strings.EqualFold(u.Host, c.imageOrigin.Host) {
		return nil, ErrInvalid
	}
	var out []byte
	err = c.withRetry(ctx, func(ctx context.Context) (bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return false, ErrInvalid
		}
		req.Header.Set("User-Agent", c.userAgent) // no token to the image host
		response, err := c.http.Do(req)
		if err != nil {
			return true, safeNetworkError(ctx, err)
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return response.StatusCode >= 500, fmt.Errorf("hardcover cover request failed (HTTP %d)", response.StatusCode)
		}
		b, err := io.ReadAll(io.LimitReader(response.Body, maxImageBytes+1))
		if err != nil {
			return true, safeNetworkError(ctx, err)
		}
		if len(b) > maxImageBytes {
			return false, errors.New("hardcover cover exceeds size limit")
		}
		out = b
		return false, nil
	})
	return out, err
}

// withRetry runs attempt (each with its own timeout, which includes pacing) and
// retries once after retryBackoff on a transient failure (network error, attempt
// timeout, HTTP 5xx), never after the caller's context is done.
func (c *Client) withRetry(ctx context.Context, attempt func(ctx context.Context) (bool, error)) error {
	for try := 0; ; try++ {
		actx, cancel := context.WithTimeout(ctx, c.timeout)
		transient, err := attempt(actx)
		cancel()
		if err == nil || !transient || try >= 1 || ctx.Err() != nil {
			return err
		}
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= retryBackoff {
			return err
		}
		timer := time.NewTimer(retryBackoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
	}
}

// wait paces requests to one per interval.
func (c *Client) wait(ctx context.Context) error {
	for {
		c.mu.Lock()
		now := time.Now()
		if !c.nextRequest.After(now) {
			c.nextRequest = now.Add(c.interval)
			c.mu.Unlock()
			return nil
		}
		target := c.nextRequest
		c.mu.Unlock()
		timer := time.NewTimer(time.Until(target))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// safeNetworkError drops the transport error (it can carry the URL) so nothing
// but the context error or a fixed message escapes.
func safeNetworkError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("hardcover request failed")
}

// CleanISBN keeps the digits (and a final X) of an ISBN: "978-0-441-17271-9" -> "9780441172719".
func CleanISBN(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == 'x' || r == 'X':
			b.WriteByte('X')
		}
	}
	out := b.String()
	if !validISBN(out) {
		return ""
	}
	return out
}

func validISBN(s string) bool {
	switch len(s) {
	case 13:
		return !strings.Contains(s, "X")
	case 10:
		return !strings.Contains(s[:9], "X")
	}
	return false
}

// counterpartISBN converts a 13-digit 978 ISBN to ISBN-10 and an ISBN-10 to
// ISBN-13; "" when there is none (979 prefix, bad input).
func counterpartISBN(s string) string {
	switch len(s) {
	case 10:
		body := "978" + s[:9]
		sum := 0
		for i, r := range body {
			d := int(r - '0')
			if i%2 == 1 {
				d *= 3
			}
			sum += d
		}
		return body + string(rune('0'+(10-sum%10)%10))
	case 13:
		if !strings.HasPrefix(s, "978") {
			return ""
		}
		body := s[3:12]
		sum := 0
		for i, r := range body {
			sum += (10 - i) * int(r-'0')
		}
		check := (11 - sum%11) % 11
		if check == 10 {
			return body + "X"
		}
		return body + string(rune('0'+check))
	}
	return ""
}

// FirstISBN returns the book's preferred ISBN: the first 13-digit one, else the
// first valid ISBN-10, "" when none.
func (b Book) FirstISBN() string {
	first10 := ""
	for _, raw := range b.ISBNs {
		c := CleanISBN(raw)
		switch {
		case len(c) == 13:
			return c
		case len(c) == 10 && first10 == "":
			first10 = c
		}
	}
	return first10
}
