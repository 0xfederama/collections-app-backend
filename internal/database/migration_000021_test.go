package database_test

import (
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 000021 (F-016, D-093, D-097) seeds the locked built-in `book` type. A custom type
// already keyed `book` is renamed to `book_custom` (a numeric suffix when taken) with
// its lists, fields and items untouched. Up is idempotent; down removes the built-in
// (its lists lose their type) and does not rename customs back.
func TestMigration000021_BookType(t *testing.T) {
	ctx := t.Context()
	dsn := newDatabase(t)
	m := newMigrate(t, dsn)
	if err := m.Migrate(20); err != nil {
		t.Fatalf("migrate to 20: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	const (
		wsA   = "00000000-0000-0000-0000-000000002101" // custom `book` with a list and items
		wsB   = "00000000-0000-0000-0000-000000002102" // custom `book` and a custom `book_custom`
		wsC   = "00000000-0000-0000-0000-000000002103" // no custom book
		defA  = "00000000-0000-0000-0000-000000002111"
		defB  = "00000000-0000-0000-0000-000000002112"
		defB2 = "00000000-0000-0000-0000-000000002113"
		listA = "00000000-0000-0000-0000-000000002121"
		itemA = "00000000-0000-0000-0000-000000002131"
	)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%.60s: %v", sql, err)
		}
	}
	str := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := pool.QueryRow(ctx, sql, args...).Scan(&s); err != nil {
			t.Fatalf("%.60s: %v", sql, err)
		}
		return s
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("%.60s: %v", sql, err)
		}
		return n
	}
	for i, ws := range []string{wsA, wsB, wsC} {
		exec(`INSERT INTO workspaces (id, name) VALUES ($1, $2)`, ws, "W"+string(rune('A'+i)))
	}
	exec(`INSERT INTO type_definitions (id, workspace_id, key, name) VALUES ($1, $2, 'book', 'Book')`, defA, wsA)
	exec(`INSERT INTO type_fields (workspace_id, type_id, key, label, field_type, position) VALUES ($1, $2, 'author', 'Author', 'text', 0)`, wsA, defA)
	exec(`INSERT INTO lists (id, workspace_id, name, type, sort_order) VALUES ($1, $2, 'Books', 'book', 0)`, listA, wsA)
	exec(`INSERT INTO items (id, workspace_id, list_id, title, metadata, sort_order) VALUES ($1, $2, $3, 'Dune', '{"author":"Herbert"}', 0)`, itemA, wsA, listA)
	exec(`INSERT INTO type_definitions (id, workspace_id, key, name) VALUES ($1, $2, 'book', 'Mine')`, defB, wsB)
	exec(`INSERT INTO type_definitions (id, workspace_id, key, name) VALUES ($1, $2, 'book_custom', 'Other')`, defB2, wsB)

	if err := m.Steps(1); err != nil {
		t.Fatalf("migrate up 000021: %v", err)
	}

	checkBuiltin := func(t *testing.T, ws string) {
		t.Helper()
		var name, sort string
		var price bool
		var pins string
		if err := pool.QueryRow(ctx, `SELECT name, allows_price, default_sort_field, default_pinned_fields::text FROM type_definitions
			WHERE workspace_id = $1 AND key = 'book' AND is_locked`, ws).Scan(&name, &price, &sort, &pins); err != nil {
			t.Fatalf("built-in book in %s: %v", ws, err)
		}
		if name != "Book" || !price || sort != "first_publish_year" || pins != `["author", "pages", "stores"]` {
			t.Errorf("built-in book = %q price=%v sort=%q pins=%s", name, price, sort, pins)
		}
		rows, err := pool.Query(ctx, `SELECT f.key || ':' || f.label || ':' || f.field_type || ':' || f.is_locked::text FROM type_fields f
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
		want := []string{
			"author:Writer:text:true", "pages:Pages:number:true", "first_publish_year:First publication year:number:true",
			"isbn:ISBN:text:true", "stores:Where to get it:stores:true", "openlibrary_id:Open Library ID:text:true",
			"lookup_disabled:Disable lookup:boolean:true",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("book fields = %v, want %v", got, want)
		}
	}

	t.Run("every workspace has the locked built-in", func(t *testing.T) {
		for _, ws := range []string{wsA, wsB, wsC} {
			checkBuiltin(t, ws)
		}
	})

	t.Run("a custom book type is renamed, its data follows", func(t *testing.T) {
		if k := str(`SELECT key FROM type_definitions WHERE id = $1`, defA); k != "book_custom" {
			t.Errorf("custom key in A = %q, want book_custom", k)
		}
		if k := str(`SELECT key FROM type_definitions WHERE id = $1`, defB); k != "book_custom_2" {
			t.Errorf("custom key in B = %q, want book_custom_2 (book_custom is taken)", k)
		}
		if k := str(`SELECT key FROM type_definitions WHERE id = $1`, defB2); k != "book_custom" {
			t.Errorf("the existing book_custom in B was touched: %q", k)
		}
		if n := str(`SELECT name FROM type_definitions WHERE id = $1`, defA); n != "Book" {
			t.Errorf("custom name = %q, want it kept (names are not unique)", n)
		}
		if typ := str(`SELECT type FROM lists WHERE id = $1`, listA); typ != "book_custom" {
			t.Errorf("list type = %q, want book_custom", typ)
		}
		if n := count(`SELECT count(*) FROM type_fields WHERE type_id = $1 AND key = 'author' AND NOT is_locked`, defA); n != 1 {
			t.Error("the custom field changed")
		}
		if md := str(`SELECT metadata::text FROM items WHERE id = $1`, itemA); md != `{"author": "Herbert"}` {
			t.Errorf("item metadata = %s", md)
		}
	})

	t.Run("a new workspace is seeded with the book type", func(t *testing.T) {
		const ws = "00000000-0000-0000-0000-000000002104"
		exec(`INSERT INTO workspaces (id, name) VALUES ($1, 'New')`, ws)
		checkBuiltin(t, ws)
	})

	t.Run("the seed is idempotent", func(t *testing.T) {
		before := count(`SELECT count(*) FROM type_fields`)
		exec(`SELECT seed_builtin_type_definitions(id) FROM workspaces`)
		if after := count(`SELECT count(*) FROM type_fields`); after != before {
			t.Errorf("fields %d -> %d after re-seeding", before, after)
		}
		if n := count(`SELECT count(*) FROM type_definitions WHERE workspace_id = $1 AND key = 'book'`, wsA); n != 1 {
			t.Errorf("book definitions in A = %d", n)
		}
	})

	t.Run("down removes the built-in, unsets its lists, keeps customs renamed", func(t *testing.T) {
		const builtinList = "00000000-0000-0000-0000-000000002122"
		exec(`INSERT INTO lists (id, workspace_id, name, type, sort_order) VALUES ($1, $2, 'Real books', 'book', 1)`, builtinList, wsC)
		if err := m.Steps(-1); err != nil {
			t.Fatalf("migrate down 000021: %v", err)
		}
		if n := count(`SELECT count(*) FROM type_definitions WHERE key = 'book'`); n != 0 {
			t.Errorf("%d book definitions left", n)
		}
		if n := count(`SELECT count(*) FROM type_fields WHERE key IN ('openlibrary_id', 'isbn')`); n != 0 {
			t.Errorf("%d built-in book fields left", n)
		}
		if n := count(`SELECT count(*) FROM lists WHERE id = $1 AND type IS NULL`, builtinList); n != 1 {
			t.Error("a list of the built-in book type kept its type")
		}
		if typ := str(`SELECT type FROM lists WHERE id = $1`, listA); typ != "book_custom" {
			t.Errorf("custom list type = %q after down, want book_custom (not renamed back)", typ)
		}
		if n := count(`SELECT count(*) FROM type_definitions WHERE id = $1 AND key = 'book_custom'`, defA); n != 1 {
			t.Error("the renamed custom type was changed by the down")
		}
	})

	t.Run("up again seeds the built-in once more", func(t *testing.T) {
		if err := m.Steps(1); err != nil {
			t.Fatalf("migrate up 000021 again: %v", err)
		}
		for _, ws := range []string{wsA, wsB, wsC} {
			checkBuiltin(t, ws)
		}
		if k := str(`SELECT key FROM type_definitions WHERE id = $1`, defA); k != "book_custom" {
			t.Errorf("custom key = %q after re-up", k)
		}
	})
}
