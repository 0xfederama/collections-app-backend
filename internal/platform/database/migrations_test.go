package database_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the pgx5:// scheme
	_ "github.com/golang-migrate/migrate/v4/source/file"     // registers file://
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	postgresImage = "postgres:18.6-alpine3.24"
	migrationsDir = "file://../../../db/migrations"
)

// schemaTables are the tables created by 000001 (DATA_MODEL §2) and 000002 (users, user_top_order, folder_members), in FK order.
var schemaTables = []string{
	"workspaces", "workspace_settings", "trash_batches", "folders", "lists",
	"attachments", "items", "item_content_versions", "lookup_imports", "users", "user_top_order", "folder_members",
}

var (
	pgOnce sync.Once
	pgCtr  *tcpostgres.PostgresContainer
	pgURL  string
	pgErr  error

	dbSeq atomic.Int64
)

func TestMain(m *testing.M) {
	code := m.Run()
	if pgCtr != nil {
		_ = testcontainers.TerminateContainer(pgCtr)
	}
	os.Exit(code)
}

// sharedPostgres starts one container for the whole test binary.
func sharedPostgres(t *testing.T) string {
	t.Helper()
	pgOnce.Do(func() {
		ctx := context.Background()
		pgCtr, pgErr = tcpostgres.Run(ctx, postgresImage,
			tcpostgres.WithDatabase("collection"),
			tcpostgres.WithUsername("collection"),
			tcpostgres.WithPassword("test"),
			testcontainers.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2).WithStartupTimeout(90*time.Second)),
		)
		if pgErr == nil {
			pgURL, pgErr = pgCtr.ConnectionString(ctx, "sslmode=disable")
		}
	})
	if pgErr != nil {
		t.Fatalf("start postgres container: %v", pgErr)
	}
	return pgURL
}

// newDatabase creates an empty database on the shared container and returns its URL, so tests never see each other's schema.
func newDatabase(t *testing.T) string {
	t.Helper()
	adminURL := sharedPostgres(t)
	ctx := t.Context()

	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer func() { _ = admin.Close(ctx) }()

	name := fmt.Sprintf("t%d", dbSeq.Add(1))
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}

	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

// newMigrate returns a migrator for dsn. Close is registered on cleanup.
func newMigrate(t *testing.T, dsn string) *migrate.Migrate {
	t.Helper()
	m, err := migrate.New(migrationsDir, strings.Replace(dsn, "postgres://", "pgx5://", 1))
	if err != nil {
		t.Fatalf("new migrate: %v", err)
	}
	t.Cleanup(func() { _, _ = m.Close() })
	return m
}

func mustUp(t *testing.T, m *migrate.Migrate) {
	t.Helper()
	if err := m.Up(); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
}

func mustDown(t *testing.T, m *migrate.Migrate) {
	t.Helper()
	if err := m.Down(); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
}

// newMigratedPool returns a pool on a fresh database with all migrations applied.
func newMigratedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := newDatabase(t)
	mustUp(t, newMigrate(t, dsn))
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func publicTables(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(),
		`SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' ORDER BY table_name`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func hasTrgm(t *testing.T, pool *pgxpool.Pool) bool {
	t.Helper()
	var ok bool
	if err := pool.QueryRow(t.Context(),
		`SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm')`).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestMigrations_UpDownUp(t *testing.T) {
	dsn := newDatabase(t)
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	m := newMigrate(t, dsn)

	want := append([]string{"schema_migrations"}, schemaTables...)
	slices.Sort(want)

	mustUp(t, m)
	if got := publicTables(t, pool); !slices.Equal(got, want) {
		t.Errorf("tables after up = %v, want %v", got, want)
	}
	if !hasTrgm(t, pool) {
		t.Error("pg_trgm missing after up")
	}

	mustDown(t, m)
	if got := publicTables(t, pool); !slices.Equal(got, []string{"schema_migrations"}) {
		t.Errorf("tables after down = %v, want only schema_migrations", got)
	}
	if hasTrgm(t, pool) {
		t.Error("pg_trgm still installed after down")
	}

	mustUp(t, m)
	if got := publicTables(t, pool); !slices.Equal(got, want) {
		t.Errorf("tables after second up = %v, want %v", got, want)
	}
}

// seedAllTables inserts one valid row into every table, including a trashed list with its batch.
func seedAllTables(ctx context.Context, tx pgx.Tx) error {
	stmts := []string{
		`INSERT INTO workspaces (id, name) VALUES ('00000000-0000-0000-0000-000000000001', 'W')`,
		`INSERT INTO workspace_settings (workspace_id) VALUES ('00000000-0000-0000-0000-000000000001')`,
		`INSERT INTO folders (id, workspace_id, name, sort_order)
		   VALUES ('00000000-0000-0000-0000-000000000002', '00000000-0000-0000-0000-000000000001', 'F', 0)`,
		`INSERT INTO lists (id, workspace_id, folder_id, name, type, sort_order)
		   VALUES ('00000000-0000-0000-0000-000000000003', '00000000-0000-0000-0000-000000000001',
		           '00000000-0000-0000-0000-000000000002', 'L', 'movie', 0)`,
		`INSERT INTO attachments (id, workspace_id, storage_key, thumbnail_storage_key, media_type, size_bytes, width, height, sha256)
		   VALUES ('00000000-0000-0000-0000-000000000004', '00000000-0000-0000-0000-000000000001',
		           'images/00/00/a.webp', 'images/00/00/a.thumb.jpg', 'image/webp', 10, 1, 1, '\x00')`,
		`INSERT INTO items (id, workspace_id, list_id, title, image_attachment_id, sort_order)
		   VALUES ('00000000-0000-0000-0000-000000000005', '00000000-0000-0000-0000-000000000001',
		           '00000000-0000-0000-0000-000000000003', 'I', '00000000-0000-0000-0000-000000000004', 0)`,
		`INSERT INTO item_content_versions (item_id, version, body, size_bytes, sha256)
		   VALUES ('00000000-0000-0000-0000-000000000005', 1, 'hi', 2, '\x00')`,
		`INSERT INTO lookup_imports (workspace_id, item_id, provider, source_id, type, imported_fields, original_title,
		                           fetched_at, refresh_after, expires_at)
		   VALUES ('00000000-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000005', 'tmdb', '603',
		           'movie', '{"release_year":1999}', 'I', now(), now() + interval '5 months', now() + interval '6 months')`,
		`INSERT INTO trash_batches (id, workspace_id, root_type, root_id, root_name, former_path)
		   VALUES ('00000000-0000-0000-0000-000000000006', '00000000-0000-0000-0000-000000000001',
		           'list', '00000000-0000-0000-0000-000000000007', 'Trashed', '[]')`,
		`INSERT INTO lists (id, workspace_id, name, type, sort_order, deleted_at, trash_batch_id)
		   VALUES ('00000000-0000-0000-0000-000000000007', '00000000-0000-0000-0000-000000000001', 'Trashed', 'generic', 1,
		           now(), '00000000-0000-0000-0000-000000000006')`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(ctx, s); err != nil {
			return fmt.Errorf("%s: %w", strings.Fields(s)[2], err)
		}
	}
	return nil
}

func TestMigrations_DownWithData(t *testing.T) {
	dsn := newDatabase(t)
	m := newMigrate(t, dsn)
	mustUp(t, m)

	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := seedAllTables(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("seed: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	mustDown(t, m)
	if got := publicTables(t, pool); !slices.Equal(got, []string{"schema_migrations"}) {
		t.Errorf("tables after down = %v, want only schema_migrations", got)
	}
	mustUp(t, m)
}
