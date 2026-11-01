package definitions

import (
	"context"
	"errors"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/platform/testdb"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testdb.Terminate()
	os.Exit(code)
}

func newDefinitionsService(t *testing.T) (*Service, *definitionDB) {
	t.Helper()
	pool := testdb.NewMigrated(t, testdb.AdminURL(t))
	workspaceID, err := workspace.Bootstrap(t.Context(), pool, "W")
	if err != nil {
		t.Fatal(err)
	}
	return NewService(NewPostgres(pool), clock.System{}), &definitionDB{pool: pool, workspaceID: workspaceID, ctx: scope.With(t.Context(), workspaceID)}
}

// definitionDB is the database behind a test service. ctx carries the workspace
// (scope) the service reads from.
type definitionDB struct {
	pool        *pgxpool.Pool
	workspaceID uuid.UUID
	ctx         context.Context
}

// builtinType returns a seeded built-in type of the test workspace.
func builtinType(t *testing.T, svc *Service, db *definitionDB, key string) Type {
	t.Helper()
	types, err := svc.List(db.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range types {
		if typ.Key == key {
			return typ
		}
	}
	t.Fatalf("built-in type %q not found", key)
	return Type{}
}

func TestF014B3_CustomFieldLifecycle(t *testing.T) {
	svc, db := newDefinitionsService(t)
	typ := builtinType(t, svc, db, "vehicle")
	rating, err := svc.CreateField(db.ctx, typ.Key, typ.Version, CreateField{Label: "Rating", Type: "number"})
	if err != nil {
		t.Fatal(err)
	}
	age, err := svc.CreateField(db.ctx, typ.Key, typ.Version+1, CreateField{Label: "Player age", Type: "number"})
	if err != nil {
		t.Fatal(err)
	}
	if age.Position != rating.Position+1 {
		t.Fatalf("created positions = rating %d age %d", rating.Position, age.Position)
	}

	version, err := svc.ReorderFields(db.ctx, typ.Key, typ.Version+2, []string{age.Key, rating.Key})
	if err != nil || version != typ.Version+3 {
		t.Fatalf("ReorderFields() = %d, %v", version, err)
	}
	fields := builtinType(t, svc, db, typ.Key).Fields
	var got []string
	for _, field := range fields {
		if !field.Locked {
			got = append(got, field.Key)
		}
	}
	if !slices.Equal(got, []string{age.Key, rating.Key}) {
		t.Errorf("custom field order = %v", got)
	}

	renamed, err := svc.RenameField(db.ctx, typ.Key, age.Key, age.Version+1, "Minimum player age")
	if err != nil || renamed.Label != "Minimum player age" || renamed.Version != age.Version+2 {
		t.Fatalf("RenameField() = %#v, %v", renamed, err)
	}
	if _, err := svc.RenameField(db.ctx, typ.Key, rating.Key, rating.Version, "Nope"); err == nil {
		t.Fatal("RenameField with stale ETag succeeded")
	} else {
		var conflict *apperr.VersionConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("RenameField stale error = %v", err)
		}
	}
}

func TestF014B3_DeleteFieldPreviewsAndAtomicallyStripsValues(t *testing.T) {
	svc, db := newDefinitionsService(t)
	typ := builtinType(t, svc, db, "vehicle")
	field, err := svc.CreateField(db.ctx, typ.Key, typ.Version, CreateField{Label: "Rating", Type: "number"})
	if err != nil {
		t.Fatal(err)
	}
	listID := uuid.New()
	if _, err := db.pool.Exec(t.Context(), `INSERT INTO lists (id, workspace_id, name, type, sort_mode, sort_order)
VALUES ($1, $2, 'Cars', $3, 'rating', 0)`, listID, db.workspaceID, typ.Key); err != nil {
		t.Fatal(err)
	}
	for i, metadata := range []string{`{"rating": 5, "notes": "keep"}`, `{"rating": 4}`} {
		if _, err := db.pool.Exec(t.Context(), `INSERT INTO items (id, workspace_id, list_id, title, metadata, sort_order)
VALUES ($1, $2, $3, $4, $5::jsonb, $6)`, uuid.New(), db.workspaceID, listID, "Car", metadata, i); err != nil {
			t.Fatal(err)
		}
	}

	count, err := svc.FieldDeletePreview(db.ctx, typ.Key, field.Key)
	if err != nil || count != 2 {
		t.Fatalf("FieldDeletePreview() = %d, %v", count, err)
	}
	deleted, err := svc.DeleteField(db.ctx, typ.Key, field.Key, field.Version)
	if err != nil || deleted != 2 {
		t.Fatalf("DeleteField() = %d, %v", deleted, err)
	}
	var values int
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM items WHERE workspace_id = $1 AND metadata ? 'rating'`, db.workspaceID).Scan(&values); err != nil {
		t.Fatal(err)
	}
	if values != 0 {
		t.Errorf("rating values remaining = %d", values)
	}
	var listSort string
	if err := db.pool.QueryRow(t.Context(), `SELECT sort_mode FROM lists WHERE id = $1`, listID).Scan(&listSort); err != nil {
		t.Fatal(err)
	}
	if listSort != "manual" {
		t.Errorf("list sort = %q, want manual", listSort)
	}
}

// fakeStore is an in-memory Store. InTx runs fn on a copy of the state and keeps
// the copy only when fn returns nil, so a failed write changes nothing, as a
// rolled-back transaction does. Like the table, it refuses a second row at the
// same (type, position) and a duplicate type key, and it refuses to delete a type
// that still has fields (the foreign key), so the order of the writes is checked.
type fakeStore struct{ state fakeState }

type fakeField struct {
	Field
	typeID uuid.UUID
}

type fakeList struct {
	id     uuid.UUID
	name   string
	typeK  string
	sort   string
	active bool
}

type fakeState struct {
	types  []Type
	fields []fakeField
	lists  []fakeList
	values map[string]int // "type/field" -> item values, trashed items included
	calls  []string
}

func (s fakeState) clone() fakeState {
	return fakeState{
		types: slices.Clone(s.types), fields: slices.Clone(s.fields), lists: slices.Clone(s.lists),
		values: maps.Clone(s.values), calls: slices.Clone(s.calls),
	}
}

func (f *fakeStore) InTx(_ context.Context, fn func(Repo) error) error {
	next := f.state.clone()
	if err := fn(&fakeRepo{s: &next}); err != nil {
		return err
	}
	f.state = next
	return nil
}

// addType seeds a type with its fields. Missing IDs and versions are filled in.
func (f *fakeStore) addType(typ Type, fields ...Field) Type {
	if typ.ID == uuid.Nil {
		typ.ID = uuid.New()
	}
	if typ.Version == 0 {
		typ.Version = 1
	}
	f.state.types = append(f.state.types, typ)
	for _, field := range fields {
		if field.ID == uuid.Nil {
			field.ID = uuid.New()
		}
		if field.Version == 0 {
			field.Version = 1
		}
		f.state.fields = append(f.state.fields, fakeField{Field: field, typeID: typ.ID})
	}
	return typ
}

func (f *fakeStore) addList(name, typeKey, sort string, active bool) {
	f.state.lists = append(f.state.lists, fakeList{id: uuid.New(), name: name, typeK: typeKey, sort: sort, active: active})
}

func (f *fakeStore) setValues(typeKey, fieldKey string, n int) {
	if f.state.values == nil {
		f.state.values = map[string]int{}
	}
	f.state.values[typeKey+"/"+fieldKey] = n
}

func (f *fakeStore) typeByKey(key string) Type {
	for _, typ := range f.state.types {
		if typ.Key == key {
			return typ
		}
	}
	return Type{}
}

func (f *fakeStore) fieldByKey(typeKey, key string) Field {
	typ := f.typeByKey(typeKey)
	for _, field := range f.state.fields {
		if field.typeID == typ.ID && field.Key == key {
			return field.Field
		}
	}
	return Field{}
}

// fakeRepo runs against one copy of the state.
type fakeRepo struct{ s *fakeState }

var _ Repo = (*fakeRepo)(nil)

var errDuplicateKey = errors.New("unique violation")

func (r *fakeRepo) typeIndex(id uuid.UUID) int {
	for i, typ := range r.s.types {
		if typ.ID == id {
			return i
		}
	}
	return -1
}

func (r *fakeRepo) fieldIndex(id uuid.UUID) int {
	for i, field := range r.s.fields {
		if field.ID == id {
			return i
		}
	}
	return -1
}

// positionTaken is the (type_id, position) unique constraint, ignoring the row itself.
func (r *fakeRepo) positionTaken(typeID uuid.UUID, position int, self uuid.UUID) bool {
	for _, field := range r.s.fields {
		if field.typeID == typeID && field.Position == position && field.ID != self {
			return true
		}
	}
	return false
}

func (r *fakeRepo) ListTypes(context.Context) ([]Type, error) {
	out := make([]Type, 0, len(r.s.types))
	for _, typ := range r.s.types {
		typ.Fields = nil
		out = append(out, typ)
	}
	return out, nil
}

func (r *fakeRepo) GetTypeByKey(_ context.Context, key string) (Type, error) {
	for _, typ := range r.s.types {
		if typ.Key == key {
			typ.Fields = nil
			return typ, nil
		}
	}
	return Type{}, apperr.NotFound("type")
}

func (r *fakeRepo) GetTypeForUpdate(_ context.Context, id uuid.UUID) (Type, error) {
	i := r.typeIndex(id)
	if i < 0 {
		return Type{}, apperr.NotFound("type")
	}
	r.s.calls = append(r.s.calls, "lock type")
	return r.s.types[i], nil
}

func (r *fakeRepo) TouchType(_ context.Context, id uuid.UUID, _ time.Time) error {
	i := r.typeIndex(id)
	if i < 0 {
		return apperr.NotFound("type")
	}
	r.s.types[i].Version++
	r.s.calls = append(r.s.calls, "touch type")
	return nil
}

func (r *fakeRepo) ListFields(_ context.Context, typeID uuid.UUID) ([]Field, error) {
	var out []Field
	for _, field := range r.s.fields {
		if field.typeID == typeID {
			out = append(out, field.Field)
		}
	}
	slices.SortFunc(out, func(a, b Field) int { return a.Position - b.Position })
	return out, nil
}

func (r *fakeRepo) GetField(_ context.Context, typeID uuid.UUID, key string) (Field, error) {
	for _, field := range r.s.fields {
		if field.typeID == typeID && field.Key == key {
			return field.Field, nil
		}
	}
	return Field{}, apperr.NotFound("type field")
}

func (r *fakeRepo) GetFieldForUpdate(_ context.Context, id uuid.UUID) (Field, error) {
	i := r.fieldIndex(id)
	if i < 0 {
		return Field{}, apperr.NotFound("type field")
	}
	r.s.calls = append(r.s.calls, "lock field")
	return r.s.fields[i].Field, nil
}

func (r *fakeRepo) NextFieldPosition(_ context.Context, typeID uuid.UUID) (int, error) {
	next := 0
	for _, field := range r.s.fields {
		if field.typeID == typeID && field.Position >= next {
			next = field.Position + 1
		}
	}
	return next, nil
}

func (r *fakeRepo) InsertField(ctx context.Context, typeID uuid.UUID, f Field, _ time.Time) (Field, error) {
	if _, err := r.GetField(ctx, typeID, f.Key); err == nil {
		return Field{}, errDuplicateKey
	}
	if r.positionTaken(typeID, f.Position, uuid.Nil) {
		return Field{}, errDuplicateKey
	}
	f.ID, f.Version = uuid.New(), 1
	r.s.fields = append(r.s.fields, fakeField{Field: f, typeID: typeID})
	r.s.calls = append(r.s.calls, "insert field")
	return f, nil
}

func (r *fakeRepo) UpdateFieldLabel(_ context.Context, id uuid.UUID, label string, _ time.Time) (Field, error) {
	i := r.fieldIndex(id)
	if i < 0 {
		return Field{}, apperr.NotFound("type field")
	}
	r.s.fields[i].Label = label
	r.s.fields[i].Version++
	r.s.calls = append(r.s.calls, "update field label")
	return r.s.fields[i].Field, nil
}

// ParkFieldPosition is pass 1 of a reorder: no version bump.
func (r *fakeRepo) ParkFieldPosition(_ context.Context, id uuid.UUID, position int, _ time.Time) error {
	i := r.fieldIndex(id)
	if i < 0 {
		return apperr.NotFound("type field")
	}
	if r.positionTaken(r.s.fields[i].typeID, position, id) {
		return errDuplicateKey
	}
	r.s.fields[i].Position = position
	r.s.calls = append(r.s.calls, "park "+r.s.fields[i].Key)
	return nil
}

// SetFieldPosition is pass 2 of a reorder: one version bump per field.
func (r *fakeRepo) SetFieldPosition(_ context.Context, id uuid.UUID, position int, _ time.Time) error {
	i := r.fieldIndex(id)
	if i < 0 {
		return apperr.NotFound("type field")
	}
	if r.positionTaken(r.s.fields[i].typeID, position, id) {
		return errDuplicateKey
	}
	r.s.fields[i].Position = position
	r.s.fields[i].Version++
	r.s.calls = append(r.s.calls, "set "+r.s.fields[i].Key)
	return nil
}

func (r *fakeRepo) DeleteField(_ context.Context, id uuid.UUID) error {
	r.s.fields = slices.DeleteFunc(r.s.fields, func(field fakeField) bool { return field.ID == id })
	r.s.calls = append(r.s.calls, "delete field")
	return nil
}

func (r *fakeRepo) CountFieldValues(_ context.Context, typeKey, fieldKey string) (int, error) {
	return r.s.values[typeKey+"/"+fieldKey], nil
}

func (r *fakeRepo) StripFieldValues(_ context.Context, typeKey, fieldKey string, _ time.Time) error {
	if r.s.values != nil {
		r.s.values[typeKey+"/"+fieldKey] = 0
	}
	r.s.calls = append(r.s.calls, "strip values")
	return nil
}

// ResetFieldDefaults drops the key from the type's default sort, and bumps the type version.
func (r *fakeRepo) ResetFieldDefaults(_ context.Context, typeID uuid.UUID, fieldKey string, _ time.Time) error {
	i := r.typeIndex(typeID)
	if i < 0 {
		return apperr.NotFound("type")
	}
	typ := r.s.types[i]
	if typ.DefaultSortField != nil && *typ.DefaultSortField == fieldKey {
		typ.DefaultSortField = nil
	}
	typ.Version++
	r.s.types[i] = typ
	r.s.calls = append(r.s.calls, "reset type defaults")
	return nil
}

func (r *fakeRepo) ResetListFieldSort(_ context.Context, typeKey, fieldKey string, _ time.Time) error {
	for i, l := range r.s.lists {
		if l.typeK == typeKey && l.sort == fieldKey {
			r.s.lists[i].sort = "manual"
		}
	}
	r.s.calls = append(r.s.calls, "reset list sort")
	return nil
}

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Unix(1_700_000_000, 0).UTC() }

func ptr[T any](v T) *T { return &v }

// gadgetStore is a test type "gadget" (version 1) with a number field rating and a
// text field brand, plus a locked built-in "movie" with the locked field release_year.
func gadgetStore() *fakeStore {
	f := &fakeStore{}
	f.addType(Type{Key: "gadget", Name: "Gadget", AllowsPrice: false},
		Field{Key: "rating", Label: "Rating", Type: "number", Position: 0},
		Field{Key: "brand", Label: "Brand", Type: "text", Position: 1},
	)
	f.addType(Type{Key: "movie", Name: "Movie", Locked: true},
		Field{Key: "release_year", Label: "Release year", Type: "number", Position: 0, Locked: true},
	)
	return f
}

func newService(store *fakeStore) *Service { return NewService(store, fixedClock{}) }

// validationFields returns the field names of a validation error, failing when err is not one.
func validationFields(t *testing.T, err error) []string {
	t.Helper()
	var ve *apperr.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a validation error", err)
	}
	out := make([]string, 0, len(ve.Errors))
	for _, fe := range ve.Errors {
		out = append(out, fe.Field)
	}
	return out
}

func wantVersionConflict(t *testing.T, err error, current int) {
	t.Helper()
	var vc *apperr.VersionConflictError
	if !errors.As(err, &vc) || vc.Current != current {
		t.Fatalf("error = %v, want version conflict with current %d", err, current)
	}
}

func wantNotFound(t *testing.T, err error, resource string) {
	t.Helper()
	var nf *apperr.NotFoundError
	if !errors.As(err, &nf) || nf.Resource != resource {
		t.Fatalf("error = %v, want not found %q", err, resource)
	}
}

func wantOrderStale(t *testing.T, err error) {
	t.Helper()
	var c *apperr.ConflictError
	if !errors.As(err, &c) || c.Code != apperr.CodeOrderStale {
		t.Fatalf("error = %v, want order_stale", err)
	}
}

func TestService_List_ReturnsFieldsInPositionOrder(t *testing.T) {
	store := gadgetStore()
	types, err := newService(store).List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, typ := range types {
		if typ.Key == "gadget" {
			for _, field := range typ.Fields {
				keys = append(keys, field.Key)
			}
		}
	}
	if !slices.Equal(keys, []string{"rating", "brand"}) {
		t.Errorf("gadget fields = %v", keys)
	}
}

// Every write refuses a stale type version and changes nothing.
func TestService_StaleTypeVersionWritesNothing(t *testing.T) {
	ops := []struct {
		name string
		run  func(m *Service, v int) error
	}{
		{"create field", func(m *Service, v int) error {
			_, err := m.CreateField(t.Context(), "gadget", v, CreateField{Label: "Colour", Type: "text"})
			return err
		}},
		{"reorder", func(m *Service, v int) error {
			_, err := m.ReorderFields(t.Context(), "gadget", v, []string{"brand", "rating"})
			return err
		}},
	}
	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			store := gadgetStore()
			before := store.state.clone()
			err := op.run(newService(store), 0)
			wantVersionConflict(t, err, 1)
			if !reflect.DeepEqual(before, store.state) {
				t.Errorf("stale write changed the store")
			}
		})
	}
}

// Field edits refuse a stale field version. The type version is not checked by
// rename and delete, so a stale field version is the only conflict.
func TestService_StaleFieldVersionWritesNothing(t *testing.T) {
	ops := []struct {
		name string
		run  func(m *Service, v int) error
	}{
		{"rename", func(m *Service, v int) error {
			_, err := m.RenameField(t.Context(), "gadget", "rating", v, "Stars")
			return err
		}},
		{"delete", func(m *Service, v int) error {
			_, err := m.DeleteField(t.Context(), "gadget", "rating", v)
			return err
		}},
	}
	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			store := &fakeStore{}
			store.addType(Type{Key: "gadget", Name: "Gadget"}, Field{Key: "rating", Label: "Rating", Type: "number", Version: 3})
			before := store.state.clone()
			err := op.run(newService(store), 2)
			wantVersionConflict(t, err, 3)
			if !reflect.DeepEqual(before, store.state) {
				t.Errorf("stale field write changed the store")
			}
		})
	}
}

func TestService_MissingTypeOrFieldIsNotFound(t *testing.T) {
	store := gadgetStore()
	m := newService(store)
	_, err := m.CreateField(t.Context(), "nope", 1, CreateField{Label: "A", Type: "text"})
	wantNotFound(t, err, "type")
	_, err = m.ReorderFields(t.Context(), "nope", 1, nil)
	wantNotFound(t, err, "type")
	_, err = m.FieldDeletePreview(t.Context(), "nope", "rating")
	wantNotFound(t, err, "type")
	_, err = m.FieldDeletePreview(t.Context(), "gadget", "nope")
	wantNotFound(t, err, "type field")
	_, err = m.RenameField(t.Context(), "gadget", "nope", 1, "x")
	wantNotFound(t, err, "type field")
	_, err = m.DeleteField(t.Context(), "gadget", "nope", 1)
	wantNotFound(t, err, "type field")
}

func TestService_LockedFieldIsRefused(t *testing.T) {
	store := gadgetStore()
	m := newService(store)
	if _, err := m.RenameField(t.Context(), "movie", "release_year", 1, "Year"); err == nil {
		t.Fatal("rename of a locked field succeeded")
	} else if fields := validationFields(t, err); !slices.Equal(fields, []string{"field"}) {
		t.Errorf("rename fields = %v", fields)
	}
	if _, err := m.DeleteField(t.Context(), "movie", "release_year", 1); err == nil {
		t.Fatal("delete of a locked field succeeded")
	} else if fields := validationFields(t, err); !slices.Equal(fields, []string{"field"}) {
		t.Errorf("delete fields = %v", fields)
	}
	if _, err := m.FieldDeletePreview(t.Context(), "movie", "release_year"); err == nil {
		t.Fatal("preview of a locked field succeeded")
	}
}

func TestService_CreateField(t *testing.T) {
	tests := []struct {
		name       string
		label      string
		fieldType  string
		wantKey    string
		wantFields []string
	}{
		{name: "key from label", label: "Player age!", fieldType: "number", wantKey: "player_age"},
		{name: "digit start gets a prefix", label: "2nd rating", fieldType: "number", wantKey: "field_2nd_rating"},
		{name: "label without a letter", label: "!!!", fieldType: "text", wantFields: []string{"label"}},
		{name: "key already exists", label: "Rating", fieldType: "number", wantFields: []string{"label"}},
		{name: "built-in only type", label: "Notes", fieldType: "streaming", wantFields: []string{"type"}},
		{name: "label too long", label: strings.Repeat("x", 101), fieldType: "text", wantFields: []string{"label"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := gadgetStore()
			before := store.state.clone()
			field, err := newService(store).CreateField(t.Context(), "gadget", 1, CreateField{Label: tt.label, Type: tt.fieldType})
			if tt.wantFields != nil {
				if fields := validationFields(t, err); !slices.Equal(fields, tt.wantFields) {
					t.Fatalf("fields = %v, want %v", fields, tt.wantFields)
				}
				if !reflect.DeepEqual(before, store.state) {
					t.Errorf("refused create changed the store")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if field.Key != tt.wantKey || field.Position != 2 || field.Version != 1 {
				t.Errorf("field = %#v", field)
			}
			if typ := store.typeByKey("gadget"); typ.Version != 2 {
				t.Errorf("type version = %d, want 2 (field-set edits bump it)", typ.Version)
			}
		})
	}
}

func TestService_RenameFieldBumpsFieldOnly(t *testing.T) {
	store := gadgetStore()
	field, err := newService(store).RenameField(t.Context(), "gadget", "rating", 1, "Stars")
	if err != nil {
		t.Fatal(err)
	}
	if field.Label != "Stars" || field.Version != 2 {
		t.Errorf("renamed field = %#v", field)
	}
	if typ := store.typeByKey("gadget"); typ.Version != 1 {
		t.Errorf("type version = %d, want 1", typ.Version)
	}
}

// Pass 1 parks every custom field above the highest position, without a version
// bump; pass 2 sets locked count plus index, one bump each. The fake refuses a
// position that is taken, so a naive one-pass swap would fail here.
func TestService_ReorderTwoPasses(t *testing.T) {
	store := &fakeStore{}
	typ := store.addType(Type{Key: "gadget", Name: "Gadget"},
		Field{Key: "base", Label: "Base", Type: "text", Position: 0, Locked: true},
		Field{Key: "a", Label: "A", Type: "text", Position: 1},
		Field{Key: "b", Label: "B", Type: "text", Position: 2},
		Field{Key: "c", Label: "C", Type: "text", Position: 3},
	)
	version, err := newService(store).ReorderFields(t.Context(), "gadget", typ.Version, []string{"c", "a", "b"})
	if err != nil {
		t.Fatalf("ReorderFields() = %v", err)
	}
	if version != typ.Version+1 {
		t.Errorf("returned version = %d, want %d", version, typ.Version+1)
	}
	want := map[string]int{"base": 0, "c": 1, "a": 2, "b": 3}
	for key, position := range want {
		field := store.fieldByKey("gadget", key)
		if field.Position != position {
			t.Errorf("%s position = %d, want %d", key, field.Position, position)
		}
		wantVersion := 1
		if key != "base" {
			wantVersion = 2
		}
		if field.Version != wantVersion {
			t.Errorf("%s version = %d, want %d", key, field.Version, wantVersion)
		}
	}
	if got := store.typeByKey("gadget").Version; got != 2 {
		t.Errorf("type version = %d, want 2", got)
	}
	firstSet, lastPark := -1, -1
	for i, call := range store.state.calls {
		if strings.HasPrefix(call, "set ") && firstSet < 0 {
			firstSet = i
		}
		if strings.HasPrefix(call, "park ") {
			lastPark = i
		}
	}
	if firstSet < 0 || lastPark > firstSet {
		t.Errorf("calls = %v, want every park before the first set", store.state.calls)
	}
}

func TestService_ReorderRefusesUnknownOrDuplicateKeys(t *testing.T) {
	tests := []struct {
		name string
		keys []string
	}{
		{name: "unknown key", keys: []string{"rating", "nope"}},
		{name: "duplicate key", keys: []string{"rating", "rating"}},
		{name: "missing key", keys: []string{"rating"}},
		{name: "locked key", keys: []string{"release_year", "rating"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := gadgetStore()
			before := store.state.clone()
			_, err := newService(store).ReorderFields(t.Context(), "gadget", 1, tt.keys)
			wantOrderStale(t, err)
			if !reflect.DeepEqual(before, store.state) {
				t.Errorf("refused reorder changed the store")
			}
		})
	}
}

func TestService_FieldDeletePreviewCountsValuesWithoutWriting(t *testing.T) {
	store := gadgetStore()
	store.setValues("gadget", "rating", 2)
	before := store.state.clone()
	count, err := newService(store).FieldDeletePreview(t.Context(), "gadget", "rating")
	if err != nil || count != 2 {
		t.Fatalf("FieldDeletePreview() = %d, %v", count, err)
	}
	if !reflect.DeepEqual(before, store.state) {
		t.Errorf("preview changed the store")
	}
}

func TestService_DeleteFieldResetsReferences(t *testing.T) {
	store := &fakeStore{}
	typ := store.addType(Type{
		Key: "gadget", Name: "Gadget", DefaultSortField: ptr("rating"),
	},
		Field{Key: "rating", Label: "Rating", Type: "number", Position: 0},
		Field{Key: "brand", Label: "Brand", Type: "text", Position: 1},
	)
	store.addList("Games", "gadget", "rating", true)
	store.addList("Other", "gadget", "manual", true)
	store.setValues("gadget", "rating", 3)

	count, err := newService(store).DeleteField(t.Context(), "gadget", "rating", 1)
	if err != nil || count != 3 {
		t.Fatalf("DeleteField() = %d, %v", count, err)
	}
	got := store.typeByKey("gadget")
	if got.Version != typ.Version+1 {
		t.Errorf("type version = %d, want %d", got.Version, typ.Version+1)
	}
	if got.DefaultSortField != nil {
		t.Errorf("default sort = %v, want nil", *got.DefaultSortField)
	}
	games := store.state.lists[0]
	if games.sort != "manual" {
		t.Errorf("Games sort = %q, want manual", games.sort)
	}
	other := store.state.lists[1]
	if other.sort != "manual" {
		t.Errorf("Other changed: sort = %q", other.sort)
	}
	if n := store.state.values["gadget/rating"]; n != 0 {
		t.Errorf("values left = %d", n)
	}
	if field := store.fieldByKey("gadget", "rating"); field.ID != uuid.Nil {
		t.Errorf("field still stored")
	}
}

func TestService_DeleteFieldStaleVersionAfterRenameWritesNothing(t *testing.T) {
	store := gadgetStore()
	m := newService(store)
	if _, err := m.RenameField(t.Context(), "gadget", "rating", 1, "Stars"); err != nil {
		t.Fatal(err)
	}
	before := store.state.clone()
	_, err := m.DeleteField(t.Context(), "gadget", "rating", 1)
	wantVersionConflict(t, err, 2)
	if !reflect.DeepEqual(before, store.state) {
		t.Errorf("stale delete changed the store")
	}
}
