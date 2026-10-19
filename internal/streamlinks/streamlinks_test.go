package streamlinks

import (
	"reflect"
	"testing"
)

func TestNormalize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Netflix", "netflix"},
		{"Disney+", "disneyplus"},
		{"Disney Plus", "disneyplus"},
		{"Apple TV+", "appletvplus"},
		{" Prime  Video ", "primevideo"},
		{"Infinity+", "infinityplus"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := Normalize(tt.in); got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestLink(t *testing.T) {
	tests := []struct{ name, platform, title, want string }{
		{"netflix", "Netflix", "The Matrix", "https://www.netflix.com/search?q=The+Matrix"},
		{"netflix ads variant", "Netflix basic with Ads", "Dune", "https://www.netflix.com/search?q=Dune"},
		{"amazon prime video", "Amazon Prime Video", "Dune", "https://www.primevideo.com/search/?phrase=Dune"},
		{"prime video", "Prime Video", "Dune", "https://www.primevideo.com/search/?phrase=Dune"},
		{"prime video with ads", "Amazon Prime Video with Ads", "Dune", "https://www.primevideo.com/search/?phrase=Dune"},
		{"disney plus", "Disney Plus", "Dune", "https://www.disneyplus.com/search?q=Dune"},
		{"disney+", "Disney+", "Dune", "https://www.disneyplus.com/search?q=Dune"},
		{"apple tv", "Apple TV", "Dune", "https://tv.apple.com/search?term=Dune"},
		{"apple tv plus", "Apple TV Plus", "Dune", "https://tv.apple.com/search?term=Dune"},
		{"apple tv+", "Apple TV+", "Dune", "https://tv.apple.com/search?term=Dune"},
		{"max", "Max", "Dune", "https://play.max.com/search?q=Dune"},
		{"hbo max", "HBO Max", "Dune", "https://play.max.com/search?q=Dune"},
		{"paramount plus", "Paramount Plus", "Dune", "https://www.paramountplus.com/search/?q=Dune"},
		{"paramount+", "Paramount+", "Dune", "https://www.paramountplus.com/search/?q=Dune"},
		{"hulu", "Hulu", "Dune", "https://www.hulu.com/search?q=Dune"},
		{"peacock", "Peacock", "Dune", "https://www.peacocktv.com/search?q=Dune"},
		{"peacock premium", "Peacock Premium", "Dune", "https://www.peacocktv.com/search?q=Dune"},
		{"youtube", "YouTube", "Dune", "https://www.youtube.com/results?search_query=Dune"},
		{"crunchyroll", "Crunchyroll", "Dune", "https://www.crunchyroll.com/search?q=Dune"},
		{"mubi", "MUBI", "Dune", "https://mubi.com/search/films?query=Dune"},
		{"title is escaped", "Netflix", "Tom & Jerry: 100% #1", "https://www.netflix.com/search?q=Tom+%26+Jerry%3A+100%25+%231"},
		{"title is trimmed", "Netflix", "  Dune  ", "https://www.netflix.com/search?q=Dune"},
		{"non-ascii title", "Netflix", "Amélie", "https://www.netflix.com/search?q=Am%C3%A9lie"},
		{"empty title", "Netflix", "   ", ""},
		{"unknown platform", "RaiPlay", "Dune", ""},
		{"similar name is not a match", "Maxdome", "Dune", ""},
		{"empty platform", "", "Dune", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Link(tt.platform, tt.title, "IT"); got != tt.want {
				t.Errorf("Link(%q, %q) = %q, want %q", tt.platform, tt.title, got, tt.want)
			}
		})
	}
}

func TestLinksOmitsUnknownAndKeepsStoredNames(t *testing.T) {
	got := Links([]string{"Netflix", "RaiPlay", "Disney+"}, "Dune", "IT")
	want := map[string]string{
		"Netflix": "https://www.netflix.com/search?q=Dune",
		"Disney+": "https://www.disneyplus.com/search?q=Dune",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Links = %v, want %v", got, want)
	}
	if got := Links([]string{"RaiPlay"}, "Dune", "IT"); got == nil || len(got) != 0 {
		t.Errorf("no known platform: %v, want empty non-nil map", got)
	}
}

func TestAddToMetadata(t *testing.T) {
	meta := map[string]any{
		"release_year": float64(1999),
		"stores":       []any{"kobo"},
		"streaming":    map[string]any{"country": "IT", "platforms": []any{"Netflix", "RaiPlay"}},
	}
	AddToMetadata(meta, "Dune")
	s := meta["streaming"].(map[string]any)
	want := map[string]string{"Netflix": "https://www.netflix.com/search?q=Dune"}
	if !reflect.DeepEqual(s["platform_links"], want) {
		t.Errorf("platform_links = %v, want %v", s["platform_links"], want)
	}
	if _, ok := meta["stores"].([]any); !ok || meta["release_year"] != float64(1999) {
		t.Errorf("other keys changed: %v", meta)
	}

	none := map[string]any{"release_year": float64(1999)}
	AddToMetadata(none, "Dune")
	if len(none) != 1 {
		t.Errorf("metadata without streaming changed: %v", none)
	}
	AddToMetadata(nil, "Dune")
}
