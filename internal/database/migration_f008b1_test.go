package database_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Fixture ids for 000006 (F008-B1 generic has no price), on top of the 000005 schema.
var (
	f008Workspace = f007ID(201)

	f008ListGen    = f007ID(210)
	f008ListGenPin = f007ID(211) // generic, pins price, sorted by price
	f008ListGenOK  = f007ID(212) // generic, pins other keys only
	f008ListMovie  = f007ID(213)

	f008ItemSingle  = f007ID(220) // min == max
	f008ItemRange   = f007ID(221)
	f008ItemMax     = f007ID(222) // only a maximum, with a body
	f008ItemMin     = f007ID(223) // only a minimum
	f008ItemTrashed = f007ID(224)
	f008ItemFrom12  = f007ID(225) // body already starts with the block
	f008ItemNoPrice = f007ID(226)
	f008ItemMovie   = f007ID(227)
)

func TestF008B1_MigrationGenericNoPrice(t *testing.T) {
	ctx := t.Context()
	dsn := newDatabase(t)
	m := newMigrate(t, dsn)
	if err := m.Migrate(5); err != nil {
		t.Fatalf("migrate to 5: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%.60s: %v", sql, err)
		}
	}
	exec(`INSERT INTO workspaces (id, name) VALUES ($1, 'W')`, f008Workspace)
	for i, l := range []struct {
		id, name, kind, sort, pinned string
	}{
		{f008ListGen, "Gen", "generic", "manual", ""},
		{f008ListGenPin, "GenPin", "generic", "price", `{"under":["price"],"right":["price"]}`},
		{f008ListGenOK, "GenOK", "generic", "title", `{"under":[],"right":[]}`},
		{f008ListMovie, "Movies", "movie", "price", `{"under":["release_year"],"right":["price"]}`},
	} {
		var pinned any
		if l.pinned != "" {
			pinned = l.pinned
		}
		exec(`INSERT INTO lists (id, workspace_id, name, kind, sort_mode, pinned_fields, sort_order) VALUES ($1, $2, $3, $4, $5, $6::text::jsonb, $7)`,
			l.id, f008Workspace, l.name, l.kind, l.sort, pinned, i)
	}
	day := time.Now().Add(-24 * time.Hour)
	exec(`INSERT INTO trash_batches (id, workspace_id, root_type, root_id, root_name, former_path, deleted_at)
	      VALUES ($1, $2, 'item', $3, 'T', '[]', $4)`, f007ID(230), f008Workspace, f008ItemTrashed, day)
	for i, it := range []struct {
		id, list string
		min, max any
		cur      any
		deleted  any
		batch    any
	}{
		{f008ItemSingle, f008ListGen, "1200.00", "1200.00", "EUR", nil, nil},
		{f008ItemRange, f008ListGen, "1000.00", "1400.00", "EUR", nil, nil},
		{f008ItemMax, f008ListGen, nil, "50.50", "USD", nil, nil},
		{f008ItemMin, f008ListGen, "9.99", nil, "GBP", nil, nil},
		{f008ItemTrashed, f008ListGen, "5.00", "5.00", "EUR", day, f007ID(230)},
		{f008ItemFrom12, f008ListGen, "7.00", "7.00", "EUR", nil, nil},
		{f008ItemNoPrice, f008ListGen, nil, nil, nil, nil, nil},
		{f008ItemMovie, f008ListMovie, "20.00", "20.00", "EUR", nil, nil},
	} {
		exec(`INSERT INTO items (id, workspace_id, list_id, title, price_min, price_max, currency, sort_order, deleted_at, trash_batch_id)
		      VALUES ($1, $2, $3, 'T', $4, $5, $6, $7, $8, $9)`, it.id, f008Workspace, it.list, it.min, it.max, it.cur, i, it.deleted, it.batch)
	}
	f002Content(t, pool, f008ItemMax, 1, "my notes")
	f002Content(t, pool, f008ItemFrom12, 1, "## From 1.2\n\n- **Price:** EUR 7.00\n\n---\n\nold")

	if err := m.Steps(1); err != nil {
		t.Fatalf("migrate up 000006: %v", err)
	}

	t.Run("the price moves into a From 1.2 block", func(t *testing.T) {
		for _, c := range []struct {
			item string
			v    int
			body string
		}{
			{f008ItemSingle, 1, "## From 1.2\n\n- **Price:** EUR 1200.00\n\n---\n"},
			{f008ItemRange, 1, "## From 1.2\n\n- **Price:** EUR 1000.00–1400.00\n\n---\n"},
			{f008ItemMax, 2, "## From 1.2\n\n- **Price:** USD up to 50.50\n\n---\n\nmy notes"},
			{f008ItemMin, 1, "## From 1.2\n\n- **Price:** GBP 9.99\n\n---\n"},
			{f008ItemTrashed, 1, "## From 1.2\n\n- **Price:** EUR 5.00\n\n---\n"},
		} {
			v, body := f007Latest(t, pool, c.item)
			if v != c.v || body != c.body {
				t.Errorf("item %s latest content = v%d %q, want v%d %q", c.item, v, body, c.v, c.body)
			}
		}
	})

	t.Run("a body that already starts with the block gets no second one", func(t *testing.T) {
		if n := f007Int(t, pool, `SELECT count(*) FROM item_content_versions WHERE item_id = $1`, f008ItemFrom12); n != 1 {
			t.Errorf("versions = %d, want 1", n)
		}
	})

	t.Run("price columns are cleared and touched items get a new version", func(t *testing.T) {
		for _, id := range []string{f008ItemSingle, f008ItemRange, f008ItemMax, f008ItemMin, f008ItemTrashed, f008ItemFrom12} {
			if n := f007Int(t, pool, `SELECT count(*) FROM items WHERE id = $1 AND price_min IS NULL AND price_max IS NULL AND currency IS NULL AND version = 2`, id); n != 1 {
				t.Errorf("item %s: price not cleared or version not bumped", id)
			}
		}
	})

	t.Run("items without a price and other kinds are left alone", func(t *testing.T) {
		if n := f007Int(t, pool, `SELECT count(*) FROM items WHERE id = $1 AND price_min IS NULL AND version = 1`, f008ItemNoPrice); n != 1 {
			t.Error("item without price changed")
		}
		if n := f007Int(t, pool, `SELECT count(*) FROM items WHERE id = $1 AND price_min = 20 AND price_max = 20 AND currency = 'EUR' AND version = 1`, f008ItemMovie); n != 1 {
			t.Error("movie item changed")
		}
		if n := f007Int(t, pool, `SELECT count(*) FROM item_content_versions WHERE item_id IN ($1, $2)`, f008ItemNoPrice, f008ItemMovie); n != 0 {
			t.Errorf("untouched items got %d content versions", n)
		}
	})

	t.Run("generic lists lose the pinned price and the price sort", func(t *testing.T) {
		if got := f007Str(t, pool, `SELECT pinned_fields::text FROM lists WHERE id = $1`, f008ListGenPin); got != `{"right": [], "under": []}` {
			t.Errorf("pinned_fields = %s", got)
		}
		if got := f007Str(t, pool, `SELECT sort_mode FROM lists WHERE id = $1`, f008ListGenPin); got != "manual" {
			t.Errorf("sort_mode = %s, want manual", got)
		}
		if n := f007Int(t, pool, `SELECT count(*) FROM lists WHERE id = $1 AND version = 1 AND pinned_fields IS NULL AND sort_mode = 'manual'`, f008ListGen); n != 1 {
			t.Error("generic list without pins changed")
		}
		if n := f007Int(t, pool, `SELECT count(*) FROM lists WHERE id = $1 AND version = 1 AND pinned_fields = '{"under":[],"right":[]}'::jsonb AND sort_mode = 'title'`, f008ListGenOK); n != 1 {
			t.Error("generic list with other settings changed")
		}
	})

	t.Run("a movie list keeps its pinned price and sort", func(t *testing.T) {
		if n := f007Int(t, pool, `SELECT count(*) FROM lists WHERE id = $1 AND sort_mode = 'price' AND version = 1 AND pinned_fields = '{"under":["release_year"],"right":["price"]}'::jsonb`, f008ListMovie); n != 1 {
			t.Error("movie list changed")
		}
	})

	t.Run("down then up again", func(t *testing.T) {
		versions := f007Int(t, pool, `SELECT count(*) FROM item_content_versions`)
		if err := m.Steps(-1); err != nil {
			t.Fatalf("migrate down 000006: %v", err)
		}
		if err := m.Steps(1); err != nil {
			t.Fatalf("migrate up 000006 again: %v", err)
		}
		if got := f007Int(t, pool, `SELECT count(*) FROM item_content_versions`); got != versions {
			t.Errorf("content versions after re-up = %d, want %d", got, versions)
		}
	})
}
