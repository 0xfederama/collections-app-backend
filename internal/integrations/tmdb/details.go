package tmdb

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// Details contains editable metadata and the poster path returned by TMDB.
type Details struct {
	ID         int            `json:"id"`
	Title      string         `json:"title"`
	Metadata   map[string]any `json:"metadata"`
	PosterPath string         `json:"poster_path"`
	WatchURL   string         `json:"watch_url,omitempty"`
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
		OriginalTitle  string `json:"original_title"`
		OriginalName   string `json:"original_name"`
		ReleaseDate    string `json:"release_date"`
		FirstAirDate   string `json:"first_air_date"`
		LastAirDate    string `json:"last_air_date"`
		Status         string `json:"status"`
		Runtime        *int   `json:"runtime"`
		EpisodeRunTime []int  `json:"episode_run_time"`
		Seasons        int    `json:"number_of_seasons"`
		PosterPath     string `json:"poster_path"`
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
		directors := c.directorNames(ctx, data.Credits.Crew)
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
