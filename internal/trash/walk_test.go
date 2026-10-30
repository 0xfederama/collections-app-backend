package trash

import (
	"slices"
	"testing"

	"github.com/google/uuid"
)

// fixture folders, named by letter so the cases read as trees:
//
//	A ── B ── C
//	│
//	D (trashed, with E below it)
//	F (its parent is missing from the rows)
//	G ↔ H (a corrupt cycle, both active)
type fixture struct {
	a, b, c, d, e, f, g, h, missing uuid.UUID
	nodes                           []FolderNode
}

func newFixture() fixture {
	var fx fixture
	fx.a, fx.b, fx.c, fx.d, fx.e, fx.f, fx.g, fx.h, fx.missing = uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	ptr := func(id uuid.UUID) *uuid.UUID { return &id }
	fx.nodes = []FolderNode{
		{ID: fx.a, Name: "A"},
		{ID: fx.b, ParentID: ptr(fx.a), Name: "B"},
		{ID: fx.c, ParentID: ptr(fx.b), Name: "C"},
		{ID: fx.d, ParentID: ptr(fx.a), Name: "D", Trashed: true},
		{ID: fx.e, ParentID: ptr(fx.d), Name: "E", Trashed: true},
		{ID: fx.f, ParentID: ptr(fx.missing), Name: "F"},
		{ID: fx.g, ParentID: ptr(fx.h), Name: "G"},
		{ID: fx.h, ParentID: ptr(fx.g), Name: "H"},
	}
	return fx
}

func sortedIDs(ids []uuid.UUID) []uuid.UUID {
	out := slices.Clone(ids)
	slices.SortFunc(out, func(x, y uuid.UUID) int { return slices.Compare(x[:], y[:]) })
	return out
}

func sameIDs(got, want []uuid.UUID) bool {
	if len(got) != len(want) {
		return false
	}
	return slices.Equal(sortedIDs(got), sortedIDs(want))
}

func TestActiveSubtree(t *testing.T) {
	fx := newFixture()
	tests := []struct {
		name string
		root uuid.UUID
		want []uuid.UUID
	}{
		{"active root with active children", fx.a, []uuid.UUID{fx.a, fx.b, fx.c}},
		{"leaf is its own subtree", fx.c, []uuid.UUID{fx.c}},
		{"trashed root has no subtree", fx.d, nil},
		{"missing root has no subtree", fx.missing, nil},
		{"cycle terminates", fx.g, []uuid.UUID{fx.g, fx.h}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := activeSubtree(fx.nodes, tc.root)
			if !sameIDs(got, tc.want) {
				t.Errorf("activeSubtree = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestActiveSubtreeStopsAtTrashedFolder(t *testing.T) {
	// A trashed middle folder ends the descent: its active child is not an active descendant.
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	nodes := []FolderNode{
		{ID: a, Name: "A"},
		{ID: b, ParentID: &a, Name: "B", Trashed: true},
		{ID: c, ParentID: &b, Name: "C"},
	}
	if got := activeSubtree(nodes, a); !sameIDs(got, []uuid.UUID{a}) {
		t.Errorf("activeSubtree = %v, want only the root", got)
	}
}

func TestSubtreeIncludesEveryState(t *testing.T) {
	fx := newFixture()
	tests := []struct {
		name string
		root uuid.UUID
		want []uuid.UUID
	}{
		{"active root with trashed descendants", fx.a, []uuid.UUID{fx.a, fx.b, fx.c, fx.d, fx.e}},
		{"trashed root and its subtree", fx.d, []uuid.UUID{fx.d, fx.e}},
		{"root not listed in the rows is still returned, with its children", fx.missing, []uuid.UUID{fx.missing, fx.f}},
		{"cycle terminates", fx.g, []uuid.UUID{fx.g, fx.h}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := subtree(fx.nodes, tc.root)
			if !sameIDs(got, tc.want) {
				t.Errorf("subtree = %v, want %v", got, tc.want)
			}
			if got[0] != tc.root {
				t.Errorf("subtree starts at %v, want the root %v", got[0], tc.root)
			}
		})
	}
}

func TestInSubtreeOf(t *testing.T) {
	fx := newFixture()
	tests := []struct {
		name      string
		candidate uuid.UUID
		folder    uuid.UUID
		want      bool
	}{
		{"candidate is the folder", fx.b, fx.b, true},
		{"descendant of the folder", fx.c, fx.a, true},
		{"descendant through a trashed folder", fx.e, fx.a, true},
		{"ancestor is not in the subtree", fx.a, fx.c, false},
		{"sibling is not in the subtree", fx.d, fx.b, false},
		{"unknown candidate is in no subtree", fx.missing, fx.a, false},
		{"unrelated folder in a cycle", fx.g, fx.a, false},
		{"cycle reaches the folder", fx.g, fx.h, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := inSubtreeOf(fx.nodes, tc.candidate, tc.folder); got != tc.want {
				t.Errorf("inSubtreeOf = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestActivePath(t *testing.T) {
	fx := newFixture()
	seg := func(id uuid.UUID, name string) PathSegment { return PathSegment{Type: TypeFolder, ID: id, Name: name} }
	tests := []struct {
		name   string
		folder uuid.UUID
		want   []PathSegment
	}{
		{"root first, folder last", fx.c, []PathSegment{seg(fx.a, "A"), seg(fx.b, "B"), seg(fx.c, "C")}},
		{"top-level folder", fx.a, []PathSegment{seg(fx.a, "A")}},
		{"trashed folder has no path", fx.d, []PathSegment{}},
		{"missing folder has no path", fx.missing, []PathSegment{}},
		{"missing parent ends the path", fx.f, []PathSegment{seg(fx.f, "F")}},
		{"cycle terminates", fx.g, []PathSegment{seg(fx.h, "H"), seg(fx.g, "G")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := activePath(fx.nodes, tc.folder)
			if got == nil || !slices.Equal(got, tc.want) {
				t.Errorf("activePath = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestActivePathStopsAtTrashedAncestor(t *testing.T) {
	// The active folder under a trashed parent keeps only itself: the path ends at the trashed ancestor.
	a, b := uuid.New(), uuid.New()
	nodes := []FolderNode{
		{ID: a, Name: "A", Trashed: true},
		{ID: b, ParentID: &a, Name: "B"},
	}
	want := []PathSegment{{Type: TypeFolder, ID: b, Name: "B"}}
	if got := activePath(nodes, b); !slices.Equal(got, want) {
		t.Errorf("activePath = %+v, want %+v", got, want)
	}
}
