// Package types validates and normalizes item metadata from type definitions.
// It is pure Go: callers load the workspace-scoped definitions from the
// database. Every key is optional and a null value means "key omitted".
package types

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
)

// Type names (also the `type` enum of the spec).
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
	maxLinkLen     = 2048
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

// Field is one persisted type field definition, reduced to the values needed
// for metadata validation.
type Field struct {
	Key     string
	Type    string
	Special bool // locked built-in field with an established wire shape
}

// Known reports whether type is one of the six built-in types. Definition
// backed callers must not use it to decide whether a workspace type exists.
func Known(typ string) bool {
	return slices.Contains([]string{Generic, Movie, TVShow, Motorcycle, Car, Camera}, typ)
}

// Validate checks metadata against the supplied type fields and returns the
// normalized map to store. Streaming and TMDB bookkeeping retain their
// established validators by key; all other fields follow their stored type.
// An undeclared key wins over field errors.
func Validate(fields []Field, in map[string]any) (map[string]any, error) {
	rules := make(map[string]rule, len(fields))
	for _, field := range fields {
		rules[field.Key] = ruleFor(field)
	}
	out := make(map[string]any, len(in))
	var errs []apperr.FieldError
	for _, key := range slices.Sorted(maps.Keys(in)) {
		r, declared := rules[key]
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
	if len(errs) > 0 {
		return nil, apperr.Validation(errs...)
	}
	return out, nil
}

func ruleFor(field Field) rule {
	// These keys are data imported from TMDB or maintain lookup state. Their
	// wire shape must remain independent of a definition label/type edit.
	if field.Special {
		switch field.Key {
		case "streaming":
			return streaming
		case "tmdb_id":
			return positiveInteger
		case "lookup_disabled":
			return boolean
		case "imdb_url":
			return imdbURL
		}
	}
	switch field.Type {
	case "text":
		return text(defaultTextMax)
	case "number":
		return finiteNumber
	case "date":
		return date
	case "boolean":
		return boolean
	case "link":
		return link
	case "streaming":
		return streaming
	default:
		return func(path string, _ any) outcome { return fail(path, "has an unsupported field type") }
	}
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

func positiveInteger(path string, v any) outcome {
	f, ok := number(v)
	if !ok || f != math.Trunc(f) || f < 1 || f > 9007199254740991 {
		return fail(path, "must be a positive integer")
	}
	return outcome{val: int(f)}
}

func finiteNumber(path string, v any) outcome {
	f, ok := number(v)
	if !ok {
		return fail(path, "must be a finite number")
	}
	return outcome{val: f}
}

func boolean(path string, v any) outcome {
	b, ok := v.(bool)
	if !ok {
		return fail(path, "must be a boolean")
	}
	return outcome{val: b}
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

func date(path string, v any) outcome {
	s, ok := v.(string)
	if !ok {
		return fail(path, "must be an ISO date")
	}
	if _, err := time.Parse(time.DateOnly, s); err != nil {
		return fail(path, "must be an ISO date")
	}
	return outcome{val: s}
}

func link(path string, v any) outcome {
	s, ok := v.(string)
	if !ok {
		return fail(path, "must be an http or https URL")
	}
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxLinkLen {
		return fail(path, "must be at most %d characters", maxLinkLen)
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fail(path, "must be an http or https URL")
	}
	return outcome{val: s}
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

// streaming checks {country (required), platforms, paid_platforms}. Undeclared keys inside it
// are reported as unknown fields too.
func streaming(path string, v any) outcome {
	obj, ok := v.(map[string]any)
	if !ok {
		return fail(path, "must be an object")
	}
	for _, key := range slices.Sorted(maps.Keys(obj)) {
		if key != "country" && key != "platforms" && key != "paid_platforms" {
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
	if raw, present := obj["paid_platforms"]; present && raw != nil {
		paid, perrs := platformList(path+".paid_platforms", raw)
		if len(perrs) == 0 {
			// Every paid platform must be one of platforms; store the platform's casing.
			offered, _ := out["platforms"].([]string)
			byKey := make(map[string]string, len(offered))
			for _, p := range offered {
				byKey[strings.ToLower(p)] = p
			}
			for i, p := range paid {
				match, ok := byKey[strings.ToLower(p)]
				if !ok {
					perrs = append(perrs, apperr.FieldError{Field: fmt.Sprintf("%s.paid_platforms[%d]", path, i), Message: "must be one of platforms"})
					continue
				}
				paid[i] = match
			}
		}
		errs = append(errs, perrs...)
		if len(perrs) == 0 && len(paid) > 0 {
			out["paid_platforms"] = paid
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

// maxPinned is the most field keys a list can pin (F009-B2): the first is the
// primary field, the second the secondary.
const maxPinned = 2

// PriceKey is the item column that can be pinned next to a type's metadata fields.
const PriceKey = "price"

// priceTypes are the types whose items can carry a price (F009-B1): price lives
// only on motorcycle, car and camera items. Generic, movie and tv_show items
// have none.
var priceTypes = map[string]bool{Motorcycle: true, Car: true, Camera: true}

var builtinPinnableKeys = map[string][]string{
	Generic:    {},
	Movie:      {"director", "imdb_url", "release_year", "runtime_minutes", "streaming"},
	TVShow:     {"end_year", "episode_minutes", "episodes_per_season", "first_air_year", "imdb_url", "seasons", "streaming"},
	Motorcycle: {"displacement_cc", "license_class", "model_year", "power_hp"},
	Car:        {"fuel_type", "make", "model", "model_year", "power_hp"},
	Camera:     {"camera_type", "megapixels", "release_year", "sensor_format"},
}

// HasPrice reports whether items of this type can carry a price (F009-B1).
func HasPrice(typ string) bool { return priceTypes[typ] }

// PinnableKeys returns the field keys a list of this type can pin, sorted: the
// type's metadata keys, plus "price" only on the types that have one. This is
// retained temporarily for the pre-F014-B4 list APIs; item validation itself
// is definition-backed.
func PinnableKeys(typ string) []string {
	fields, ok := builtinPinnableKeys[typ]
	if !ok {
		return nil
	}
	if !HasPrice(typ) {
		return append([]string{}, fields...)
	}
	out := append([]string{}, fields...)
	out = append(out, PriceKey)
	slices.Sort(out)
	return out
}

// ValidatePinned checks the pinned fields of a list of this type (F009-B2): an
// ordered list of at most 2 unique pinnable keys, the first the primary. Errors
// are on "pinned_fields[i]". The result is never nil for an unknown type (which
// errors) but is the empty list for "pins nothing". A key that is not (or no
// longer) a field of the type is rejected: a list of a price-less type cannot
// pin "price".
func ValidatePinned(typ string, keys []string) ([]string, error) {
	if !Known(typ) {
		return nil, apperr.Validation(apperr.FieldError{Field: "type", Message: "is not a known item type"})
	}
	allowed := PinnableKeys(typ)
	var errs []apperr.FieldError
	if len(keys) > maxPinned {
		errs = append(errs, apperr.FieldError{Field: "pinned_fields", Message: fmt.Sprintf("must have at most %d entries", maxPinned)})
	}
	seen := make(map[string]bool, len(keys))
	for i, k := range keys {
		field := fmt.Sprintf("pinned_fields[%d]", i)
		switch {
		case len(allowed) == 0:
			errs = append(errs, apperr.FieldError{Field: field, Message: "lists of this type have no pinnable fields"})
		case !slices.Contains(allowed, k):
			errs = append(errs, apperr.FieldError{Field: field, Message: "must be one of " + strings.Join(allowed, ", ")})
		case seen[k]:
			errs = append(errs, apperr.FieldError{Field: field, Message: "is a duplicate key"})
		}
		seen[k] = true
	}
	if len(errs) > 0 {
		return nil, apperr.Validation(errs...)
	}
	return append([]string{}, keys...), nil
}
