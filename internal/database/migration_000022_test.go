package database_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 000022 (F-016, D-096) generalises tmdb_imports to lookup_imports: provider + text
// source_id (backfilled from tmdb_id), nullable retention deadlines for providers
// without a retention duty (D-095). Down keeps the tmdb rows and drops the others.
func TestMigration000022_LookupImports(t *testing.T) {
	ctx := t.Context()
	dsn := newDatabase(t)
	m := newMigrate(t, dsn)
	if err := m.Migrate(21); err != nil {
		t.Fatalf("migrate to 21: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	const (
		ws      = "00000000-0000-0000-0000-000000002201"
		movies  = "00000000-0000-0000-0000-000000002211"
		books   = "00000000-0000-0000-0000-000000002212"
		matrix  = "00000000-0000-0000-0000-000000002221"
		matrix2 = "00000000-0000-0000-0000-000000002222"
		dune    = "00000000-0000-0000-0000-000000002223"
		spare   = "00000000-0000-0000-0000-000000002224"
		duneAtt = "00000000-0000-0000-0000-000000002231"
	)
	exec := func(sql string, args ...any) error {
		t.Helper()
		_, err := pool.Exec(ctx, sql, args...)
		return err
	}
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if err := exec(sql, args...); err != nil {
			t.Fatalf("%.80s: %v", sql, err)
		}
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("%.80s: %v", sql, err)
		}
		return n
	}
	mustExec(`INSERT INTO workspaces (id, name) VALUES ($1, 'W')`, ws)
	mustExec(`INSERT INTO lists (id, workspace_id, name, type, sort_order) VALUES ($1, $2, 'Movies', 'movie', 0)`, movies, ws)
	mustExec(`INSERT INTO lists (id, workspace_id, name, type, sort_order) VALUES ($1, $2, 'Books', 'book', 1)`, books, ws)
	for i, it := range []struct{ id, list, title string }{{matrix, movies, "Matrix"}, {matrix2, movies, "Matrix 2"}, {dune, books, "Dune"}, {spare, movies, "Spare"}} {
		mustExec(`INSERT INTO items (id, workspace_id, list_id, title, sort_order) VALUES ($1, $2, $3, $4, $5)`, it.id, ws, it.list, it.title, i)
	}
	mustExec(`INSERT INTO attachments (id, workspace_id, storage_key, thumbnail_storage_key, media_type, size_bytes, width, height, sha256)
		VALUES ($1, $2, 'images/22/22/a.png', 'images/22/22/a.thumb.jpg', 'image/png', 10, 1, 1, '\x00')`, duneAtt, ws)
	// Two tmdb rows, one with a poster (all five poster columns).
	mustExec(`INSERT INTO tmdb_imports (workspace_id, item_id, tmdb_id, type, imported_fields, original_title, fetched_at, refresh_after, expires_at)
		VALUES ($1, $2, 603, 'movie', '{"tmdb_id":603}', 'matrix', now(), now() + interval '5 months', now() + interval '6 months')`, ws, matrix)
	mustExec(`INSERT INTO tmdb_imports (workspace_id, item_id, tmdb_id, type, imported_fields, original_title, fetched_at, refresh_after, expires_at,
		attachment_id, poster_path, poster_fetched_at, poster_refresh_after, poster_expires_at)
		VALUES ($1, $2, 604, 'movie', '{}', 'matrix 2', now(), now() + interval '5 months', now() + interval '6 months',
		$3, '/p.jpg', now(), now() + interval '5 months', now() + interval '6 months')`, ws, matrix2, duneAtt)

	if err := m.Steps(1); err != nil {
		t.Fatalf("migrate up 000022: %v", err)
	}

	t.Run("tmdb rows are kept with provider and source_id", func(t *testing.T) {
		if n := count(`SELECT count(*) FROM lookup_imports WHERE provider = 'tmdb' AND source_id IN ('603', '604')`); n != 2 {
			t.Fatalf("migrated tmdb rows = %d, want 2", n)
		}
		if n := count(`SELECT count(*) FROM information_schema.tables WHERE table_name = 'tmdb_imports'`); n != 0 {
			t.Errorf("tmdb_imports still exists")
		}
		if n := count(`SELECT count(*) FROM information_schema.columns WHERE table_name = 'lookup_imports' AND column_name = 'tmdb_id'`); n != 0 {
			t.Errorf("tmdb_id column still exists")
		}
	})

	t.Run("an Open Library row has no retention deadlines", func(t *testing.T) {
		mustExec(`INSERT INTO lookup_imports (workspace_id, item_id, provider, source_id, type, original_title, fetched_at,
			attachment_id, poster_path, poster_fetched_at)
			VALUES ($1, $2, 'openlibrary', 'OL45804W', 'book', 'dune', now(), $3, 'OL1-L', now())`, ws, dune, duneAtt)
		if n := count(`SELECT count(*) FROM lookup_imports WHERE provider = 'openlibrary' AND refresh_after IS NULL AND expires_at IS NULL
			AND poster_refresh_after IS NULL AND poster_expires_at IS NULL`); n != 1 {
			t.Fatalf("open library row count = %d", n)
		}
	})

	t.Run("constraints reject the wrong shapes", func(t *testing.T) {
		const insert = `INSERT INTO lookup_imports (workspace_id, item_id, provider, source_id, type, original_title, fetched_at, refresh_after, expires_at)
			VALUES ($1, $2, $3, $4, $5, 'x', now(), $6, $7)`
		later, muchLater := time.Now().AddDate(0, 5, 0), time.Now().AddDate(0, 6, 0)
		for _, tc := range []struct {
			name              string
			provider, source  string
			typ               string
			refreshAfter, exp any
		}{
			{"unknown provider", "google", "1", "book", nil, nil},
			{"tmdb zero id", "tmdb", "0", "movie", later, muchLater},
			{"tmdb non numeric", "tmdb", "OL1W", "movie", later, muchLater},
			{"tmdb without retention", "tmdb", "5", "movie", nil, nil},
			{"openlibrary bad id", "openlibrary", "603", "book", nil, nil},
			{"openlibrary with retention", "openlibrary", "OL1W", "book", later, muchLater},
			{"openlibrary on a movie", "openlibrary", "OL1W", "movie", nil, nil},
			{"tmdb on a book", "tmdb", "5", "book", later, muchLater},
		} {
			if err := exec(insert, ws, spare, tc.provider, tc.source, tc.typ, tc.refreshAfter, tc.exp); err == nil {
				mustExec(`DELETE FROM lookup_imports WHERE item_id = $1`, spare)
				t.Errorf("%s: insert was accepted", tc.name)
			}
		}
	})

	t.Run("down keeps tmdb rows and drops the others, then up again", func(t *testing.T) {
		if err := m.Steps(-1); err != nil {
			t.Fatalf("migrate down 000022: %v", err)
		}
		if n := count(`SELECT count(*) FROM tmdb_imports`); n != 2 {
			t.Errorf("tmdb_imports rows after down = %d, want 2 (the book row is dropped)", n)
		}
		if n := count(`SELECT count(*) FROM tmdb_imports WHERE tmdb_id IN (603, 604)`); n != 2 {
			t.Errorf("tmdb ids after down are wrong")
		}
		if n := count(`SELECT count(*) FROM items WHERE id = $1`, dune); n != 1 {
			t.Errorf("the book item must survive the down migration")
		}
		if err := m.Steps(1); err != nil {
			t.Fatalf("migrate up 000022 again: %v", err)
		}
		if n := count(`SELECT count(*) FROM lookup_imports WHERE provider = 'tmdb' AND source_id IN ('603', '604')`); n != 2 {
			t.Errorf("tmdb rows after second up = %d, want 2", n)
		}
	})
}
