package database_test

import (
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 000023 (F-016 cleanup, API 0.13.0) removes the built-in book's first publication year,
// drops `openlibrary` from the stores, and turns the preferred stores list into one
// preferred_store. Custom types (incl. `book_custom`) are untouched. Down is lossy for data.
func TestMigration000023_BookCleanup(t *testing.T) {
	ctx := t.Context()
	dsn := newDatabase(t)
	m := newMigrate(t, dsn)
	if err := m.Migrate(22); err != nil {
		t.Fatalf("migrate to 22: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	const (
		wsA = "00000000-0000-0000-0000-000000002301" // preferred [openlibrary, kobo], book data
		wsB = "00000000-0000-0000-0000-000000002302" // preferred [openlibrary] only
		wsC = "00000000-0000-0000-0000-000000002303" // preferred []
		wsD = "00000000-0000-0000-0000-000000002304" // preferred [bookshop, openlibrary]

		listSorted = "00000000-0000-0000-0000-000000002311" // sort first_publish_year, pins [first_publish_year, author, stores]
		listPinned = "00000000-0000-0000-0000-000000002312" // manual, pins [first_publish_year]
		listOther  = "00000000-0000-0000-0000-000000002313" // sort title, pins [author]: untouched
		listCustom = "00000000-0000-0000-0000-000000002314" // book_custom, sort first_publish_year
		itemBook   = "00000000-0000-0000-0000-000000002321"
		itemTrash  = "00000000-0000-0000-0000-000000002322"
		itemOnlyOL = "00000000-0000-0000-0000-000000002323"
		itemCustom = "00000000-0000-0000-0000-000000002324"
		itemTitle  = "00000000-0000-0000-0000-000000002325"
		customType = "00000000-0000-0000-0000-000000002331"
	)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%.70s: %v", sql, err)
		}
	}
	str := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := pool.QueryRow(ctx, sql, args...).Scan(&s); err != nil {
			t.Fatalf("%.70s: %v", sql, err)
		}
		return s
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("%.70s: %v", sql, err)
		}
		return n
	}
	for i, ws := range []string{wsA, wsB, wsC, wsD} {
		exec(`INSERT INTO workspaces (id, name) VALUES ($1, $2)`, ws, "W"+string(rune('A'+i))) // the trigger seeds the built-ins
	}
	for ws, stores := range map[string]string{wsA: "{openlibrary,kobo}", wsB: "{openlibrary}", wsC: "{}", wsD: "{bookshop,openlibrary}"} {
		exec(`INSERT INTO workspace_settings (workspace_id, preferred_stores) VALUES ($1, $2::text[])`, ws, stores)
	}
	exec(`INSERT INTO lists (id, workspace_id, name, type, sort_mode, pinned_fields, sort_order) VALUES ($1, $2, 'Sorted', 'book', 'first_publish_year', '["first_publish_year","author","stores"]', 0)`, listSorted, wsA)
	exec(`INSERT INTO lists (id, workspace_id, name, type, sort_mode, pinned_fields, sort_order) VALUES ($1, $2, 'Pinned', 'book', 'manual', '["first_publish_year"]', 1)`, listPinned, wsA)
	exec(`INSERT INTO lists (id, workspace_id, name, type, sort_mode, pinned_fields, sort_order) VALUES ($1, $2, 'Other', 'book', 'title', '["author"]', 2)`, listOther, wsA)
	exec(`INSERT INTO type_definitions (id, workspace_id, key, name) VALUES ($1, $2, 'book_custom', 'Mine')`, customType, wsA)
	exec(`INSERT INTO type_fields (workspace_id, type_id, key, label, field_type, position) VALUES ($1, $2, 'first_publish_year', 'Year', 'number', 0)`, wsA, customType)
	exec(`INSERT INTO lists (id, workspace_id, name, type, sort_mode, sort_order) VALUES ($1, $2, 'Custom', 'book_custom', 'first_publish_year', 3)`, listCustom, wsA)
	exec(`INSERT INTO items (id, workspace_id, list_id, title, metadata, sort_order) VALUES ($1, $2, $3, 'Dune', '{"author":"Herbert","first_publish_year":1965,"stores":["openlibrary","kobo"],"openlibrary_id":"OL1W"}', 0)`, itemBook, wsA, listSorted)
	exec(`INSERT INTO trash_batches (id, workspace_id, root_type, root_id, root_name, former_path)
		VALUES ('00000000-0000-0000-0000-000000002341', $1, 'item', $2, 'Trashed', '[]')`, wsA, itemTrash)
	exec(`INSERT INTO items (id, workspace_id, list_id, title, metadata, sort_order, deleted_at, trash_batch_id)
		VALUES ($1, $2, $3, 'Trashed', '{"first_publish_year":1,"stores":["openlibrary"]}', 1, now(), '00000000-0000-0000-0000-000000002341')`, itemTrash, wsA, listSorted)
	exec(`INSERT INTO items (id, workspace_id, list_id, title, metadata, sort_order) VALUES ($1, $2, $3, 'OnlyOL', '{"stores":["openlibrary"]}', 2)`, itemOnlyOL, wsA, listPinned)
	exec(`INSERT INTO items (id, workspace_id, list_id, title, metadata, sort_order) VALUES ($1, $2, $3, 'Mine', '{"first_publish_year":2001,"stores":["openlibrary"]}', 0)`, itemCustom, wsA, listCustom)
	exec(`INSERT INTO items (id, workspace_id, list_id, title, metadata, sort_order) VALUES ($1, $2, $3, 'Clean', '{"stores":["bookshop"]}', 0)`, itemTitle, wsA, listOther)
	exec(`INSERT INTO lookup_imports (workspace_id, item_id, provider, source_id, type, imported_fields, original_title, fetched_at)
		VALUES ($1, $2, 'openlibrary', 'OL1W', 'book', '{"author":"Herbert","first_publish_year":1965}', 'dune', now())`, wsA, itemBook)

	if err := m.Steps(1); err != nil {
		t.Fatalf("migrate up 000023: %v", err)
	}

	bookFields := func(ws string) []string {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT f.position::text || ':' || f.key FROM type_fields f
			JOIN type_definitions d ON d.id = f.type_id WHERE d.workspace_id = $1 AND d.key = 'book' AND d.is_locked ORDER BY f.position`, ws)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var got []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			got = append(got, s)
		}
		return got
	}
	wantFields := []string{"0:author", "1:pages", "2:isbn", "3:stores", "4:openlibrary_id", "5:lookup_disabled"}

	t.Run("the built-in book has no first publication year, positions are compact", func(t *testing.T) {
		for _, ws := range []string{wsA, wsB, wsC, wsD} {
			if got := bookFields(ws); !reflect.DeepEqual(got, wantFields) {
				t.Errorf("book fields in %s = %v, want %v", ws, got, wantFields)
			}
			if sort := count(`SELECT count(*) FROM type_definitions WHERE workspace_id = $1 AND key = 'book' AND default_sort_field IS NULL`, ws); sort != 1 {
				t.Errorf("book default_sort_field is not NULL in %s", ws)
			}
			if pins := str(`SELECT default_pinned_fields::text FROM type_definitions WHERE workspace_id = $1 AND key = 'book'`, ws); pins != `["author", "pages", "stores"]` {
				t.Errorf("book default pins = %s", pins)
			}
		}
	})

	t.Run("item metadata, snapshots and stores are cleaned", func(t *testing.T) {
		if md := str(`SELECT metadata::text FROM items WHERE id = $1`, itemBook); md != `{"author": "Herbert", "stores": ["kobo"], "openlibrary_id": "OL1W"}` {
			t.Errorf("book item = %s", md)
		}
		if md := str(`SELECT metadata::text FROM items WHERE id = $1`, itemTrash); md != `{"stores": []}` {
			t.Errorf("trashed book item = %s (trashed items are cleaned too)", md)
		}
		if md := str(`SELECT metadata::text FROM items WHERE id = $1`, itemOnlyOL); md != `{"stores": []}` {
			t.Errorf("only-openlibrary item = %s", md)
		}
		if v := count(`SELECT version FROM items WHERE id = $1`, itemTitle); v != 1 {
			t.Errorf("an item without the stripped keys was bumped to v%d", v)
		}
		if f := str(`SELECT imported_fields::text FROM lookup_imports WHERE item_id = $1`, itemBook); f != `{"author": "Herbert"}` {
			t.Errorf("snapshot = %s", f)
		}
	})

	t.Run("lists sorted or pinned by the field fall back", func(t *testing.T) {
		if sm := str(`SELECT sort_mode FROM lists WHERE id = $1`, listSorted); sm != "manual" {
			t.Errorf("sorted list sort_mode = %q, want manual", sm)
		}
		if p := str(`SELECT pinned_fields::text FROM lists WHERE id = $1`, listSorted); p != `["author", "stores"]` {
			t.Errorf("sorted list pins = %s", p)
		}
		if n := count(`SELECT count(*) FROM lists WHERE id = $1 AND pinned_fields IS NULL AND sort_mode = 'manual' AND version = 2`, listPinned); n != 1 {
			t.Error("a list pinning only the field must get NULL pins (type default) and a version bump")
		}
		if n := count(`SELECT count(*) FROM lists WHERE id = $1 AND version = 1 AND sort_mode = 'title' AND pinned_fields = '["author"]'`, listOther); n != 1 {
			t.Error("an unrelated list changed")
		}
	})

	t.Run("a custom type keeps its field, sort and stores", func(t *testing.T) {
		if n := count(`SELECT count(*) FROM type_fields WHERE type_id = $1 AND key = 'first_publish_year'`, customType); n != 1 {
			t.Error("the custom field was deleted")
		}
		if sm := str(`SELECT sort_mode FROM lists WHERE id = $1`, listCustom); sm != "first_publish_year" {
			t.Errorf("custom list sort_mode = %q", sm)
		}
		if md := str(`SELECT metadata::text FROM items WHERE id = $1`, itemCustom); md != `{"stores": ["openlibrary"], "first_publish_year": 2001}` {
			t.Errorf("custom item = %s", md)
		}
	})

	t.Run("preferred_stores becomes preferred_store", func(t *testing.T) {
		for ws, want := range map[string]string{wsA: "kobo", wsB: "amazon", wsC: "amazon", wsD: "bookshop"} {
			if got := str(`SELECT preferred_store FROM workspace_settings WHERE workspace_id = $1`, ws); got != want {
				t.Errorf("preferred_store in %s = %q, want %q", ws, got, want)
			}
		}
		if n := count(`SELECT count(*) FROM information_schema.columns WHERE table_name = 'workspace_settings' AND column_name = 'preferred_stores'`); n != 0 {
			t.Error("preferred_stores still exists")
		}
		for _, bad := range []string{"'openlibrary'", "'audible'", "NULL"} {
			if _, err := pool.Exec(ctx, `UPDATE workspace_settings SET preferred_store = `+bad+` WHERE workspace_id = $1`, wsA); err == nil {
				t.Errorf("preferred_store = %s was accepted", bad)
			}
		}
	})

	t.Run("a fresh workspace is seeded without the field and with a manual default sort", func(t *testing.T) {
		const ws = "00000000-0000-0000-0000-000000002305"
		exec(`INSERT INTO workspaces (id, name) VALUES ($1, 'Fresh')`, ws)
		if got := bookFields(ws); !reflect.DeepEqual(got, wantFields) {
			t.Errorf("fresh book fields = %v, want %v", got, wantFields)
		}
		if n := count(`SELECT count(*) FROM type_definitions WHERE workspace_id = $1 AND key = 'book' AND default_sort_field IS NULL`, ws); n != 1 {
			t.Error("fresh book default_sort_field is not NULL")
		}
		if n := count(`SELECT count(*) FROM type_fields WHERE workspace_id = $1`, ws); n != 35 {
			t.Errorf("fresh built-in field count = %d, want 35", n)
		}
	})

	t.Run("down restores the structure (data is not restored), then up again", func(t *testing.T) {
		if err := m.Steps(-1); err != nil {
			t.Fatalf("migrate down 000023: %v", err)
		}
		want := []string{"0:author", "1:pages", "2:first_publish_year", "3:isbn", "4:stores", "5:openlibrary_id", "6:lookup_disabled"}
		if got := bookFields(wsA); !reflect.DeepEqual(got, want) {
			t.Errorf("book fields after down = %v, want %v", got, want)
		}
		if n := count(`SELECT count(*) FROM type_definitions WHERE workspace_id = $1 AND key = 'book' AND default_sort_field = 'first_publish_year'`, wsA); n != 1 {
			t.Error("book default sort not restored")
		}
		if got := str(`SELECT preferred_stores::text FROM workspace_settings WHERE workspace_id = $1`, wsA); got != "{kobo}" {
			t.Errorf("preferred_stores after down = %s", got)
		}
		if md := str(`SELECT metadata::text FROM items WHERE id = $1`, itemBook); md != `{"author": "Herbert", "stores": ["kobo"], "openlibrary_id": "OL1W"}` {
			t.Errorf("down must not touch item data: %s", md)
		}
		if err := m.Steps(1); err != nil {
			t.Fatalf("migrate up 000023 again: %v", err)
		}
		if got := bookFields(wsA); !reflect.DeepEqual(got, wantFields) {
			t.Errorf("book fields after second up = %v, want %v", got, wantFields)
		}
		if got := str(`SELECT preferred_store FROM workspace_settings WHERE workspace_id = $1`, wsA); got != "kobo" {
			t.Errorf("preferred_store after second up = %q", got)
		}
	})
}
