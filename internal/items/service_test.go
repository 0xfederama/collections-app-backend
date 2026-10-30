package items_test

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/items/core"
	"github.com/0xfederama/collections-app-backend/internal/items/types"
)

// fakeDB is an in-memory Store, Repo, TypeSource and SettingsSource. A failed
// transaction rolls back, as the database does. Every write is recorded in calls,
// and a type or settings read made inside a transaction is a test failure.
type fakeDB struct {
	t        *testing.T
	items    map[uuid.UUID]core.Item
	lists    map[uuid.UUID]items.ListInfo
	paths    map[uuid.UUID][]core.PathSegment // optional; PathOf defaults to the list alone
	versions map[uuid.UUID]int                // content version by item
	defs     map[string]items.TypeDef
	fields   map[string][]types.Field

	preferred   string
	noSettings  bool  // no settings row
	settingsErr error // a failing settings read

	calls    []string
	inTx     bool
	beforeTx func(*fakeDB) // runs once when a transaction opens: a concurrent write
}

func newFakeDB(t *testing.T) *fakeDB {
	return &fakeDB{
		t:     t,
		items: map[uuid.UUID]core.Item{}, lists: map[uuid.UUID]items.ListInfo{},
		paths: map[uuid.UUID][]core.PathSegment{}, versions: map[uuid.UUID]int{},
		defs: map[string]items.TypeDef{
			"movie":    {Key: "movie"},
			"vehicle":  {Key: "vehicle"},
			"wishlist": {Key: "wishlist", AllowsPrice: true},
		},
		fields: map[string][]types.Field{
			"movie":    {{Key: "release_year", Type: "number"}, {Key: "director", Type: "text"}, {Key: "owned", Type: "boolean"}},
			"vehicle":  {{Key: "make", Type: "text"}},
			"wishlist": {{Key: "store", Type: "text"}, {Key: "stores", Type: "stores"}},
		},
		preferred: "kobo",
	}
}

func (f *fakeDB) InTx(_ context.Context, fn func(items.Repo) error) error {
	if hook := f.beforeTx; hook != nil {
		f.beforeTx = nil
		hook(f)
	}
	savedItems, savedLists, savedCalls := maps.Clone(f.items), maps.Clone(f.lists), len(f.calls)
	f.inTx = true
	defer func() { f.inTx = false }()
	if err := fn(f); err != nil {
		f.items, f.lists, f.calls = savedItems, savedLists, f.calls[:savedCalls]
		return err
	}
	return nil
}

func (f *fakeDB) LockTree(context.Context) error {
	f.calls = append(f.calls, "lock")
	return nil
}

// view fills the list type, as the item read joins the list.
func (f *fakeDB) view(it core.Item) core.Item {
	if l, ok := f.lists[it.ListID]; ok {
		it.Type = l.Type
	}
	return it
}

func (f *fakeDB) Get(_ context.Context, id uuid.UUID) (core.Item, error) {
	it, ok := f.items[id]
	if !ok {
		return core.Item{}, apperr.NotFound("item")
	}
	return f.view(it), nil
}

func (f *fakeDB) GetForUpdate(ctx context.Context, id uuid.UUID) (core.Item, error) {
	return f.Get(ctx, id)
}

func (f *fakeDB) ListByList(_ context.Context, listID uuid.UUID) ([]core.Item, error) {
	out := []core.Item{}
	for _, it := range f.items {
		if it.ListID == listID {
			out = append(out, f.view(it))
		}
	}
	slices.SortFunc(out, func(a, b core.Item) int { return a.SortOrder - b.SortOrder })
	return out, nil
}

func (f *fakeDB) ListInfo(_ context.Context, listID uuid.UUID) (items.ListInfo, error) {
	l, ok := f.lists[listID]
	if !ok {
		return items.ListInfo{}, apperr.NotFound("list")
	}
	return l, nil
}

func (f *fakeDB) PathOf(_ context.Context, listID uuid.UUID) ([]core.PathSegment, error) {
	l, ok := f.lists[listID]
	if !ok {
		return nil, apperr.NotFound("list")
	}
	if p, ok := f.paths[listID]; ok {
		return p, nil
	}
	return []core.PathSegment{{Type: "list", ID: l.ID, Name: l.Name}}, nil
}

func (f *fakeDB) MaxContentVersions(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]int, error) {
	out := map[uuid.UUID]int{}
	for _, id := range ids {
		if v, ok := f.versions[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

func (f *fakeDB) NextSortOrder(_ context.Context, listID uuid.UUID) (int, error) {
	next := 0
	for _, it := range f.items {
		if it.ListID == listID && it.SortOrder+1 > next {
			next = it.SortOrder + 1
		}
	}
	return next, nil
}

func (f *fakeDB) Insert(_ context.Context, it core.Item, now time.Time) (uuid.UUID, error) {
	f.calls = append(f.calls, "insert")
	it.ID = uuid.New()
	it.Version = 1
	it.CreatedAt, it.UpdatedAt = now, now
	f.items[it.ID] = it
	return it.ID, nil
}

func (f *fakeDB) Update(_ context.Context, it core.Item, now time.Time) error {
	f.calls = append(f.calls, "update")
	cur, ok := f.items[it.ID]
	if !ok {
		return apperr.NotFound("item")
	}
	cur.Title, cur.Price, cur.Website, cur.Metadata = it.Title, it.Price, it.Website, it.Metadata
	cur.Version++
	cur.UpdatedAt = now
	f.items[it.ID] = cur
	return nil
}

func (f *fakeDB) Move(_ context.Context, id, listID uuid.UUID, sortOrder int, now time.Time) error {
	f.calls = append(f.calls, "move")
	cur := f.items[id]
	cur.ListID, cur.SortOrder, cur.Version, cur.UpdatedAt = listID, sortOrder, cur.Version+1, now
	f.items[id] = cur
	return nil
}

func (f *fakeDB) SetSortOrder(_ context.Context, id uuid.UUID, sortOrder int, now time.Time) error {
	f.calls = append(f.calls, "sort")
	cur := f.items[id]
	cur.SortOrder, cur.UpdatedAt = sortOrder, now
	f.items[id] = cur
	return nil
}

func (f *fakeDB) TypeFields(_ context.Context, typ *string) (items.TypeDef, []types.Field, error) {
	if f.inTx {
		f.t.Error("type read inside the transaction")
	}
	if typ == nil {
		return items.TypeDef{}, nil, nil
	}
	def, ok := f.defs[*typ]
	if !ok {
		return items.TypeDef{}, nil, apperr.NotFound("type")
	}
	return def, f.fields[*typ], nil
}

func (f *fakeDB) PreferredStore(context.Context) (string, error) {
	if f.inTx {
		f.t.Error("settings read inside the transaction")
	}
	if f.settingsErr != nil {
		return "", f.settingsErr
	}
	if f.noSettings {
		return "", apperr.NotFound("settings")
	}
	return f.preferred, nil
}

// writes returns the write calls made, in order (the tree lock is not a write).
func (f *fakeDB) writes() []string {
	var out []string
	for _, c := range f.calls {
		if c != "lock" {
			out = append(out, c)
		}
	}
	return out
}

// addList adds an active list of typ and returns its id.
func (f *fakeDB) addList(typ *string, sortMode string) uuid.UUID {
	id := uuid.New()
	f.lists[id] = items.ListInfo{ID: id, Name: "L", SortMode: sortMode, Type: typ}
	return id
}

// addItem adds an active item at a position; meta is raw JSON ("" is {}).
func (f *fakeDB) addItem(list uuid.UUID, title string, order int, meta string) uuid.UUID {
	id := uuid.New()
	if meta == "" {
		meta = "{}"
	}
	f.items[id] = core.Item{ID: id, ListID: list, Title: title, SortOrder: order, Version: 1, Metadata: json.RawMessage(meta)}
	return id
}

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Unix(1_700_000_000, 0).UTC() }

func strPtr(s string) *string { return &s }

var (
	movie    = strPtr("movie")
	vehicle  = strPtr("vehicle")
	wishlist = strPtr("wishlist")
	ghost    = strPtr("ghost") // a type with no definition
)

// describe names an error for the tables: "ok", "validation:<fields>", "notfound:<resource>",
// "conflict:<code>", "version_conflict", "unknown:<path>", "invalid_sort", "invalid_filter".
func describe(err error) string {
	var (
		v  *apperr.ValidationError
		nf *apperr.NotFoundError
		c  *apperr.ConflictError
		vc *apperr.VersionConflictError
		uf *apperr.UnknownFieldError
	)
	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &v):
		names := make([]string, 0, len(v.Errors))
		for _, fe := range v.Errors {
			names = append(names, fe.Field)
		}
		return "validation:" + strings.Join(names, ",")
	case errors.As(err, &nf):
		return "notfound:" + nf.Resource
	case errors.As(err, &c):
		return "conflict:" + string(c.Code)
	case errors.As(err, &vc):
		return "version_conflict"
	case errors.As(err, &uf):
		return "unknown:" + uf.Path
	case errors.Is(err, core.ErrInvalidSort):
		return "invalid_sort"
	case errors.Is(err, core.ErrInvalidFilter):
		return "invalid_filter"
	}
	return "error:" + err.Error()
}

// listOf adds an active list of typ.
func listOf(typ *string) func(*fakeDB) uuid.UUID {
	return func(db *fakeDB) uuid.UUID { return db.addList(typ, "manual") }
}

func missingList(*fakeDB) uuid.UUID { return uuid.New() }

// retypedBeforeTx adds a list of type from that becomes to when the next
// transaction opens, as a concurrent type change would.
func retypedBeforeTx(from, to *string) func(*fakeDB) uuid.UUID {
	return func(db *fakeDB) uuid.UUID {
		id := db.addList(from, "manual")
		db.beforeTx = func(d *fakeDB) {
			l := d.lists[id]
			l.Type = to
			d.lists[id] = l
		}
		return id
	}
}

func metaOf(t *testing.T, it core.Item) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(it.Metadata, &m); err != nil {
		t.Fatalf("metadata %s: %v", it.Metadata, err)
	}
	return m
}

func eur(amount string) *core.Price {
	return &core.Price{Min: strPtr(amount), Max: strPtr(amount), Currency: "EUR"}
}

func TestCreate_ErrorOrder(t *testing.T) {
	price := eur("12.00")
	tests := []struct {
		name string
		list func(*fakeDB) uuid.UUID
		in   core.CreateInput
		want string
	}{
		{"field rules come before a missing list", missingList, core.CreateInput{Title: ""}, "validation:title"},
		{"a missing list is 404", missingList, core.CreateInput{Title: "t"}, "notfound:list"},
		{"a price on a type without one is 422 before the metadata", listOf(movie), core.CreateInput{Title: "t", Price: price, Metadata: map[string]any{"make": "x"}}, "validation:price"},
		{"a price on a list with no type is 422", listOf(nil), core.CreateInput{Title: "t", Price: price}, "validation:price"},
		{"an undeclared metadata key is 400", listOf(movie), core.CreateInput{Title: "t", Metadata: map[string]any{"make": "VW"}}, "unknown:metadata.make"},
		{"metadata on a list with no type must be empty", listOf(nil), core.CreateInput{Title: "t", Metadata: map[string]any{"release_year": float64(1999)}}, "unknown:metadata.release_year"},
		{"a type without a definition is 422 on type", listOf(ghost), core.CreateInput{Title: "t"}, "validation:type"},
		{"a type changed before the transaction is a 409", retypedBeforeTx(movie, vehicle), core.CreateInput{Title: "t"}, "conflict:type_mismatch"},
		{"a price on a wishlist item is accepted", listOf(wishlist), core.CreateInput{Title: "t", Price: price}, "ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newFakeDB(t)
			listID := tt.list(db)
			_, err := items.NewItems(db, db, db, fixedClock{}).Create(t.Context(), listID, tt.in)
			if got := describe(err); got != tt.want {
				t.Fatalf("Create = %s, want %s", got, tt.want)
			}
			if tt.want == "ok" {
				if got := db.writes(); !reflect.DeepEqual(got, []string{"insert"}) {
					t.Fatalf("writes = %v, want [insert]", got)
				}
			} else if got := db.writes(); len(got) != 0 {
				t.Fatalf("rejected create wrote %v", got)
			}
		})
	}
}

func TestCreate_AppendsUnderTheTreeLock(t *testing.T) {
	db := newFakeDB(t)
	l := db.addList(movie, "manual")
	db.addItem(l, "a", 0, "")
	db.addItem(l, "b", 1, "")
	it, err := items.NewItems(db, db, db, fixedClock{}).Create(t.Context(), l, core.CreateInput{Title: "  c  "})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if it.Title != "c" || it.SortOrder != 2 || it.Version != 1 || it.ListID != l {
		t.Fatalf("created item = %+v", it)
	}
	if len(db.calls) == 0 || db.calls[0] != "lock" {
		t.Fatalf("calls = %v, want the tree lock first", db.calls)
	}
}

func TestCreate_PreferredStoreSeeding(t *testing.T) {
	tests := []struct {
		name       string
		meta       map[string]any
		noSettings bool
		want       any // the stored stores value, nil when absent
	}{
		{"an unset stores field takes the preferred store", nil, false, []any{"kobo"}},
		{"a null stores field takes the preferred store", map[string]any{"stores": nil}, false, []any{"kobo"}},
		{"an explicit empty list wins", map[string]any{"stores": []any{}}, false, []any{}},
		{"an explicit value wins", map[string]any{"stores": []any{"amazon"}}, false, []any{"amazon"}},
		{"no settings row seeds nothing", nil, true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newFakeDB(t)
			db.noSettings = tt.noSettings
			l := db.addList(wishlist, "manual")
			it, err := items.NewItems(db, db, db, fixedClock{}).Create(t.Context(), l, core.CreateInput{Title: "t", Metadata: tt.meta})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if got := metaOf(t, it)["stores"]; !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("stores = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestCreate_SettingsFailureOnlyWhenNeeded(t *testing.T) {
	boom := errors.New("settings read failed")

	db := newFakeDB(t)
	db.settingsErr = boom
	plain := db.addList(vehicle, "manual")
	if _, err := items.NewItems(db, db, db, fixedClock{}).Create(t.Context(), plain, core.CreateInput{Title: "t"}); err != nil {
		t.Fatalf("a type without stores fields must not need settings: %v", err)
	}

	withStores := db.addList(wishlist, "manual")
	_, err := items.NewItems(db, db, db, fixedClock{}).Create(t.Context(), withStores, core.CreateInput{Title: "t"})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the settings failure", err)
	}
	if got := db.writes(); !reflect.DeepEqual(got, []string{"insert"}) {
		t.Fatalf("writes = %v: only the first create may write", got)
	}
}

func TestGet_PathAndContentVersion(t *testing.T) {
	db := newFakeDB(t)
	folder := uuid.New()
	l := db.addList(movie, "manual")
	db.paths[l] = []core.PathSegment{{Type: "folder", ID: folder, Name: "Films"}, {Type: "list", ID: l, Name: "L"}}
	it := db.addItem(l, "a", 0, "")
	db.versions[it] = 3

	svc := items.NewItems(db, db, db, fixedClock{})
	got, err := svc.Get(t.Context(), it)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ContentVersion != 3 || got.Type == nil || *got.Type != "movie" {
		t.Fatalf("item = %+v", got)
	}
	if len(got.Path) != 2 || got.Path[0].Name != "Films" || got.Path[1].Type != "list" {
		t.Fatalf("path = %+v", got.Path)
	}
	if _, err := svc.Get(t.Context(), uuid.New()); describe(err) != "notfound:item" {
		t.Fatalf("missing item = %s", describe(err))
	}
}

func TestList_ErrorOrder(t *testing.T) {
	tests := []struct {
		name   string
		list   func(*fakeDB) uuid.UUID
		params core.ListParams
		want   string
	}{
		{"a missing list is 404", missingList, core.ListParams{}, "notfound:list"},
		{"a type without a definition is an invalid sort", listOf(ghost), core.ListParams{}, "invalid_sort"},
		{"sort by price on a type without one is an invalid sort", listOf(movie), core.ListParams{Sort: "price"}, "invalid_sort"},
		{"sort by an unknown field is an invalid sort", listOf(movie), core.ListParams{Sort: "nope"}, "invalid_sort"},
		{"a stores field is not sortable", listOf(wishlist), core.ListParams{Sort: "stores"}, "invalid_sort"},
		{"a filter value without a field is an invalid filter", listOf(movie), core.ListParams{FilterValue: "x"}, "invalid_filter"},
		{"a boolean filter takes true or false", listOf(movie), core.ListParams{FilterField: "owned", FilterValue: "maybe"}, "invalid_filter"},
		{"a number range with min above max is an invalid filter", listOf(movie), core.ListParams{FilterField: "release_year", FilterMin: "2000", FilterMax: "1990"}, "invalid_filter"},
		{"a valid filter and a field sort", listOf(movie), core.ListParams{Sort: "release_year", FilterField: "owned", FilterValue: "true"}, "ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newFakeDB(t)
			listID := tt.list(db)
			_, err := items.NewItems(db, db, db, fixedClock{}).List(t.Context(), listID, tt.params)
			if got := describe(err); got != tt.want {
				t.Fatalf("List = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestList_Rules(t *testing.T) {
	t.Run("the default sort is the list's sort mode", func(t *testing.T) {
		db := newFakeDB(t)
		l := db.addList(movie, "title")
		db.addItem(l, "b", 0, "")
		db.addItem(l, "A", 1, "")
		db.addItem(l, "c", 2, "")
		page, err := items.NewItems(db, db, db, fixedClock{}).List(t.Context(), l, core.ListParams{})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if got := titles(page); !reflect.DeepEqual(got, []string{"A", "b", "c"}) {
			t.Fatalf("titles = %v", got)
		}
	})

	t.Run("paging keeps the total and the offset", func(t *testing.T) {
		db := newFakeDB(t)
		l := db.addList(movie, "manual")
		for i, title := range []string{"a", "b", "c", "d", "e"} {
			db.addItem(l, title, i, "")
		}
		page, err := items.NewItems(db, db, db, fixedClock{}).List(t.Context(), l, core.ListParams{Limit: 2, Offset: 2})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if got := titles(page); !reflect.DeepEqual(got, []string{"c", "d"}) || page.Total != 5 || page.Limit != 2 || page.Offset != 2 {
			t.Fatalf("page = %v total=%d limit=%d offset=%d", got, page.Total, page.Limit, page.Offset)
		}
	})

	t.Run("content versions come with the page and other lists are not listed", func(t *testing.T) {
		db := newFakeDB(t)
		l := db.addList(movie, "manual")
		other := db.addList(movie, "manual")
		a := db.addItem(l, "a", 0, "")
		db.addItem(other, "elsewhere", 0, "")
		db.versions[a] = 3
		page, err := items.NewItems(db, db, db, fixedClock{}).List(t.Context(), l, core.ListParams{})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(page.Items) != 1 || page.Items[0].ContentVersion != 3 || page.Total != 1 {
			t.Fatalf("page = %+v", page)
		}
	})
}

func titles(page core.ItemPage) []string {
	out := make([]string, 0, len(page.Items))
	for _, it := range page.Items {
		out = append(out, it.Title)
	}
	return out
}

func TestUpdate_ErrorOrder(t *testing.T) {
	type setup func(*fakeDB) (uuid.UUID, int, core.Patch)
	movieItem := func(db *fakeDB) (uuid.UUID, uuid.UUID) {
		l := db.addList(movie, "manual")
		return db.addItem(l, "a", 0, `{"release_year":1999}`), l
	}
	tests := []struct {
		name  string
		setup setup
		want  string
	}{
		{"field rules come before a missing item", func(*fakeDB) (uuid.UUID, int, core.Patch) {
			return uuid.New(), 1, core.Patch{Title: strPtr("")}
		}, "validation:title"},
		{"a missing item is 404", func(*fakeDB) (uuid.UUID, int, core.Patch) {
			return uuid.New(), 1, core.Patch{Title: strPtr("x")}
		}, "notfound:item"},
		{"a stale version wins over bad metadata", func(db *fakeDB) (uuid.UUID, int, core.Patch) {
			id, _ := movieItem(db)
			it := db.items[id]
			it.Version = 2
			db.items[id] = it
			return id, 1, core.Patch{Metadata: map[string]any{"make": "x"}}
		}, "version_conflict"},
		{"a price on a type without one is 422 before the merge", func(db *fakeDB) (uuid.UUID, int, core.Patch) {
			id, _ := movieItem(db)
			return id, 1, core.Patch{Price: core.Optional[core.Price]{Set: true, Value: eur("1.00")}, Metadata: map[string]any{"make": "x"}}
		}, "validation:price"},
		{"an undeclared metadata key is 400", func(db *fakeDB) (uuid.UUID, int, core.Patch) {
			id, _ := movieItem(db)
			return id, 1, core.Patch{Metadata: map[string]any{"make": "x"}}
		}, "unknown:metadata.make"},
		{"a trashed list is 404", func(db *fakeDB) (uuid.UUID, int, core.Patch) {
			id, l := movieItem(db)
			delete(db.lists, l)
			return id, 1, core.Patch{Title: strPtr("x")}
		}, "notfound:list"},
		{"a type changed before the transaction is a version conflict", func(db *fakeDB) (uuid.UUID, int, core.Patch) {
			l := retypedBeforeTx(movie, vehicle)(db)
			id := db.addItem(l, "a", 0, "")
			return id, 1, core.Patch{Title: strPtr("x")}
		}, "version_conflict"},
		{"a price on a wishlist item is accepted", func(db *fakeDB) (uuid.UUID, int, core.Patch) {
			l := db.addList(wishlist, "manual")
			id := db.addItem(l, "a", 0, "")
			return id, 1, core.Patch{Price: core.Optional[core.Price]{Set: true, Value: eur("1.00")}}
		}, "ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newFakeDB(t)
			id, version, p := tt.setup(db)
			_, err := items.NewItems(db, db, db, fixedClock{}).Update(t.Context(), id, version, p)
			if got := describe(err); got != tt.want {
				t.Fatalf("Update = %s, want %s", got, tt.want)
			}
			if got := db.writes(); len(got) != 0 && tt.want != "ok" {
				t.Fatalf("rejected update wrote %v", got)
			}
		})
	}
}

func TestUpdate_Rules(t *testing.T) {
	t.Run("a patch that changes nothing writes nothing", func(t *testing.T) {
		db := newFakeDB(t)
		l := db.addList(movie, "manual")
		id := db.addItem(l, "a", 0, `{"release_year":1999}`)
		it, err := items.NewItems(db, db, db, fixedClock{}).Update(t.Context(), id, 1, core.Patch{
			Title:    strPtr("a"),
			Metadata: map[string]any{"release_year": float64(1999)},
		})
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if got := db.writes(); len(got) != 0 || it.Version != 1 {
			t.Fatalf("writes = %v version = %d, want none and 1", got, it.Version)
		}
	})

	t.Run("a changed title bumps the version", func(t *testing.T) {
		db := newFakeDB(t)
		l := db.addList(movie, "manual")
		id := db.addItem(l, "a", 0, "")
		it, err := items.NewItems(db, db, db, fixedClock{}).Update(t.Context(), id, 1, core.Patch{Title: strPtr("b")})
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if it.Title != "b" || it.Version != 2 {
			t.Fatalf("item = %+v", it)
		}
	})

	t.Run("a single price is stored as min and max", func(t *testing.T) {
		db := newFakeDB(t)
		l := db.addList(wishlist, "manual")
		id := db.addItem(l, "a", 0, "")
		p := core.Patch{Price: core.Optional[core.Price]{Set: true, Value: &core.Price{Amount: strPtr("3.00"), Currency: "EUR"}}}
		it, err := items.NewItems(db, db, db, fixedClock{}).Update(t.Context(), id, 1, p)
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if it.Price == nil || *it.Price.Min != "3.00" || *it.Price.Max != "3.00" {
			t.Fatalf("price = %+v", it.Price)
		}
	})
}

func TestMove_ErrorOrder(t *testing.T) {
	type setup func(*fakeDB) (uuid.UUID, int, uuid.UUID)
	tests := []struct {
		name  string
		setup setup
		want  string
	}{
		{"a missing item is 404", func(*fakeDB) (uuid.UUID, int, uuid.UUID) {
			return uuid.New(), 1, uuid.New()
		}, "notfound:item"},
		{"a stale version is a version conflict", func(db *fakeDB) (uuid.UUID, int, uuid.UUID) {
			l := db.addList(movie, "manual")
			id := db.addItem(l, "a", 0, "")
			return id, 2, db.addList(movie, "manual")
		}, "version_conflict"},
		{"a missing destination is destination_unavailable", func(db *fakeDB) (uuid.UUID, int, uuid.UUID) {
			id := db.addItem(db.addList(movie, "manual"), "a", 0, "")
			return id, 1, uuid.New()
		}, "conflict:destination_unavailable"},
		{"a list of another type is a type_mismatch", func(db *fakeDB) (uuid.UUID, int, uuid.UUID) {
			id := db.addItem(db.addList(movie, "manual"), "a", 0, "")
			return id, 1, db.addList(vehicle, "manual")
		}, "conflict:type_mismatch"},
		{"a typed item cannot go to a list with no type", func(db *fakeDB) (uuid.UUID, int, uuid.UUID) {
			id := db.addItem(db.addList(movie, "manual"), "a", 0, "")
			return id, 1, db.addList(nil, "manual")
		}, "conflict:type_mismatch"},
		{"a list with no type takes a list with no type", func(db *fakeDB) (uuid.UUID, int, uuid.UUID) {
			id := db.addItem(db.addList(nil, "manual"), "a", 0, "")
			return id, 1, db.addList(nil, "manual")
		}, "ok"},
		{"lists of the same type are fine", func(db *fakeDB) (uuid.UUID, int, uuid.UUID) {
			id := db.addItem(db.addList(movie, "manual"), "a", 0, "")
			return id, 1, db.addList(movie, "manual")
		}, "ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newFakeDB(t)
			id, version, dest := tt.setup(db)
			_, err := items.NewItems(db, db, db, fixedClock{}).Move(t.Context(), id, version, dest)
			if got := describe(err); got != tt.want {
				t.Fatalf("Move = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestMove_AppendsToDestination(t *testing.T) {
	db := newFakeDB(t)
	src := db.addList(movie, "manual")
	dest := db.addList(movie, "manual")
	db.addItem(dest, "x", 0, "")
	db.addItem(dest, "y", 1, "")
	id := db.addItem(src, "a", 0, "")
	it, err := items.NewItems(db, db, db, fixedClock{}).Move(t.Context(), id, 1, dest)
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if it.ListID != dest || it.SortOrder != 2 || it.Version != 2 {
		t.Fatalf("moved item = %+v", it)
	}
	if got := db.calls; len(got) == 0 || got[0] != "lock" {
		t.Fatalf("calls = %v, want the tree lock first", got)
	}
}

func TestReorder_ErrorOrder(t *testing.T) {
	type setup func(*fakeDB) (uuid.UUID, []uuid.UUID)
	twoItems := func(db *fakeDB) (uuid.UUID, uuid.UUID, uuid.UUID) {
		l := db.addList(movie, "manual")
		return l, db.addItem(l, "a", 0, ""), db.addItem(l, "b", 1, "")
	}
	tests := []struct {
		name  string
		setup setup
		want  string
	}{
		{"a missing list is 404", func(*fakeDB) (uuid.UUID, []uuid.UUID) {
			return uuid.New(), nil
		}, "notfound:list"},
		{"a missing id is order_stale", func(db *fakeDB) (uuid.UUID, []uuid.UUID) {
			l, a, _ := twoItems(db)
			return l, []uuid.UUID{a}
		}, "conflict:order_stale"},
		{"a foreign id is order_stale", func(db *fakeDB) (uuid.UUID, []uuid.UUID) {
			l, a, _ := twoItems(db)
			return l, []uuid.UUID{a, uuid.New()}
		}, "conflict:order_stale"},
		{"a repeated id is order_stale", func(db *fakeDB) (uuid.UUID, []uuid.UUID) {
			l, a, _ := twoItems(db)
			return l, []uuid.UUID{a, a}
		}, "conflict:order_stale"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newFakeDB(t)
			listID, ids := tt.setup(db)
			err := items.NewItems(db, db, db, fixedClock{}).Reorder(t.Context(), listID, ids)
			if got := describe(err); got != tt.want {
				t.Fatalf("Reorder = %s, want %s", got, tt.want)
			}
			if got := db.writes(); len(got) != 0 {
				t.Fatalf("rejected reorder wrote %v", got)
			}
		})
	}
}

func TestReorder_SetsPositionsAndKeepsVersions(t *testing.T) {
	db := newFakeDB(t)
	l := db.addList(movie, "manual")
	a := db.addItem(l, "a", 0, "")
	b := db.addItem(l, "b", 1, "")
	if err := items.NewItems(db, db, db, fixedClock{}).Reorder(t.Context(), l, []uuid.UUID{b, a}); err != nil {
		t.Fatalf("Reorder: %v", err)
	}
	if db.items[b].SortOrder != 0 || db.items[a].SortOrder != 1 {
		t.Fatalf("positions: b=%d a=%d", db.items[b].SortOrder, db.items[a].SortOrder)
	}
	if db.items[a].Version != 1 || db.items[b].Version != 1 {
		t.Fatal("reorder bumped item versions")
	}
	if got := db.calls; !slices.Equal(got, []string{"lock", "sort", "sort"}) {
		t.Fatalf("calls = %v", got)
	}
}
