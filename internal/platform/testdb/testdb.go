// Package testdb gives tests a throwaway migrated Postgres database on one
// shared testcontainers container per test binary. Only _test files import it.
package testdb

import (
	"context"
	"fmt"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/0xfederama/collections-app-backend/db"
	"github.com/0xfederama/collections-app-backend/internal/platform/database"
)

const postgresImage = "postgres:18.6-alpine3.24"

var (
	once      sync.Once
	ctr       *tcpostgres.PostgresContainer
	sharedURL string
	startErr  error

	seq atomic.Int64
)

// Terminate stops the shared container. Call it from TestMain after m.Run.
func Terminate() {
	if ctr != nil {
		_ = testcontainers.TerminateContainer(ctr)
	}
}

// AdminURL starts the shared container on first use and returns its URL.
func AdminURL(t *testing.T) string {
	t.Helper()
	once.Do(func() {
		ctx := context.Background()
		ctr, startErr = tcpostgres.Run(ctx, postgresImage,
			tcpostgres.WithDatabase("collection"),
			tcpostgres.WithUsername("collection"),
			tcpostgres.WithPassword("test"),
			testcontainers.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2).WithStartupTimeout(90*time.Second)),
		)
		if startErr == nil {
			sharedURL, startErr = ctr.ConnectionString(ctx, "sslmode=disable")
		}
	})
	if startErr != nil {
		t.Fatalf("start postgres container: %v", startErr)
	}
	return sharedURL
}

// NewMigrated creates an empty database on the Postgres at adminURL, applies
// all migrations and returns a pool on it, closed on cleanup.
func NewMigrated(t *testing.T, adminURL string) *pgxpool.Pool {
	t.Helper()
	ctx := t.Context()

	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer func() { _ = admin.Close(ctx) }()
	name := fmt.Sprintf("tdb%d", seq.Add(1))
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	dsn := u.String()

	// The same embedded files and code path as `api migrate`.
	if _, err := database.Migrate(db.Migrations(), "migrations", dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}
