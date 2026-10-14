package kinds_test

import (
	"encoding/json"
	"errors"
	"reflect"
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
		{"imdb m. host and long id, no slash", "movie", `{"imdb_url":"https://m.imdb.com/title/tt0123456789"}`, m{"imdb_url": "https://m.imdb.com/title/tt0123456789"}},
		{"imdb bare host", "movie", `{"imdb_url":"https://imdb.com/title/tt1234567/"}`, m{"imdb_url": "https://imdb.com/title/tt1234567/"}},
		{"null keys are omitted", "movie", `{"release_year":null,"streaming":null,"imdb_url":null,"runtime_minutes":90}`, m{"runtime_minutes": 90}},
		{"streaming without platforms", "movie", `{"streaming":{"country":"US"}}`, m{"streaming": m{"country": "US"}}},
		{"streaming null platforms", "movie", `{"streaming":{"country":"US","platforms":null}}`, m{"streaming": m{"country": "US"}}},
		{"streaming platforms trimmed", "movie", `{"streaming":{"country":"IT","platforms":["  Netflix  "]}}`, m{"streaming": m{"country": "IT", "platforms": []string{"Netflix"}}}},
		{"tv_show open end", "tv_show", `{"first_air_year":2008,"seasons":5}`, m{"first_air_year": 2008, "seasons": 5}},
		{"tv_show end equals first", "tv_show", `{"first_air_year":2008,"end_year":2008}`, m{"first_air_year": 2008, "end_year": 2008}},
		{"tv_show end without first", "tv_show", `{"end_year":2013}`, m{"end_year": 2013}},
		{"motorcycle full", "motorcycle", `{"make":" Ducati ","model":"Monster","model_year":2021,"segment":"naked","displacement_cc":937,"power_hp":111.0,"torque_nm":93.0,"wet_weight_kg":188,"seat_height_mm":820,"license_class":"A2"}`,
			m{"make": "Ducati", "model": "Monster", "model_year": 2021, "segment": "naked", "displacement_cc": 937, "power_hp": 111.0, "torque_nm": 93.0,
				"wet_weight_kg": 188, "seat_height_mm": 820, "license_class": "A2"}},
		{"motorcycle one decimal", "motorcycle", `{"power_hp":95.5}`, m{"power_hp": 95.5}},
		{"motorcycle bounds", "motorcycle", `{"power_hp":0,"torque_nm":1000,"seat_height_mm":300,"displacement_cc":5000}`, m{"power_hp": 0.0, "torque_nm": 1000.0, "seat_height_mm": 300, "displacement_cc": 5000}},
		{"text at the limit", "motorcycle", `{"make":"` + long + `"}`, m{"make": long}},
		{"car electric with range", "car", `{"fuel_type":"electric","electric_range_km":500,"seats":5}`, m{"fuel_type": "electric", "electric_range_km": 500, "seats": 5}},
		{"car plugin hybrid with range", "car", `{"fuel_type":"plugin_hybrid","electric_range_km":60}`, m{"fuel_type": "plugin_hybrid", "electric_range_km": 60}},
		{"car range without fuel", "car", `{"electric_range_km":400}`, m{"electric_range_km": 400}},
		{"car petrol", "car", `{"make":"VW","model":"Golf","trim":"GTI","model_year":2019,"body_type":"hatchback","fuel_type":"petrol","transmission":"manual","power_hp":245.0,"seats":5}`,
			m{"make": "VW", "model": "Golf", "trim": "GTI", "model_year": 2019, "body_type": "hatchback", "fuel_type": "petrol", "transmission": "manual", "power_hp": 245.0, "seats": 5}},
		{"camera full", "camera", `{"make":"Sony","model":"A7","release_year":2013,"camera_type":"mirrorless","sensor_format":"full_frame","megapixels":24.3,"lens_mount":"Sony E","weight_g":474}`,
			m{"make": "Sony", "model": "A7", "release_year": 2013, "camera_type": "mirrorless", "sensor_format": "full_frame", "megapixels": 24.3, "lens_mount": "Sony E", "weight_g": 474}},
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
		{"car", "transmission", []string{"manual", "automatic"}},
		{"camera", "camera_type", []string{"mirrorless", "dslr", "compact", "rangefinder", "medium_format", "film", "action", "other"}},
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
		{"moto make empty", "motorcycle", `{"make":"   "}`, []string{"metadata.make"}},
		{"moto make too long", "motorcycle", `{"make":"` + strings.Repeat("a", 101) + `"}`, []string{"metadata.make"}},
		{"moto make number", "motorcycle", `{"make":7}`, []string{"metadata.make"}},
		{"moto segment 51", "motorcycle", `{"segment":"` + strings.Repeat("a", 51) + `"}`, []string{"metadata.segment"}},
		{"moto year below", "motorcycle", `{"model_year":1884}`, []string{"metadata.model_year"}},
		{"moto displacement zero", "motorcycle", `{"displacement_cc":0}`, []string{"metadata.displacement_cc"}},
		{"moto displacement above", "motorcycle", `{"displacement_cc":5001}`, []string{"metadata.displacement_cc"}},
		{"moto power two decimals", "motorcycle", `{"power_hp":95.55}`, []string{"metadata.power_hp"}},
		{"moto power negative", "motorcycle", `{"power_hp":-1}`, []string{"metadata.power_hp"}},
		{"moto power above", "motorcycle", `{"power_hp":1000.1}`, []string{"metadata.power_hp"}},
		{"moto torque above", "motorcycle", `{"torque_nm":1001}`, []string{"metadata.torque_nm"}},
		{"moto weight zero", "motorcycle", `{"wet_weight_kg":0}`, []string{"metadata.wet_weight_kg"}},
		{"moto seat below", "motorcycle", `{"seat_height_mm":299}`, []string{"metadata.seat_height_mm"}},
		{"moto seat above", "motorcycle", `{"seat_height_mm":1201}`, []string{"metadata.seat_height_mm"}},
		{"car power above", "car", `{"power_hp":2000.1}`, []string{"metadata.power_hp"}},
		{"car seats zero", "car", `{"seats":0}`, []string{"metadata.seats"}},
		{"car seats ten", "car", `{"seats":10}`, []string{"metadata.seats"}},
		{"car body type 51", "car", `{"body_type":"` + strings.Repeat("a", 51) + `"}`, []string{"metadata.body_type"}},
		{"car range with petrol", "car", `{"fuel_type":"petrol","electric_range_km":400}`, []string{"metadata.electric_range_km"}},
		{"car range with hybrid", "car", `{"fuel_type":"hybrid","electric_range_km":10}`, []string{"metadata.electric_range_km"}},
		{"car range above", "car", `{"fuel_type":"electric","electric_range_km":2001}`, []string{"metadata.electric_range_km"}},
		{"car range zero", "car", `{"electric_range_km":0}`, []string{"metadata.electric_range_km"}},
		{"camera year below", "camera", `{"release_year":1899}`, []string{"metadata.release_year"}},
		{"camera megapixels below", "camera", `{"megapixels":0}`, []string{"metadata.megapixels"}},
		{"camera megapixels above", "camera", `{"megapixels":500.1}`, []string{"metadata.megapixels"}},
		{"camera megapixels two decimals", "camera", `{"megapixels":24.35}`, []string{"metadata.megapixels"}},
		{"camera lens mount 51", "camera", `{"lens_mount":"` + strings.Repeat("a", 51) + `"}`, []string{"metadata.lens_mount"}},
		{"camera weight zero", "camera", `{"weight_g":0}`, []string{"metadata.weight_g"}},
		{"camera weight above", "camera", `{"weight_g":10001}`, []string{"metadata.weight_g"}},
		{"several errors in key order", "camera", `{"weight_g":0,"release_year":1800,"megapixels":"x"}`, []string{"metadata.megapixels", "metadata.release_year", "metadata.weight_g"}},
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
		{"movie key of another kind", "movie", `{"make":"Ducati"}`, "metadata.make"},
		{"movie typo", "movie", `{"runtime":100}`, "metadata.runtime"},
		{"unknown key wins over bad value", "movie", `{"release_year":1,"zzz":true}`, "metadata.zzz"},
		{"null unknown key still unknown", "movie", `{"zzz":null}`, "metadata.zzz"},
		{"first unknown key alphabetically", "car", `{"b":1,"a":1}`, "metadata.a"},
		{"streaming unknown key", "movie", `{"streaming":{"country":"IT","region":"EU"}}`, "metadata.streaming.region"},
		{"tv key of movie", "tv_show", `{"release_year":2000}`, "metadata.release_year"},
		{"camera key of car", "camera", `{"seats":5}`, "metadata.seats"},
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
	if _, err := kinds.Validate("motorcycle", in); err != nil {
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
