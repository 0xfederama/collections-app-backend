package tmdb

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"
)

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
	var data searchResponse
	if err := c.getJSON(ctx, "/search/"+endpoint, url.Values{"query": {query}, "language": {language}}, &data); err != nil {
		return nil, err
	}
	// Search has no translations, so a non-Latin localized title costs one extra
	// en-US search (same query, matched by id), and only when one is needed.
	var english map[int]string
	if !strings.EqualFold(language, "en-US") && anyNonLatin(typ, data.Results) {
		if english, err = c.englishSearchTitles(ctx, typ, endpoint, query); err != nil {
			return nil, err
		}
	}
	results := make([]Match, 0, len(data.Results))
	for _, item := range data.Results {
		localized, original, date := searchNames(typ, item)
		var titles []string
		if en, ok := english[item.ID]; ok {
			titles = []string{en}
		}
		title := chooseTitle(localized, titles, original)
		if original == "" {
			original = title
		}
		results = append(results, Match{ID: item.ID, Title: title, OriginalTitle: original, Year: year(date), PosterURL: c.posterURL(item.PosterPath), Overview: item.Overview})
	}
	c.store(key, results)
	return results, nil
}

type searchItem struct {
	ID            int    `json:"id"`
	Title         string `json:"title"`
	Name          string `json:"name"`
	OriginalTitle string `json:"original_title"`
	OriginalName  string `json:"original_name"`
	ReleaseDate   string `json:"release_date"`
	FirstAirDate  string `json:"first_air_date"`
	PosterPath    string `json:"poster_path"`
	Overview      string `json:"overview"`
}

type searchResponse struct {
	Results []searchItem `json:"results"`
}

// searchNames returns the localized title, the original title and the release
// date of one result, read from the movie or the TV field names.
func searchNames(typ string, item searchItem) (string, string, string) {
	if typ == "tv_show" {
		return item.Name, item.OriginalName, item.FirstAirDate
	}
	return item.Title, item.OriginalTitle, item.ReleaseDate
}

func anyNonLatin(typ string, items []searchItem) bool {
	for _, item := range items {
		if localized, _, _ := searchNames(typ, item); !isLatin(localized) {
			return true
		}
	}
	return false
}

// englishSearchTitles maps result ids to their en-US title from one search call.
// TMDB answers an untranslated title with its original, which the chooser skips
// when it is not Latin.
func (c *Client) englishSearchTitles(ctx context.Context, typ, endpoint, query string) (map[int]string, error) {
	var data searchResponse
	if err := c.getJSON(ctx, "/search/"+endpoint, url.Values{"query": {query}, "language": {"en-US"}}, &data); err != nil {
		return nil, err
	}
	titles := make(map[int]string, len(data.Results))
	for _, item := range data.Results {
		if localized, _, _ := searchNames(typ, item); localized != "" {
			titles[item.ID] = localized
		}
	}
	return titles, nil
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

func year(date string) int {
	if len(date) < 4 {
		return 0
	}
	n, _ := strconv.Atoi(date[:4])
	return n
}
