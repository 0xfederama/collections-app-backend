package database_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// f007ID builds a fixed UUID for fixture row n.
func f007ID(n int) string { return fmt.Sprintf("00000000-0000-0000-0000-%012d", n) }

// Fixture ids (1.0 data, migrated by 000002).
var (
	f007Workspace = f007ID(1)
	f007Folder    = f007ID(2)

	f007ListFilms  = f007ID(10) // movies only, in the folder
	f007ListGarage = f007ID(11) // car + generic + motorcycle, in the folder
	f007ListRoot   = f007ID(12) // generic + movie + tv_show, at the root
	f007ListEmpty  = f007ID(13) // no items
	f007ListGone   = f007ID(14) // trashed, camera + movie, root of its own batch

	f007BatchGone = f007ID(20) // batch of the trashed list
	f007BatchItem = f007ID(21) // batch of one item trashed on its own, inside the trashed list

	f007Matrix   = f007ID(30) // summary + status, content v1
	f007Plain    = f007ID(31) // nothing to move, no content
	f007Coupe    = f007ID(32) // dropped keys, content v1+v2
	f007Bike     = f007ID(33) // summary, status, dropped keys, no content
	f007Gadget   = f007ID(34) // generic, nothing to move
	f007PlainCar = f007ID(35) // only a dropped metadata key, no content
	f007Show     = f007ID(36) // multi-line summary
	f007RootMov  = f007ID(37) // status only
	f007RootGen  = f007ID(38) // generic, nothing to move
	f007Camera   = f007ID(39) // trashed with the list, only a dropped key
	f007GoneMov  = f007ID(40) // trashed with the list
	f007Alone    = f007ID(41) // trashed on its own before the list, kind movie
)

func f007Seed(ctx context.Context, pool *pgxpool.Pool) error {
	day := time.Now().Add(-24 * time.Hour)
	twoDays := time.Now().Add(-48 * time.Hour)
	exec := func(sql string, args ...any) error {
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("%.60s: %w", sql, err)
		}
		return nil
	}
	if err := exec(`INSERT INTO workspaces (id, name) VALUES ($1, 'W')`, f007Workspace); err != nil {
		return err
	}
	if err := exec(`INSERT INTO workspace_settings (workspace_id, default_currency) VALUES ($1, 'USD')`, f007Workspace); err != nil {
		return err
	}
	if err := exec(`INSERT INTO folders (id, workspace_id, name, sort_order) VALUES ($1, $2, 'F', 0)`, f007Folder, f007Workspace); err != nil {
		return err
	}
	if err := exec(`INSERT INTO trash_batches (id, workspace_id, root_type, root_id, root_name, former_path, deleted_at)
	                VALUES ($1, $2, 'list', $3, 'Gone', '[{"type":"folder","id":"x","name":"F"}]', $4)`,
		f007BatchGone, f007Workspace, f007ListGone, day); err != nil {
		return err
	}
	if err := exec(`INSERT INTO trash_batches (id, workspace_id, root_type, root_id, root_name, former_path, deleted_at)
	                VALUES ($1, $2, 'item', $3, 'Alone', '[]', $4)`,
		f007BatchItem, f007Workspace, f007Alone, twoDays); err != nil {
		return err
	}
	for _, l := range []struct {
		id      string
		folder  any
		name    string
		order   int
		trashed bool
	}{
		{f007ListFilms, f007Folder, "Films", 0, false},
		{f007ListGarage, f007Folder, "Garage", 1, false},
		{f007ListRoot, nil, "Mixed root", 0, false},
		{f007ListEmpty, nil, "Empty", 1, false},
		{f007ListGone, f007Folder, "Gone", 2, true},
	} {
		var deleted, batch any
		if l.trashed {
			deleted, batch = day, f007BatchGone
		}
		if err := exec(`INSERT INTO lists (id, workspace_id, folder_id, name, description, sort_order, deleted_at, trash_batch_id)
		                VALUES ($1, $2, $3, $4, 'desc', $5, $6, $7)`,
			l.id, f007Workspace, l.folder, l.name, l.order, deleted, batch); err != nil {
			return err
		}
	}
	const item = `INSERT INTO items (id, workspace_id, list_id, title, kind, summary, status, official_website_url,
	                                    price_min, price_max, currency, metadata, sort_order, deleted_at, trash_batch_id)
	              VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::text::numeric, $10::text::numeric, $11, $12::text::jsonb, $13, $14, $15)`
	for _, it := range []struct {
		id, list, title, kind string
		summary, status, site any
		pmin, pmax, currency  any
		metadata              string
		order                 int
		deleted, batch        any
	}{
		{f007Matrix, f007ListFilms, "The Matrix", "movie", "A classic", "watched", "https://example.com/m", "5.00", "10.00", "EUR",
			`{"release_year":1999,"imdb_url":"https://www.imdb.com/title/tt0133093/"}`, 0, nil, nil},
		{f007Plain, f007ListFilms, "Plain movie", "movie", nil, nil, nil, nil, nil, nil, `{"release_year":2000}`, 1, nil, nil},
		{f007Coupe, f007ListGarage, "Coupe", "car", nil, nil, nil, nil, nil, nil,
			`{"make":"BMW","trim":"GT","seats":4,"transmission":"automatic","fuel_type":"petrol"}`, 0, nil, nil},
		{f007Bike, f007ListGarage, "Bike", "motorcycle", "Fast one", "owned", nil, nil, nil, nil,
			`{"make":"Ducati","segment":"naked","torque_nm":90.5,"wet_weight_kg":200,"seat_height_mm":800}`, 1, nil, nil},
		{f007Gadget, f007ListGarage, "Gadget", "generic", nil, nil, nil, nil, nil, nil, `{}`, 2, nil, nil},
		{f007PlainCar, f007ListGarage, "Plain car", "car", nil, nil, nil, nil, nil, nil, `{"seats":5}`, 3, nil, nil},
		{f007RootGen, f007ListRoot, "Generic thing", "generic", nil, nil, nil, nil, nil, nil, `{}`, 0, nil, nil},
		{f007RootMov, f007ListRoot, "Status movie", "movie", nil, "to watch", nil, nil, nil, nil, `{"runtime_minutes":90}`, 1, nil, nil},
		{f007Show, f007ListRoot, "A show", "tv_show", "line1\nline2", nil, nil, nil, nil, nil,
			`{"first_air_year":2008,"seasons":5,"streaming":{"country":"IT"}}`, 2, nil, nil},
		{f007Camera, f007ListGone, "Old camera", "camera", nil, nil, nil, nil, nil, nil, `{"make":"Leica","weight_g":400}`, 0, day, f007BatchGone},
		{f007GoneMov, f007ListGone, "Gone movie", "movie", nil, nil, nil, nil, nil, nil, `{}`, 1, day, f007BatchGone},
		{f007Alone, f007ListGone, "Alone", "movie", nil, nil, nil, nil, nil, nil, `{}`, 2, twoDays, f007BatchItem},
	} {
		if err := exec(item, it.id, f007Workspace, it.list, it.title, it.kind, it.summary, it.status, it.site,
			it.pmin, it.pmax, it.currency, it.metadata, it.order, it.deleted, it.batch); err != nil {
			return err
		}
	}
	return nil
}

func f007Str(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var s string
	if err := pool.QueryRow(t.Context(), sql, args...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return s
}

func f007Int(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func f007ListOf(t *testing.T, pool *pgxpool.Pool, item string) string {
	t.Helper()
	return f007Str(t, pool, `SELECT l.name FROM items i JOIN lists l ON l.id = i.list_id WHERE i.id = $1`, item)
}

func f007Latest(t *testing.T, pool *pgxpool.Pool, item string) (int, string) {
	t.Helper()
	var v int
	var body string
	var size int
	var sum []byte
	err := pool.QueryRow(t.Context(),
		`SELECT version, body, size_bytes, sha256 FROM item_content_versions WHERE item_id = $1 ORDER BY version DESC LIMIT 1`,
		item).Scan(&v, &body, &size, &sum)
	if err != nil {
		return 0, ""
	}
	if size != len([]byte(body)) {
		t.Errorf("item %s: size_bytes = %d, want %d", item, size, len([]byte(body)))
	}
	want := sha256.Sum256([]byte(body))
	if string(sum) != string(want[:]) {
		t.Errorf("item %s: sha256 does not match body", item)
	}
	return v, body
}

func TestF007_MigrationRestructure(t *testing.T) {
	ctx := t.Context()
	dsn := newDatabase(t)
	m := newMigrate(t, dsn)
	if err := m.Migrate(1); err != nil {
		t.Fatalf("migrate to 1: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	if err := f007Seed(ctx, pool); err != nil {
		t.Fatalf("seed 1.0 fixture: %v", err)
	}
	for _, c := range []struct {
		item string
		v    int
		body string
	}{
		{f007Matrix, 1, "old body"},
		{f007Coupe, 1, "a"},
		{f007Coupe, 2, "b"},
	} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO item_content_versions (item_id, version, body, size_bytes, sha256) VALUES ($1, $2, $3, $4, $5)`,
			c.item, c.v, c.body, len(c.body), func() []byte { s := sha256.Sum256([]byte(c.body)); return s[:] }()); err != nil {
			t.Fatal(err)
		}
	}

	if err := m.Steps(1); err != nil {
		t.Fatalf("migrate up 000002: %v", err)
	}

	t.Run("lists get the kind of their items", func(t *testing.T) {
		for _, c := range []struct{ name, kind string }{
			{"Films", "movie"},
			{"Garage · Car", "car"},
			{"Garage · Item", "generic"},
			{"Garage · Motorcycle", "motorcycle"},
			{"Mixed root · Item", "generic"},
			{"Mixed root · Movie", "movie"},
			{"Mixed root · TV show", "tv_show"},
			{"Empty", "generic"},
			{"Gone · Camera", "camera"},
			{"Gone · Movie", "movie"},
		} {
			if got := f007Str(t, pool, `SELECT kind FROM lists WHERE name = $1`, c.name); got != c.kind {
				t.Errorf("list %q kind = %q, want %q", c.name, got, c.kind)
			}
		}
		if n := f007Int(t, pool, `SELECT count(*) FROM lists`); n != 10 {
			t.Errorf("lists = %d, want 10", n)
		}
	})

	t.Run("mixed lists are split in place with items re-pointed", func(t *testing.T) {
		for _, c := range []struct{ item, list string }{
			{f007Matrix, "Films"}, {f007Plain, "Films"},
			{f007Coupe, "Garage · Car"}, {f007PlainCar, "Garage · Car"},
			{f007Gadget, "Garage · Item"}, {f007Bike, "Garage · Motorcycle"},
			{f007RootGen, "Mixed root · Item"}, {f007RootMov, "Mixed root · Movie"}, {f007Show, "Mixed root · TV show"},
			{f007Camera, "Gone · Camera"}, {f007GoneMov, "Gone · Movie"}, {f007Alone, "Gone · Movie"},
		} {
			if got := f007ListOf(t, pool, c.item); got != c.list {
				t.Errorf("item %s in %q, want %q", c.item, got, c.list)
			}
		}
		for _, name := range []string{"Garage · Item", "Garage · Motorcycle", "Gone · Movie"} {
			if got := f007Str(t, pool, `SELECT folder_id::text FROM lists WHERE name = $1`, name); got != f007Folder {
				t.Errorf("piece %q folder = %q, want %s", name, got, f007Folder)
			}
		}
		for _, name := range []string{"Mixed root · Movie", "Mixed root · TV show"} {
			if n := f007Int(t, pool, `SELECT count(*) FROM lists WHERE name = $1 AND folder_id IS NULL`, name); n != 1 {
				t.Errorf("piece %q not at the root", name)
			}
		}
		if got := f007Str(t, pool, `SELECT description FROM lists WHERE name = 'Garage · Motorcycle'`); got != "desc" {
			t.Errorf("piece description = %q", got)
		}
	})

	t.Run("from 1.0 block is a new content version", func(t *testing.T) {
		for _, c := range []struct {
			item string
			v    int
			body string
		}{
			{f007Matrix, 2, "## From 1.0\n\n- **Summary:** A classic\n- **Status:** watched\n\n---\n\nold body"},
			{f007Coupe, 3, "## From 1.0\n\n- **Seats:** 4\n- **Transmission:** automatic\n- **Trim:** GT\n\n---\n\nb"},
			{f007Bike, 1, "## From 1.0\n\n- **Summary:** Fast one\n- **Status:** owned\n" +
				"- **Seat height mm:** 800\n- **Segment:** naked\n- **Torque nm:** 90.5\n- **Wet weight kg:** 200\n\n---\n"},
			{f007PlainCar, 1, "## From 1.0\n\n- **Seats:** 5\n\n---\n"},
			{f007Show, 1, "## From 1.0\n\n- **Summary:** line1\n  line2\n\n---\n"},
			{f007RootMov, 1, "## From 1.0\n\n- **Status:** to watch\n\n---\n"},
			{f007Camera, 1, "## From 1.0\n\n- **Weight g:** 400\n\n---\n"},
		} {
			v, body := f007Latest(t, pool, c.item)
			if v != c.v || body != c.body {
				t.Errorf("item %s latest content = v%d %q, want v%d %q", c.item, v, body, c.v, c.body)
			}
		}
		if n := f007Int(t, pool, `SELECT count(*) FROM item_content_versions WHERE item_id = $1`, f007Coupe); n != 3 {
			t.Errorf("coupe versions = %d, want 3 (old ones kept)", n)
		}
		for _, item := range []string{f007Plain, f007Gadget, f007RootGen, f007GoneMov, f007Alone} {
			if n := f007Int(t, pool, `SELECT count(*) FROM item_content_versions WHERE item_id = $1`, item); n != 0 {
				t.Errorf("item %s has %d content versions, want none", item, n)
			}
		}
	})

	t.Run("metadata keeps kept keys only", func(t *testing.T) {
		for _, c := range []struct{ item, want string }{
			{f007Matrix, `{"imdb_url": "https://www.imdb.com/title/tt0133093/", "release_year": 1999}`},
			{f007Plain, `{"release_year": 2000}`},
			{f007Coupe, `{"make": "BMW", "fuel_type": "petrol"}`},
			{f007Bike, `{"make": "Ducati"}`},
			{f007PlainCar, `{}`},
			{f007Show, `{"seasons": 5, "streaming": {"country": "IT"}, "first_air_year": 2008}`},
			{f007RootMov, `{"runtime_minutes": 90}`},
			{f007Camera, `{"make": "Leica"}`},
		} {
			if n := f007Int(t, pool, `SELECT count(*) FROM items WHERE id = $1 AND metadata = $2::jsonb`, c.item, c.want); n != 1 {
				t.Errorf("item %s metadata = %s, want %s", c.item,
					f007Str(t, pool, `SELECT metadata::text FROM items WHERE id = $1`, c.item), c.want)
			}
		}
		// touched items bump the metadata version, untouched ones do not
		for item, want := range map[string]int{f007Matrix: 2, f007Coupe: 2, f007Bike: 2, f007Plain: 1, f007Gadget: 1} {
			if got := f007Int(t, pool, `SELECT version FROM items WHERE id = $1`, item); got != want {
				t.Errorf("item %s version = %d, want %d", item, got, want)
			}
		}
	})

	t.Run("website and prices are carried over", func(t *testing.T) {
		if got := f007Str(t, pool, `SELECT website_url FROM items WHERE id = $1`, f007Matrix); got != "https://example.com/m" {
			t.Errorf("website_url = %q", got)
		}
		if got := f007Str(t, pool, `SELECT price_min::text || '-' || price_max::text || ' ' || currency FROM items WHERE id = $1`, f007Matrix); got != "5.00-10.00 EUR" {
			t.Errorf("price = %q", got)
		}
		for _, col := range []string{"kind", "summary", "status", "official_website_url"} {
			if n := f007Int(t, pool, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'items' AND column_name = $1`, col); n != 0 {
				t.Errorf("items.%s still exists", col)
			}
		}
		if n := f007Int(t, pool, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'workspace_settings' AND column_name = 'default_currency'`); n != 0 {
			t.Error("workspace_settings.default_currency still exists")
		}
	})

	t.Run("trashed split pieces own their batch", func(t *testing.T) {
		var pieceID, pieceBatch string
		if err := pool.QueryRow(ctx, `SELECT id::text, trash_batch_id::text FROM lists WHERE name = 'Gone · Movie'`).Scan(&pieceID, &pieceBatch); err != nil {
			t.Fatal(err)
		}
		if pieceBatch == f007BatchGone {
			t.Error("piece shares the original batch")
		}
		var rootID, rootName, rootType string
		if err := pool.QueryRow(ctx, `SELECT root_id::text, root_name, root_type FROM trash_batches WHERE id = $1`, pieceBatch).Scan(&rootID, &rootName, &rootType); err != nil {
			t.Fatal(err)
		}
		if rootID != pieceID || rootName != "Gone · Movie" || rootType != "list" {
			t.Errorf("piece batch = (%s, %q, %s), want (%s, %q, list)", rootID, rootName, rootType, pieceID, "Gone · Movie")
		}
		if got := f007Str(t, pool, `SELECT root_name FROM trash_batches WHERE id = $1`, f007BatchGone); got != "Gone · Camera" {
			t.Errorf("original batch root_name = %q", got)
		}
		if got := f007Str(t, pool, `SELECT trash_batch_id::text FROM items WHERE id = $1`, f007GoneMov); got != pieceBatch {
			t.Errorf("batch-trashed item batch = %s, want piece batch", got)
		}
		if got := f007Str(t, pool, `SELECT trash_batch_id::text FROM items WHERE id = $1`, f007Camera); got != f007BatchGone {
			t.Errorf("camera batch = %s, want original", got)
		}
		if got := f007Str(t, pool, `SELECT trash_batch_id::text FROM items WHERE id = $1`, f007Alone); got != f007BatchItem {
			t.Errorf("separately trashed item batch = %s, want its own", got)
		}
		f007AssertNoDangling(t, pool)
	})

	t.Run("down then up again", func(t *testing.T) {
		lists := f007Int(t, pool, `SELECT count(*) FROM lists`)
		versions := f007Int(t, pool, `SELECT count(*) FROM item_content_versions`)
		if err := m.Steps(-1); err != nil {
			t.Fatalf("migrate down 000002: %v", err)
		}
		if got := f007Str(t, pool, `SELECT kind FROM items WHERE id = $1`, f007Bike); got != "motorcycle" {
			t.Errorf("kind after down = %q, want motorcycle", got)
		}
		if err := m.Steps(1); err != nil {
			t.Fatalf("migrate up 000002 again: %v", err)
		}
		if got := f007Int(t, pool, `SELECT count(*) FROM lists`); got != lists {
			t.Errorf("lists after re-up = %d, want %d", got, lists)
		}
		if got := f007Int(t, pool, `SELECT count(*) FROM item_content_versions`); got != versions {
			t.Errorf("content versions after re-up = %d, want %d", got, versions)
		}
		f007AssertNoDangling(t, pool)
	})
}

func f007AssertNoDangling(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	for _, tbl := range []string{"folders", "lists", "items"} {
		q := fmt.Sprintf(`SELECT count(*) FROM %s r WHERE r.trash_batch_id IS NOT NULL
		                    AND NOT EXISTS (SELECT 1 FROM trash_batches b WHERE b.id = r.trash_batch_id)`, tbl)
		if n := f007Int(t, pool, q); n != 0 {
			t.Errorf("%s: %d rows reference a missing trash batch", tbl, n)
		}
	}
	for _, c := range []struct{ typ, tbl string }{{"folder", "folders"}, {"list", "lists"}, {"item", "items"}} {
		q := fmt.Sprintf(`SELECT count(*) FROM trash_batches b WHERE b.root_type = '%s'
		                    AND NOT EXISTS (SELECT 1 FROM %s r WHERE r.id = b.root_id AND r.trash_batch_id = b.id)`, c.typ, c.tbl)
		if n := f007Int(t, pool, q); n != 0 {
			t.Errorf("%d %s batches have a missing root", n, c.typ)
		}
	}
}
