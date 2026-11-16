package tmdb

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// maxDirectorRunes is the movie `director` text limit (items/types defaultTextMax).
const maxDirectorRunes = 100

// F-028 (D-162): the score is imported only from this many votes; below it the average is noise.
const minRatingVotes = 10

// Limits of the genres written to an item (items/types text_list: 1 to 10 names of 1 to 100 characters).
const (
	maxGenres     = 10
	maxGenreRunes = 100
)

// The test-stack movie's summary, score, vote count and genres (TMDB_MOCK_FIXTURE).
const (
	mockOverview = "A mock synopsis for the test stack, so the item page has a description to show."
	mockRating   = 7.4
	mockVotes    = 1200
)

// joinNames joins names with ", " keeping whole names in order and stopping
// before the result exceeds max runes. A first name that is too long is cut on
// a rune boundary.
func joinNames(names []string, max int) string {
	var b strings.Builder
	n := 0
	for i, name := range names {
		add := utf8.RuneCountInString(name)
		if i > 0 {
			add += 2
		}
		if n+add > max {
			if i == 0 {
				return string([]rune(name)[:max])
			}
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(name)
		n += add
	}
	return b.String()
}

// Details contains editable metadata and the poster path returned by TMDB. Overview is the
// summary in the requested language, or in English when that one is empty (F-028).
type Details struct {
	ID         int            `json:"id"`
	Title      string         `json:"title"`
	Metadata   map[string]any `json:"metadata"`
	PosterPath string         `json:"poster_path"`
	WatchURL   string         `json:"watch_url,omitempty"`
	Overview   string         `json:"overview,omitempty"`
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
		metadata["tmdb_vote_count"] = mockVotes
		metadata["tmdb_rating"] = mockRating
		metadata["genres"] = []string{"Drama", "Science Fiction"}
		return Details{ID: mockMovieID, Title: mockMovieQuery, Metadata: metadata, Overview: mockOverview}, nil
	}
	if c == nil || c.key == "" {
		return Details{}, ErrDisabled
	}
	endpoint, err := typeEndpoint(typ)
	if err != nil || id <= 0 {
		return Details{}, ErrInvalid
	}
	var data struct {
		ID             int     `json:"id"`
		Title          string  `json:"title"`
		Name           string  `json:"name"`
		Overview       string  `json:"overview"`
		VoteAverage    float64 `json:"vote_average"`
		VoteCount      int     `json:"vote_count"`
		Genres         []genre `json:"genres"`
		OriginalTitle  string  `json:"original_title"`
		OriginalName   string  `json:"original_name"`
		ReleaseDate    string  `json:"release_date"`
		FirstAirDate   string  `json:"first_air_date"`
		LastAirDate    string  `json:"last_air_date"`
		Status         string  `json:"status"`
		Runtime        *int    `json:"runtime"`
		EpisodeRunTime []int   `json:"episode_run_time"`
		Seasons        int     `json:"number_of_seasons"`
		PosterPath     string  `json:"poster_path"`
		Credits        struct {
			Crew []crewMember `json:"crew"`
		} `json:"credits"`
		Translations struct {
			Items []translation `json:"translations"`
		} `json:"translations"`
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
	appendTo := "external_ids,watch/providers,translations"
	if typ == "movie" {
		appendTo += ",credits"
	}
	if err := c.getJSON(ctx, "/"+endpoint+"/"+strconv.Itoa(id), url.Values{"language": {language}, "append_to_response": {appendTo}}, &data); err != nil {
		return Details{}, err
	}
	if data.ID <= 0 || data.ID != id {
		return Details{}, errors.New("TMDB returned invalid details")
	}
	localized, original, date := data.Title, data.OriginalTitle, data.ReleaseDate
	if typ == "tv_show" {
		localized, original, date = data.Name, data.OriginalName, data.FirstAirDate
	}
	title := chooseTitle(localized, englishTitles(typ, data.Translations.Items), original)
	overview := strings.TrimSpace(data.Overview)
	if overview == "" && !isEnglish(language) {
		overview = c.englishOverview(ctx, endpoint, id)
	}
	metadata := make(map[string]any)
	// The vote count comes with every import; the score only from minRatingVotes votes (D-162).
	metadata["tmdb_vote_count"] = data.VoteCount
	if data.VoteCount >= minRatingVotes {
		metadata["tmdb_rating"] = math.Round(data.VoteAverage*10) / 10
	}
	if genres := genreNames(data.Genres); len(genres) > 0 {
		metadata["genres"] = genres
	}
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
		directors := c.directorNames(ctx, data.Credits.Crew)
		if len(directors) > 0 {
			metadata["director"] = joinNames(directors, maxDirectorRunes)
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
		return Details{ID: data.ID, Title: title, Metadata: metadata, PosterPath: data.PosterPath, WatchURL: providers.Link, Overview: overview}, nil
	}
	return Details{ID: data.ID, Title: title, Metadata: metadata, PosterPath: data.PosterPath, Overview: overview}, nil
}

// isEnglish reports whether a metadata language is English ("en", "en-US", ...).
func isEnglish(language string) bool {
	primary, _, _ := strings.Cut(language, "-")
	return strings.EqualFold(primary, "en")
}

// englishOverview is the one extra call of an empty overview in a non-English language (F-028,
// D-162): the en-US details, read for the overview only. Best effort, like the person lookups: a
// failure keeps the overview empty and the title is still imported.
func (c *Client) englishOverview(ctx context.Context, endpoint string, id int) string {
	var data struct {
		Overview string `json:"overview"`
	}
	if err := c.getJSON(ctx, "/"+endpoint+"/"+strconv.Itoa(id), url.Values{"language": {"en-US"}}, &data); err != nil {
		slog.WarnContext(ctx, "TMDB English overview lookup failed; importing without a summary", "endpoint", endpoint, "err", err)
		return ""
	}
	return strings.TrimSpace(data.Overview)
}

// genre is one entry of TMDB's genres array.
type genre struct {
	Name string `json:"name"`
}

// genreNames returns the genre names, trimmed, without empty ones, at most maxGenres in TMDB's order.
// A name longer than maxGenreRunes is cut.
func genreNames(genres []genre) []string {
	out := make([]string, 0, len(genres))
	for _, g := range genres {
		name := strings.TrimSpace(g.Name)
		if name == "" {
			continue
		}
		if utf8.RuneCountInString(name) > maxGenreRunes {
			name = strings.TrimSpace(string([]rune(name)[:maxGenreRunes]))
		}
		out = append(out, name)
		if len(out) == maxGenres {
			break
		}
	}
	return out
}

type crewMember struct {
	ID   int    `json:"id"`
	Job  string `json:"job"`
	Name string `json:"name"`
}

type provider struct {
	Name string `json:"provider_name"`
	ID   int    `json:"provider_id"`
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

func contains(items []string, item string) bool {
	for _, v := range items {
		if v == item {
			return true
		}
	}
	return false
}
