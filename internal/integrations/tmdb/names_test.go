package tmdb

import "testing"

func TestIsLatin(t *testing.T) {
	for s, want := range map[string]bool{
		"Amélie":                true,
		"Ça":                    true,
		"Tiếng Việt":            true,
		"Ame\u0301lie":          true, // decomposed accent: the combining mark is ignored
		"2001: A Space Odyssey": true,
		"Mr. & Mrs. Smith":      true,
		"Pokémon":               true,
		"Привет":                false,
		"Ελληνικά":              false,
		"中文":                    false,
		"기생충":                   false,
		"ひらがな":                  false,
		"カタカナ":                  false,
		"Kill Bill 黒":           false,
		"2001":                  false, // no letters at all: not usable as a Latin name
		"":                      false,
	} {
		if got := isLatin(s); got != want {
			t.Errorf("isLatin(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestChooseTitle(t *testing.T) {
	for name, tc := range map[string]struct {
		localized string
		english   []string
		original  string
		want      string
	}{
		"localized Latin wins":                {"Odissea", []string{"Odyssey"}, "2001", "Odissea"},
		"english before Latin original":       {"기생충", []string{"Parasite"}, "기생충", "Parasite"},
		"us english first then other english": {"기생충", []string{"Parasite", "Parasites"}, "기생충", "Parasite"},
		"non-Latin english skipped":           {"기생충", []string{"기생충"}, "Gisaengchung", "Gisaengchung"},
		"Latin original when no english":      {"Ночной дозор", nil, "Nochnoy dozor", "Nochnoy dozor"},
		"last resort is non-Latin original":   {"영화", nil, "영화", "영화"},
		"last resort falls back to localized": {"영화", nil, "", "영화"},
		"digits-only title is not Latin":      {"2001", nil, "2001", "2001"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := chooseTitle(tc.localized, tc.english, tc.original); got != tc.want {
				t.Errorf("chooseTitle(%q, %q, %q) = %q, want %q", tc.localized, tc.english, tc.original, got, tc.want)
			}
		})
	}
}

func TestChooseName(t *testing.T) {
	for name, tc := range map[string]struct {
		name  string
		alias []string
		want  string
	}{
		"Latin name kept":               {"Wes Anderson", []string{"Anderson"}, "Wes Anderson"},
		"first Latin also_known_as":     {"是枝裕和", []string{"是枝裕和", "Hirokazu Kore-eda", "Kore-eda"}, "Hirokazu Kore-eda"},
		"no Latin alias keeps the name": {"山田洋次", []string{"ヤマダ"}, "山田洋次"},
		"no aliases keeps the name":     {"김기덕", nil, "김기덕"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := chooseName(tc.name, tc.alias); got != tc.want {
				t.Errorf("chooseName(%q, %q) = %q, want %q", tc.name, tc.alias, got, tc.want)
			}
		})
	}
}

func TestEnglishTitles(t *testing.T) {
	tr := func(iso, country, title string) translation {
		var v translation
		v.ISO639, v.ISO3166 = iso, country
		v.Data.Title, v.Data.Name = title, title
		return v
	}
	items := []translation{
		tr("it", "IT", "Parasite it"),
		tr("en", "GB", "Parasites"),
		tr("en", "US", "Parasite"),
		tr("en", "CA", ""),
	}
	if got := englishTitles("movie", items); len(got) != 2 || got[0] != "Parasite" || got[1] != "Parasites" {
		t.Errorf("englishTitles(movie) = %q, want US then GB", got)
	}
	if got := englishTitles("tv_show", items[:1]); len(got) != 0 {
		t.Errorf("englishTitles(tv_show) without en = %q", got)
	}
	if got := englishTitles("movie", nil); len(got) != 0 {
		t.Errorf("englishTitles(nil) = %q", got)
	}
}
