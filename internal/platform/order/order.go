// Package order holds the sibling-order rules shared by folders, lists and the sidebar tree: which
// order a user sees at the top level, and where a moved entry lands before a given sibling. Pure
// functions over ids, no I/O.
package order

import (
	"cmp"
	"slices"

	"github.com/google/uuid"
)

// Caller returns ids in the order the caller sees them (GET /tree): the entries the caller ordered
// come first, by their own position; the others follow in the order they came in (the shared
// sort_order, then id). ids must already be in shared order. ids is not modified.
func Caller(ids []uuid.UUID, position map[uuid.UUID]int) []uuid.UUID {
	out := slices.Clone(ids)
	slices.SortStableFunc(out, func(a, b uuid.UUID) int {
		pa, okA := position[a]
		pb, okB := position[b]
		switch {
		case okA && okB:
			return cmp.Compare(pa, pb)
		case okA:
			return -1
		case okB:
			return 1
		}
		return 0
	})
	return out
}

// Place returns ids with moved taken out and put in front of before, or last when before is nil.
// ok is false when before is set but not among the other ids (unknown, not a sibling, or the moved
// entry itself). ids is not modified.
func Place(ids []uuid.UUID, moved uuid.UUID, before *uuid.UUID) (out []uuid.UUID, ok bool) {
	out = make([]uuid.UUID, 0, len(ids)+1)
	for _, id := range ids {
		if id != moved {
			out = append(out, id)
		}
	}
	if before == nil {
		return append(out, moved), true
	}
	i := slices.Index(out, *before)
	if i < 0 {
		return nil, false
	}
	return slices.Insert(out, i, moved), true
}
