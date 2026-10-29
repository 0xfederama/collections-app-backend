package definitions_test

// The package's TestMain (service_test.go) terminates the shared Postgres
// container for this binary; this file adds no TestMain of its own.

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/definitions"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/platform/testdb"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

var (
	repoNow   = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	repoLater = repoNow.Add(time.Hour)
)

// repoEnv is a migrated database with one bootstrapped workspace. ctx is scoped to it.
type repoEnv struct {
	t     *testing.T
	pool  *pgxpool.Pool
	store *definitions.Postgres
	ctx   context.Context
	ws    uuid.UUID
}

func newRepoEnv(t *testing.T) repoEnv {
	t.Helper()
	pool := testdb.NewMigrated(t, testdb.AdminURL(t))
	ws, err := workspace.Bootstrap(t.Context(), pool, "Ours")
	if err != nil {
		t.Fatal(err)
	}
	return repoEnv{t: t, pool: pool, store: definitions.NewPostgres(pool), ctx: scope.With(t.Context(), ws), ws: ws}
}

// do runs fn in one transaction and fails the test when it errors.
func (e repoEnv) do(fn func(r definitions.Repo) error) {
	e.t.Helper()
	if err := e.store.InTx(e.ctx, fn); err != nil {
		e.t.Fatal(err)
	}
}

func (e repoEnv) typeByKey(key string) definitions.Type {
	e.t.Helper()
	var out definitions.Type
	e.do(func(r definitions.Repo) error {
		var err error
		out, err = r.GetTypeByKey(e.ctx, key)
		return err
	})
	return out
}

// insertType creates a custom type through the repository.
func (e repoEnv) insertType(key string) definitions.Type {
	e.t.Helper()
	var out definitions.Type
	e.do(func(r definitions.Repo) error {
		var err error
		out, err = r.InsertType(e.ctx, definitions.Type{Key: key, Name: "Gadgets"}, repoNow)
		return err
	})
	return out
}

// insertField appends a field to typ through the repository.
func (e repoEnv) insertField(typ definitions.Type, key string, position int) definitions.Field {
	e.t.Helper()
	var out definitions.Field
	e.do(func(r definitions.Repo) error {
		var err error
		out, err = r.InsertField(e.ctx, typ.ID, definitions.Field{Key: key, Label: key, Type: "number", Position: position}, repoNow)
		return err
	})
	return out
}

// insertList writes a list of a type with raw SQL (the lists repository is another feature).
func (e repoEnv) insertList(typeKey, sortMode, pinned string) uuid.UUID {
	e.t.Helper()
	var id uuid.UUID
	err := e.pool.QueryRow(e.ctx, `INSERT INTO lists (workspace_id, name, sort_order, type, sort_mode, pinned_fields)
		VALUES ($1, 'L', 0, $2, $3, $4::jsonb) RETURNING id`, e.ws, typeKey, sortMode, pinned).Scan(&id)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

// insertItem writes an active item with raw SQL.
func (e repoEnv) insertItem(list uuid.UUID, metadata string) uuid.UUID {
	e.t.Helper()
	var id uuid.UUID
	err := e.pool.QueryRow(e.ctx, `INSERT INTO items (workspace_id, list_id, title, sort_order, metadata)
		VALUES ($1, $2, 'I', 0, $3::jsonb) RETURNING id`, e.ws, list, metadata).Scan(&id)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func isNotFoundErr(err error, resource string) bool {
	var nf *apperr.NotFoundError
	return errors.As(err, &nf) && nf.Resource == resource
}

func repoPtr[T any](v T) *T { return &v }

func TestRepo_ListTypesAndGetByKey(t *testing.T) {
	e := newRepoEnv(t)
	var listed []definitions.Type
	e.do(func(r definitions.Repo) error {
		var err error
		listed, err = r.ListTypes(e.ctx)
		return err
	})
	if len(listed) == 0 || !listed[0].Locked {
		t.Fatalf("ListTypes = %+v, want the locked built-in types first", listed)
	}
	var book definitions.Type
	for _, typ := range listed {
		if typ.Key == "book" {
			book = typ
		}
		if typ.Fields != nil {
			t.Errorf("ListTypes filled Fields of %s; the service composes them", typ.Key)
		}
	}
	if book.ID == uuid.Nil || !book.Locked {
		t.Fatalf("book in ListTypes = %+v, want a locked type", book)
	}
	if got := e.typeByKey("book"); got.ID != book.ID || got.Version != book.Version || got.Name != book.Name {
		t.Errorf("GetTypeByKey(book) = %+v, want %+v", got, book)
	}
	err := e.store.InTx(e.ctx, func(r definitions.Repo) error {
		_, err := r.GetTypeByKey(e.ctx, "no-such-type")
		return err
	})
	if !isNotFoundErr(err, "type") {
		t.Errorf("GetTypeByKey(unknown) err = %v, want NotFound type", err)
	}
}

func TestRepo_ListFieldsAndGetField(t *testing.T) {
	e := newRepoEnv(t)
	book := e.typeByKey("book")
	var fields []definitions.Field
	e.do(func(r definitions.Repo) error {
		var err error
		fields, err = r.ListFields(e.ctx, book.ID)
		return err
	})
	keys := map[string]bool{}
	for i, f := range fields {
		keys[f.Key] = true
		if i > 0 && f.Position <= fields[i-1].Position {
			t.Errorf("fields not in position order: %+v", fields)
		}
	}
	for _, want := range []string{"author", "pages", "isbn", "stores", "openlibrary_id", "lookup_disabled"} {
		if !keys[want] {
			t.Errorf("fields = %+v, missing %s", fields, want)
		}
	}
	e.do(func(r definitions.Repo) error {
		got, err := r.GetField(e.ctx, book.ID, "author")
		if err != nil || got.Key != "author" || got.ID == uuid.Nil {
			t.Errorf("GetField(author) = %+v, %v", got, err)
		}
		return nil
	})
	err := e.store.InTx(e.ctx, func(r definitions.Repo) error {
		_, err := r.GetField(e.ctx, book.ID, "no_such_field")
		return err
	})
	if !isNotFoundErr(err, "type field") {
		t.Errorf("GetField(unknown) err = %v, want NotFound type field", err)
	}
}

func TestRepo_LockForUpdateIsNotFoundWhenMissing(t *testing.T) {
	e := newRepoEnv(t)
	book := e.typeByKey("book")
	e.do(func(r definitions.Repo) error {
		got, err := r.GetTypeForUpdate(e.ctx, book.ID)
		if err != nil || got.ID != book.ID {
			t.Errorf("GetTypeForUpdate = %+v, %v", got, err)
		}
		return nil
	})
	var fieldID uuid.UUID
	e.do(func(r definitions.Repo) error {
		f, err := r.GetField(e.ctx, book.ID, "author")
		fieldID = f.ID
		return err
	})
	e.do(func(r definitions.Repo) error {
		got, err := r.GetFieldForUpdate(e.ctx, fieldID)
		if err != nil || got.ID != fieldID {
			t.Errorf("GetFieldForUpdate = %+v, %v", got, err)
		}
		return nil
	})
	for _, tt := range []struct {
		name     string
		lock     func(r definitions.Repo) error
		resource string
	}{
		{"type", func(r definitions.Repo) error { _, err := r.GetTypeForUpdate(e.ctx, uuid.New()); return err }, "type"},
		{"field", func(r definitions.Repo) error { _, err := r.GetFieldForUpdate(e.ctx, uuid.New()); return err }, "type field"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := e.store.InTx(e.ctx, tt.lock)
			if !isNotFoundErr(err, tt.resource) {
				t.Errorf("err = %v, want NotFound %s", err, tt.resource)
			}
		})
	}
}

func TestRepo_InsertUpdateDeleteType(t *testing.T) {
	e := newRepoEnv(t)
	var created definitions.Type
	e.do(func(r definitions.Repo) error {
		var err error
		created, err = r.InsertType(e.ctx, definitions.Type{
			Key: "gadgets", Name: "Gadgets", DefaultIcon: repoPtr("i:tag"), AllowsPrice: true,
			DefaultSortField: repoPtr("price"), DefaultPinnedFields: &[]string{"title", "price"},
		}, repoNow)
		return err
	})
	if created.ID == uuid.Nil || created.Version != 1 || created.Locked {
		t.Fatalf("InsertType = %+v, want a new unlocked version 1", created)
	}
	if got := e.typeByKey("gadgets"); !reflect.DeepEqual(got, created) {
		t.Errorf("GetTypeByKey = %+v, want %+v", got, created)
	}

	// Default pins: NULL, the empty list, and a list round-trip as they are stored.
	for _, tt := range []struct {
		name string
		pins *[]string
	}{
		{"nil is NULL", nil},
		{"empty list", &[]string{}},
		{"ordered keys", &[]string{"price", "title"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			se := e
			se.t = t
			var typ definitions.Type
			se.do(func(r definitions.Repo) error {
				var err error
				typ, err = r.InsertType(se.ctx, definitions.Type{Key: "pins_" + tt.name[:3], Name: "P", DefaultPinnedFields: tt.pins}, repoNow)
				return err
			})
			got := se.typeByKey(typ.Key)
			if !reflect.DeepEqual(got.DefaultPinnedFields, tt.pins) {
				t.Errorf("DefaultPinnedFields = %v, want %v", got.DefaultPinnedFields, tt.pins)
			}
		})
	}

	cur := e.typeByKey("gadgets")
	cur.Name = "Gizmos"
	cur.DefaultIcon = nil
	cur.AllowsPrice = false
	cur.DefaultSortField = nil
	cur.DefaultPinnedFields = &[]string{"title"}
	var updated definitions.Type
	e.do(func(r definitions.Repo) error {
		var err error
		updated, err = r.UpdateType(e.ctx, cur, repoLater)
		return err
	})
	if updated.Name != "Gizmos" || updated.DefaultIcon != nil || updated.AllowsPrice || updated.DefaultSortField != nil ||
		!reflect.DeepEqual(updated.DefaultPinnedFields, &[]string{"title"}) || updated.Version != 2 {
		t.Errorf("UpdateType = %+v, want the merged values at version 2", updated)
	}

	e.do(func(r definitions.Repo) error {
		return r.TouchType(e.ctx, created.ID, repoLater)
	})
	if got := e.typeByKey("gadgets"); got.Version != 3 {
		t.Errorf("version after TouchType = %d, want 3", got.Version)
	}

	e.do(func(r definitions.Repo) error {
		if err := r.DeleteFieldsOfType(e.ctx, created.ID); err != nil {
			return err
		}
		return r.DeleteType(e.ctx, created.ID)
	})
	err := e.store.InTx(e.ctx, func(r definitions.Repo) error {
		_, err := r.GetTypeByKey(e.ctx, "gadgets")
		return err
	})
	if !isNotFoundErr(err, "type") {
		t.Errorf("after DeleteType err = %v, want NotFound type", err)
	}
}

func TestRepo_InsertLabelDeleteField(t *testing.T) {
	e := newRepoEnv(t)
	typ := e.insertType("gadgets")
	e.do(func(r definitions.Repo) error {
		next, err := r.NextFieldPosition(e.ctx, typ.ID)
		if err != nil || next != 0 {
			t.Errorf("NextFieldPosition of an empty type = %d, %v; want 0", next, err)
		}
		return err
	})
	rating := e.insertField(typ, "rating", 0)
	if rating.Version != 1 || rating.Position != 0 || rating.Type != "number" || rating.Locked {
		t.Fatalf("InsertField = %+v, want version 1 at position 0", rating)
	}
	e.do(func(r definitions.Repo) error {
		next, err := r.NextFieldPosition(e.ctx, typ.ID)
		if err != nil || next != 1 {
			t.Errorf("NextFieldPosition = %d, %v; want 1", next, err)
		}
		renamed, err := r.UpdateFieldLabel(e.ctx, rating.ID, "Score", repoLater)
		if err != nil || renamed.Label != "Score" || renamed.Version != 2 {
			t.Errorf("UpdateFieldLabel = %+v, %v; want label Score, version 2", renamed, err)
		}
		return err
	})
	e.do(func(r definitions.Repo) error {
		if err := r.DeleteField(e.ctx, rating.ID); err != nil {
			return err
		}
		fields, err := r.ListFields(e.ctx, typ.ID)
		if err != nil || len(fields) != 0 {
			t.Errorf("ListFields after DeleteField = %+v, %v; want none", fields, err)
		}
		return err
	})
}

// The (type_id, position) unique constraint is not deferrable, so a reorder is
// two passes: park every field past the maximum, then set the final positions.
func TestRepo_ReorderParkThenSet(t *testing.T) {
	e := newRepoEnv(t)
	typ := e.insertType("gadgets")
	a := e.insertField(typ, "a", 0)
	b := e.insertField(typ, "b", 1)
	c := e.insertField(typ, "c", 2)

	// A direct move onto an occupied position is refused by the constraint.
	err := e.store.InTx(e.ctx, func(r definitions.Repo) error {
		return r.SetFieldPosition(e.ctx, c.ID, 0, repoLater)
	})
	if err == nil {
		t.Fatal("SetFieldPosition onto an occupied position succeeded, want a unique violation")
	}

	e.do(func(r definitions.Repo) error {
		for _, f := range []definitions.Field{a, b, c} {
			if err := r.ParkFieldPosition(e.ctx, f.ID, f.Position+3, repoLater); err != nil {
				return err
			}
		}
		return nil
	})
	e.do(func(r definitions.Repo) error {
		fields, err := r.ListFields(e.ctx, typ.ID)
		if err != nil {
			return err
		}
		for _, f := range fields {
			if f.Version != 1 || f.Position < 3 {
				t.Errorf("parked field %s = position %d version %d, want position >= 3 and version 1 (parking bumps no version)", f.Key, f.Position, f.Version)
			}
		}
		return nil
	})

	e.do(func(r definitions.Repo) error {
		for i, f := range []definitions.Field{c, a, b} {
			if err := r.SetFieldPosition(e.ctx, f.ID, i, repoLater); err != nil {
				return err
			}
		}
		return nil
	})
	e.do(func(r definitions.Repo) error {
		fields, err := r.ListFields(e.ctx, typ.ID)
		if err != nil {
			return err
		}
		var keys []string
		for _, f := range fields {
			keys = append(keys, f.Key)
			if f.Version != 2 {
				t.Errorf("field %s version = %d, want 2 (one bump per reorder)", f.Key, f.Version)
			}
		}
		if !reflect.DeepEqual(keys, []string{"c", "a", "b"}) {
			t.Errorf("order after reorder = %v, want [c a b]", keys)
		}
		return nil
	})
}

// Values, defaults and list settings that name a field are cleaned up when the
// field is deleted (the service orders these calls).
func TestRepo_FieldValuesAndDefaults(t *testing.T) {
	e := newRepoEnv(t)
	typ := e.insertType("gadgets")
	e.insertField(typ, "rating", 0)
	cur := e.typeByKey("gadgets")
	cur.DefaultSortField = repoPtr("rating")
	cur.DefaultPinnedFields = &[]string{"rating", "title"}
	e.do(func(r definitions.Repo) error {
		_, err := r.UpdateType(e.ctx, cur, repoLater)
		return err
	})

	listA := e.insertList("gadgets", "rating", `["rating","title"]`)
	itemWithValue := e.insertItem(listA, `{"rating": 4, "note": "x"}`)
	itemWithoutValue := e.insertItem(listA, `{"note": "y"}`)
	listOther := e.insertList("movie", "manual", `[]`)
	itemOther := e.insertItem(listOther, `{"rating": 5}`)

	e.do(func(r definitions.Repo) error {
		n, err := r.CountFieldValues(e.ctx, "gadgets", "rating")
		if err != nil || n != 1 {
			t.Errorf("CountFieldValues = %d, %v; want 1 (only the gadgets item)", n, err)
		}
		return err
	})
	e.do(func(r definitions.Repo) error {
		refs, err := r.ListActiveListsByType(e.ctx, "gadgets")
		if err != nil || len(refs) != 1 || refs[0].ID != listA {
			t.Errorf("ListActiveListsByType(gadgets) = %+v, %v; want list A", refs, err)
		}
		none, err := r.ListActiveListsByType(e.ctx, "no_such_type")
		if err != nil || len(none) != 0 {
			t.Errorf("ListActiveListsByType(unknown) = %+v, %v; want none", none, err)
		}
		return err
	})

	e.do(func(r definitions.Repo) error {
		return r.ResetFieldDefaults(e.ctx, typ.ID, "rating", repoLater)
	})
	if got := e.typeByKey("gadgets"); got.DefaultSortField != nil || !reflect.DeepEqual(got.DefaultPinnedFields, &[]string{"title"}) || got.Version != 3 {
		t.Errorf("type after ResetFieldDefaults = %+v, want no sort, pins [title], version 3", got)
	}

	e.do(func(r definitions.Repo) error {
		if err := r.ResetListFieldPins(e.ctx, "gadgets", "rating", repoLater); err != nil {
			return err
		}
		return r.ResetListFieldSort(e.ctx, "gadgets", "rating", repoLater)
	})
	var pinsNull bool
	var sortMode string
	var listVersion int
	if err := e.pool.QueryRow(e.ctx, `SELECT pinned_fields IS NULL, sort_mode, version FROM lists WHERE id = $1`, listA).
		Scan(&pinsNull, &sortMode, &listVersion); err != nil {
		t.Fatal(err)
	}
	if !pinsNull || sortMode != "manual" || listVersion != 3 {
		t.Errorf("list A = pins null %v, sort %q, version %d; want pins NULL, sort manual, version 3", pinsNull, sortMode, listVersion)
	}

	e.do(func(r definitions.Repo) error {
		return r.StripFieldValues(e.ctx, "gadgets", "rating", repoLater)
	})
	var hasRating bool
	var itemVersion int
	if err := e.pool.QueryRow(e.ctx, `SELECT metadata ? 'rating', version FROM items WHERE id = $1`, itemWithValue).
		Scan(&hasRating, &itemVersion); err != nil {
		t.Fatal(err)
	}
	if hasRating || itemVersion != 2 {
		t.Errorf("item with value = has rating %v, version %d; want no rating, version 2", hasRating, itemVersion)
	}
	if err := e.pool.QueryRow(e.ctx, `SELECT version FROM items WHERE id = $1`, itemWithoutValue).Scan(&itemVersion); err != nil || itemVersion != 1 {
		t.Errorf("item without value version = %d, %v; want 1 (untouched)", itemVersion, err)
	}
	if err := e.pool.QueryRow(e.ctx, `SELECT metadata ? 'rating' FROM items WHERE id = $1`, itemOther).Scan(&hasRating); err != nil || !hasRating {
		t.Errorf("item of another type lost its value: has rating %v, %v", hasRating, err)
	}
	e.do(func(r definitions.Repo) error {
		n, err := r.CountFieldValues(e.ctx, "gadgets", "rating")
		if err != nil || n != 0 {
			t.Errorf("CountFieldValues after strip = %d, %v; want 0", n, err)
		}
		return err
	})
}

func TestRepo_ListStoresFields(t *testing.T) {
	e := newRepoEnv(t)
	e.insertType("gadgets")
	var got []definitions.StoresField
	e.do(func(r definitions.Repo) error {
		var err error
		got, err = r.ListStoresFields(e.ctx)
		return err
	})
	found := false
	for _, sf := range got {
		found = found || (sf.TypeKey == "book" && sf.FieldKey == "stores")
		if sf.TypeKey == "gadgets" {
			t.Errorf("ListStoresFields lists a field of a type without stores fields: %+v", sf)
		}
	}
	if !found {
		t.Errorf("ListStoresFields = %+v, want book.stores", got)
	}
}
