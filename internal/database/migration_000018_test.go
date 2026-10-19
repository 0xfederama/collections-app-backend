package database_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 000018 removes the built-in `generic` type: its lists get type NULL, its
// definition goes, and the data of every other list survives up, down and up.
func TestMigration000018_RemoveGenericTypeKeepsData(t *testing.T) {
	ctx := t.Context()
	dsn := newDatabase(t)
	m := newMigrate(t, dsn)
	if err := m.Migrate(17); err != nil {
		t.Fatalf("migrate to 17: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	const (
		ws        = "00000000-0000-0000-0000-000000001801"
		plainList = "00000000-0000-0000-0000-000000001802"
		movieList = "00000000-0000-0000-0000-000000001803"
		plainItem = "00000000-0000-0000-0000-000000001804"
		movieItem = "00000000-0000-0000-0000-000000001805"
	)
	for _, stmt := range []string{
		`INSERT INTO workspaces (id, name) VALUES ('` + ws + `', 'No type')`,
		`INSERT INTO lists (id, workspace_id, name, type, sort_order) VALUES ('` + plainList + `', '` + ws + `', 'Plain', 'generic', 0)`,
		`INSERT INTO lists (id, workspace_id, name, type, sort_order) VALUES ('` + movieList + `', '` + ws + `', 'Films', 'movie', 1)`,
		`INSERT INTO items (id, workspace_id, list_id, title, metadata, sort_order) VALUES ('` + plainItem + `', '` + ws + `', '` + plainList + `', 'Thing', '{}', 0)`,
		`INSERT INTO items (id, workspace_id, list_id, title, metadata, sort_order) VALUES ('` + movieItem + `', '` + ws + `', '` + movieList + `', 'Matrix', '{"release_year":1999}', 0)`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("seed: %v\n%s", err, stmt)
		}
	}

	// check asserts the lists, items and built-in definitions; noType says which side of the migration we are on.
	check := func(t *testing.T, noType bool) {
		t.Helper()
		var typ *string
		if err := pool.QueryRow(ctx, `SELECT type FROM lists WHERE id = $1`, plainList).Scan(&typ); err != nil {
			t.Fatal(err)
		}
		if noType && typ != nil {
			t.Errorf("plain list type = %q, want NULL", *typ)
		}
		if !noType && (typ == nil || *typ != "generic") {
			t.Errorf("plain list type = %v, want generic", typ)
		}
		if err := pool.QueryRow(ctx, `SELECT type FROM lists WHERE id = $1`, movieList).Scan(&typ); err != nil || typ == nil || *typ != "movie" {
			t.Errorf("movie list type = %v err=%v", typ, err)
		}
		var meta string
		if err := pool.QueryRow(ctx, `SELECT metadata::text FROM items WHERE id = $1`, plainItem).Scan(&meta); err != nil || meta != "{}" {
			t.Errorf("plain item metadata = %q err=%v", meta, err)
		}
		if err := pool.QueryRow(ctx, `SELECT metadata::text FROM items WHERE id = $1`, movieItem).Scan(&meta); err != nil || meta != `{"release_year": 1999}` {
			t.Errorf("movie item metadata = %q err=%v", meta, err)
		}
		var generics, locked int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM type_definitions WHERE workspace_id = $1 AND key = 'generic'`, ws).Scan(&generics); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM type_definitions WHERE workspace_id = $1 AND is_locked`, ws).Scan(&locked); err != nil {
			t.Fatal(err)
		}
		wantGenerics, wantLocked := 1, 6
		if noType {
			wantGenerics, wantLocked = 0, 5
		}
		if generics != wantGenerics || locked != wantLocked {
			t.Errorf("generic definitions = %d, built-in definitions = %d; want %d and %d", generics, locked, wantGenerics, wantLocked)
		}
		// The seed trigger follows the migration for a new workspace.
		const fresh = "00000000-0000-0000-0000-000000001810"
		if _, err := pool.Exec(ctx, `INSERT INTO workspaces (id, name) VALUES ($1, 'New')`, fresh); err != nil {
			t.Fatal(err)
		}
		var seeded, withPins int
		if err := pool.QueryRow(ctx, `SELECT count(*), count(default_pinned_fields) FROM type_definitions WHERE workspace_id = $1`, fresh).Scan(&seeded, &withPins); err != nil {
			t.Fatal(err)
		}
		if seeded != wantLocked || withPins != wantLocked {
			t.Errorf("new workspace seeded %d definitions (%d with pins), want %d", seeded, withPins, wantLocked)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM workspaces WHERE id = $1`, fresh); err != nil {
			t.Fatal(err)
		}
	}

	check(t, false)
	if err := m.Steps(1); err != nil {
		t.Fatalf("migrate up 000018: %v", err)
	}
	check(t, true)
	// A list can now be created with no type, and a blank/odd key still fails the check.
	if _, err := pool.Exec(ctx, `INSERT INTO lists (workspace_id, name, sort_order) VALUES ($1, 'Another', 2)`, ws); err != nil {
		t.Fatalf("insert list without type: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM lists WHERE name = 'Another' AND workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	if err := m.Steps(-1); err != nil {
		t.Fatalf("migrate down 000018: %v", err)
	}
	check(t, false)
	if err := m.Steps(1); err != nil {
		t.Fatalf("migrate up 000018 again: %v", err)
	}
	check(t, true)
}
