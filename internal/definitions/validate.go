package definitions

import (
	"strings"
	"unicode/utf8"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
)

// Pure rules of the type and field service: no I/O, so they are tested without
// a database.

const (
	maxFieldLabelRunes = 100
	maxFieldKeyLen     = 32
)

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
