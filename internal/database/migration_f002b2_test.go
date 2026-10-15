package database_test

import (
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Fixture ids for 000003 (F-002 kind fields), on top of the 1.1 schema (000002).
var (
	f002Workspace = f007ID(101)

	f002ListBikes = f007ID(110)
	f002ListCams  = f007ID(111)
	f002ListCars  = f007ID(112)
	f002ListShows = f007ID(113)

	f002BikeFull    = f007ID(120) // make + model, no content
	f002BikeBody    = f007ID(121) // make only, with a content body
	f002BikeNone    = f007ID(122) // no make/model: untouched
	f002CamFull     = f007ID(123) // make + model + camera key, no content
	f002CamTrashed  = f007ID(124) // trashed, model only
	f002CarKeeps    = f007ID(125) // car keeps make/model
	f002ShowUntouch = f007ID(126) // tv_show untouched
	f002BikeFrom11  = f007ID(127) // latest body already starts with the block
	f002BikeEmpty   = f007ID(128) // empty make, keys removed without a block
	f002BikeBig     = f007ID(129) // body over 1 MiB gets cut
)

func f002Content(t *testing.T, pool *pgxpool.Pool, item string, version int, body string) {
	t.Helper()
	sum := sha256.Sum256([]byte(body))
	if _, err := pool.Exec(t.Context(),
		`INSERT INTO item_content_versions (item_id, version, body, size_bytes, sha256) VALUES ($1, $2, $3, $4, $5)`,
		item, version, body, len(body), sum[:]); err != nil {
		t.Fatal(err)
	}
}

func TestF002B2_MigrationKindFields(t *testing.T) {
	ctx := t.Context()
	dsn := newDatabase(t)
	m := newMigrate(t, dsn)
	if err := m.Migrate(2); err != nil {
		t.Fatalf("migrate to 2: %v", err)
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
	exec(`INSERT INTO workspaces (id, name) VALUES ($1, 'W')`, f002Workspace)
	for i, l := range []struct{ id, name, kind string }{
		{f002ListBikes, "Bikes", "motorcycle"}, {f002ListCams, "Cameras", "camera"},
		{f002ListCars, "Cars", "car"}, {f002ListShows, "Shows", "tv_show"},
	} {
		exec(`INSERT INTO lists (id, workspace_id, name, kind, sort_order) VALUES ($1, $2, $3, $4, $5)`,
			l.id, f002Workspace, l.name, l.kind, i)
	}
	day := time.Now().Add(-24 * time.Hour)
	exec(`INSERT INTO trash_batches (id, workspace_id, root_type, root_id, root_name, former_path, deleted_at)
	      VALUES ($1, $2, 'item', $3, 'T', '[]', $4)`, f007ID(130), f002Workspace, f002CamTrashed, day)
	for i, it := range []struct {
		id, list, meta string
		deleted, batch any
	}{
		{f002BikeFull, f002ListBikes, `{"make":"Ducati","model":"Monster","model_year":2024}`, nil, nil},
		{f002BikeBody, f002ListBikes, `{"make":"Honda","displacement_cc":750}`, nil, nil},
		{f002BikeNone, f002ListBikes, `{"model_year":2000}`, nil, nil},
		{f002CamFull, f002ListCams, `{"make":"Sony","model":"A7","megapixels":24.3}`, nil, nil},
		{f002CamTrashed, f002ListCams, `{"model":"F3"}`, day, f007ID(130)},
		{f002CarKeeps, f002ListCars, `{"make":"VW","model":"Golf"}`, nil, nil},
		{f002ShowUntouch, f002ListShows, `{"seasons":3}`, nil, nil},
		{f002BikeFrom11, f002ListBikes, `{"make":"Yamaha"}`, nil, nil},
		{f002BikeEmpty, f002ListBikes, `{"make":"","model_year":1999}`, nil, nil},
		{f002BikeBig, f002ListBikes, `{"model":"Big"}`, nil, nil},
	} {
		exec(`INSERT INTO items (id, workspace_id, list_id, title, metadata, sort_order, deleted_at, trash_batch_id)
		      VALUES ($1, $2, $3, 'T', $4::text::jsonb, $5, $6, $7)`, it.id, f002Workspace, it.list, it.meta, i, it.deleted, it.batch)
	}
	f002Content(t, pool, f002BikeBody, 1, "my notes")
	f002Content(t, pool, f002BikeFrom11, 1, "## From 1.1\n\n- **Make:** Yamaha\n\n---\n\nold")
	// 1 MiB of 3-byte characters: the old body is cut at a character boundary to fit.
	bigBody := strings.Repeat("€", 1048576/3)
	f002Content(t, pool, f002BikeBig, 1, bigBody)

	if err := m.Steps(1); err != nil {
		t.Fatalf("migrate up 000003: %v", err)
	}

	t.Run("motorcycle and camera get the From 1.1 block", func(t *testing.T) {
		for _, c := range []struct {
			item string
			v    int
			body string
		}{
			{f002BikeFull, 1, "## From 1.1\n\n- **Make:** Ducati\n- **Model:** Monster\n\n---\n"},
			{f002CamFull, 1, "## From 1.1\n\n- **Make:** Sony\n- **Model:** A7\n\n---\n"},
			{f002CamTrashed, 1, "## From 1.1\n\n- **Model:** F3\n\n---\n"},
		} {
			v, body := f007Latest(t, pool, c.item)
			if v != c.v || body != c.body {
				t.Errorf("item %s latest content = v%d %q, want v%d %q", c.item, v, body, c.v, c.body)
			}
		}
	})

	t.Run("an existing body is kept after the block", func(t *testing.T) {
		v, body := f007Latest(t, pool, f002BikeBody)
		want := "## From 1.1\n\n- **Make:** Honda\n\n---\n\nmy notes"
		if v != 2 || body != want {
			t.Errorf("latest content = v%d %q, want v2 %q", v, body, want)
		}
		if n := f007Int(t, pool, `SELECT count(*) FROM item_content_versions WHERE item_id = $1`, f002BikeBody); n != 2 {
			t.Errorf("versions = %d, want 2 (old one kept)", n)
		}
	})

	t.Run("a body over 1 MiB is cut to fit and the block is kept", func(t *testing.T) {
		v, body := f007Latest(t, pool, f002BikeBig)
		head := "## From 1.1\n\n- **Model:** Big\n\n---\n\n"
		if v != 2 || !strings.HasPrefix(body, head) || len(body) > 1048576 || !strings.HasSuffix(body, "€") {
			t.Errorf("latest content = v%d, %d bytes, prefix ok = %v", v, len(body), strings.HasPrefix(body, head))
		}
	})

	t.Run("a body that already starts with the block gets no second one", func(t *testing.T) {
		if n := f007Int(t, pool, `SELECT count(*) FROM item_content_versions WHERE item_id = $1`, f002BikeFrom11); n != 1 {
			t.Errorf("versions = %d, want 1", n)
		}
	})

	t.Run("make and model leave the metadata, other keys stay", func(t *testing.T) {
		for _, c := range []struct {
			item, want string
			version    int
		}{
			{f002BikeFull, `{"model_year": 2024}`, 2},
			{f002BikeBody, `{"displacement_cc": 750}`, 2},
			{f002CamFull, `{"megapixels": 24.3}`, 2},
			{f002CamTrashed, `{}`, 2},
			{f002BikeEmpty, `{"model_year": 1999}`, 2},
			{f002BikeFrom11, `{}`, 2},
		} {
			if n := f007Int(t, pool, `SELECT count(*) FROM items WHERE id = $1 AND metadata = $2::jsonb AND version = $3`, c.item, c.want, c.version); n != 1 {
				t.Errorf("item %s = %s v%d, want %s v%d", c.item,
					f007Str(t, pool, `SELECT metadata::text FROM items WHERE id = $1`, c.item),
					f007Int(t, pool, `SELECT version FROM items WHERE id = $1`, c.item), c.want, c.version)
			}
		}
		if n := f007Int(t, pool, `SELECT count(*) FROM item_content_versions WHERE item_id = $1`, f002BikeEmpty); n != 0 {
			t.Errorf("empty make produced %d content versions, want 0", n)
		}
	})

	t.Run("other kinds and untouched items are left alone", func(t *testing.T) {
		for _, c := range []struct{ item, want string }{
			{f002CarKeeps, `{"make": "VW", "model": "Golf"}`},
			{f002ShowUntouch, `{"seasons": 3}`},
			{f002BikeNone, `{"model_year": 2000}`},
		} {
			if n := f007Int(t, pool, `SELECT count(*) FROM items WHERE id = $1 AND metadata = $2::jsonb AND version = 1`, c.item, c.want); n != 1 {
				t.Errorf("item %s changed: %s", c.item, f007Str(t, pool, `SELECT metadata::text FROM items WHERE id = $1`, c.item))
			}
			if n := f007Int(t, pool, `SELECT count(*) FROM item_content_versions WHERE item_id = $1`, c.item); n != 0 {
				t.Errorf("item %s has %d content versions, want none", c.item, n)
			}
		}
	})

	t.Run("down then up again", func(t *testing.T) {
		versions := f007Int(t, pool, `SELECT count(*) FROM item_content_versions`)
		if err := m.Steps(-1); err != nil {
			t.Fatalf("migrate down 000003: %v", err)
		}
		if err := m.Steps(1); err != nil {
			t.Fatalf("migrate up 000003 again: %v", err)
		}
		if got := f007Int(t, pool, `SELECT count(*) FROM item_content_versions`); got != versions {
			t.Errorf("content versions after re-up = %d, want %d", got, versions)
		}
	})
}
