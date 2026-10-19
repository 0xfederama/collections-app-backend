package database_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 000019 (F-016, API 0.12.0) lets a type default and a list pin up to three fields.
// Down keeps the first two of every longer array and restores the old CHECK.
func TestMigration000019_PinnedFieldsMaxThree(t *testing.T) {
	ctx := t.Context()
	dsn := newDatabase(t)
	m := newMigrate(t, dsn)
	if err := m.Migrate(18); err != nil {
		t.Fatalf("migrate to 18: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	const (
		ws   = "00000000-0000-0000-0000-000000001901"
		list = "00000000-0000-0000-0000-000000001902"
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
	exec(`INSERT INTO workspaces (id, name) VALUES ($1, 'Pins')`, ws)
	exec(`INSERT INTO type_definitions (workspace_id, key, name, allows_price, default_pinned_fields) VALUES ($1, 'board_game', 'Board game', false, '["rating","status"]'::jsonb)`, ws)
	exec(`INSERT INTO lists (id, workspace_id, name, type, sort_order, pinned_fields) VALUES ($1, $2, 'Games', 'board_game', 0, '["rating","status","title"]'::jsonb)`, list, ws)

	insertDefault := func(key, pins string) error {
		_, err := pool.Exec(ctx, `INSERT INTO type_definitions (workspace_id, key, name, allows_price, default_pinned_fields) VALUES ($1, $2, $2, false, $3::jsonb)`, ws, key, pins)
		return err
	}

	if err := m.Steps(1); err != nil {
		t.Fatalf("migrate up 000019: %v", err)
	}

	t.Run("a type default accepts three pins and rejects four", func(t *testing.T) {
		if err := insertDefault("three", `["a","b","c"]`); err != nil {
			t.Errorf("three pins rejected: %v", err)
		}
		if err := insertDefault("four", `["a","b","c","d"]`); err == nil {
			t.Error("four pins accepted")
		}
	})

	t.Run("down keeps the first two pins of longer arrays and restores the limit of two", func(t *testing.T) {
		if err := m.Steps(-1); err != nil {
			t.Fatalf("migrate down 000019: %v", err)
		}
		if n := count(`SELECT count(*) FROM type_definitions WHERE workspace_id = $1 AND key = 'three' AND default_pinned_fields = '["a", "b"]'::jsonb AND version = 2`, ws); n != 1 {
			t.Error("type default was not truncated to its first two pins")
		}
		if n := count(`SELECT count(*) FROM lists WHERE id = $1 AND pinned_fields = '["rating", "status"]'::jsonb AND version = 2`, list); n != 1 {
			t.Error("list pins were not truncated to their first two")
		}
		if n := count(`SELECT count(*) FROM type_definitions WHERE workspace_id = $1 AND key = 'board_game' AND version = 1`, ws); n != 1 {
			t.Error("type default of two pins changed")
		}
		if err := insertDefault("again", `["a","b","c"]`); err == nil {
			t.Error("three pins accepted after down")
		}
	})

	t.Run("up again allows three pins", func(t *testing.T) {
		if err := m.Steps(1); err != nil {
			t.Fatalf("migrate up 000019 again: %v", err)
		}
		if err := insertDefault("again", `["a","b","c"]`); err != nil {
			t.Errorf("three pins rejected after re-up: %v", err)
		}
	})
}
