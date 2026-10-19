package database_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 000020 (F-016, API 0.12.0, D-096) adds the locked `stores` field type and
// workspace_settings.preferred_stores (provider keys). Down drops the setting,
// and refuses while a stores field exists (the restored CHECK fails on it).
func TestMigration000020_PreferredStores(t *testing.T) {
	ctx := t.Context()
	dsn := newDatabase(t)
	m := newMigrate(t, dsn)
	if err := m.Migrate(19); err != nil {
		t.Fatalf("migrate to 19: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	const (
		ws  = "00000000-0000-0000-0000-000000002001"
		typ = "00000000-0000-0000-0000-000000002002"
	)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%.60s: %v", sql, err)
		}
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("%.60s: %v", sql, err)
		}
		return n
	}
	exec(`INSERT INTO workspaces (id, name) VALUES ($1, 'Stores')`, ws)
	exec(`INSERT INTO workspace_settings (workspace_id) VALUES ($1)`, ws)
	exec(`INSERT INTO type_definitions (id, workspace_id, key, name, is_locked) VALUES ($1, $2, 'book', 'Book', true)`, typ, ws)

	// insertField adds a field of the type; locked selects a built-in field (is_locked).
	insertField := func(key, fieldType string, locked bool) error {
		_, err := pool.Exec(ctx, `INSERT INTO type_fields (workspace_id, type_id, key, label, field_type, position, is_locked)
			VALUES ($1, $2, $3, $3, $4, (SELECT count(*) FROM type_fields WHERE type_id = $2), $5)`, ws, typ, key, fieldType, locked)
		return err
	}

	t.Run("before 000020 there is no stores field type", func(t *testing.T) {
		if err := insertField("before", "stores", true); err == nil {
			t.Error("stores field accepted before 000020")
		}
	})

	if err := m.Steps(1); err != nil {
		t.Fatalf("migrate up 000020: %v", err)
	}

	t.Run("a locked stores field is accepted, custom ones are not", func(t *testing.T) {
		if err := insertField("shops", "stores", true); err != nil {
			t.Errorf("locked stores field rejected: %v", err)
		}
		if err := insertField("custom_shops", "stores", false); err == nil {
			t.Error("custom stores field accepted")
		}
		if err := insertField("audio", "audible", true); err == nil {
			t.Error("unknown field type accepted")
		}
	})

	t.Run("preferred_stores defaults to an empty array", func(t *testing.T) {
		if n := count(`SELECT count(*) FROM workspace_settings WHERE workspace_id = $1 AND preferred_stores = '{}'::text[]`, ws); n != 1 {
			t.Error("existing settings row does not default to an empty preferred_stores")
		}
	})

	t.Run("preferred_stores accepts the six keys and rejects others", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `UPDATE workspace_settings SET preferred_stores = '{openlibrary,bookshop,amazon,apple_books,google_play_books,kobo}' WHERE workspace_id = $1`, ws); err != nil {
			t.Errorf("six known keys rejected: %v", err)
		}
		if _, err := pool.Exec(ctx, `UPDATE workspace_settings SET preferred_stores = '{kobo,audible}' WHERE workspace_id = $1`, ws); err == nil {
			t.Error("unknown key accepted")
		}
		if _, err := pool.Exec(ctx, `UPDATE workspace_settings SET preferred_stores = ARRAY['kobo', NULL]::text[] WHERE workspace_id = $1`, ws); err == nil {
			t.Error("null element accepted")
		}
		if _, err := pool.Exec(ctx, `UPDATE workspace_settings SET preferred_stores = NULL WHERE workspace_id = $1`, ws); err == nil {
			t.Error("null array accepted")
		}
	})

	t.Run("down drops the setting and the field type once no stores field exists", func(t *testing.T) {
		// While a stores field exists the restored CHECK fails: the down changes nothing and
		// leaves the migrator dirty, so it is forced back to 20 before retrying.
		if err := m.Steps(-1); err == nil {
			t.Fatal("down succeeded with a stores field present")
		}
		if err := m.Force(20); err != nil {
			t.Fatalf("force 20: %v", err)
		}
		if n := count(`SELECT count(*) FROM type_fields WHERE key = 'shops' AND field_type = 'stores'`); n != 1 {
			t.Fatal("the failed down changed the stores field")
		}
		exec(`DELETE FROM type_fields WHERE field_type = 'stores'`)
		if err := m.Steps(-1); err != nil {
			t.Fatalf("migrate down 000020: %v", err)
		}
		if n := count(`SELECT count(*) FROM information_schema.columns WHERE table_name = 'workspace_settings' AND column_name = 'preferred_stores'`); n != 0 {
			t.Error("preferred_stores survived the down")
		}
		if err := insertField("again", "stores", true); err == nil {
			t.Error("stores field accepted after down")
		}
	})

	t.Run("up again accepts a stores field", func(t *testing.T) {
		if err := m.Steps(1); err != nil {
			t.Fatalf("migrate up 000020 again: %v", err)
		}
		if err := insertField("shops", "stores", true); err != nil {
			t.Errorf("locked stores field rejected after re-up: %v", err)
		}
	})
}
