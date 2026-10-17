package kinds_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/items/kinds"
)

type m = map[string]any

// parse decodes JSON the way the HTTP layer does: numbers become float64.
func parse(t *testing.T, s string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// wantFields asserts a 422 whose field list is exactly fields.
func wantFields(t *testing.T, err error, fields ...string) {
	t.Helper()
	var v *apperr.ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("err = %v, want a validation error on %v", err, fields)
	}
	var got []string
	for _, fe := range v.Errors {
		got = append(got, fe.Field)
	}
	if !reflect.DeepEqual(got, fields) {
		t.Fatalf("fields = %v, want %v", got, fields)
	}
}

func TestAT05_AcceptedMetadata(t *testing.T) {
	long := strings.Repeat("é", 100)
	tests := []struct {
		name, kind, in string
		want           m
	}{
		{"generic empty", "generic", `{}`, m{}},
		{"movie full", "movie", `{"release_year":1999,"runtime_minutes":136,"imdb_url":"https://www.imdb.com/title/tt0133093/","streaming":{"country":"IT","platforms":["Netflix","Prime Video"]}}`,
			m{"release_year": 1999, "runtime_minutes": 136, "imdb_url": "https://www.imdb.com/title/tt0133093/",
				"streaming": m{"country": "IT", "platforms": []string{"Netflix", "Prime Video"}}}},
		{"movie bounds", "movie", `{"release_year":1870,"runtime_minutes":1000}`, m{"release_year": 1870, "runtime_minutes": 1000}},
		{"movie upper bounds", "movie", `{"release_year":2100,"runtime_minutes":1}`, m{"release_year": 2100, "runtime_minutes": 1}},
		{"F013 movie lookup fields", "movie", `{"director":"  Lana Wachowski, Lilly Wachowski  ","tmdb_id":603,"lookup_disabled":true}`,
			m{"director": "Lana Wachowski, Lilly Wachowski", "tmdb_id": 603, "lookup_disabled": true}},
		{"F013 tv lookup fields", "tv_show", `{"tmdb_id":1396,"lookup_disabled":false}`, m{"tmdb_id": 1396, "lookup_disabled": false}},
		{"imdb m. host and long id, no slash", "movie", `{"imdb_url":"https://m.imdb.com/title/tt0123456789"}`, m{"imdb_url": "https://m.imdb.com/title/tt0123456789"}},
		{"imdb bare host", "movie", `{"imdb_url":"https://imdb.com/title/tt1234567/"}`, m{"imdb_url": "https://imdb.com/title/tt1234567/"}},
		{"null keys are omitted", "movie", `{"release_year":null,"streaming":null,"imdb_url":null,"runtime_minutes":90}`, m{"runtime_minutes": 90}},
		{"F002B1 paid platforms", "movie", `{"streaming":{"country":"IT","platforms":["Netflix","Prime Video"],"paid_platforms":["Prime Video"]}}`,
			m{"streaming": m{"country": "IT", "platforms": []string{"Netflix", "Prime Video"}, "paid_platforms": []string{"Prime Video"}}}},
		{"F002B1 paid platforms normalized to platform casing", "movie", `{"streaming":{"country":"IT","platforms":["Netflix","Prime Video"],"paid_platforms":["  prime video "]}}`,
			m{"streaming": m{"country": "IT", "platforms": []string{"Netflix", "Prime Video"}, "paid_platforms": []string{"Prime Video"}}}},
		{"F002B1 paid platforms null", "movie", `{"streaming":{"country":"IT","platforms":["Netflix"],"paid_platforms":null}}`,
			m{"streaming": m{"country": "IT", "platforms": []string{"Netflix"}}}},
		{"F002B1 paid platforms empty", "movie", `{"streaming":{"country":"IT","platforms":["Netflix"],"paid_platforms":[]}}`,
			m{"streaming": m{"country": "IT", "platforms": []string{"Netflix"}}}},
		{"F002B1 paid platforms empty without platforms", "movie", `{"streaming":{"country":"IT","paid_platforms":[]}}`,
			m{"streaming": m{"country": "IT"}}},
		{"streaming without platforms", "movie", `{"streaming":{"country":"US"}}`, m{"streaming": m{"country": "US"}}},
		{"streaming null platforms", "movie", `{"streaming":{"country":"US","platforms":null}}`, m{"streaming": m{"country": "US"}}},
		{"streaming platforms trimmed", "movie", `{"streaming":{"country":"IT","platforms":["  Netflix  "]}}`, m{"streaming": m{"country": "IT", "platforms": []string{"Netflix"}}}},
		{"tv_show open end", "tv_show", `{"first_air_year":2008,"seasons":5}`, m{"first_air_year": 2008, "seasons": 5}},
		{"tv_show end equals first", "tv_show", `{"first_air_year":2008,"end_year":2008}`, m{"first_air_year": 2008, "end_year": 2008}},
		{"tv_show end without first", "tv_show", `{"end_year":2013}`, m{"end_year": 2013}},
		{"motorcycle full", "motorcycle", `{"model_year":2021,"displacement_cc":937,"power_hp":111.0,"license_class":"A2"}`,
			m{"model_year": 2021, "displacement_cc": 937, "power_hp": 111.0, "license_class": "A2"}},
		{"motorcycle one decimal", "motorcycle", `{"power_hp":95.5}`, m{"power_hp": 95.5}},
		{"motorcycle bounds", "motorcycle", `{"power_hp":0,"displacement_cc":5000}`, m{"power_hp": 0.0, "displacement_cc": 5000}},
		{"text at the limit", "car", `{"make":"` + long + `"}`, m{"make": long}},
		{"car electric", "car", `{"fuel_type":"electric","model_year":2023}`, m{"fuel_type": "electric", "model_year": 2023}},
		{"car petrol", "car", `{"make":"VW","model":"Golf","model_year":2019,"fuel_type":"petrol","power_hp":245.0}`,
			m{"make": "VW", "model": "Golf", "model_year": 2019, "fuel_type": "petrol", "power_hp": 245.0}},
		{"camera full", "camera", `{"camera_type":"mirrorless","release_year":2013,"sensor_format":"full_frame","megapixels":24.3}`,
			m{"camera_type": "mirrorless", "release_year": 2013, "sensor_format": "full_frame", "megapixels": 24.3}},
		{"F002B2 tv episode fields", "tv_show", `{"episodes_per_season":10,"episode_minutes":45}`, m{"episodes_per_season": 10, "episode_minutes": 45}},
		{"F002B2 tv episode bounds", "tv_show", `{"episodes_per_season":500,"episode_minutes":1}`, m{"episodes_per_season": 500, "episode_minutes": 1}},
		{"camera megapixel bounds", "camera", `{"megapixels":0.1}`, m{"megapixels": 0.1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := kinds.Validate(tt.kind, parse(t, tt.in))
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v\nwant %#v", got, tt.want)
			}
		})
	}
}

func TestAT05_EveryEnumValueIsAccepted(t *testing.T) {
	enums := []struct {
		kind, key string
		values    []string
	}{
		{"motorcycle", "license_class", []string{"AM", "A1", "A2", "A"}},
		{"car", "fuel_type", []string{"petrol", "diesel", "hybrid", "plugin_hybrid", "electric", "lpg", "cng", "hydrogen", "other"}},
		{"camera", "camera_type", []string{"mirrorless", "dslr", "film"}},
		{"camera", "sensor_format", []string{"full_frame", "aps_c", "micro_four_thirds", "one_inch", "medium_format", "film_35mm", "other"}},
	}
	for _, e := range enums {
		for _, v := range e.values {
			if _, err := kinds.Validate(e.kind, m{e.key: v}); err != nil {
				t.Errorf("%s.%s=%q rejected: %v", e.kind, e.key, v, err)
			}
		}
		if _, err := kinds.Validate(e.kind, m{e.key: "nope"}); err == nil {
			t.Errorf("%s.%s=nope accepted", e.kind, e.key)
		}
		if _, err := kinds.Validate(e.kind, m{e.key: strings.ToUpper(e.values[len(e.values)-1]) + "x"}); err == nil {
			t.Errorf("%s.%s case variant accepted", e.kind, e.key)
		}
	}
}

func TestAT05_RejectedValues(t *testing.T) {
	tests := []struct {
		name, kind, in string
		fields         []string
	}{
		{"movie year below", "movie", `{"release_year":1869}`, []string{"metadata.release_year"}},
		{"movie year above", "movie", `{"release_year":2101}`, []string{"metadata.release_year"}},
		{"movie year fraction", "movie", `{"release_year":1999.5}`, []string{"metadata.release_year"}},
		{"movie year string", "movie", `{"release_year":"1999"}`, []string{"metadata.release_year"}},
		{"movie year bool", "movie", `{"release_year":true}`, []string{"metadata.release_year"}},
		{"movie runtime zero", "movie", `{"runtime_minutes":0}`, []string{"metadata.runtime_minutes"}},
		{"movie runtime above", "movie", `{"runtime_minutes":1001}`, []string{"metadata.runtime_minutes"}},
		{"F013 director blank", "movie", `{"director":"   "}`, []string{"metadata.director"}},
		{"F013 director too long", "movie", `{"director":"` + strings.Repeat("a", 301) + `"}`, []string{"metadata.director"}},
		{"F013 tmdb id zero", "movie", `{"tmdb_id":0}`, []string{"metadata.tmdb_id"}},
		{"F013 tmdb id fraction", "tv_show", `{"tmdb_id":1.5}`, []string{"metadata.tmdb_id"}},
		{"F013 lookup disabled must be boolean", "movie", `{"lookup_disabled":"true"}`, []string{"metadata.lookup_disabled"}},
		{"imdb http", "movie", `{"imdb_url":"http://www.imdb.com/title/tt0133093/"}`, []string{"metadata.imdb_url"}},
		{"imdb other host", "movie", `{"imdb_url":"https://example.com/title/tt0133093/"}`, []string{"metadata.imdb_url"}},
		{"imdb lookalike host", "movie", `{"imdb_url":"https://evilimdb.com/title/tt0133093/"}`, []string{"metadata.imdb_url"}},
		{"imdb short id", "movie", `{"imdb_url":"https://www.imdb.com/title/tt013309/"}`, []string{"metadata.imdb_url"}},
		{"imdb long id", "movie", `{"imdb_url":"https://www.imdb.com/title/tt01330930000/"}`, []string{"metadata.imdb_url"}},
		{"imdb name page", "movie", `{"imdb_url":"https://www.imdb.com/name/nm0000206/"}`, []string{"metadata.imdb_url"}},
		{"imdb trailing path", "movie", `{"imdb_url":"https://www.imdb.com/title/tt0133093/reviews"}`, []string{"metadata.imdb_url"}},
		{"imdb not a string", "movie", `{"imdb_url":5}`, []string{"metadata.imdb_url"}},
		{"streaming without country", "movie", `{"streaming":{"platforms":["Netflix"]}}`, []string{"metadata.streaming.country"}},
		{"streaming country lowercase", "movie", `{"streaming":{"country":"it"}}`, []string{"metadata.streaming.country"}},
		{"streaming country 3 letters", "movie", `{"streaming":{"country":"ITA"}}`, []string{"metadata.streaming.country"}},
		{"streaming country number", "movie", `{"streaming":{"country":1}}`, []string{"metadata.streaming.country"}},
		{"streaming not an object", "movie", `{"streaming":"Netflix"}`, []string{"metadata.streaming"}},
		{"streaming platforms not array", "movie", `{"streaming":{"country":"IT","platforms":"Netflix"}}`, []string{"metadata.streaming.platforms"}},
		{"F002B1 paid platform not in platforms", "movie", `{"streaming":{"country":"IT","platforms":["Netflix"],"paid_platforms":["Hulu"]}}`, []string{"metadata.streaming.paid_platforms[0]"}},
		{"F002B1 paid platform without platforms", "movie", `{"streaming":{"country":"IT","paid_platforms":["Netflix"]}}`, []string{"metadata.streaming.paid_platforms[0]"}},
		{"F002B1 paid second entry not in platforms", "movie", `{"streaming":{"country":"IT","platforms":["Netflix"],"paid_platforms":["netflix","Hulu"]}}`, []string{"metadata.streaming.paid_platforms[1]"}},
		{"F002B1 paid duplicate", "movie", `{"streaming":{"country":"IT","platforms":["Netflix"],"paid_platforms":["Netflix","NETFLIX"]}}`, []string{"metadata.streaming.paid_platforms[1]"}},
		{"F002B1 paid not array", "movie", `{"streaming":{"country":"IT","platforms":["Netflix"],"paid_platforms":"Netflix"}}`, []string{"metadata.streaming.paid_platforms"}},
		{"F002B1 paid entry not string", "movie", `{"streaming":{"country":"IT","platforms":["Netflix"],"paid_platforms":[1]}}`, []string{"metadata.streaming.paid_platforms[0]"}},
		{"F002B1 paid blank entry", "movie", `{"streaming":{"country":"IT","platforms":["Netflix"],"paid_platforms":[" "]}}`, []string{"metadata.streaming.paid_platforms[0]"}},
		{"F002B1 paid too many", "movie", `{"streaming":{"country":"IT","platforms":[` + platforms(30) + `],"paid_platforms":[` + platforms(31) + `]}}`, []string{"metadata.streaming.paid_platforms"}},
		{"streaming duplicate platform", "movie", `{"streaming":{"country":"IT","platforms":["Netflix","netflix"]}}`, []string{"metadata.streaming.platforms[1]"}},
		{"streaming duplicate after trim", "movie", `{"streaming":{"country":"IT","platforms":["Netflix"," NETFLIX "]}}`, []string{"metadata.streaming.platforms[1]"}},
		{"streaming empty platform", "movie", `{"streaming":{"country":"IT","platforms":["  "]}}`, []string{"metadata.streaming.platforms[0]"}},
		{"streaming platform too long", "movie", `{"streaming":{"country":"IT","platforms":["` + strings.Repeat("a", 61) + `"]}}`, []string{"metadata.streaming.platforms[0]"}},
		{"streaming platform not string", "movie", `{"streaming":{"country":"IT","platforms":[1]}}`, []string{"metadata.streaming.platforms[0]"}},
		{"streaming 31 platforms", "movie", `{"streaming":{"country":"IT","platforms":[` + platforms(31) + `]}}`, []string{"metadata.streaming.platforms"}},
		{"tv end before first", "tv_show", `{"first_air_year":2010,"end_year":2009}`, []string{"metadata.end_year"}},
		{"tv first below", "tv_show", `{"first_air_year":1919}`, []string{"metadata.first_air_year"}},
		{"tv end above", "tv_show", `{"end_year":2101}`, []string{"metadata.end_year"}},
		{"tv seasons zero", "tv_show", `{"seasons":0}`, []string{"metadata.seasons"}},
		{"tv seasons above", "tv_show", `{"seasons":201}`, []string{"metadata.seasons"}},
		{"car make empty", "car", `{"make":"   "}`, []string{"metadata.make"}},
		{"car make too long", "car", `{"make":"` + strings.Repeat("a", 101) + `"}`, []string{"metadata.make"}},
		{"car make number", "car", `{"make":7}`, []string{"metadata.make"}},
		{"F002B2 tv episodes zero", "tv_show", `{"episodes_per_season":0}`, []string{"metadata.episodes_per_season"}},
		{"F002B2 tv episodes above", "tv_show", `{"episodes_per_season":501}`, []string{"metadata.episodes_per_season"}},
		{"F002B2 tv episodes fraction", "tv_show", `{"episodes_per_season":1.5}`, []string{"metadata.episodes_per_season"}},
		{"F002B2 tv minutes zero", "tv_show", `{"episode_minutes":0}`, []string{"metadata.episode_minutes"}},
		{"F002B2 tv minutes above", "tv_show", `{"episode_minutes":1001}`, []string{"metadata.episode_minutes"}},
		{"moto year below", "motorcycle", `{"model_year":1884}`, []string{"metadata.model_year"}},
		{"moto displacement zero", "motorcycle", `{"displacement_cc":0}`, []string{"metadata.displacement_cc"}},
		{"moto displacement above", "motorcycle", `{"displacement_cc":5001}`, []string{"metadata.displacement_cc"}},
		{"moto power two decimals", "motorcycle", `{"power_hp":95.55}`, []string{"metadata.power_hp"}},
		{"moto power negative", "motorcycle", `{"power_hp":-1}`, []string{"metadata.power_hp"}},
		{"moto power above", "motorcycle", `{"power_hp":1000.1}`, []string{"metadata.power_hp"}},
		{"car power above", "car", `{"power_hp":2000.1}`, []string{"metadata.power_hp"}},
		{"camera year below", "camera", `{"release_year":1899}`, []string{"metadata.release_year"}},
		{"camera megapixels below", "camera", `{"megapixels":0}`, []string{"metadata.megapixels"}},
		{"camera megapixels above", "camera", `{"megapixels":500.1}`, []string{"metadata.megapixels"}},
		{"camera megapixels two decimals", "camera", `{"megapixels":24.35}`, []string{"metadata.megapixels"}},
		{"several errors in key order", "camera", `{"camera_type":"","release_year":1800,"megapixels":"x"}`, []string{"metadata.camera_type", "metadata.megapixels", "metadata.release_year"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := kinds.Validate(tt.kind, parse(t, tt.in))
			wantFields(t, err, tt.fields...)
		})
	}
}

func platforms(n int) string {
	var parts []string
	for i := range n {
		parts = append(parts, `"p`+string(rune('a'+i%26))+strings.Repeat("x", i/26)+`"`)
	}
	return strings.Join(parts, ",")
}

func TestAT05_ThirtyPlatformsAreAccepted(t *testing.T) {
	if _, err := kinds.Validate("movie", parse(t, `{"streaming":{"country":"IT","platforms":[`+platforms(30)+`]}}`)); err != nil {
		t.Fatal(err)
	}
}

func TestAT05_UnknownKeysAreUnknownFields(t *testing.T) {
	tests := []struct {
		name, kind, in, path string
	}{
		{"generic must be empty", "generic", `{"anything":1}`, "metadata.anything"},
		{"F002B2 motorcycle make dropped", "motorcycle", `{"make":"Ducati"}`, "metadata.make"},
		{"F002B2 motorcycle model dropped", "motorcycle", `{"model":"Monster"}`, "metadata.model"},
		{"F002B2 camera make dropped", "camera", `{"make":"Sony"}`, "metadata.make"},
		{"F002B2 camera model dropped", "camera", `{"model":"A7"}`, "metadata.model"},
		{"F002B2 camera_type on a motorcycle", "motorcycle", `{"camera_type":"dslr"}`, "metadata.camera_type"},
		{"movie key of another kind", "movie", `{"make":"Ducati"}`, "metadata.make"},
		{"movie typo", "movie", `{"runtime":100}`, "metadata.runtime"},
		{"unknown key wins over bad value", "movie", `{"release_year":1,"zzz":true}`, "metadata.zzz"},
		{"null unknown key still unknown", "movie", `{"zzz":null}`, "metadata.zzz"},
		{"first unknown key alphabetically", "car", `{"b":1,"a":1}`, "metadata.a"},
		{"streaming unknown key", "movie", `{"streaming":{"country":"IT","region":"EU"}}`, "metadata.streaming.region"},
		{"tv key of movie", "tv_show", `{"release_year":2000}`, "metadata.release_year"},
		{"camera key of car", "camera", `{"fuel_type":"petrol"}`, "metadata.fuel_type"},
		{"TestF007 dropped movie key", "movie", `{"summary":"x"}`, "metadata.summary"},
		{"TestF007 dropped motorcycle key", "motorcycle", `{"torque_nm":93}`, "metadata.torque_nm"},
		{"TestF007 dropped car key", "car", `{"electric_range_km":400}`, "metadata.electric_range_km"},
		{"TestF007 dropped camera key", "camera", `{"lens_mount":"Sony E"}`, "metadata.lens_mount"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := kinds.Validate(tt.kind, parse(t, tt.in))
			var u *apperr.UnknownFieldError
			if !errors.As(err, &u) || u.Path != tt.path {
				t.Fatalf("err = %v, want unknown field %q", err, tt.path)
			}
		})
	}
}

func TestAT05_UnknownKind(t *testing.T) {
	for _, kind := range []string{"book", "", "Movie", "tv-show"} {
		_, err := kinds.Validate(kind, m{})
		wantFields(t, err, "kind")
		if kinds.Known(kind) {
			t.Errorf("Known(%q) = true", kind)
		}
	}
	for _, kind := range []string{"generic", "movie", "tv_show", "motorcycle", "car", "camera"} {
		if !kinds.Known(kind) {
			t.Errorf("Known(%q) = false", kind)
		}
	}
}

func TestValidateDoesNotMutateInput(t *testing.T) {
	in := m{"make": "  Ducati  ", "power_hp": nil}
	if _, err := kinds.Validate("car", in); err != nil {
		t.Fatal(err)
	}
	if in["make"] != "  Ducati  " || len(in) != 2 {
		t.Fatalf("input mutated: %#v", in)
	}
}

func TestValidateAcceptsGoIntegers(t *testing.T) {
	got, err := kinds.Validate("movie", m{"release_year": 1999, "runtime_minutes": int64(90)})
	if err != nil {
		t.Fatal(err)
	}
	if got["release_year"] != 1999 || got["runtime_minutes"] != 90 {
		t.Fatalf("got %#v", got)
	}
}

func TestF007_SortKey(t *testing.T) {
	tests := []struct {
		kind, key string
		ok        bool
	}{
		{"movie", "release_year", true},
		{"tv_show", "first_air_year", true},
		{"motorcycle", "displacement_cc", true},
		{"car", "power_hp", true},
		{"camera", "megapixels", true},
		{"generic", "", false},
		{"book", "", false},
	}
	for _, tt := range tests {
		key, ok := kinds.SortKey(tt.kind)
		if key != tt.key || ok != tt.ok {
			t.Errorf("SortKey(%q) = %q, %v; want %q, %v", tt.kind, key, ok, tt.key, tt.ok)
		}
	}
}

func TestF009B1_PinnableKeysPerKind(t *testing.T) {
	tests := []struct {
		kind string
		want []string
	}{
		{"generic", nil},
		{"movie", []string{"director", "imdb_url", "release_year", "runtime_minutes", "streaming"}},
		{"tv_show", []string{"end_year", "episode_minutes", "episodes_per_season", "first_air_year", "imdb_url", "seasons", "streaming"}},
		{"motorcycle", []string{"displacement_cc", "license_class", "model_year", "power_hp", "price"}},
		{"car", []string{"fuel_type", "make", "model", "model_year", "power_hp", "price"}},
		{"camera", []string{"camera_type", "megapixels", "price", "release_year", "sensor_format"}},
		{"book", nil},
	}
	for _, tt := range tests {
		if got := kinds.PinnableKeys(tt.kind); !slices.Equal(got, tt.want) {
			t.Errorf("PinnableKeys(%q) = %v, want %v", tt.kind, got, tt.want)
		}
	}
}

func TestF009B1_HasPrice(t *testing.T) {
	tests := map[string]bool{
		"generic": false, "movie": false, "tv_show": false,
		"motorcycle": true, "car": true, "camera": true, "book": false,
	}
	for kind, want := range tests {
		if got := kinds.HasPrice(kind); got != want {
			t.Errorf("HasPrice(%q) = %v, want %v", kind, got, want)
		}
	}
}

func TestF009B2_ValidatePinned(t *testing.T) {
	tests := []struct {
		name       string
		kind       string
		keys       []string
		wantFields []string
	}{
		{"ok", "movie", []string{"release_year", "streaming"}, nil},
		{"empty pins nothing", "movie", nil, nil},
		{"one is fine", "movie", []string{"release_year"}, nil},
		{"F013 director is pinnable", "movie", []string{"director"}, nil},
		{"F013 provider id is internal", "movie", []string{"tmdb_id"}, []string{"pinned_fields[0]"}},
		{"F013 lookup opt out is internal", "tv_show", []string{"lookup_disabled"}, []string{"pinned_fields[0]"}},
		{"TestF009B1 price on movie", "movie", []string{"price"}, []string{"pinned_fields[0]"}},
		{"TestF009B1 price on tv_show", "tv_show", []string{"price"}, []string{"pinned_fields[0]"}},
		{"TestF009B1 generic has no pinnable key", "generic", []string{"price"}, []string{"pinned_fields[0]"}},
		{"TestF009B1 generic empty is fine", "generic", nil, nil},
		{"unknown key", "movie", []string{"release_year", "make"}, []string{"pinned_fields[1]"}},
		{"duplicate", "movie", []string{"release_year", "release_year"}, []string{"pinned_fields[1]"}},
		{"three keys", "movie", []string{"release_year", "runtime_minutes", "imdb_url"}, []string{"pinned_fields"}},
		{"price is fine on motorcycle", "motorcycle", []string{"price", "power_hp"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := kinds.ValidatePinned(tt.kind, tt.keys)
			if tt.wantFields == nil {
				if err != nil {
					t.Fatalf("got %v", err)
				}
				if got == nil {
					t.Fatal("want a non-nil empty slice")
				}
				return
			}
			var ve *apperr.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want validation", err)
			}
			var fields []string
			for _, fe := range ve.Errors {
				fields = append(fields, fe.Field)
			}
			if !slices.Equal(fields, tt.wantFields) {
				t.Errorf("fields = %v, want %v", fields, tt.wantFields)
			}
		})
	}
}
