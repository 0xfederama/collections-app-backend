package database_test

import (
	"os"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// F018-B1 (migration 000025): the built-in set is book, vehicle, movie, tv_show, wishlist (plural names);
// motorcycle/car lists become vehicle lists, camera lists become no-type lists, prices survive only on
// wishlist, and custom types keyed vehicle/wishlist are renamed <key>_custom with their data intact.
func TestF018B1_TypeRefactorMigration(t *testing.T) {
	ctx := t.Context()
	dsn := newDatabase(t)
	m := newMigrate(t, dsn)
	if err := m.Migrate(24); err != nil {
		t.Fatalf("migrate to 24: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	const (
		wsA = "00000000-0000-0000-0000-000000001801"
		wsB = "00000000-0000-0000-0000-000000001802" // vehicle_custom already exists
		wsC = "00000000-0000-0000-0000-000000001803" // customised movie name, no data

		lMoto     = "00000000-0000-0000-0000-000000001811"
		lCar      = "00000000-0000-0000-0000-000000001812"
		lCarPrice = "00000000-0000-0000-0000-000000001813"
		lCamera   = "00000000-0000-0000-0000-000000001814"
		lMovie    = "00000000-0000-0000-0000-000000001815"
		lTV       = "00000000-0000-0000-0000-000000001816"
		lBook     = "00000000-0000-0000-0000-000000001817"
		lNone     = "00000000-0000-0000-0000-000000001818"
		lCustVeh  = "00000000-0000-0000-0000-000000001819"
		lCustWish = "00000000-0000-0000-0000-00000000181a"
		lBookCust = "00000000-0000-0000-0000-00000000181b"
		lCustVehB = "00000000-0000-0000-0000-00000000181c"

		iMoto     = "00000000-0000-0000-0000-000000001821"
		iCar      = "00000000-0000-0000-0000-000000001822"
		iCamera   = "00000000-0000-0000-0000-000000001823"
		iMovie    = "00000000-0000-0000-0000-000000001824"
		iTV       = "00000000-0000-0000-0000-000000001825"
		iBook     = "00000000-0000-0000-0000-000000001826"
		iNone     = "00000000-0000-0000-0000-000000001827"
		iCustVeh  = "00000000-0000-0000-0000-000000001828"
		iCustWish = "00000000-0000-0000-0000-000000001829"
		iBookCust = "00000000-0000-0000-0000-00000000182a"
		iCustVehB = "00000000-0000-0000-0000-00000000182b"

		tCustVeh  = "00000000-0000-0000-0000-000000001831"
		tCustWish = "00000000-0000-0000-0000-000000001832"
		tCustVehB = "00000000-0000-0000-0000-000000001833"
		tCustPre  = "00000000-0000-0000-0000-000000001834"
		tBookCust = "00000000-0000-0000-0000-000000001835"
	)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%.70s: %v", sql, err)
		}
	}
	str := func(sql string, args ...any) string {
		t.Helper()
		var s *string
		if err := pool.QueryRow(ctx, sql, args...).Scan(&s); err != nil {
			t.Fatalf("%.70s: %v", sql, err)
		}
		if s == nil {
			return "<null>"
		}
		return *s
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("%.70s: %v", sql, err)
		}
		return n
	}

	for i, ws := range []string{wsA, wsB, wsC} {
		exec(`INSERT INTO workspaces (id, name) VALUES ($1, $2)`, ws, "W"+string(rune('A'+i))) // the trigger seeds the old built-ins
	}
	exec(`UPDATE type_definitions SET name = 'Films' WHERE workspace_id = $1 AND key = 'movie'`, wsC)

	// Custom types colliding with the new keys (wsA), a pre-existing vehicle_custom (wsB), a custom book_custom (wsA).
	exec(`INSERT INTO type_definitions (id, workspace_id, key, name, allows_price) VALUES ($1, $2, 'vehicle', 'My vehicles', true)`, tCustVeh, wsA)
	exec(`INSERT INTO type_fields (workspace_id, type_id, key, label, field_type, position) VALUES ($1, $2, 'colour', 'Colour', 'text', 0)`, wsA, tCustVeh)
	exec(`INSERT INTO type_definitions (id, workspace_id, key, name, allows_price) VALUES ($1, $2, 'wishlist', 'My wishes', true)`, tCustWish, wsA)
	exec(`INSERT INTO type_fields (workspace_id, type_id, key, label, field_type, position) VALUES ($1, $2, 'store', 'Store', 'text', 0)`, wsA, tCustWish)
	exec(`INSERT INTO type_definitions (id, workspace_id, key, name) VALUES ($1, $2, 'vehicle_custom', 'Already here')`, tCustPre, wsB)
	exec(`INSERT INTO type_definitions (id, workspace_id, key, name, allows_price) VALUES ($1, $2, 'vehicle', 'B vehicles', true)`, tCustVehB, wsB)
	exec(`INSERT INTO type_definitions (id, workspace_id, key, name, allows_price) VALUES ($1, $2, 'book_custom', 'Mine', true)`, tBookCust, wsA)

	list := func(id, ws, name, typ, sortMode string, pins any, order int) {
		t.Helper()
		var typeArg any
		if typ != "" {
			typeArg = typ
		}
		exec(`INSERT INTO lists (id, workspace_id, name, type, sort_mode, pinned_fields, icon, sort_order)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb, '🙂', $7)`, id, ws, name, typeArg, sortMode, pins, order)
	}
	item := func(id, ws, listID, md string, priced bool) {
		t.Helper()
		if priced {
			exec(`INSERT INTO items (id, workspace_id, list_id, title, metadata, price_min, price_max, currency, sort_order)
				VALUES ($1, $2, $3, 'Fixture', $4::jsonb, 100, 200, 'EUR', 0)`, id, ws, listID, md)
			return
		}
		exec(`INSERT INTO items (id, workspace_id, list_id, title, metadata, sort_order) VALUES ($1, $2, $3, 'Fixture', $4::jsonb, 0)`, id, ws, listID, md)
	}
	list(lMoto, wsA, "Moto", "motorcycle", "license_class", `["displacement_cc","price","license_class"]`, 0)
	list(lCar, wsA, "Car", "car", "power_hp", `["model_year","price"]`, 1)
	list(lCarPrice, wsA, "Car price", "car", "price", `["price"]`, 2)
	list(lCamera, wsA, "Camera", "camera", "megapixels", `["camera_type","price"]`, 3)
	list(lMovie, wsA, "Movie", "movie", "release_year", `["runtime_minutes"]`, 4)
	list(lTV, wsA, "TV", "tv_show", "manual", nil, 5)
	list(lBook, wsA, "Book", "book", "price", `["author","price"]`, 6)
	list(lNone, wsA, "None", "", "title", nil, 7)
	list(lCustVeh, wsA, "Cust veh", "vehicle", "colour", `["colour"]`, 8)
	list(lCustWish, wsA, "Cust wish", "wishlist", "manual", nil, 9)
	list(lBookCust, wsA, "Book custom", "book_custom", "price", `["price"]`, 10)
	list(lCustVehB, wsB, "B veh", "vehicle", "manual", nil, 0)

	item(iMoto, wsA, lMoto, `{"model_year":2021,"displacement_cc":937,"power_hp":111,"license_class":"A2","extra":1}`, true)
	item(iCar, wsA, lCar, `{"make":"Lancia","model":"Delta","model_year":1992,"fuel_type":"petrol","power_hp":210}`, true)
	item(iCamera, wsA, lCamera, `{"camera_type":"mirrorless","release_year":2013,"sensor_format":"full_frame","megapixels":24.3}`, true)
	item(iMovie, wsA, lMovie, `{"release_year":1999,"runtime_minutes":136}`, false)
	item(iTV, wsA, lTV, `{"seasons":5}`, false)
	item(iBook, wsA, lBook, `{"author":"Herbert","pages":412}`, true)
	item(iNone, wsA, lNone, `{}`, true)
	item(iCustVeh, wsA, lCustVeh, `{"colour":"red"}`, true)
	item(iCustWish, wsA, lCustWish, `{"store":"Shop"}`, true)
	item(iBookCust, wsA, lBookCust, `{}`, true)
	item(iCustVehB, wsB, lCustVehB, `{}`, true)

	if err := m.Steps(1); err != nil {
		t.Fatalf("migrate up 000025: %v", err)
	}

	type fieldRow struct{ Key, Label, FieldType string }
	fieldsOf := func(ws, key string) []fieldRow {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT f.key, f.label, f.field_type FROM type_fields f
			JOIN type_definitions d ON d.id = f.type_id WHERE d.workspace_id = $1 AND d.key = $2 AND d.is_locked
			AND f.is_locked ORDER BY f.position`, ws, key)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var got []fieldRow
		for rows.Next() {
			var r fieldRow
			if err := rows.Scan(&r.Key, &r.Label, &r.FieldType); err != nil {
				t.Fatal(err)
			}
			got = append(got, r)
		}
		return got
	}
	priceCount := func(id string) int {
		return count(`SELECT count(*) FROM items WHERE id = $1 AND (price_min IS NOT NULL OR price_max IS NOT NULL OR currency IS NOT NULL)`, id)
	}

	t.Run("the built-ins are exactly five with plural names", func(t *testing.T) {
		rows, err := pool.Query(ctx, `SELECT key, name FROM type_definitions WHERE workspace_id = $1 AND is_locked ORDER BY key`, wsA)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var got []string
		for rows.Next() {
			var k, n string
			if err := rows.Scan(&k, &n); err != nil {
				t.Fatal(err)
			}
			got = append(got, k+"="+n)
		}
		want := []string{"book=Books", "movie=Movies", "tv_show=TV shows", "vehicle=Vehicles", "wishlist=Wishlist"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("built-ins = %v, want %v", got, want)
		}
		if n := count(`SELECT count(*) FROM type_definitions WHERE key IN ('motorcycle','car','camera')`); n != 0 {
			t.Errorf("%d leftover motorcycle/car/camera definitions", n)
		}
		if n := count(`SELECT count(*) FROM type_fields f LEFT JOIN type_definitions d ON d.id = f.type_id WHERE d.id IS NULL`); n != 0 {
			t.Errorf("%d orphan fields", n)
		}
		if got := str(`SELECT name FROM type_definitions WHERE workspace_id = $1 AND key = 'movie'`, wsC); got != "Films" {
			t.Errorf("customised movie name = %q, want kept", got)
		}
		if got := str(`SELECT name FROM type_definitions WHERE workspace_id = $1 AND key = 'tv_show'`, wsC); got != "TV shows" {
			t.Errorf("tv_show name in C = %q", got)
		}
		if got := str(`SELECT version::text FROM type_definitions WHERE workspace_id = $1 AND key = 'movie'`, wsA); got != "2" {
			t.Errorf("movie version = %s, want bumped to 2", got)
		}
	})

	t.Run("vehicle and wishlist definitions", func(t *testing.T) {
		for _, ws := range []string{wsA, wsB, wsC} {
			if got := str(`SELECT concat_ws('|', default_icon, allows_price::text, COALESCE(default_sort_field, 'null'), default_pinned_fields::text, is_locked::text)
				FROM type_definitions WHERE workspace_id = $1 AND key = 'vehicle'`, ws); got != `i:car|false|null|["model_year", "power_hp"]|true` {
				t.Errorf("vehicle in %s = %s", ws, got)
			}
			if got := str(`SELECT concat_ws('|', default_icon, allows_price::text, COALESCE(default_sort_field, 'null'), default_pinned_fields::text, is_locked::text)
				FROM type_definitions WHERE workspace_id = $1 AND key = 'wishlist'`, ws); got != `i:tag|true|null|["price"]|true` {
				t.Errorf("wishlist in %s = %s", ws, got)
			}
			if got := str(`SELECT allows_price::text FROM type_definitions WHERE workspace_id = $1 AND key = 'book'`, ws); got != "false" {
				t.Errorf("book allows_price in %s = %s", ws, got)
			}
			if n := count(`SELECT count(*) FROM type_definitions WHERE workspace_id = $1 AND is_locked AND allows_price`, ws); n != 1 {
				t.Errorf("%d locked types allow a price in %s, want only wishlist", n, ws)
			}
		}
		want := []fieldRow{
			{"make", "Make", "text"}, {"model", "Model", "text"}, {"model_year", "Year", "number"},
			{"displacement_cc", "Engine size (cc)", "number"}, {"power_hp", "Power (hp)", "number"},
		}
		if got := fieldsOf(wsA, "vehicle"); !reflect.DeepEqual(got, want) {
			t.Errorf("vehicle fields = %v", got)
		}
		if got := fieldsOf(wsA, "wishlist"); len(got) != 0 {
			t.Errorf("wishlist fields = %v, want none", got)
		}
		if got := fieldsOf(wsA, "movie"); len(got) != 7 || got[1].Label != "Runtime" {
			t.Errorf("movie fields = %v", got)
		}
		if got := fieldsOf(wsA, "book"); len(got) != 6 {
			t.Errorf("book fields = %v", got)
		}
	})

	t.Run("colliding custom types are renamed with their data intact", func(t *testing.T) {
		if got := str(`SELECT key || '|' || name || '|' || is_locked::text || '|' || version FROM type_definitions WHERE id = $1`, tCustVeh); got != "vehicle_custom|My vehicles|false|2" {
			t.Errorf("custom vehicle = %s", got)
		}
		if got := str(`SELECT key || '|' || name FROM type_definitions WHERE id = $1`, tCustWish); got != "wishlist_custom|My wishes" {
			t.Errorf("custom wishlist = %s", got)
		}
		if got := str(`SELECT key FROM type_definitions WHERE id = $1`, tCustVehB); got != "vehicle_custom_2" {
			t.Errorf("custom vehicle in B = %s, want vehicle_custom_2", got)
		}
		if got := str(`SELECT key FROM type_definitions WHERE id = $1`, tCustPre); got != "vehicle_custom" {
			t.Errorf("pre-existing vehicle_custom = %s", got)
		}
		if got := str(`SELECT key FROM type_definitions WHERE id = $1`, tBookCust); got != "book_custom" {
			t.Errorf("book_custom = %s", got)
		}
		for id, want := range map[string]string{lCustVeh: "vehicle_custom", lCustWish: "wishlist_custom", lCustVehB: "vehicle_custom_2"} {
			if got := str(`SELECT type FROM lists WHERE id = $1`, id); got != want {
				t.Errorf("list %s type = %s, want %s", id, got, want)
			}
		}
		if got := str(`SELECT sort_mode || '|' || pinned_fields::text FROM lists WHERE id = $1`, lCustVeh); got != `colour|["colour"]` {
			t.Errorf("custom vehicle list = %s", got)
		}
		if got := count(`SELECT count(*) FROM type_fields WHERE type_id IN ($1, $2) AND key IN ('colour', 'store')`, tCustVeh, tCustWish); got != 2 {
			t.Errorf("custom fields = %d, want 2", got)
		}
		if got := str(`SELECT metadata::text FROM items WHERE id = $1`, iCustVeh); got != `{"colour": "red"}` {
			t.Errorf("custom vehicle item = %s", got)
		}
		if got := str(`SELECT metadata::text FROM items WHERE id = $1`, iCustWish); got != `{"store": "Shop"}` {
			t.Errorf("custom wishlist item = %s", got)
		}
		for _, id := range []string{iCustVeh, iCustWish, iBookCust, iCustVehB} {
			if priceCount(id) != 1 || count(`SELECT version FROM items WHERE id = $1`, id) != 1 {
				t.Errorf("custom-type item %s was touched", id)
			}
		}
		if got := str(`SELECT sort_mode FROM lists WHERE id = $1`, lBookCust); got != "price" {
			t.Errorf("book_custom list sort = %s, want price kept", got)
		}
	})

	t.Run("motorcycle and car lists become vehicle lists", func(t *testing.T) {
		for _, id := range []string{lMoto, lCar, lCarPrice} {
			if got := str(`SELECT type FROM lists WHERE id = $1`, id); got != "vehicle" {
				t.Errorf("list %s type = %s", id, got)
			}
		}
		for id, want := range map[string]string{
			lMoto:     `manual|["displacement_cc"]`,
			lCar:      `power_hp|["model_year"]`,
			lCarPrice: `manual|<null>`,
		} {
			got := str(`SELECT sort_mode || '|' || COALESCE(pinned_fields::text, '<null>') FROM lists WHERE id = $1`, id)
			if got != want {
				t.Errorf("list %s sort|pins = %s, want %s", id, got, want)
			}
		}
		if got := str(`SELECT metadata::text FROM items WHERE id = $1`, iMoto); got != `{"power_hp": 111, "model_year": 2021, "displacement_cc": 937}` {
			t.Errorf("moto item = %s", got)
		}
		if got := str(`SELECT metadata::text FROM items WHERE id = $1`, iCar); got != `{"make": "Lancia", "model": "Delta", "power_hp": 210, "model_year": 1992}` {
			t.Errorf("car item = %s", got)
		}
		for _, id := range []string{iMoto, iCar} {
			if priceCount(id) != 0 {
				t.Errorf("vehicle item %s keeps a price", id)
			}
			if v := count(`SELECT version FROM items WHERE id = $1`, id); v != 2 {
				t.Errorf("vehicle item %s version = %d, want 2", id, v)
			}
		}
		if got := str(`SELECT icon FROM lists WHERE id = $1`, lMoto); got != "🙂" {
			t.Errorf("list icon = %s, want untouched", got)
		}
	})

	t.Run("camera lists become no-type lists", func(t *testing.T) {
		if got := str(`SELECT COALESCE(type, '<null>') || '|' || sort_mode || '|' || COALESCE(pinned_fields::text, '<null>') || '|' || icon FROM lists WHERE id = $1`, lCamera); got != "<null>|manual|<null>|🙂" {
			t.Errorf("camera list = %s", got)
		}
		if got := str(`SELECT metadata::text FROM items WHERE id = $1`, iCamera); got != `{}` {
			t.Errorf("camera item = %s", got)
		}
		if priceCount(iCamera) != 0 {
			t.Errorf("camera item keeps a price")
		}
	})

	t.Run("book lists lose prices, price sort and price pin", func(t *testing.T) {
		if got := str(`SELECT sort_mode || '|' || pinned_fields::text FROM lists WHERE id = $1`, lBook); got != `manual|["author"]` {
			t.Errorf("book list = %s", got)
		}
		if priceCount(iBook) != 0 {
			t.Errorf("book item keeps a price")
		}
		if got := str(`SELECT metadata::text FROM items WHERE id = $1`, iBook); got != `{"pages": 412, "author": "Herbert"}` {
			t.Errorf("book metadata = %s", got)
		}
	})

	t.Run("movie, tv, and no-type lists are untouched", func(t *testing.T) {
		for _, id := range []string{lMovie, lTV, lNone} {
			if v := count(`SELECT version FROM lists WHERE id = $1`, id); v != 1 {
				t.Errorf("list %s bumped to v%d", id, v)
			}
		}
		for _, id := range []string{iMovie, iTV} {
			if v := count(`SELECT version FROM items WHERE id = $1`, id); v != 1 {
				t.Errorf("item %s bumped to v%d", id, v)
			}
		}
		if priceCount(iNone) != 1 {
			t.Errorf("no-type item lost its price (only camera/vehicle/book are cleared)")
		}
	})

	t.Run("a fresh workspace gets exactly the five built-ins", func(t *testing.T) {
		const ws = "00000000-0000-0000-0000-000000001804"
		exec(`INSERT INTO workspaces (id, name) VALUES ($1, 'Fresh')`, ws)
		if n := count(`SELECT count(*) FROM type_definitions WHERE workspace_id = $1 AND is_locked`, ws); n != 5 {
			t.Errorf("fresh built-ins = %d, want 5", n)
		}
		if n := count(`SELECT count(*) FROM type_definitions WHERE workspace_id = $1 AND key IN ('motorcycle','car','camera')`, ws); n != 0 {
			t.Errorf("fresh workspace has old types")
		}
		if got := str(`SELECT name FROM type_definitions WHERE workspace_id = $1 AND key = 'movie'`, ws); got != "Movies" {
			t.Errorf("fresh movie name = %s", got)
		}
		if got := fieldsOf(ws, "vehicle"); len(got) != 5 {
			t.Errorf("fresh vehicle fields = %v", got)
		}
	})

	t.Run("re-running the migration changes nothing", func(t *testing.T) {
		snapshot := func() string {
			return str(`SELECT (
				SELECT string_agg(id::text || ':' || version::text, ',' ORDER BY id) FROM (
					SELECT id, version FROM lists UNION ALL SELECT id, version FROM items
					UNION ALL SELECT id, version FROM type_definitions UNION ALL SELECT id, version FROM type_fields) s
				) || '/' || (SELECT count(*) FROM type_definitions)::text`)
		}
		before := snapshot()
		sql, err := os.ReadFile("../../db/migrations/000025_type_refactor.up.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("second run: %v", err)
		}
		if after := snapshot(); after != before {
			t.Errorf("second run changed data:\nbefore %s\nafter  %s", before, after)
		}
	})

	t.Run("down is lossy but runs, and up works again", func(t *testing.T) {
		if err := m.Steps(-1); err != nil {
			t.Fatalf("migrate down 000025: %v", err)
		}
		if n := count(`SELECT count(*) FROM type_definitions WHERE workspace_id = $1 AND is_locked`, wsA); n != 6 {
			t.Errorf("built-ins after down = %d, want 6", n)
		}
		if got := str(`SELECT name FROM type_definitions WHERE workspace_id = $1 AND key = 'movie'`, wsA); got != "Movie" {
			t.Errorf("movie name after down = %s", got)
		}
		if n := count(`SELECT count(*) FROM type_definitions WHERE key IN ('vehicle','wishlist') AND is_locked`); n != 0 {
			t.Errorf("%d built-in vehicle/wishlist after down", n)
		}
		if err := m.Steps(1); err != nil {
			t.Fatalf("migrate up again: %v", err)
		}
		if n := count(`SELECT count(*) FROM type_definitions WHERE workspace_id = $1 AND is_locked`, wsA); n != 5 {
			t.Errorf("built-ins after second up = %d, want 5", n)
		}
	})
}
