package items_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/items"
)

func TestF026B2_Create_UniqueTitle(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  string
	}{
		{"same title", "Amélie", "conflict:duplicate_title"},
		{"trimmed, case and accent insensitive", "  amelie ", "conflict:duplicate_title"},
		{"decomposed accent", "Amélie", "conflict:duplicate_title"},
		{"another title", "Amelie 2", "ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newFakeDB(t)
			l := db.addList(movie, "manual")
			db.addItem(l, "Amélie", 0, "")
			other := db.addList(movie, "manual")
			db.addItem(other, "Other", 0, "")
			_, err := items.NewService(db, db, db, fixedClock{}).Create(t.Context(), l, items.CreateInput{Title: tt.title})
			if got := describe(err); got != tt.want {
				t.Fatalf("Create = %s, want %s", got, tt.want)
			}
		})
	}

	t.Run("the same title in another list is fine", func(t *testing.T) {
		db := newFakeDB(t)
		db.addItem(db.addList(movie, "manual"), "Amélie", 0, "")
		l := db.addList(movie, "manual")
		_, err := items.NewService(db, db, db, fixedClock{}).Create(t.Context(), l, items.CreateInput{Title: "amelie"})
		if got := describe(err); got != "ok" {
			t.Fatalf("Create = %s", got)
		}
	})
}

func TestF026B2_Update_UniqueTitle(t *testing.T) {
	tests := []struct {
		name  string
		patch items.Patch
		want  string
	}{
		{"rename to another item's title", items.Patch{Title: strPtr("b")}, "conflict:duplicate_title"},
		{"rename to a variant of another item's title", items.Patch{Title: strPtr(" B ")}, "conflict:duplicate_title"},
		{"rename to a variant of its own title", items.Patch{Title: strPtr("A")}, "ok"},
		{"same title", items.Patch{Title: strPtr("a")}, "ok"},
		{"no title", items.Patch{Website: items.Optional[string]{Set: true}}, "ok"},
		{"a free title", items.Patch{Title: strPtr("c")}, "ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newFakeDB(t)
			l := db.addList(movie, "manual")
			id := db.addItem(l, "a", 0, "")
			db.addItem(l, "b", 1, "")
			_, err := items.NewService(db, db, db, fixedClock{}).Update(t.Context(), id, 1, tt.patch)
			if got := describe(err); got != tt.want {
				t.Fatalf("Update = %s, want %s", got, tt.want)
			}
		})
	}

	t.Run("an existing duplicate pair stays editable in other fields", func(t *testing.T) {
		db := newFakeDB(t)
		l := db.addList(wishlist, "manual")
		id := db.addItem(l, "dup", 0, "")
		db.addItem(l, "DUP", 1, "")
		p := items.Patch{Title: strPtr("dup"), Website: items.Optional[string]{Set: true, Value: strPtr("https://example.com")}}
		if _, err := items.NewService(db, db, db, fixedClock{}).Update(t.Context(), id, 1, p); err != nil {
			t.Fatalf("Update: %v", err)
		}
	})
}

func TestF026B2_Move_UniqueTitle(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*fakeDB) (uuid.UUID, uuid.UUID)
		want  string
	}{
		{"equal title in destination", func(db *fakeDB) (uuid.UUID, uuid.UUID) {
			id := db.addItem(db.addList(movie, "manual"), "Amélie", 0, "")
			d := db.addList(movie, "manual")
			db.addItem(d, "amelie", 0, "")
			return id, d
		}, "conflict:duplicate_title"},
		{"type mismatch wins over a duplicate", func(db *fakeDB) (uuid.UUID, uuid.UUID) {
			id := db.addItem(db.addList(movie, "manual"), "a", 0, "")
			d := db.addList(vehicle, "manual")
			db.addItem(d, "a", 0, "")
			return id, d
		}, "conflict:type_mismatch"},
		{"no equal title", func(db *fakeDB) (uuid.UUID, uuid.UUID) {
			id := db.addItem(db.addList(movie, "manual"), "a", 0, "")
			d := db.addList(movie, "manual")
			db.addItem(d, "b", 0, "")
			return id, d
		}, "ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newFakeDB(t)
			id, dest := tt.setup(db)
			_, err := items.NewService(db, db, db, fixedClock{}).Move(t.Context(), id, 1, dest)
			if got := describe(err); got != tt.want {
				t.Fatalf("Move = %s, want %s", got, tt.want)
			}
		})
	}
}
