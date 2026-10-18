package database_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestF014B1_MigrationSeedsBuiltInDefinitions(t *testing.T) {
	ctx := t.Context()
	dsn := newDatabase(t)
	m := newMigrate(t, dsn)
	if err := m.Migrate(13); err != nil {
		t.Fatalf("migrate to 13: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	const workspaceID = "00000000-0000-0000-0000-000000001401"
	if _, err := pool.Exec(ctx, `INSERT INTO workspaces (id, name) VALUES ($1, 'Definitions')`, workspaceID); err != nil {
		t.Fatal(err)
	}
	for position, fixture := range []struct {
		id, kind, metadata string
	}{
		{"00000000-0000-0000-0000-000000001411", "generic", `{}`},
		{"00000000-0000-0000-0000-000000001412", "movie", `{"release_year":1999,"runtime_minutes":136,"director":"Lana Wachowski","imdb_url":"https://www.imdb.com/title/tt0133093/","streaming":{"country":"IT","platforms":["Netflix"]},"tmdb_id":603,"lookup_disabled":true}`},
		{"00000000-0000-0000-0000-000000001413", "tv_show", `{"first_air_year":2008,"end_year":2013,"seasons":5,"episodes_per_season":13,"episode_minutes":47,"imdb_url":"https://www.imdb.com/title/tt0903747/","streaming":{"country":"IT","platforms":["Netflix"]},"tmdb_id":1396,"lookup_disabled":false}`},
		{"00000000-0000-0000-0000-000000001414", "motorcycle", `{"model_year":2021,"displacement_cc":937,"power_hp":111,"license_class":"A2"}`},
		{"00000000-0000-0000-0000-000000001415", "car", `{"make":"Lancia","model":"Delta","model_year":1992,"fuel_type":"petrol","power_hp":210}`},
		{"00000000-0000-0000-0000-000000001416", "camera", `{"camera_type":"mirrorless","release_year":2013,"sensor_format":"full_frame","megapixels":24.3}`},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO lists (id, workspace_id, name, kind, sort_order) VALUES ($1, $2, $3, $4, $5)`, fixture.id, workspaceID, fixture.kind, fixture.kind, position); err != nil {
			t.Fatalf("insert %s list: %v", fixture.kind, err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO items (workspace_id, list_id, title, metadata, sort_order) VALUES ($1, $2, 'Fixture', $3::jsonb, 0)`, workspaceID, fixture.id, fixture.metadata); err != nil {
			t.Fatalf("insert %s item: %v", fixture.kind, err)
		}
	}

	if err := m.Steps(1); err != nil {
		t.Fatalf("migrate up 000014: %v", err)
	}

	wantKinds := []struct {
		key, name, sortField string
		allowsPrice          bool
	}{
		{"generic", "Generic", "", false},
		{"movie", "Movie", "release_year", false},
		{"tv_show", "TV show", "first_air_year", false},
		{"motorcycle", "Motorcycle", "displacement_cc", true},
		{"car", "Car", "power_hp", true},
		{"camera", "Camera", "megapixels", true},
	}
	for _, want := range wantKinds {
		var name string
		var sortField *string
		var allowsPrice, locked bool
		var version int
		err := pool.QueryRow(ctx, `SELECT name, default_sort_field, allows_price, is_locked, version
			FROM kind_definitions WHERE workspace_id = $1 AND key = $2`, workspaceID, want.key).
			Scan(&name, &sortField, &allowsPrice, &locked, &version)
		if err != nil {
			t.Fatalf("load %s definition: %v", want.key, err)
		}
		gotSortField := ""
		if sortField != nil {
			gotSortField = *sortField
		}
		if name != want.name || gotSortField != want.sortField || allowsPrice != want.allowsPrice || !locked || version != 1 {
			t.Errorf("%s definition = name %q sort %q allows_price %t locked %t version %d", want.key, name, gotSortField, allowsPrice, locked, version)
		}
	}

	wantFields := map[string][]struct {
		key, label, fieldType string
	}{
		"generic": {},
		"movie": {
			{"release_year", "Release year", "number"}, {"runtime_minutes", "Runtime (minutes)", "number"},
			{"director", "Director", "text"}, {"imdb_url", "IMDb URL", "link"}, {"streaming", "Streaming", "streaming"},
			{"tmdb_id", "TMDB ID", "number"}, {"lookup_disabled", "Disable lookup", "boolean"},
		},
		"tv_show": {
			{"first_air_year", "First air year", "number"}, {"end_year", "End year", "number"}, {"seasons", "Seasons", "number"},
			{"episodes_per_season", "Episodes per season", "number"}, {"episode_minutes", "Episode minutes", "number"},
			{"imdb_url", "IMDb URL", "link"}, {"streaming", "Streaming", "streaming"}, {"tmdb_id", "TMDB ID", "number"},
			{"lookup_disabled", "Disable lookup", "boolean"},
		},
		"motorcycle": {
			{"model_year", "Model year", "number"}, {"displacement_cc", "Displacement (cc)", "number"},
			{"power_hp", "Power (hp)", "number"}, {"license_class", "License class", "text"},
		},
		"car": {
			{"make", "Make", "text"}, {"model", "Model", "text"}, {"model_year", "Model year", "number"},
			{"fuel_type", "Fuel type", "text"}, {"power_hp", "Power (hp)", "number"},
		},
		"camera": {
			{"camera_type", "Camera type", "text"}, {"release_year", "Release year", "number"},
			{"sensor_format", "Sensor format", "text"}, {"megapixels", "Megapixels", "number"},
		},
	}
	for kind, fields := range wantFields {
		rows, err := pool.Query(ctx, `SELECT f.key, f.label, f.field_type, f.position, f.is_locked, f.version
			FROM kind_fields f JOIN kind_definitions k ON k.id = f.kind_id
			WHERE f.workspace_id = $1 AND k.key = $2 ORDER BY f.position`, workspaceID, kind)
		if err != nil {
			t.Fatalf("list %s fields: %v", kind, err)
		}
		for position, want := range fields {
			if !rows.Next() {
				t.Errorf("%s is missing field %s", kind, want.key)
				break
			}
			var key, label, fieldType string
			var gotPosition, version int
			var locked bool
			if err := rows.Scan(&key, &label, &fieldType, &gotPosition, &locked, &version); err != nil {
				t.Fatal(err)
			}
			if key != want.key || label != want.label || fieldType != want.fieldType || gotPosition != position || !locked || version != 1 {
				t.Errorf("%s field %d = key %q label %q type %q position %d locked %t version %d", kind, position, key, label, fieldType, gotPosition, locked, version)
			}
		}
		if rows.Next() {
			t.Errorf("%s has unexpected extra fields", kind)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}

	if err := m.Steps(-1); err != nil {
		t.Fatalf("migrate down 000014: %v", err)
	}
	if err := m.Steps(1); err != nil {
		t.Fatalf("migrate up 000014 again: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM kind_definitions WHERE workspace_id = $1`, workspaceID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(wantKinds) {
		t.Errorf("definitions after down/up = %d, want %d", count, len(wantKinds))
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM items WHERE workspace_id = $1`, workspaceID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(wantKinds) {
		t.Errorf("items after migration = %d, want %d", count, len(wantKinds))
	}
}

func TestF014B4_MigrationReplacesKindKeyWithDefaultField(t *testing.T) {
	ctx := t.Context()
	dsn := newDatabase(t)
	m := newMigrate(t, dsn)
	if err := m.Migrate(14); err != nil {
		t.Fatalf("migrate to 14: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	const workspaceID = "00000000-0000-0000-0000-000000001420"
	const listID = "00000000-0000-0000-0000-000000001421"
	if _, err := pool.Exec(ctx, `INSERT INTO workspaces (id, name) VALUES ($1, 'Sort migration')`, workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT seed_builtin_kind_definitions($1)`, workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lists (id, workspace_id, name, kind, sort_mode, sort_order)
VALUES ($1, $2, 'Films', 'movie', 'kind_key', 0)`, listID, workspaceID); err != nil {
		t.Fatal(err)
	}
	if err := m.Steps(1); err != nil {
		t.Fatalf("migrate up 000015: %v", err)
	}
	var sortMode string
	var version int
	if err := pool.QueryRow(ctx, `SELECT sort_mode, version FROM lists WHERE id = $1`, listID).Scan(&sortMode, &version); err != nil {
		t.Fatal(err)
	}
	if sortMode != "release_year" || version != 2 {
		t.Fatalf("after up: sort_mode=%q version=%d", sortMode, version)
	}
	if err := m.Steps(-1); err != nil {
		t.Fatalf("migrate down 000015: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT sort_mode, version FROM lists WHERE id = $1`, listID).Scan(&sortMode, &version); err != nil {
		t.Fatal(err)
	}
	if sortMode != "kind_key" || version != 3 {
		t.Fatalf("after down: sort_mode=%q version=%d", sortMode, version)
	}
}
