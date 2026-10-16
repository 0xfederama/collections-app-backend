package database_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestF013B4B5_MigrationDirectorAndMetadataLanguage(t *testing.T) {
	ctx := t.Context()
	dsn := newDatabase(t)
	m := newMigrate(t, dsn)
	if err := m.Migrate(10); err != nil {
		t.Fatalf("migrate to 10: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	const workspaceID = "00000000-0000-0000-0000-000000000501"
	const listID = "00000000-0000-0000-0000-000000000502"
	if _, err := pool.Exec(ctx, `INSERT INTO workspaces (id, name) VALUES ($1, 'W')`, workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workspace_settings (workspace_id) VALUES ($1)`, workspaceID); err != nil {
		t.Fatal(err)
	}

	if err := m.Steps(1); err != nil {
		t.Fatalf("migrate up 000011: %v", err)
	}
	var language string
	if err := pool.QueryRow(ctx, `SELECT metadata_language FROM workspace_settings WHERE workspace_id = $1`, workspaceID).Scan(&language); err != nil {
		t.Fatal(err)
	}
	if language != "en-US" {
		t.Errorf("existing workspace metadata_language = %q, want en-US", language)
	}
	if _, err := pool.Exec(ctx, `UPDATE workspace_settings SET metadata_language = 'it-IT' WHERE workspace_id = $1`, workspaceID); err != nil {
		t.Fatalf("valid language rejected: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workspace_settings SET metadata_language = 'IT-it' WHERE workspace_id = $1`, workspaceID); err == nil {
		t.Error("invalid language accepted by CHECK")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO lists (id, workspace_id, name, kind, sort_mode, sort_order) VALUES ($1, $2, 'Films', 'movie', 'director', 0)`, listID, workspaceID); err != nil {
		t.Fatalf("director sort rejected after up: %v", err)
	}

	if err := m.Steps(-1); err != nil {
		t.Fatalf("migrate down 000011: %v", err)
	}
	var sortMode string
	var version int
	if err := pool.QueryRow(ctx, `SELECT sort_mode, version FROM lists WHERE id = $1`, listID).Scan(&sortMode, &version); err != nil {
		t.Fatal(err)
	}
	if sortMode != "manual" || version != 2 {
		t.Errorf("down list = sort %q version %d, want manual version 2", sortMode, version)
	}
	var columns int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'workspace_settings' AND column_name = 'metadata_language'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != 0 {
		t.Error("metadata_language still exists after down")
	}

	if err := m.Steps(1); err != nil {
		t.Fatalf("migrate up 000011 again: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT metadata_language FROM workspace_settings WHERE workspace_id = $1`, workspaceID).Scan(&language); err != nil {
		t.Fatal(err)
	}
	if language != "en-US" {
		t.Errorf("re-up metadata_language = %q, want en-US", language)
	}
}
