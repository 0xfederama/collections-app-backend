// Package streamlinks builds the read-only streaming.platform_links of an item: for
// a streaming service name (as TMDB spells it), the https URL that opens the
// service's search for the item's title. Links are computed at read time and never
// stored. The table is fixed; a service not in it has no link (the web falls back to
// streaming.link). The URL patterns are documented in docs/modules/streamlinks.md.
package streamlinks

import (
	"net/url"
	"strings"
	"unicode"
)

// service is one search URL pattern; the escaped title is appended to prefix.
type service struct{ prefix string }

var (
	netflix    = service{"https://www.netflix.com/search?q="}
	primeVideo = service{"https://www.primevideo.com/search/?phrase="}
	disney     = service{"https://www.disneyplus.com/search?q="}
	appleTV    = service{"https://tv.apple.com/search?term="}
	max        = service{"https://play.max.com/search?q="}
	paramount  = service{"https://www.paramountplus.com/search/?q="}
	hulu       = service{"https://www.hulu.com/search?q="}
	peacock    = service{"https://www.peacocktv.com/search?q="}
	youtube    = service{"https://www.youtube.com/results?search_query="}
	crunchy    = service{"https://www.crunchyroll.com/search?q="}
	mubi       = service{"https://mubi.com/search/films?query="}
)

// exact maps a normalised platform name to its service.
var exact = map[string]service{
	"netflix":          netflix,
	"amazonprimevideo": primeVideo,
	"primevideo":       primeVideo,
	"disneyplus":       disney,
	"appletv":          appleTV,
	"appletvplus":      appleTV,
	"max":              max,
	"hbomax":           max,
	"paramountplus":    paramount,
	"hulu":             hulu,
	"peacock":          peacock,
	"peacockpremium":   peacock,
	"youtube":          youtube,
	"crunchyroll":      crunchy,
	"mubi":             mubi,
}

// prefixes tolerate TMDB's plan variants ("Netflix basic with Ads", "Amazon Prime
// Video with Ads"). Tried in order after the exact match.
var prefixes = []struct {
	prefix string
	svc    service
}{
	{"netflix", netflix},
	{"amazonprimevideo", primeVideo},
	{"primevideo", primeVideo},
	{"disneyplus", disney},
}

// Normalize lowercases a platform name, spells "+" as "plus" and drops every other
// character that is not a letter or digit: "Disney+" and "Disney Plus" both give
// "disneyplus".
func Normalize(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r == '+':
			b.WriteString("plus")
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

func lookup(platform string) (service, bool) {
	n := Normalize(platform)
	if s, ok := exact[n]; ok {
		return s, true
	}
	for _, p := range prefixes {
		if strings.HasPrefix(n, p.prefix) {
			return p.svc, true
		}
	}
	return service{}, false
}

// Link returns the search URL of a platform for a title, or "" when the platform is
// not in the table or the trimmed title is empty. country is the item's
// streaming.country; every host in the table is global today, so it is unused.
func Link(platform, title, country string) string {
	_ = country
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	s, ok := lookup(platform)
	if !ok {
		return ""
	}
	return s.prefix + url.QueryEscape(title)
}

// Links maps each given platform name (exactly as stored) to its search URL, leaving
// out the ones without a link. It returns an empty, non-nil map when none has one.
func Links(platforms []string, title, country string) map[string]string {
	out := map[string]string{}
	for _, p := range platforms {
		if l := Link(p, title, country); l != "" {
			out[p] = l
		}
	}
	return out
}

// AddToMetadata sets platform_links on every streaming object of an item's decoded
// metadata, i.e. every top-level object that has a platforms list (movie and TV
// types carry exactly one, under "streaming"). It is the single builder behind the
// full item and the rows, a pure function of the title and the stored platform
// names, so it needs no queries. meta is modified in place; nested objects are
// copied first so shared decoded data is never aliased.
func AddToMetadata(meta map[string]any, title string) {
	for key, v := range meta {
		obj, ok := v.(map[string]any)
		if !ok {
			continue
		}
		raw, ok := obj["platforms"].([]any)
		if !ok {
			continue
		}
		names := make([]string, 0, len(raw))
		for _, r := range raw {
			if name, ok := r.(string); ok {
				names = append(names, name)
			}
		}
		country, _ := obj["country"].(string)
		next := make(map[string]any, len(obj)+1)
		for k, val := range obj {
			next[k] = val
		}
		next["platform_links"] = Links(names, title, country)
		meta[key] = next
	}
}
