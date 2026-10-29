package seed_test

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0xfederama/collections-app-backend/internal/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/seed"
	"github.com/0xfederama/collections-app-backend/internal/testdb"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testdb.Terminate()
	os.Exit(code)
}

type typeRow struct {
	Key              string  `db:"key"`
	Name             string  `db:"name"`
	DefaultIcon      *string `db:"default_icon"`
	AllowsPrice      bool    `db:"allows_price"`
	DefaultSortField *string `db:"default_sort_field"`
	DefaultPinned    *string `db:"default_pinned_fields"`
	IsLocked         bool    `db:"is_locked"`
	Version          int32   `db:"version"`
}

type fieldRow struct {
	TypeKey   string `db:"type_key"`
	Key       string `db:"key"`
	Label     string `db:"label"`
	FieldType string `db:"field_type"`
	Position  int32  `db:"position"`
	IsLocked  bool   `db:"is_locked"`
	Version   int32  `db:"version"`
}

// newWorkspace inserts a workspace with the raw SQL insert (nothing seeds it: the baseline has no triggers).
func newWorkspace(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	ws := uuid.New()
	if _, err := pool.Exec(t.Context(), "INSERT INTO workspaces (id, name) VALUES ($1, 'W')", ws); err != nil {
		t.Fatal(err)
	}
	return ws
}

func readTypes(t *testing.T, pool *pgxpool.Pool, ws uuid.UUID) []typeRow {
	t.Helper()
	rows, err := pool.Query(t.Context(), `
SELECT key, name, default_icon, allows_price, default_sort_field, default_pinned_fields::text AS default_pinned_fields, is_locked, version
FROM type_definitions WHERE workspace_id = $1 ORDER BY key`, ws)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByName[typeRow])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func readFields(t *testing.T, pool *pgxpool.Pool, ws uuid.UUID) []fieldRow {
	t.Helper()
	rows, err := pool.Query(t.Context(), `
SELECT td.key AS type_key, tf.key, tf.label, tf.field_type, tf.position, tf.is_locked, tf.version
FROM type_fields tf JOIN type_definitions td ON td.id = tf.type_id AND td.workspace_id = tf.workspace_id
WHERE tf.workspace_id = $1 ORDER BY td.key, tf.key`, ws)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByName[fieldRow])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func countRows(t *testing.T, pool *pgxpool.Pool, ws uuid.UUID, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table+" WHERE workspace_id = $1", ws).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSeed_BuiltinShape(t *testing.T) {
	pool := testdb.NewMigrated(t, testdb.AdminURL(t))
	ctx := t.Context()
	ws := newWorkspace(t, pool)
	if err := seed.BuiltinTypes(ctx, sqlc.New(pool), ws); err != nil {
		t.Fatal(err)
	}

	types := readTypes(t, pool, ws)
	if len(types) != 5 {
		t.Fatalf("seeded %d types, want 5", len(types))
	}
	wantPins := map[string][]string{
		"movie":    {"runtime_minutes", "streaming"},
		"tv_show":  {"seasons", "streaming"},
		"book":     {"author", "pages", "stores"},
		"vehicle":  {"model_year", "power_hp"},
		"wishlist": {"price"},
	}
	for _, typ := range types {
		if !typ.IsLocked {
			t.Errorf("type %s is not locked", typ.Key)
		}
		if typ.DefaultPinned == nil {
			t.Errorf("type %s has no default pins", typ.Key)
			continue
		}
		var pins []string
		if err := json.Unmarshal([]byte(*typ.DefaultPinned), &pins); err != nil {
			t.Fatalf("pins of type %s: %v", typ.Key, err)
		}
		if !slices.Equal(pins, wantPins[typ.Key]) {
			t.Errorf("default pins of type %s = %v, want %v", typ.Key, pins, wantPins[typ.Key])
		}
	}

	fields := readFields(t, pool, ws)
	if len(fields) != 27 {
		t.Fatalf("seeded %d fields, want 27", len(fields))
	}
	for _, f := range fields {
		if !f.IsLocked {
			t.Errorf("field %s.%s is not locked", f.TypeKey, f.Key)
		}
	}
}

func TestSeed_Idempotent(t *testing.T) {
	pool := testdb.NewMigrated(t, testdb.AdminURL(t))
	ctx := t.Context()
	ws := newWorkspace(t, pool)
	q := sqlc.New(pool)

	for range 2 {
		if err := seed.BuiltinTypes(ctx, q, ws); err != nil {
			t.Fatal(err)
		}
	}
	if n := countRows(t, pool, ws, "type_definitions"); n != 5 {
		t.Errorf("type_definitions = %d, want 5", n)
	}
	if n := countRows(t, pool, ws, "type_fields"); n != 27 {
		t.Errorf("type_fields = %d, want 27", n)
	}

	// A user edit of a locked label and of the default pins survives a re-seed.
	if _, err := pool.Exec(ctx, "UPDATE type_fields SET label = 'Edited' WHERE workspace_id = $1 AND key = 'make'", ws); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE type_definitions SET default_pinned_fields = '["make"]' WHERE workspace_id = $1 AND key = 'vehicle'`, ws); err != nil {
		t.Fatal(err)
	}
	if err := seed.BuiltinTypes(ctx, q, ws); err != nil {
		t.Fatal(err)
	}
	var label string
	if err := pool.QueryRow(ctx, "SELECT label FROM type_fields WHERE workspace_id = $1 AND key = 'make'", ws).Scan(&label); err != nil {
		t.Fatal(err)
	}
	if label != "Edited" {
		t.Errorf("label of locked field = %q, want it kept as %q", label, "Edited")
	}
	var pins string
	if err := pool.QueryRow(ctx, `SELECT default_pinned_fields::text FROM type_definitions WHERE workspace_id = $1 AND key = 'vehicle'`, ws).Scan(&pins); err != nil {
		t.Fatal(err)
	}
	if pins != `["make"]` {
		t.Errorf("default pins of vehicle = %s, want the user edit kept", pins)
	}
}
