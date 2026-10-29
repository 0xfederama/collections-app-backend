package seed_test

import (
	"os"
	"reflect"
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

// newWorkspace inserts a workspace with the raw SQL insert (the trigger seeds it).
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

func TestSeed_MatchesSQLFunction(t *testing.T) {
	pool := testdb.NewMigrated(t, testdb.AdminURL(t))
	ctx := t.Context()
	// A is seeded by the trigger (the SQL function); B is seeded by Go.
	wsA := newWorkspace(t, pool)
	wsB := newWorkspace(t, pool)
	if _, err := pool.Exec(ctx, "DELETE FROM type_fields WHERE workspace_id = $1", wsB); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM type_definitions WHERE workspace_id = $1", wsB); err != nil {
		t.Fatal(err)
	}
	if err := seed.BuiltinTypes(ctx, sqlc.New(pool), wsB); err != nil {
		t.Fatal(err)
	}

	typesA, typesB := readTypes(t, pool, wsA), readTypes(t, pool, wsB)
	if len(typesA) != 5 {
		t.Fatalf("SQL function seeded %d types, want 5", len(typesA))
	}
	if !reflect.DeepEqual(typesA, typesB) {
		t.Errorf("built-in types differ:\nSQL: %+v\nGo:  %+v", typesA, typesB)
	}
	fieldsA, fieldsB := readFields(t, pool, wsA), readFields(t, pool, wsB)
	if len(fieldsA) != 27 {
		t.Fatalf("SQL function seeded %d fields, want 27", len(fieldsA))
	}
	if !reflect.DeepEqual(fieldsA, fieldsB) {
		t.Errorf("built-in fields differ:\nSQL: %+v\nGo:  %+v", fieldsA, fieldsB)
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
