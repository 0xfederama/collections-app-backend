package database_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 000017 renames the concept "kind" to "type"; data, constraint names and the
// workspace seed trigger must survive, in both directions.
func TestMigration000017_KindToTypeKeepsData(t *testing.T) {
	ctx := t.Context()
	dsn := newDatabase(t)
	m := newMigrate(t, dsn)
	if err := m.Migrate(16); err != nil {
		t.Fatalf("migrate to 16: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	const ws = "00000000-0000-0000-0000-000000001701"
	const list = "00000000-0000-0000-0000-000000001702"
	const item = "00000000-0000-0000-0000-000000001703"
	const defID = "00000000-0000-0000-0000-000000001704"
	for _, stmt := range []string{
		`INSERT INTO workspaces (id, name) VALUES ('` + ws + `', 'Rename')`,
		`INSERT INTO kind_definitions (id, workspace_id, key, name) VALUES ('` + defID + `', '` + ws + `', 'book', 'Book')`,
		`INSERT INTO kind_fields (workspace_id, kind_id, key, label, field_type, position) VALUES ('` + ws + `', '` + defID + `', 'author', 'Author', 'text', 0)`,
		`INSERT INTO lists (id, workspace_id, name, kind, sort_order) VALUES ('` + list + `', '` + ws + `', 'Films', 'movie', 0)`,
		`INSERT INTO items (id, workspace_id, list_id, title, metadata, sort_order) VALUES ('` + item + `', '` + ws + `', '` + list + `', 'Matrix', '{"release_year":1999}', 0)`,
		`INSERT INTO tmdb_imports (workspace_id, item_id, tmdb_id, kind, imported_fields, original_title, fetched_at, refresh_after, expires_at)
		   VALUES ('` + ws + `', '` + item + `', 603, 'movie', '{}', 'The Matrix', now(), now() + interval '5 months', now() + interval '6 months')`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("seed: %v\n%s", err, stmt)
		}
	}

	check := func(t *testing.T, defs, fields, fkCol, col string) {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+defs+` WHERE workspace_id = $1 AND key = 'book' AND name = 'Book'`, ws).Scan(&n); err != nil || n != 1 {
			t.Fatalf("custom definition after rename: n=%d err=%v", n, err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+fields+` WHERE `+fkCol+` = $1 AND key = 'author' AND field_type = 'text'`, defID).Scan(&n); err != nil || n != 1 {
			t.Fatalf("custom field after rename: n=%d err=%v", n, err)
		}
		var got string
		if err := pool.QueryRow(ctx, `SELECT `+col+` FROM lists WHERE id = $1`, list).Scan(&got); err != nil || got != "movie" {
			t.Fatalf("list %s = %q err=%v", col, got, err)
		}
		if err := pool.QueryRow(ctx, `SELECT `+col+` FROM tmdb_imports WHERE item_id = $1`, item).Scan(&got); err != nil || got != "movie" {
			t.Fatalf("tmdb_imports %s = %q err=%v", col, got, err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+defs+` WHERE workspace_id = $1 AND is_locked`, ws).Scan(&n); err != nil || n != 6 {
			t.Fatalf("built-in definitions = %d err=%v", n, err)
		}
		// The seed trigger still fires for a new workspace.
		id := "00000000-0000-0000-0000-000000001710"
		if _, err := pool.Exec(ctx, `INSERT INTO workspaces (id, name) VALUES ($1, 'New')`, id); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+defs+` WHERE workspace_id = $1 AND is_locked AND default_pinned_fields IS NOT NULL`, id).Scan(&n); err != nil || n != 6 {
			t.Fatalf("seeded definitions with pins = %d err=%v", n, err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM workspaces WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}

	if err := m.Steps(1); err != nil {
		t.Fatalf("migrate up 000017: %v", err)
	}
	check(t, "type_definitions", "type_fields", "type_id", "type")
	var name string
	if err := pool.QueryRow(ctx, `SELECT conname FROM pg_constraint WHERE conname = 'lists_type_check'`).Scan(&name); err != nil {
		t.Fatalf("lists_type_check missing: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT conname FROM pg_constraint WHERE conname = 'type_fields_type_workspace_fkey'`).Scan(&name); err != nil {
		t.Fatalf("type_fields_type_workspace_fkey missing: %v", err)
	}
	// Postgres 18 auto-names NOT NULL constraints after the column and keeps the
	// old name on rename; nothing maps them, so they are not counted.
	var leftovers int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM pg_constraint WHERE conname LIKE '%kind%' AND contype <> 'n') +
		(SELECT count(*) FROM pg_indexes WHERE schemaname = 'public' AND indexname LIKE '%kind%') +
		(SELECT count(*) FROM pg_proc WHERE proname LIKE '%kind%') +
		(SELECT count(*) FROM pg_trigger WHERE tgname LIKE '%kind%') +
		(SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public' AND column_name LIKE '%kind%')`).Scan(&leftovers); err != nil {
		t.Fatal(err)
	}
	if leftovers != 0 {
		t.Errorf("%d schema objects still carry \"kind\" in their name", leftovers)
	}

	if err := m.Steps(-1); err != nil {
		t.Fatalf("migrate down 000017: %v", err)
	}
	check(t, "kind_definitions", "kind_fields", "kind_id", "kind")
	if err := m.Steps(1); err != nil {
		t.Fatalf("migrate up 000017 again: %v", err)
	}
	check(t, "type_definitions", "type_fields", "type_id", "type")
}
