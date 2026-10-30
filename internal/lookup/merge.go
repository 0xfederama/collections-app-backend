package lookup

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/items/types"
)

const maxLookupTitleRunes = 300

func validateApply(p ApplyParams) error {
	if p.Mode == ModeCreateUntracked {
		// No provider, no source id: only a book create with the typed input.
		if p.Type != types.Book || p.CreateInput == nil {
			return fmt.Errorf("untracked lookup create needs a book and its input")
		}
		if _, err := cleanLookupTitle(p.Title); err != nil {
			return err
		}
		return nil
	}
	if !slices.Contains(providerTypes[p.Provider], p.Type) {
		return apperr.Validation(apperr.FieldError{Field: "type", Message: "lookup is available only for movie, tv_show and book"})
	}
	if err := ValidSourceID(p.Provider, p.SourceID); err != nil {
		return err
	}
	switch p.Mode {
	case ModeCreate, ModeFill, ModeRematch, ModeRefresh, ModeOwned:
	default:
		return fmt.Errorf("unknown lookup mode %q", p.Mode)
	}
	if _, err := cleanLookupTitle(p.Title); err != nil {
		return err
	}
	if p.Mode == ModeCreate && p.CreateInput == nil {
		return fmt.Errorf("lookup create input is missing")
	}
	return nil
}

func mergeMetadata(mode Mode, current, candidate map[string]any, old Record, hasOld bool, idKey string) (map[string]any, map[string]any) {
	out := cloneLookupMap(current)
	flatCandidate := flatten(candidate)
	snapshots := map[string]any{}
	switch mode {
	case ModeRematch, ModeRefresh:
		if hasOld {
			removeUnchangedOwned(out, old.ImportedFields)
		}
		for path, value := range flatCandidate {
			setPath(out, path, value)
			snapshots[path] = value
		}
	case ModeOwned:
		if hasOld {
			for path, previous := range old.ImportedFields {
				currentValue, present := getPath(out, path)
				if !present || !equalValue(currentValue, previous) {
					continue
				}
				if fresh, ok := flatCandidate[path]; ok {
					setPath(out, path, fresh)
					snapshots[path] = fresh
				} else {
					deletePath(out, path)
				}
			}
		}
		for path, value := range flatCandidate {
			if _, alreadyOwned := snapshots[path]; alreadyOwned {
				continue
			}
			if _, present := getPath(out, path); !present {
				setPath(out, path, value)
				snapshots[path] = value
			}
		}
	default: // fill
		if hasOld {
			for path, previous := range old.ImportedFields {
				if currentValue, present := getPath(out, path); present && equalValue(currentValue, previous) {
					snapshots[path] = currentValue
				}
			}
		}
		for path, value := range flatCandidate {
			if _, present := getPath(out, path); !present {
				setPath(out, path, value)
				snapshots[path] = value
			}
		}
	}
	// The provider identity is part of every successful match, including fill.
	// Maintenance relies on this owned snapshot to detect user removal/opt-out.
	setPath(out, idKey, flatCandidate[idKey])
	snapshots[idKey] = flatCandidate[idKey]
	return out, snapshots
}

// fillOnly sets provider values that are applied only where the item has none:
// a missing (or null) key and, with emptyLists, an empty list. They are not
// snapshots, so they are never treated as imported and never replaced or removed.
func fillOnly(metadata, values map[string]any, emptyLists bool) {
	for path, value := range flatten(values) {
		current, present := getPath(metadata, path)
		if present && current != nil {
			if list, isList := current.([]any); !emptyLists || !isList || len(list) != 0 {
				continue
			}
		}
		setPath(metadata, path, normalizeValue(value))
	}
}

func mergeTitle(p ApplyParams, current string, old Record, hasOld bool) (string, *string, string) {
	title, _ := cleanLookupTitle(p.Title)
	original := p.OriginalTitle
	if original == "" {
		original = current
	}
	if hasOld {
		original = old.OriginalTitle
	}
	switch p.Mode {
	case ModeOwned:
		if hasOld && old.ImportedTitle != nil && current == *old.ImportedTitle {
			return title, &title, original
		}
		return current, nil, original
	case ModeFill:
		if hasOld && old.ImportedTitle != nil && current == *old.ImportedTitle {
			return current, old.ImportedTitle, original
		}
		return current, nil, original
	case ModeRematch, ModeRefresh:
		if hasOld && old.ImportedTitle != nil && current != *old.ImportedTitle {
			original = p.OriginalTitle
			if original == "" {
				original = current
			}
		}
		return title, &title, original
	default:
		return current, nil, original
	}
}

func removeUnchangedOwned(metadata map[string]any, snapshots map[string]any) {
	for path, previous := range snapshots {
		if current, ok := getPath(metadata, path); ok && equalValue(current, previous) {
			deletePath(metadata, path)
		}
	}
}

func cleanLookupTitle(raw string) (string, error) {
	title := strings.TrimSpace(raw)
	if n := utf8.RuneCountInString(title); n < 1 || n > maxLookupTitleRunes {
		return title, apperr.Validation(apperr.FieldError{Field: "title", Message: "must be 1 to 300 characters"})
	}
	return title, nil
}

func cleanLookupWebsite(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	s := strings.TrimSpace(*raw)
	u, err := url.ParseRequestURI(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(s) > 2048 {
		return nil, apperr.Validation(apperr.FieldError{Field: "website", Message: "must be an http or https URL of at most 2048 characters"})
	}
	return &s, nil
}

func lookupStorageKeys(id uuid.UUID, ext string) (string, string) {
	s := id.String()
	base := "images/" + s[:2] + "/" + s[2:4] + "/" + s
	return base + "." + ext, base + ".thumb.jpg"
}

func snapshotPresent(metadata map[string]any, requested map[string]any) map[string]any {
	out := map[string]any{}
	for path := range requested {
		if value, ok := getPath(metadata, path); ok {
			out[path] = value
		}
	}
	return out
}
