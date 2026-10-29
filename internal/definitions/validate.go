package definitions

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/lists"
)

// Pure rules of the type and field service: no I/O, so they are tested without
// a database. The helpers are named apart from the old service.go (removed in
// B3c T4); the rules are the same.

const (
	maxFieldLabelRunes = 100
	maxDefaultPins     = 3
	maxFieldKeyLen     = 32
	iconRule           = "must be one emoji or i: followed by a lowercase name of 1 to 40 letters, digits or hyphens"
)

// isFieldKey reports whether key is a field key: a lowercase letter, then up to 31
// lowercase letters, digits or underscores. "store_links" is reserved.
func isFieldKey(key string) bool {
	if key == "store_links" || len(key) == 0 || len(key) > maxFieldKeyLen || key[0] < 'a' || key[0] > 'z' {
		return false
	}
	for _, r := range key[1:] {
		valid := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_'
		if !valid {
			return false
		}
	}
	return true
}

// normalizeTypeName trims a type name and checks it has 1 to 100 characters.
func normalizeTypeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 100 {
		return "", apperr.Validation(apperr.FieldError{Field: "name", Message: "must be 1 to 100 characters"})
	}
	return name, nil
}

// normalizeFieldLabel trims a field label and checks it has 1 to 100 characters.
func normalizeFieldLabel(label string) (string, error) {
	label = strings.TrimSpace(label)
	if n := utf8.RuneCountInString(label); n == 0 || n > maxFieldLabelRunes {
		return "", apperr.Validation(apperr.FieldError{Field: "label", Message: "must be 1 to 100 characters"})
	}
	return label, nil
}

// isCustomFieldType reports whether a user may create a field of this type.
func isCustomFieldType(fieldType string) bool {
	return fieldType == "text" || fieldType == "number" || fieldType == "date" || fieldType == "boolean" || fieldType == "link"
}

// keyFromLabel lowercases s and keeps letters and digits, joining runs of other
// characters with one underscore. It may return "".
func keyFromLabel(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "_") {
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

// fieldKeyFromLabel derives the key of a new custom field: a label without a
// letter is refused, a key starting with a digit gets a "field_" prefix, and the
// key is at most 32 characters.
func fieldKeyFromLabel(label string) (string, error) {
	key := keyFromLabel(label)
	if key == "" {
		return "", apperr.Validation(apperr.FieldError{Field: "label", Message: "must contain a letter"})
	}
	if key[0] >= '0' && key[0] <= '9' {
		key = "field_" + key
	}
	if len(key) > maxFieldKeyLen {
		key = strings.TrimRight(key[:maxFieldKeyLen], "_")
	}
	return key, nil
}

// checkDefaultIcon accepts an emoji or an i: icon name (see lists.ValidIcon).
func checkDefaultIcon(icon string) error {
	if !lists.ValidIcon(icon) {
		return apperr.Validation(apperr.FieldError{Field: "default_icon", Message: iconRule})
	}
	return nil
}

// checkNewTypeDefaults validates the defaults of a type that is being created,
// before it has fields. Only price is known: a default sort or pin of "price"
// needs AllowsPrice, any other key must be a field key. It returns the first error.
func checkNewTypeDefaults(in CreateType) error {
	if in.DefaultIcon != nil {
		if err := checkDefaultIcon(*in.DefaultIcon); err != nil {
			return err
		}
	}
	if in.DefaultSortField != nil && (!isFieldKey(*in.DefaultSortField) || (*in.DefaultSortField == "price" && !in.AllowsPrice)) {
		return apperr.Validation(apperr.FieldError{Field: "default_sort_field", Message: "must be a field key"})
	}
	if in.DefaultPinnedFields == nil {
		return nil
	}
	if len(*in.DefaultPinnedFields) > maxDefaultPins {
		return apperr.Validation(apperr.FieldError{Field: "default_pinned_fields", Message: "must have at most 3 entries"})
	}
	seen := map[string]bool{}
	for i, pin := range *in.DefaultPinnedFields {
		if (!isFieldKey(pin) || (pin == "price" && !in.AllowsPrice)) || seen[pin] {
			return apperr.Validation(apperr.FieldError{Field: fmt.Sprintf("default_pinned_fields[%d]", i), Message: "must be a unique field key"})
		}
		seen[pin] = true
	}
	return nil
}

// checkDefaults validates the defaults of an existing type against its fields.
// The allowed pins are the field keys plus "price" when the type allows it. A
// stores field is pinnable but never a sort key (F-016). All problems are
// reported together.
func checkDefaults(allowsPrice bool, fields []Field, sort *string, pins *[]string) error {
	allowed := make(map[string]struct{}, len(fields)+1)
	sortable := make(map[string]struct{}, len(fields)+1)
	for _, field := range fields {
		allowed[field.Key] = struct{}{}
		if field.Type != "stores" {
			sortable[field.Key] = struct{}{}
		}
	}
	if allowsPrice {
		allowed["price"] = struct{}{}
		sortable["price"] = struct{}{}
	}
	var errs []apperr.FieldError
	if sort != nil && (!isFieldKey(*sort) || !inKeySet(sortable, *sort)) {
		errs = append(errs, apperr.FieldError{Field: "default_sort_field", Message: "must be a field key of this type"})
	}
	if pins != nil {
		if len(*pins) > maxDefaultPins {
			errs = append(errs, apperr.FieldError{Field: "default_pinned_fields", Message: "must have at most 3 entries"})
		} else {
			seen := make(map[string]struct{}, len(*pins))
			for i, pin := range *pins {
				if !isFieldKey(pin) || !inKeySet(allowed, pin) {
					errs = append(errs, apperr.FieldError{Field: fmt.Sprintf("default_pinned_fields[%d]", i), Message: "must be a field key of this type"})
					continue
				}
				if _, exists := seen[pin]; exists {
					errs = append(errs, apperr.FieldError{Field: fmt.Sprintf("default_pinned_fields[%d]", i), Message: "must be unique"})
				}
				seen[pin] = struct{}{}
			}
		}
	}
	if len(errs) > 0 {
		return apperr.Validation(errs...)
	}
	return nil
}

func inKeySet(set map[string]struct{}, key string) bool {
	_, ok := set[key]
	return ok
}

// sameOptionalString compares two nullable strings; nil equals only nil.
func sameOptionalString(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// sameStringList compares two nullable lists; nil equals only nil.
func sameStringList(a, b *[]string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return slices.Equal(*a, *b)
}

// sameKeySet reports whether a and b hold the same keys, each once.
func sameKeySet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]struct{}, len(a))
	for _, key := range a {
		seen[key] = struct{}{}
	}
	for _, key := range b {
		if _, ok := seen[key]; !ok {
			return false
		}
		delete(seen, key)
	}
	return len(seen) == 0
}
