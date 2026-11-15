package order

import (
	"slices"
	"testing"

	"github.com/google/uuid"
)

func TestCaller(t *testing.T) {
	a, b, c, d := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	tests := []struct {
		name     string
		ids      []uuid.UUID
		position map[uuid.UUID]int
		want     []uuid.UUID
	}{
		{"no caller order keeps the shared order", []uuid.UUID{a, b, c}, nil, []uuid.UUID{a, b, c}},
		{"ordered entries first by position", []uuid.UUID{a, b, c, d}, map[uuid.UUID]int{c: 0, a: 1}, []uuid.UUID{c, a, b, d}},
		{"unordered keep their relative shared order", []uuid.UUID{d, a, c, b}, map[uuid.UUID]int{b: 0}, []uuid.UUID{b, d, a, c}},
		{"empty", nil, nil, []uuid.UUID{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := slices.Clone(tt.ids)
			got := Caller(tt.ids, tt.position)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("Caller = %v, want %v", got, tt.want)
			}
			if !slices.Equal(in, tt.ids) {
				t.Fatalf("Caller modified its input: %v", tt.ids)
			}
		})
	}
}

func TestPlace(t *testing.T) {
	a, b, c, x := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	ptr := func(id uuid.UUID) *uuid.UUID { return &id }
	tests := []struct {
		name   string
		ids    []uuid.UUID
		moved  uuid.UUID
		before *uuid.UUID
		want   []uuid.UUID
		ok     bool
	}{
		{"into a list in front of the first", []uuid.UUID{a, b}, x, ptr(a), []uuid.UUID{x, a, b}, true},
		{"into a list in front of the last", []uuid.UUID{a, b}, x, ptr(b), []uuid.UUID{a, x, b}, true},
		{"nil before appends", []uuid.UUID{a, b}, x, nil, []uuid.UUID{a, b, x}, true},
		{"same list reorders: moved leaves its place", []uuid.UUID{a, x, b, c}, x, ptr(c), []uuid.UUID{a, b, x, c}, true},
		{"same list, before itself is not a sibling", []uuid.UUID{a, x, b}, x, ptr(x), nil, false},
		{"before is not among the siblings", []uuid.UUID{a, b}, x, ptr(c), nil, false},
		{"empty list, before is missing", nil, x, ptr(a), nil, false},
		{"empty list, appends", nil, x, nil, []uuid.UUID{x}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := slices.Clone(tt.ids)
			got, ok := Place(tt.ids, tt.moved, tt.before)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if tt.ok && !slices.Equal(got, tt.want) {
				t.Fatalf("Place = %v, want %v", got, tt.want)
			}
			if !slices.Equal(in, tt.ids) {
				t.Fatalf("Place modified its input: %v", tt.ids)
			}
		})
	}
}
