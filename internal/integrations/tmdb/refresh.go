package tmdb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

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
		return nil, ErrRateLimited
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

func (c *Client) posterURL(p string) string {
	if !safePosterPath(p) {
		return ""
	}
	u := *c.imageBase
	u.Path = path.Join(u.Path, "w185", strings.TrimPrefix(p, "/"))
	return u.String()
}

func safePosterPath(p string) bool {
	return strings.HasPrefix(p, "/") && len(p) > 1 && !strings.ContainsAny(p, "\\?#") && !strings.Contains(p, "..") && path.Clean(p) == p
}
