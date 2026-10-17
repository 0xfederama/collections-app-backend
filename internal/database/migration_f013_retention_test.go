package database_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestF013B3_MigrationTMDBImports(t *testing.T) {
	ctx := t.Context()
	dsn := newDatabase(t)
	m := newMigrate(t, dsn)
	if err := m.Migrate(11); err != nil {
		t.Fatalf("migrate to 11: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	workspaceID, listID, itemID, invalidItemID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspaces (id, name) VALUES ($1, 'W');
		INSERT INTO lists (id, workspace_id, name, kind, sort_order) VALUES ($2, $1, 'Movies', 'movie', 0);
		INSERT INTO items (id, workspace_id, list_id, title, sort_order) VALUES ($3, $1, $2, 'Matrix', 0);
		INSERT INTO items (id, workspace_id, list_id, title, sort_order) VALUES ($4, $1, $2, 'Invalid', 1)`,
		workspaceID, listID, itemID, invalidItemID); err != nil {
		t.Fatal(err)
	}
	if err := m.Steps(1); err != nil {
		t.Fatalf("migrate up 000012: %v", err)
	}
	now := time.Date(2026, time.January, 31, 0, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO tmdb_imports
		(workspace_id, item_id, tmdb_id, kind, imported_fields, original_title, fetched_at, refresh_after, expires_at)
		VALUES ($1, $2, 603, 'movie', '{"tmdb_id":603}', 'matrix', $3, $4, $5)`,
		workspaceID, itemID, now, now.AddDate(0, 5, 0), now.AddDate(0, 6, 0)); err != nil {
		t.Fatalf("insert provenance: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO tmdb_imports
		(workspace_id, item_id, tmdb_id, kind, original_title, fetched_at, refresh_after, expires_at)
		VALUES ($1, $2, 0, 'movie', 'matrix', $3, $4, $5)`,
		workspaceID, invalidItemID, now, now.AddDate(0, 5, 0), now.AddDate(0, 6, 0)); err == nil {
		t.Error("tmdb_id constraint accepted zero")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM items WHERE id = $1`, itemID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tmdb_imports WHERE item_id = $1`, itemID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("provenance rows after item cascade = %d", count)
	}
	if err := m.Steps(-1); err != nil {
		t.Fatalf("migrate down 000012: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'tmdb_imports'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Error("tmdb_imports remains after down")
	}
}
