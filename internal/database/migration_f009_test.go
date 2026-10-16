package database_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Fixture ids for 000007 (F009-B1 price kinds), on top of the 000006 schema.
var (
	f009Workspace = f007ID(301)

	f009ListMovie     = f007ID(310) // movie, sort price, pins release_year + price
	f009ListTV        = f007ID(311) // tv_show, sort price, pins first_air_year + price
	f009ListMovieOK   = f007ID(312) // movie, sort title, no price pin: untouched
	f009ListMovieSort = f007ID(313) // movie, sort price, no pins: only the sort changes
	f009ListCar       = f007ID(314) // car (has a price), sort price, pins make + price: untouched

	f009ItemSingle  = f007ID(320) // min == max
	f009ItemRange   = f007ID(321)
	f009ItemMax     = f007ID(322) // only a maximum, with a body
	f009ItemMin     = f007ID(323) // only a minimum
	f009ItemTrashed = f007ID(324)
	f009ItemFrom13  = f007ID(325) // latest body already starts with the block
	f009ItemNoPrice = f007ID(326)
	f009ItemTV      = f007ID(327)
	f009ItemCar     = f007ID(328) // car item keeps its price
)

func TestF009B1_MigrationPriceKinds(t *testing.T) {
	ctx := t.Context()
	dsn := newDatabase(t)
	m := newMigrate(t, dsn)
	if err := m.Migrate(6); err != nil {
		t.Fatalf("migrate to 6: %v", err)
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
	exec(`INSERT INTO workspaces (id, name) VALUES ($1, 'W')`, f009Workspace)
	for i, l := range []struct {
		id, name, kind, sort, pinned string
	}{
		{f009ListMovie, "Movies", "movie", "price", `{"under":["release_year"],"right":["price"]}`},
		{f009ListTV, "Shows", "tv_show", "price", `{"under":["first_air_year"],"right":["price"]}`},
		{f009ListMovieOK, "Other films", "movie", "title", `{"under":[],"right":[]}`},
		{f009ListMovieSort, "Sort only", "movie", "price", ""},
		{f009ListCar, "Cars", "car", "price", `{"under":["make"],"right":["price"]}`},
	} {
		var pinned any
		if l.pinned != "" {
			pinned = l.pinned
		}
		exec(`INSERT INTO lists (id, workspace_id, name, kind, sort_mode, pinned_fields, sort_order) VALUES ($1, $2, $3, $4, $5, $6::text::jsonb, $7)`,
			l.id, f009Workspace, l.name, l.kind, l.sort, pinned, i)
	}
	day := time.Now().Add(-24 * time.Hour)
	exec(`INSERT INTO trash_batches (id, workspace_id, root_type, root_id, root_name, former_path, deleted_at)
	      VALUES ($1, $2, 'item', $3, 'T', '[]', $4)`, f007ID(330), f009Workspace, f009ItemTrashed, day)
	for i, it := range []struct {
		id, list string
		min, max any
		cur      any
		deleted  any
		batch    any
	}{
		{f009ItemSingle, f009ListMovie, "1200.00", "1200.00", "EUR", nil, nil},
		{f009ItemRange, f009ListMovie, "1000.00", "1400.00", "EUR", nil, nil},
		{f009ItemMax, f009ListMovie, nil, "50.50", "USD", nil, nil},
		{f009ItemMin, f009ListMovie, "9.99", nil, "GBP", nil, nil},
		{f009ItemTrashed, f009ListMovie, "5.00", "5.00", "EUR", day, f007ID(330)},
		{f009ItemFrom13, f009ListMovie, "7.00", "7.00", "EUR", nil, nil},
		{f009ItemNoPrice, f009ListMovie, nil, nil, nil, nil, nil},
		{f009ItemTV, f009ListTV, "30.00", "30.00", "USD", nil, nil},
		{f009ItemCar, f009ListCar, "20000.00", "20000.00", "EUR", nil, nil},
	} {
		exec(`INSERT INTO items (id, workspace_id, list_id, title, price_min, price_max, currency, sort_order, deleted_at, trash_batch_id)
		      VALUES ($1, $2, $3, 'T', $4, $5, $6, $7, $8, $9)`, it.id, f009Workspace, it.list, it.min, it.max, it.cur, i, it.deleted, it.batch)
	}
	f002Content(t, pool, f009ItemMax, 1, "my notes")
	f002Content(t, pool, f009ItemFrom13, 1, "## From 1.3\n\n- **Price:** EUR 7.00\n\n---\n\nold")

	if err := m.Steps(1); err != nil {
		t.Fatalf("migrate up 000007: %v", err)
	}

	t.Run("the price moves into a From 1.3 block", func(t *testing.T) {
		for _, c := range []struct {
			item string
			v    int
			body string
		}{
			{f009ItemSingle, 1, "## From 1.3\n\n- **Price:** EUR 1200.00\n\n---\n"},
			{f009ItemRange, 1, "## From 1.3\n\n- **Price:** EUR 1000.00–1400.00\n\n---\n"},
			{f009ItemMax, 2, "## From 1.3\n\n- **Price:** USD up to 50.50\n\n---\n\nmy notes"},
			{f009ItemMin, 1, "## From 1.3\n\n- **Price:** GBP 9.99\n\n---\n"},
			{f009ItemTrashed, 1, "## From 1.3\n\n- **Price:** EUR 5.00\n\n---\n"},
			{f009ItemTV, 1, "## From 1.3\n\n- **Price:** USD 30.00\n\n---\n"},
		} {
			v, body := f007Latest(t, pool, c.item)
			if v != c.v || body != c.body {
				t.Errorf("item %s latest content = v%d %q, want v%d %q", c.item, v, body, c.v, c.body)
			}
		}
	})

	t.Run("a body that already starts with the block gets no second one", func(t *testing.T) {
		if n := f007Int(t, pool, `SELECT count(*) FROM item_content_versions WHERE item_id = $1`, f009ItemFrom13); n != 1 {
			t.Errorf("versions = %d, want 1", n)
		}
	})

	t.Run("price columns are cleared and touched items get a new version", func(t *testing.T) {
		for _, id := range []string{f009ItemSingle, f009ItemRange, f009ItemMax, f009ItemMin, f009ItemTrashed, f009ItemFrom13, f009ItemTV} {
			if n := f007Int(t, pool, `SELECT count(*) FROM items WHERE id = $1 AND price_min IS NULL AND price_max IS NULL AND currency IS NULL AND version = 2`, id); n != 1 {
				t.Errorf("item %s: price not cleared or version not bumped", id)
			}
		}
	})

	t.Run("items without a price and price kinds are left alone", func(t *testing.T) {
		if n := f007Int(t, pool, `SELECT count(*) FROM items WHERE id = $1 AND price_min IS NULL AND version = 1`, f009ItemNoPrice); n != 1 {
			t.Error("item without price changed")
		}
		if n := f007Int(t, pool, `SELECT count(*) FROM items WHERE id = $1 AND price_min = 20000 AND price_max = 20000 AND currency = 'EUR' AND version = 1`, f009ItemCar); n != 1 {
			t.Error("car item changed")
		}
		if n := f007Int(t, pool, `SELECT count(*) FROM item_content_versions WHERE item_id IN ($1, $2)`, f009ItemNoPrice, f009ItemCar); n != 0 {
			t.Errorf("untouched items got %d content versions", n)
		}
	})

	t.Run("price-less lists lose the pinned price and the price sort", func(t *testing.T) {
		for _, c := range []struct {
			id, pinned string
		}{
			{f009ListMovie, `{"under":["release_year"],"right":[]}`},
			{f009ListTV, `{"under":["first_air_year"],"right":[]}`},
		} {
			if n := f007Int(t, pool, `SELECT count(*) FROM lists WHERE id = $1 AND sort_mode = 'manual' AND pinned_fields = $2::jsonb AND version = 2`, c.id, c.pinned); n != 1 {
				t.Errorf("list %s: pinned/sort not updated once", c.id)
			}
		}
		if n := f007Int(t, pool, `SELECT count(*) FROM lists WHERE id = $1 AND sort_mode = 'manual' AND pinned_fields IS NULL AND version = 2`, f009ListMovieSort); n != 1 {
			t.Error("sort-only list not switched to manual")
		}
		if n := f007Int(t, pool, `SELECT count(*) FROM lists WHERE id = $1 AND version = 1 AND sort_mode = 'title' AND pinned_fields = '{"under":[],"right":[]}'::jsonb`, f009ListMovieOK); n != 1 {
			t.Error("list without a price pin or sort changed")
		}
	})

	t.Run("a price kind keeps its pinned price and sort", func(t *testing.T) {
		if n := f007Int(t, pool, `SELECT count(*) FROM lists WHERE id = $1 AND sort_mode = 'price' AND version = 1 AND pinned_fields = '{"under":["make"],"right":["price"]}'::jsonb`, f009ListCar); n != 1 {
			t.Error("car list changed")
		}
	})

	t.Run("down then up again is idempotent", func(t *testing.T) {
		versions := f007Int(t, pool, `SELECT count(*) FROM item_content_versions`)
		if err := m.Steps(-1); err != nil {
			t.Fatalf("migrate down 000007: %v", err)
		}
		if err := m.Steps(1); err != nil {
			t.Fatalf("migrate up 000007 again: %v", err)
		}
		if got := f007Int(t, pool, `SELECT count(*) FROM item_content_versions`); got != versions {
			t.Errorf("content versions after re-up = %d, want %d", got, versions)
		}
	})
}

// Fixture ids for 000008 (F009-B2 ordered pinned fields), on top of the 000007 schema.
var (
	f009bWorkspace = f007ID(401)

	f009bListOverflow = f007ID(410) // two under + one right: the third is dropped
	f009bListRightTop = f007ID(411) // no under, three right: the first two survive
	f009bListDup      = f007ID(412) // a key in both slots appears once
	f009bListOne      = f007ID(413)
	f009bListEmpty    = f007ID(414)
	f009bListNull     = f007ID(415) // no pins: stays NULL
)

func TestF009B2_MigrationPinnedOrdered(t *testing.T) {
	ctx := t.Context()
	dsn := newDatabase(t)
	m := newMigrate(t, dsn)
	if err := m.Migrate(7); err != nil {
		t.Fatalf("migrate to 7: %v", err)
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
	exec(`INSERT INTO workspaces (id, name) VALUES ($1, 'W')`, f009bWorkspace)
	for i, l := range []struct {
		id, pinned string
	}{
		{f009bListOverflow, `{"under":["release_year","streaming"],"right":["price"]}`},
		{f009bListRightTop, `{"under":[],"right":["model_year","power_hp","price"]}`},
		{f009bListDup, `{"under":["make","model"],"right":["make"]}`},
		{f009bListOne, `{"under":["megapixels"],"right":[]}`},
		{f009bListEmpty, `{"under":[],"right":[]}`},
		{f009bListNull, ""},
	} {
		var pinned any
		if l.pinned != "" {
			pinned = l.pinned
		}
		exec(`INSERT INTO lists (id, workspace_id, name, kind, pinned_fields, sort_order) VALUES ($1, $2, 'L', 'movie', $3::text::jsonb, $4)`,
			l.id, f009bWorkspace, pinned, i)
	}

	if err := m.Steps(1); err != nil {
		t.Fatalf("migrate up 000008: %v", err)
	}

	t.Run("the slots become an ordered, de-duplicated list of at most two keys", func(t *testing.T) {
		for _, c := range []struct {
			id, want string
		}{
			{f009bListOverflow, `["release_year", "streaming"]`},
			{f009bListRightTop, `["model_year", "power_hp"]`},
			{f009bListDup, `["make", "model"]`},
			{f009bListOne, `["megapixels"]`},
			{f009bListEmpty, `[]`},
		} {
			if got := f007Str(t, pool, `SELECT pinned_fields::text FROM lists WHERE id = $1`, c.id); got != c.want {
				t.Errorf("list %s: pinned_fields = %s, want %s", c.id, got, c.want)
			}
			if n := f007Int(t, pool, `SELECT count(*) FROM lists WHERE id = $1 AND version = 2`, c.id); n != 1 {
				t.Errorf("list %s: version not bumped once", c.id)
			}
		}
	})

	t.Run("a list with no pins is left NULL", func(t *testing.T) {
		if n := f007Int(t, pool, `SELECT count(*) FROM lists WHERE id = $1 AND pinned_fields IS NULL AND version = 1`, f009bListNull); n != 1 {
			t.Error("list without pins changed")
		}
	})

	t.Run("down puts every key back in under, then up again is stable", func(t *testing.T) {
		if err := m.Steps(-1); err != nil {
			t.Fatalf("migrate down 000008: %v", err)
		}
		if n := f007Int(t, pool, `SELECT count(*) FROM lists WHERE id = $1 AND pinned_fields = '{"under":["release_year","streaming"],"right":[]}'::jsonb`, f009bListOverflow); n != 1 {
			t.Error("down did not restore the under/right slots")
		}
		if err := m.Steps(1); err != nil {
			t.Fatalf("migrate up 000008 again: %v", err)
		}
		if got := f007Str(t, pool, `SELECT pinned_fields::text FROM lists WHERE id = $1`, f009bListOverflow); got != `["release_year", "streaming"]` {
			t.Errorf("re-up: pinned_fields = %s", got)
		}
	})
}
