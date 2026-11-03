package database_test

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"

	"github.com/0xfederama/collections-app-backend/db"
	"github.com/0xfederama/collections-app-backend/internal/platform/database"
)

func files(m map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, body := range m {
		fsys["m/"+name] = &fstest.MapFile{Data: []byte(body)}
	}
	return fsys
}

func migrationState(t *testing.T, dsn string) (version int, dirty bool) {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(t.Context()) }()
	if err := conn.QueryRow(t.Context(), "SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty); err != nil {
		t.Fatal(err)
	}
	return version, dirty
}

func tableExists(t *testing.T, dsn, name string) bool {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(t.Context()) }()
	var ok bool
	if err := conn.QueryRow(t.Context(), "SELECT to_regclass($1) IS NOT NULL", name).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

// D-118: a failing file rolls back as a whole and leaves schema_migrations clean.
func TestMigrateFailedFileRollsBackAndStaysClean(t *testing.T) {
	dsn := newDatabase(t)
	fsys := files(map[string]string{
		"000001_a.up.sql": "CREATE TABLE a (x int);",
		"000002_b.up.sql": "CREATE TABLE b (x int);\nSELECT 1/0;",
	})
	v, err := database.Migrate(fsys, "m", dsn)
	if err == nil || v != 1 {
		t.Fatalf("Migrate = %d, %v; want 1 and an error", v, err)
	}
	if got, dirty := migrationState(t, dsn); got != 1 || dirty {
		t.Fatalf("schema_migrations = %d dirty=%v, want 1 clean", got, dirty)
	}
	if !tableExists(t, dsn, "a") || tableExists(t, dsn, "b") {
		t.Fatal("want table a kept and table b rolled back")
	}
}

func TestMigrateFirstFileFailsLeavesNoVersion(t *testing.T) {
	dsn := newDatabase(t)
	fsys := files(map[string]string{"000001_a.up.sql": "CREATE TABLE a (x int);\nSELECT 1/0;"})
	if _, err := database.Migrate(fsys, "m", dsn); err == nil {
		t.Fatal("want an error")
	}
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(t.Context()) }()
	var n int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM schema_migrations").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 || tableExists(t, dsn, "a") {
		t.Fatalf("want no version row and no table a, got %d rows", n)
	}
}

func TestMigrateRefusesDirtyState(t *testing.T) {
	dsn := newDatabase(t)
	one := map[string]string{"000001_a.up.sql": "CREATE TABLE a (x int);"}
	if _, err := database.Migrate(files(one), "m", dsn); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), "UPDATE schema_migrations SET dirty = true"); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close(t.Context())

	one["000002_b.up.sql"] = "CREATE TABLE b (x int);"
	_, err = database.Migrate(files(one), "m", dsn)
	if !errors.Is(err, database.ErrDirty) || !strings.Contains(err.Error(), "migrate force") {
		t.Fatalf("err = %v, want ErrDirty with a hint", err)
	}
	if tableExists(t, dsn, "b") {
		t.Fatal("a dirty database must not get new migrations")
	}
}

func TestMigrateEmbeddedIsIdempotent(t *testing.T) {
	dsn := newDatabase(t)
	v1, err := database.Migrate(db.Migrations(), "migrations", dsn)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := database.Migrate(db.Migrations(), "migrations", dsn)
	if err != nil || v2 != v1 || v1 == 0 {
		t.Fatalf("second run = %d, %v; first %d", v2, err, v1)
	}
}

// Statements that cannot run inside the one transaction a file gets (D-118).
var nonTransactional = regexp.MustCompile(`(?i)\b(BEGIN|COMMIT|ROLLBACK|START\s+TRANSACTION|CONCURRENTLY|VACUUM|ALTER\s+SYSTEM|CREATE\s+DATABASE|DROP\s+DATABASE)\b`)

var sqlComment = regexp.MustCompile(`--[^\n]*`)

func TestMigrationFilesAreTransactional(t *testing.T) {
	paths, err := filepath.Glob("../../../db/migrations/*.sql")
	if err != nil || len(paths) == 0 {
		t.Fatalf("glob: %v (%d files)", err, len(paths))
	}
	for _, p := range paths {
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if m := nonTransactional.FindString(sqlComment.ReplaceAllString(string(body), "")); m != "" {
			t.Errorf("%s: %q cannot run in the file's single transaction (D-118)", filepath.Base(p), m)
		}
	}
}
