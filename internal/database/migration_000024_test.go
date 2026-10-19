package database_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 000024 relabels the built-in movie field runtime_minutes from "Runtime (minutes)" to "Runtime" on
// locked rows still carrying the seeded label, and recreates the seed function for new workspaces.
// A customised label and custom types are untouched. Down restores the seeded label only.
func TestMigration000024_RuntimeLabel(t *testing.T) {
	ctx := t.Context()
	dsn := newDatabase(t)
	m := newMigrate(t, dsn)
	if err := m.Migrate(23); err != nil {
		t.Fatalf("migrate to 23: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	const (
		wsA        = "00000000-0000-0000-0000-000000002401" // seeded label
		wsB        = "00000000-0000-0000-0000-000000002402" // label customised to "Length"
		wsC        = "00000000-0000-0000-0000-000000002403" // custom type with a "Runtime (minutes)" field
		customType = "00000000-0000-0000-0000-000000002431"
	)
	exec := func(sql string, args ...any) int64 {
		t.Helper()
		tag, err := pool.Exec(ctx, sql, args...)
		if err != nil {
			t.Fatalf("%.70s: %v", sql, err)
		}
		return tag.RowsAffected()
	}
	str := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := pool.QueryRow(ctx, sql, args...).Scan(&s); err != nil {
			t.Fatalf("%.70s: %v", sql, err)
		}
		return s
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("%.70s: %v", sql, err)
		}
		return n
	}
	const movieRuntime = `SELECT f.label FROM type_fields f JOIN type_definitions d ON d.id = f.type_id
		WHERE d.workspace_id = $1 AND d.key = 'movie' AND d.is_locked AND f.key = 'runtime_minutes'`
	const movieRuntimeVersion = `SELECT f.version FROM type_fields f JOIN type_definitions d ON d.id = f.type_id
		WHERE d.workspace_id = $1 AND d.key = 'movie' AND d.is_locked AND f.key = 'runtime_minutes'`

	exec(`INSERT INTO workspaces (id, name) VALUES ($1, 'A'), ($2, 'B'), ($3, 'C')`, wsA, wsB, wsC) // the trigger seeds the built-ins
	exec(`UPDATE type_fields f SET label = 'Length' FROM type_definitions d
		WHERE d.id = f.type_id AND d.workspace_id = $1 AND d.key = 'movie' AND f.key = 'runtime_minutes'`, wsB)
	exec(`INSERT INTO type_definitions (id, workspace_id, key, name) VALUES ($1, $2, 'film_custom', 'Film')`, customType, wsC)
	exec(`INSERT INTO type_fields (workspace_id, type_id, key, label, field_type, position) VALUES ($1, $2, 'runtime_minutes', 'Runtime (minutes)', 'number', 0)`, wsC, customType)

	if got := str(movieRuntime, wsA); got != "Runtime (minutes)" {
		t.Fatalf("seeded label before up = %q", got)
	}

	if err := m.Steps(1); err != nil {
		t.Fatalf("migrate up 000024: %v", err)
	}

	t.Run("the seeded built-in label becomes Runtime and bumps the version", func(t *testing.T) {
		if got := str(movieRuntime, wsA); got != "Runtime" {
			t.Errorf("label in A = %q, want Runtime", got)
		}
		if got := str(movieRuntimeVersion, wsA); got != "2" {
			t.Errorf("version in A = %s, want 2", got)
		}
		if n := count(`SELECT count(*) FROM type_fields f JOIN type_definitions d ON d.id = f.type_id
			WHERE d.workspace_id = $1 AND d.key = 'movie' AND f.key = 'runtime_minutes' AND f.is_locked`, wsA); n != 1 {
			t.Error("the field key or lock state changed")
		}
	})

	t.Run("a customised built-in label is kept", func(t *testing.T) {
		if got := str(movieRuntime, wsB); got != "Length" {
			t.Errorf("customised label in B = %q, want Length", got)
		}
	})

	t.Run("a custom type keeps its field label", func(t *testing.T) {
		if got := str(`SELECT label FROM type_fields WHERE type_id = $1 AND key = 'runtime_minutes'`, customType); got != "Runtime (minutes)" {
			t.Errorf("custom field label = %q", got)
		}
	})

	t.Run("a fresh workspace is seeded with Runtime", func(t *testing.T) {
		const ws = "00000000-0000-0000-0000-000000002404"
		exec(`INSERT INTO workspaces (id, name) VALUES ($1, 'Fresh')`, ws)
		if got := str(movieRuntime, ws); got != "Runtime" {
			t.Errorf("fresh label = %q, want Runtime", got)
		}
		if n := count(`SELECT count(*) FROM type_fields WHERE workspace_id = $1`, ws); n != 35 {
			t.Errorf("fresh built-in field count = %d, want 35", n)
		}
	})

	t.Run("the update is idempotent: a second run matches no rows", func(t *testing.T) {
		// Same statement as the up migration.
		n := exec(`UPDATE type_fields tf SET label = 'Runtime', version = tf.version + 1, updated_at = now()
FROM type_definitions d
WHERE d.id = tf.type_id AND d.workspace_id = tf.workspace_id AND d.key = 'movie' AND d.is_locked
  AND tf.key = 'runtime_minutes' AND tf.label = 'Runtime (minutes)'`)
		if n != 0 {
			t.Errorf("second run updated %d rows", n)
		}
		if got := str(movieRuntimeVersion, wsA); got != "2" {
			t.Errorf("version in A after second run = %s, want 2", got)
		}
	})

	t.Run("down restores the seeded label only, then up again", func(t *testing.T) {
		if err := m.Steps(-1); err != nil {
			t.Fatalf("migrate down 000024: %v", err)
		}
		if got := str(movieRuntime, wsA); got != "Runtime (minutes)" {
			t.Errorf("label in A after down = %q", got)
		}
		if got := str(movieRuntime, wsB); got != "Length" {
			t.Errorf("customised label in B after down = %q", got)
		}
		if got := str(`SELECT label FROM type_fields WHERE type_id = $1 AND key = 'runtime_minutes'`, customType); got != "Runtime (minutes)" {
			t.Errorf("custom field label after down = %q", got)
		}
		const ws = "00000000-0000-0000-0000-000000002405"
		exec(`INSERT INTO workspaces (id, name) VALUES ($1, 'Fresh down')`, ws)
		if got := str(movieRuntime, ws); got != "Runtime (minutes)" {
			t.Errorf("fresh label after down = %q, want Runtime (minutes)", got)
		}
		if err := m.Steps(1); err != nil {
			t.Fatalf("migrate up 000024 again: %v", err)
		}
		if got := str(movieRuntime, wsA); got != "Runtime" {
			t.Errorf("label in A after second up = %q", got)
		}
	})
}
