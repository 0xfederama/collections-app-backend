// Package kinds validates and normalizes item metadata per kind (DATA_MODEL
// §2.1). It is pure Go: no database, no HTTP. Every key is optional, `null`
// means "key omitted", text is trimmed, and the enum-like values live here
// (not in CHECK constraints) so a new value needs no migration.
package kinds

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
)

// Kind names (also the `kind` enum of the spec).
const (
	Generic    = "generic"
	Movie      = "movie"
	TVShow     = "tv_show"
	Motorcycle = "motorcycle"
	Car        = "car"
	Camera     = "camera"
)

const (
	defaultTextMax = 100
	maxPlatforms   = 30
	maxPlatformLen = 60
)

var (
	imdbRe    = regexp.MustCompile(`^https://(www\.|m\.)?imdb\.com/title/tt\d{7,10}/?$`)
	countryRe = regexp.MustCompile(`^[A-Z]{2}$`)
)

// outcome is what one field rule returns: the normalized value, or field
// errors, or the dotted path of an undeclared key.
type outcome struct {
	val     any
	errs    []apperr.FieldError
	unknown string
}

// rule validates one value found at the dotted path "metadata.<key>".
type rule func(path string, v any) outcome

type schema struct {
	fields map[string]rule
	// cross checks rules that span keys, on the normalized values.
	cross func(out map[string]any) []apperr.FieldError
}

var registry = map[string]schema{
	Generic: {fields: map[string]rule{}},
	Movie: {fields: map[string]rule{
		"release_year":    intRange(1870, 2100),
		"runtime_minutes": intRange(1, 1000),
		"imdb_url":        imdbURL,
		"streaming":       streaming,
	}},
	TVShow: {
		fields: map[string]rule{
			"first_air_year": intRange(1920, 2100),
			"end_year":       intRange(1920, 2100),
			"seasons":        intRange(1, 200),
			"imdb_url":       imdbURL,
			"streaming":      streaming,
		},
		cross: func(out map[string]any) []apperr.FieldError {
			first, ok1 := out["first_air_year"].(int)
			end, ok2 := out["end_year"].(int)
			if ok1 && ok2 && end < first {
				return []apperr.FieldError{{Field: "metadata.end_year", Message: "must not be before first_air_year"}}
			}
			return nil
		},
	},
	Motorcycle: {fields: map[string]rule{
		"make":            text(defaultTextMax),
		"model":           text(defaultTextMax),
		"model_year":      intRange(1885, 2100),
		"segment":         text(50),
		"displacement_cc": intRange(1, 5000),
		"power_hp":        decimalRange(0, 1000),
		"torque_nm":       decimalRange(0, 1000),
		"wet_weight_kg":   intRange(1, 1000),
		"seat_height_mm":  intRange(300, 1200),
		"license_class":   enum("AM", "A1", "A2", "A"),
	}},
	Car: {
		fields: map[string]rule{
			"make":              text(defaultTextMax),
			"model":             text(defaultTextMax),
			"trim":              text(defaultTextMax),
			"model_year":        intRange(1885, 2100),
			"body_type":         text(50),
			"fuel_type":         enum("petrol", "diesel", "hybrid", "plugin_hybrid", "electric", "lpg", "cng", "hydrogen", "other"),
			"transmission":      enum("manual", "automatic"),
			"power_hp":          decimalRange(0, 2000),
			"seats":             intRange(1, 9),
			"electric_range_km": intRange(1, 2000),
		},
		cross: func(out map[string]any) []apperr.FieldError {
			fuel, hasFuel := out["fuel_type"].(string)
			_, hasRange := out["electric_range_km"]
			if hasRange && hasFuel && fuel != "electric" && fuel != "plugin_hybrid" {
				return []apperr.FieldError{{Field: "metadata.electric_range_km", Message: "is only allowed for electric and plugin_hybrid cars"}}
			}
			return nil
		},
	},
	Camera: {fields: map[string]rule{
		"make":          text(defaultTextMax),
		"model":         text(defaultTextMax),
		"release_year":  intRange(1900, 2100),
		"camera_type":   enum("mirrorless", "dslr", "compact", "rangefinder", "medium_format", "film", "action", "other"),
		"sensor_format": enum("full_frame", "aps_c", "micro_four_thirds", "one_inch", "medium_format", "film_35mm", "other"),
		"megapixels":    decimalRange(0.1, 500),
		"lens_mount":    text(50),
		"weight_g":      intRange(1, 10000),
	}},
}

// Known reports whether kind is one of the six MVP kinds.
func Known(kind string) bool {
	_, ok := registry[kind]
	return ok
}

// Validate checks metadata against the kind and returns the normalized map to
// store: trimmed text, `null` keys dropped, integers as int, decimals rounded
// to one place. The error is *apperr.UnknownFieldError for an undeclared key
// (path "metadata.<key>") or *apperr.ValidationError with one entry per bad
// field; an undeclared key wins over field errors. An unknown kind is a
// validation error on `kind`.
func Validate(kind string, in map[string]any) (map[string]any, error) {
	sc, ok := registry[kind]
	if !ok {
		return nil, apperr.Validation(apperr.FieldError{Field: "kind", Message: "is not a known item kind"})
	}
	out := make(map[string]any, len(in))
	var errs []apperr.FieldError
	for _, key := range slices.Sorted(maps.Keys(in)) {
		r, declared := sc.fields[key]
		if !declared {
			return nil, apperr.UnknownField("metadata." + key)
		}
		if in[key] == nil {
			continue
		}
		res := r("metadata."+key, in[key])
		if res.unknown != "" {
			return nil, apperr.UnknownField(res.unknown)
		}
		if len(res.errs) > 0 {
			errs = append(errs, res.errs...)
			continue
		}
		out[key] = res.val
	}
	if sc.cross != nil && len(errs) == 0 {
		errs = append(errs, sc.cross(out)...)
	}
	if len(errs) > 0 {
		return nil, apperr.Validation(errs...)
	}
	return out, nil
}

func fail(path, format string, args ...any) outcome {
	return outcome{errs: []apperr.FieldError{{Field: path, Message: fmt.Sprintf(format, args...)}}}
}

// number reads a JSON number (float64, json.Number or a Go integer).
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, !math.IsNaN(n) && !math.IsInf(n, 0)
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func intRange(lo, hi int) rule {
	return func(path string, v any) outcome {
		f, ok := number(v)
		if !ok || f != math.Trunc(f) {
			return fail(path, "must be an integer")
		}
		if f < float64(lo) || f > float64(hi) {
			return fail(path, "must be between %d and %d", lo, hi)
		}
		return outcome{val: int(f)}
	}
}

// decimalRange accepts numbers with at most one decimal place.
func decimalRange(lo, hi float64) rule {
	return func(path string, v any) outcome {
		f, ok := number(v)
		if !ok {
			return fail(path, "must be a number")
		}
		tenths := f * 10
		if math.Abs(tenths-math.Round(tenths)) > 1e-6 {
			return fail(path, "must have at most 1 decimal place")
		}
		f = math.Round(tenths) / 10
		if f < lo || f > hi {
			return fail(path, "must be between %v and %v", lo, hi)
		}
		return outcome{val: f}
	}
}

func text(maxLen int) rule {
	return func(path string, v any) outcome {
		s, ok := v.(string)
		if !ok {
			return fail(path, "must be a string")
		}
		s = strings.TrimSpace(s)
		if n := utf8.RuneCountInString(s); n < 1 || n > maxLen {
			return fail(path, "must be 1 to %d characters", maxLen)
		}
		return outcome{val: s}
	}
}

func enum(values ...string) rule {
	return func(path string, v any) outcome {
		s, ok := v.(string)
		if !ok {
			return fail(path, "must be a string")
		}
		for _, allowed := range values {
			if s == allowed {
				return outcome{val: s}
			}
		}
		return fail(path, "must be one of %s", strings.Join(values, ", "))
	}
}

func imdbURL(path string, v any) outcome {
	s, ok := v.(string)
	if !ok {
		return fail(path, "must be a string")
	}
	s = strings.TrimSpace(s)
	if !imdbRe.MatchString(s) {
		return fail(path, "must be an IMDb title URL like https://www.imdb.com/title/tt0133093/")
	}
	return outcome{val: s}
}

// streaming checks {country (required), platforms}. Undeclared keys inside it
// are reported as unknown fields too.
func streaming(path string, v any) outcome {
	obj, ok := v.(map[string]any)
	if !ok {
		return fail(path, "must be an object")
	}
	for _, key := range slices.Sorted(maps.Keys(obj)) {
		if key != "country" && key != "platforms" {
			return outcome{unknown: path + "." + key}
		}
	}
	out := map[string]any{}
	var errs []apperr.FieldError
	switch c := obj["country"].(type) {
	case nil:
		errs = append(errs, apperr.FieldError{Field: path + ".country", Message: "is required"})
	case string:
		if !countryRe.MatchString(c) {
			errs = append(errs, apperr.FieldError{Field: path + ".country", Message: "must be an ISO 3166-1 alpha-2 code like IT"})
		} else {
			out["country"] = c
		}
	default:
		errs = append(errs, apperr.FieldError{Field: path + ".country", Message: "must be a string"})
	}
	if raw, present := obj["platforms"]; present && raw != nil {
		platforms, perrs := platformList(path+".platforms", raw)
		errs = append(errs, perrs...)
		if len(perrs) == 0 {
			out["platforms"] = platforms
		}
	}
	if len(errs) > 0 {
		return outcome{errs: errs}
	}
	return outcome{val: out}
}

func platformList(path string, raw any) ([]string, []apperr.FieldError) {
	list, ok := raw.([]any)
	if !ok {
		return nil, []apperr.FieldError{{Field: path, Message: "must be an array of strings"}}
	}
	if len(list) > maxPlatforms {
		return nil, []apperr.FieldError{{Field: path, Message: fmt.Sprintf("must have at most %d entries", maxPlatforms)}}
	}
	out := make([]string, 0, len(list))
	seen := make(map[string]bool, len(list))
	for i, item := range list {
		field := fmt.Sprintf("%s[%d]", path, i)
		s, ok := item.(string)
		if !ok {
			return nil, []apperr.FieldError{{Field: field, Message: "must be a string"}}
		}
		s = strings.TrimSpace(s)
		if n := utf8.RuneCountInString(s); n < 1 || n > maxPlatformLen {
			return nil, []apperr.FieldError{{Field: field, Message: fmt.Sprintf("must be 1 to %d characters", maxPlatformLen)}}
		}
		key := strings.ToLower(s)
		if seen[key] {
			return nil, []apperr.FieldError{{Field: field, Message: "is a duplicate platform"}}
		}
		seen[key] = true
		out = append(out, s)
	}
	return out, nil
}
