package lookup

import (
	"errors"
	"sort"
	"strings"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/items/types"
)

// validateImport validates lookup metadata like items.ValidateImport, but
// provider data never blocks a write (R28): a top-level key that fails is
// dropped (replaced by fallback[key] when the item already stores one) and the
// check runs again. Keys in keep (the provider id, values the client sent) are
// never dropped: their errors are returned. It returns the valid metadata and
// the sorted names of the dropped fields.
func validateImport(typ *string, fields []types.Field, in, fallback map[string]any, keep map[string]bool) (map[string]any, []string, error) {
	work := make(map[string]any, len(in))
	for k, v := range in {
		work[k] = v
	}
	var dropped []string
	for {
		out, err := items.ValidateImport(typ, fields, work)
		if err == nil {
			sort.Strings(dropped)
			return out, dropped, nil
		}
		var ve *apperr.ValidationError
		if !errors.As(err, &ve) {
			return nil, nil, err
		}
		progress := false
		for _, fe := range ve.Errors {
			key := topKey(strings.TrimPrefix(fe.Field, "metadata."))
			if _, ok := work[key]; !ok || keep[key] {
				continue
			}
			if old, ok := fallback[key]; ok {
				work[key] = old
			} else {
				delete(work, key)
			}
			dropped = append(dropped, key)
			progress = true
		}
		if !progress {
			return nil, nil, err
		}
		// A stored fallback that is itself invalid is dropped on the next pass.
		fallback = nil
	}
}

func topKey(path string) string {
	if i := strings.IndexAny(path, ".["); i >= 0 {
		return path[:i]
	}
	return path
}

func (s *DBStore) warnDropped(dropped []string) {
	if len(dropped) > 0 && s.log != nil {
		s.log.Warn("lookup: dropped invalid provider fields", "fields", dropped)
	}
}
