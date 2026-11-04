package tmdb

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// latinFixture serves canned TMDB responses by path, keyed on the language
// parameter for search, and counts every request.
func latinFixture(t *testing.T, responses map[string]string) (*Client, *[]string) {
	t.Helper()
	var paths []string
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		if strings.HasPrefix(key, "/3/search/") {
			key += "?" + r.URL.Query().Get("language")
		}
		paths = append(paths, key)
		body, ok := responses[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	})
	return c, &paths
}

func TestLatinScriptSearchTitles(t *testing.T) {
	for name, tc := range map[string]struct {
		responses map[string]string
		typ       string
		want      string
		calls     int
	}{
		"korean film without italian uses english": {
			responses: map[string]string{
				"/3/search/movie?it-IT": `{"results":[{"id":1,"title":"기생충","original_title":"기생충","release_date":"2019-05-30"}]}`,
				"/3/search/movie?en-US": `{"results":[{"id":1,"title":"Parasite","original_title":"기생충","release_date":"2019-05-30"}]}`,
			},
			typ: "movie", want: "Parasite", calls: 2,
		},
		"latin original when no english translation": {
			responses: map[string]string{
				"/3/search/movie?it-IT": `{"results":[{"id":2,"title":"Ночной дозор","original_title":"Nochnoy dozor"}]}`,
				"/3/search/movie?en-US": `{"results":[{"id":2,"title":"Ночной дозор","original_title":"Nochnoy dozor"}]}`,
			},
			typ: "movie", want: "Nochnoy dozor", calls: 2,
		},
		"korean series uses english name": {
			responses: map[string]string{
				"/3/search/tv?it-IT": `{"results":[{"id":3,"name":"오징어 게임","original_name":"오징어 게임","first_air_date":"2021-09-17"}]}`,
				"/3/search/tv?en-US": `{"results":[{"id":3,"name":"Squid Game","original_name":"오징어 게임","first_air_date":"2021-09-17"}]}`,
			},
			typ: "tv_show", want: "Squid Game", calls: 2,
		},
		"latin results need no english search": {
			responses: map[string]string{
				"/3/search/movie?it-IT": `{"results":[{"id":4,"title":"Amélie","original_title":"Le Fabuleux Destin"}]}`,
			},
			typ: "movie", want: "Amélie", calls: 1,
		},
		"all non-latin keeps the original": {
			responses: map[string]string{
				"/3/search/movie?it-IT": `{"results":[{"id":5,"title":"영화","original_title":"영화"}]}`,
				"/3/search/movie?en-US": `{"results":[]}`,
			},
			typ: "movie", want: "영화", calls: 2,
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, paths := latinFixture(t, tc.responses)
			matches, err := c.Search(context.Background(), tc.typ, "query", "it-IT")
			if err != nil || len(matches) != 1 {
				t.Fatalf("matches = %+v, err = %v", matches, err)
			}
			if matches[0].Title != tc.want {
				t.Errorf("title = %q, want %q", matches[0].Title, tc.want)
			}
			if len(*paths) != tc.calls {
				t.Errorf("TMDB calls = %v, want %d", *paths, tc.calls)
			}
		})
	}
}

func TestLatinScriptDetailsTitlesAndDirectors(t *testing.T) {
	for name, tc := range map[string]struct {
		responses map[string]string
		typ       string
		id        int
		title     string
		director  string // "" means the metadata has no director key
		paths     []string
	}{
		"korean film without italian uses us english translation": {
			responses: map[string]string{
				"/3/movie/101": `{"id":101,"title":"기생충","original_title":"기생충","release_date":"2019-05-30",
					"translations":{"translations":[{"iso_639_1":"it","iso_3166_1":"IT","data":{"title":""}},
					{"iso_639_1":"en","iso_3166_1":"GB","data":{"title":"Parasites"}},
					{"iso_639_1":"en","iso_3166_1":"US","data":{"title":"Parasite"}}]}}`,
			},
			typ: "movie", id: 101, title: "Parasite", paths: []string{"/3/movie/101"},
		},
		"latin original when there is no english translation": {
			responses: map[string]string{
				"/3/movie/102": `{"id":102,"title":"Ночной дозор","original_title":"Nochnoy dozor","translations":{"translations":[]}}`,
			},
			typ: "movie", id: 102, title: "Nochnoy dozor", paths: []string{"/3/movie/102"},
		},
		"localized latin title is kept without a translation lookup": {
			responses: map[string]string{
				"/3/movie/103": `{"id":103,"title":"Amélie","original_title":"Le Fabuleux Destin d'Amélie Poulain"}`,
			},
			typ: "movie", id: 103, title: "Amélie", paths: []string{"/3/movie/103"},
		},
		"japanese director with latin also_known_as": {
			responses: map[string]string{
				"/3/movie/104": `{"id":104,"title":"誰も知らない","original_title":"誰も知らない","credits":{"crew":[{"id":5,"job":"Director","name":"是枝裕和"}]}}`,
				"/3/person/5":  `{"id":5,"name":"是枝裕和","also_known_as":["是枝裕和","Hirokazu Kore-eda","Kore-eda Hirokazu"]}`,
			},
			typ: "movie", id: 104, title: "誰も知らない", director: "Hirokazu Kore-eda",
			paths: []string{"/3/movie/104", "/3/person/5"},
		},
		"all non-latin keeps the originals as last resort": {
			responses: map[string]string{
				"/3/movie/105": `{"id":105,"title":"영화","original_title":"영화","credits":{"crew":[{"id":6,"job":"Director","name":"김기덕"}]}}`,
				"/3/person/6":  `{"id":6,"name":"김기덕","also_known_as":["ヤマダ"]}`,
			},
			typ: "movie", id: 105, title: "영화", director: "김기덕",
			paths: []string{"/3/movie/105", "/3/person/6"},
		},
		"latin director name needs no person lookup": {
			responses: map[string]string{
				"/3/movie/106": `{"id":106,"title":"Film","credits":{"crew":[{"id":7,"job":"Director","name":"Wes Anderson"}]}}`,
			},
			typ: "movie", id: 106, title: "Film", director: "Wes Anderson", paths: []string{"/3/movie/106"},
		},
		"invalid person details keep the credit name": {
			responses: map[string]string{
				"/3/movie/109": `{"id":109,"title":"Film","credits":{"crew":[{"id":9,"job":"Director","name":"山田"}]}}`,
				"/3/person/9":  `{"id":10,"also_known_as":["Yamada"]}`,
			},
			typ: "movie", id: 109, title: "Film", director: "山田", paths: []string{"/3/movie/109", "/3/person/9"},
		},
		"person 404 keeps the credit name": {
			responses: map[string]string{
				"/3/movie/107": `{"id":107,"title":"Film","credits":{"crew":[{"id":8,"job":"Director","name":"山田"}]}}`,
			},
			typ: "movie", id: 107, title: "Film", director: "山田", paths: []string{"/3/movie/107", "/3/person/8"},
		},
		"korean series uses english name": {
			responses: map[string]string{
				"/3/tv/108": `{"id":108,"name":"오징어 게임","original_name":"오징어 게임","translations":{"translations":[{"iso_639_1":"en","iso_3166_1":"US","data":{"name":"Squid Game"}}]}}`,
			},
			typ: "tv_show", id: 108, title: "Squid Game", paths: []string{"/3/tv/108"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, paths := latinFixture(t, tc.responses)
			d, err := c.Details(context.Background(), tc.typ, tc.id, "it-IT", "")
			if err != nil {
				t.Fatal(err)
			}
			if d.Title != tc.title {
				t.Errorf("title = %q, want %q", d.Title, tc.title)
			}
			if got, _ := d.Metadata["director"].(string); got != tc.director {
				t.Errorf("director = %q, want %q", got, tc.director)
			}
			if strings.Join(*paths, ",") != strings.Join(tc.paths, ",") {
				t.Errorf("TMDB paths = %v, want %v", *paths, tc.paths)
			}
		})
	}
}

func TestDirectorPersonLookupsAreBounded(t *testing.T) {
	crew := `{"id":1,"job":"Director","name":"山田一"},{"id":2,"job":"Director","name":"山田二"},` +
		`{"id":3,"job":"Director","name":"山田三"},{"id":4,"job":"Director","name":"山田四"}`
	responses := map[string]string{"/3/movie/200": `{"id":200,"title":"Film","credits":{"crew":[` + crew + `]}}`}
	for id := 1; id <= 4; id++ {
		n := strconv.Itoa(id)
		responses["/3/person/"+n] = `{"id":` + n + `,"also_known_as":["Name ` + n + `"]}`
	}
	c, paths := latinFixture(t, responses)
	d, err := c.Details(context.Background(), "movie", 200, "it-IT", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(*paths) != 1+maxPersonLookups {
		t.Errorf("TMDB paths = %v, want one movie call and %d person calls", *paths, maxPersonLookups)
	}
	if got := d.Metadata["director"]; got != "Name 1, Name 2, Name 3, 山田四" {
		t.Errorf("director = %q", got)
	}
}
