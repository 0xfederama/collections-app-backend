package lists

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
)

// Limits and enums from DATA_MODEL §2 (the same rules are CHECK constraints).
const (
	maxNameLen          = 200
	maxDescriptionBytes = 64 << 10 // Markdown, decoded UTF-8 bytes
)

var displayModes = map[string]bool{"compact": true, "thumbnail": true, "grid": true}

// DefaultSortMode is the sort of a list created without sort_mode and the fallback when a type change leaves
// the stored sort without a meaning.
const DefaultSortMode = "manual"

// normalizePatch runs the checks of an update that need no row, in the order the API has always answered them:
// the name is trimmed and checked, the description is checked (an error here is returned at once) and an empty one
// becomes nil, then the display and sort modes and a blank type key are checked together. The result is what
// planUpdate takes: a trimmed name and a nil for an empty description, so "no change" compares the stored values.
func normalizePatch(p Patch) (Patch, error) {
	var errs []apperr.FieldError
	if p.Name != nil {
		name, fe := cleanName(*p.Name)
		p.Name = &name
		if fe != nil {
			errs = append(errs, *fe)
		}
	}
	if p.Description.Set {
		if err := validateDescription(p.Description.Value); err != nil {
			return Patch{}, err
		}
		p.Description.Value = nilIfEmpty(p.Description.Value)
	}
	errs = append(errs, enumErrors(p.DisplayMode, p.SortMode)...)
	if p.Type.Set {
		errs = append(errs, typeErrors(p.Type.Value)...)
	}
	if err := validate(errs); err != nil {
		return Patch{}, err
	}
	return p, nil
}

// validateListFields checks a create request and returns the trimmed name.
func validateListFields(name *string, displayMode, sortMode *string) (string, []apperr.FieldError) {
	var errs []apperr.FieldError
	clean, fe := cleanName(*name)
	if fe != nil {
		errs = append(errs, *fe)
	}
	errs = append(errs, enumErrors(displayMode, sortMode)...)
	return clean, errs
}

// cleanName trims name and checks the 1–200 character rule.
func cleanName(name string) (string, *apperr.FieldError) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxNameLen {
		return name, &apperr.FieldError{Field: "name", Message: fmt.Sprintf("must be 1 to %d characters", maxNameLen)}
	}
	return name, nil
}

func validate(errs []apperr.FieldError) error {
	if len(errs) == 0 {
		return nil
	}
	return apperr.Validation(errs...)
}

// typeErrors checks that a given type key is not blank (nil is a list with no
// type); existence is checked against this workspace's definitions inside the
// write transaction.
func typeErrors(typ *string) []apperr.FieldError {
	if typ != nil && strings.TrimSpace(*typ) == "" {
		return []apperr.FieldError{{Field: "type", Message: "must not be empty (send null for no type)"}}
	}
	return nil
}

func unknownListType() error {
	return apperr.Validation(apperr.FieldError{Field: "type", Message: "must name a type defined in this workspace"})
}

// validateDescription applies the Markdown description rules, like the item
// content: at most 64 KiB of decoded UTF-8 bytes (413), valid UTF-8 without NUL
// (422; Postgres text cannot store NUL).
func validateDescription(d *string) error {
	if d == nil {
		return nil
	}
	if len(*d) > maxDescriptionBytes {
		return apperr.PayloadTooLarge(maxDescriptionBytes)
	}
	if !utf8.ValidString(*d) {
		return apperr.Validation(apperr.FieldError{Field: "description", Message: "must be valid UTF-8"})
	}
	if strings.ContainsRune(*d, 0) {
		return apperr.Validation(apperr.FieldError{Field: "description", Message: "must not contain NUL characters"})
	}
	return nil
}

// nilIfEmpty turns the empty description into nil: both clear it.
func nilIfEmpty(d *string) *string {
	if d != nil && *d == "" {
		return nil
	}
	return d
}

func enumErrors(displayMode, sortMode *string) []apperr.FieldError {
	var errs []apperr.FieldError
	if displayMode != nil && !displayModes[*displayMode] {
		errs = append(errs, apperr.FieldError{Field: "display_mode", Message: "must be one of compact, thumbnail, grid"})
	}
	return errs
}

// validateDefinitionSort checks a sort mode against the built-in modes and the fields of the list's type.
// typ is only used for the message (nil is a list with no type, which has no fields).
func validateDefinitionSort(typ *string, sortMode string, allowsPrice bool, fields []FieldInfo, field string) error {
	if sortMode == "manual" || sortMode == "title" || sortMode == "updated" || (sortMode == "price" && allowsPrice) {
		return nil
	}
	for _, f := range fields {
		if f.Key == sortMode && f.FieldType != "stores" && sortMode != "manual" && sortMode != "title" && sortMode != "updated" && sortMode != "price" {
			return nil
		}
	}
	if typ == nil {
		return apperr.Validation(apperr.FieldError{Field: field, Message: "must be a built-in sort mode (a list with no type has no fields)"})
	}
	return apperr.Validation(apperr.FieldError{Field: field, Message: fmt.Sprintf("must be a built-in sort mode or a field of type %s", *typ)})
}

// validateDefinitionPins checks the ordered pinned keys against the type (at most 3, each a field of the type or
// price when the type allows it, no duplicates) and returns them normalized (never nil).
func validateDefinitionPins(allowsPrice bool, fields []FieldInfo, in *PinnedFields) (*PinnedFields, error) {
	if len(*in) > 3 {
		return nil, apperr.Validation(apperr.FieldError{Field: "pinned_fields", Message: "must have at most 3 entries"})
	}
	seen := make(map[string]bool, len(*in))
	for i, key := range *in {
		valid := key == "price" && allowsPrice
		for _, f := range fields {
			valid = valid || f.Key == key
		}
		if !valid {
			return nil, apperr.Validation(apperr.FieldError{Field: fmt.Sprintf("pinned_fields[%d]", i), Message: "must name a field of this type"})
		}
		if seen[key] {
			return nil, apperr.Validation(apperr.FieldError{Field: fmt.Sprintf("pinned_fields[%d]", i), Message: "must be unique"})
		}
		seen[key] = true
	}
	p := *in
	if p == nil {
		p = PinnedFields{}
	}
	return &p, nil
}

func equalPinned(a, b *PinnedFields) bool {
	if a == nil || b == nil {
		return a == b
	}
	return slices.Equal(*a, *b)
}

func equalPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
